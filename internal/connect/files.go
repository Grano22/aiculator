package connect

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Limits for reading spreadsheets. A file is a few kilobytes of zip that can claim to hold
// gigabytes, so every part is read through a limit.
const (
	maxSheetFile = 64 << 20
	maxSheetPart = 256 << 20
	maxRows      = 1 << 20
)

// sheet is a grid of text, addressed from zero.
type sheet [][]string

func (s sheet) cell(row, col int) string {
	if row < len(s) && col < len(s[row]) {
		return strings.TrimSpace(s[row][col])
	}
	return ""
}

// readXLSX reads one sheet using only the standard library, so that a format this simple
// adds no dependency and nothing to the binary's size. It reads values, not formulas:
// a file saved by Excel stores each formula's last result, and that is what comes back.
func readXLSX(file, sheetName string) (sheet, error) {
	if fi, err := os.Stat(file); err != nil {
		return nil, err
	} else if fi.Size() > maxSheetFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", file, maxSheetFile)
	}
	z, err := zip.OpenReader(file)
	if err != nil {
		return nil, fmt.Errorf("%s: not an .xlsx file: %w", file, err)
	}
	defer z.Close()
	parts := make(map[string]*zip.File, len(z.File))
	for _, f := range z.File {
		parts[f.Name] = f
	}
	target, err := sheetPath(parts, sheetName)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	shared, err := sharedStrings(parts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	dates, err := dateStyles(parts)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	f := parts[target]
	if f == nil {
		return nil, fmt.Errorf("%s: missing %s", file, target)
	}
	return parseSheet(f, shared, dates)
}

func openPart(f *zip.File) (io.ReadCloser, *xml.Decoder, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, nil, err
	}
	return rc, xml.NewDecoder(io.LimitReader(rc, maxSheetPart)), nil
}

func decodePart(f *zip.File, v any) error {
	rc, dec, err := openPart(f)
	if err != nil {
		return err
	}
	defer rc.Close()
	return dec.Decode(v)
}

func sheetPath(parts map[string]*zip.File, want string) (string, error) {
	wb := parts["xl/workbook.xml"]
	rels := parts["xl/_rels/workbook.xml.rels"]
	if wb == nil || rels == nil {
		return "", errors.New("not an .xlsx workbook")
	}
	var book struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := decodePart(wb, &book); err != nil {
		return "", err
	}
	var rel struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := decodePart(rels, &rel); err != nil {
		return "", err
	}
	if len(book.Sheets) == 0 {
		return "", errors.New("the workbook has no sheets")
	}
	var names []string
	for _, sh := range book.Sheets {
		names = append(names, sh.Name)
		if want != "" && !strings.EqualFold(sh.Name, want) {
			continue
		}
		for _, r := range rel.Items {
			if r.ID == sh.RID {
				if strings.HasPrefix(r.Target, "/") {
					return strings.TrimPrefix(r.Target, "/"), nil
				}
				return path.Join("xl", r.Target), nil
			}
		}
	}
	return "", fmt.Errorf("no sheet named %q; these exist: %s", want, strings.Join(names, ", "))
}

// richText is a run of text, possibly split into styled pieces. Phonetic hints are left out.
type richText struct {
	T string `xml:"t"`
	R []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

func (t richText) text() string {
	if len(t.R) == 0 {
		return t.T
	}
	var b strings.Builder
	for _, r := range t.R {
		b.WriteString(r.T)
	}
	return b.String()
}

func sharedStrings(parts map[string]*zip.File) ([]string, error) {
	f := parts["xl/sharedStrings.xml"]
	if f == nil {
		return nil, nil // A sheet of only numbers or inline text has none.
	}
	rc, dec, err := openPart(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var out []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "si" {
			var t richText
			if err := dec.DecodeElement(&t, &se); err != nil {
				return nil, err
			}
			out = append(out, t.text())
		}
	}
}

var dateFormat = regexp.MustCompile(`(?i)[ymdh]`)

// dateStyles finds which cell styles show a date. A date is stored as a plain number, and
// only its style says otherwise.
func dateStyles(parts map[string]*zip.File) (map[int]bool, error) {
	f := parts["xl/styles.xml"]
	if f == nil {
		return nil, nil
	}
	var st struct {
		NumFmts []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		Xfs []struct {
			NumFmt int `xml:"numFmtId,attr"`
		} `xml:"cellXfs>xf"`
	}
	if err := decodePart(f, &st); err != nil {
		return nil, err
	}
	custom := map[int]bool{}
	for _, n := range st.NumFmts {
		clean := regexp.MustCompile(`"[^"]*"|\[[^\]]*\]|\\.`).ReplaceAllString(n.Code, "")
		custom[n.ID] = dateFormat.MatchString(clean)
	}
	out := map[int]bool{}
	for i, x := range st.Xfs {
		if (x.NumFmt >= 14 && x.NumFmt <= 22) || (x.NumFmt >= 45 && x.NumFmt <= 47) || custom[x.NumFmt] {
			out[i] = true
		}
	}
	return out, nil
}

// serialDate converts Excel's day count. Its epoch is 1899-12-30, which absorbs the 1900
// leap year bug for every date after February of that year.
func serialDate(v string) (string, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 1 || f > 2958465 {
		return "", false
	}
	days := int(f)
	t := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
	frac := f - float64(days)
	if frac < 1e-9 {
		return t.Format("2006-01-02"), true
	}
	return t.Add(time.Duration(frac * float64(24*time.Hour)).Round(time.Second)).Format("2006-01-02 15:04:05"), true
}

// parseRef reads a reference such as BC12 into zero-based column and row.
func parseRef(ref string) (col, row int, ok bool) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		col = col*26 + int(ref[i]-'A') + 1
		i++
	}
	if i == 0 || i == len(ref) || col > 1<<14 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(ref[i:])
	if err != nil || n < 1 || n > maxRows {
		return 0, 0, false
	}
	return col - 1, n - 1, true
}

func parseSheet(f *zip.File, shared []string, dates map[int]bool) (sheet, error) {
	rc, dec, err := openPart(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var grid sheet
	nextRow := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return grid, nil
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "c" {
			continue
		}
		var c struct {
			Ref   string   `xml:"r,attr"`
			Type  string   `xml:"t,attr"`
			Style int      `xml:"s,attr"`
			V     string   `xml:"v"`
			Is    richText `xml:"is"`
		}
		if err := dec.DecodeElement(&c, &se); err != nil {
			return nil, err
		}
		col, row, ok := parseRef(c.Ref)
		if !ok {
			col, row = 0, nextRow
		}
		nextRow = row
		var text string
		switch c.Type {
		case "s":
			if i, err := strconv.Atoi(c.V); err == nil && i >= 0 && i < len(shared) {
				text = shared[i]
			}
		case "inlineStr":
			text = c.Is.text()
		case "b":
			text = strconv.FormatBool(c.V == "1")
		case "e":
			continue // An error value such as #REF! is not data.
		case "str":
			text = c.V
		default:
			text = c.V
			if dates[c.Style] {
				if d, ok := serialDate(c.V); ok {
					text = d
				}
			}
		}
		for len(grid) <= row {
			grid = append(grid, nil)
		}
		for len(grid[row]) <= col {
			grid[row] = append(grid[row], "")
		}
		grid[row][col] = text
	}
}

func readCSV(file string) (sheet, error) {
	if fi, err := os.Stat(file); err != nil {
		return nil, err
	} else if fi.Size() > maxSheetFile {
		return nil, fmt.Errorf("%s is larger than %d bytes", file, maxSheetFile)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if len(rows) > 0 && len(rows[0]) > 0 {
		rows[0][0] = strings.TrimPrefix(rows[0][0], "\xef\xbb\xbf") // Excel writes a byte order mark.
	}
	return sheet(rows), nil
}

// fileSource reads a spreadsheet, either as a table with a header row or as regions.
type fileSource struct {
	src  *Source
	file string
}

func hashText(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = io.WriteString(h, p)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Pull implements Connector.
func (f *fileSource) Pull(ctx context.Context, opts PullOptions, yield func(Item) error) error {
	var (
		grid sheet
		err  error
	)
	if f.src.Kind == "xlsx" {
		grid, err = readXLSX(f.file, f.src.Sheet)
	} else {
		grid, err = readCSV(f.file)
	}
	if err != nil {
		return err
	}
	if len(f.src.Regions) > 0 {
		return f.pullRegions(ctx, grid, yield)
	}
	return f.pullTable(ctx, grid, yield)
}

func (f *fileSource) pullTable(ctx context.Context, grid sheet, yield func(Item) error) error {
	header := max(f.src.HeaderRow, 1) - 1
	if header >= len(grid) {
		return fmt.Errorf("%s: no row %d to use as the header", f.file, header+1)
	}
	names := make([]string, len(grid[header]))
	for i := range names {
		names[i] = grid.cell(header, i)
	}
	for row := header + 1; row < len(grid); row++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		fields := make(map[string]any, len(names))
		var all []string
		for col, name := range names {
			v := grid.cell(row, col)
			all = append(all, v)
			if name != "" && v != "" {
				fields[name] = v
			}
		}
		if len(fields) == 0 {
			continue // Blank rows are common at the end of a sheet.
		}
		it := Item{Fields: fields, Version: hashText(all...)}
		it.Key = resolveText(f.src.Fields.Key, it)
		if it.Key == "" {
			// With no key column the title identifies a row, so that adding rows above it,
			// or sorting the sheet, cannot turn it into a different task.
			it.Key = hashText(resolveText(f.src.Fields.Title, it))
		}
		if err := yield(it); err != nil {
			return err
		}
	}
	return nil
}

func (f *fileSource) pullRegions(ctx context.Context, grid sheet, yield func(Item) error) error {
	for _, r := range f.src.Regions {
		left, right, ok := strings.Cut(r.Range, ":")
		if !ok {
			right = left
		}
		c1, r1, ok1 := parseRef(left)
		c2, r2, ok2 := parseRef(right)
		if !ok1 || !ok2 || c2 < c1 || r2 < r1 {
			return fmt.Errorf("region %s: bad range %q", r.Name, r.Range)
		}
		for row := r1; row <= min(r2, len(grid)-1); row++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for col := c1; col <= c2; col++ {
				v := grid.cell(row, col)
				if v == "" {
					continue
				}
				it := Item{
					Key: hashText(v), Version: hashText(v, r.Name),
					Fields: map[string]any{"value": v, "region": r.Name},
				}
				if err := yield(it); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
