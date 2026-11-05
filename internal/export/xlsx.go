package export

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The format allows 32,767 characters in a cell, and refuses the whole file if one has more.
const maxCell = 32767

var sheetUnsafe = regexp.MustCompile(`[\[\]:*?/\\']`)

func sheetName(title string) string {
	s := strings.TrimSpace(sheetUnsafe.ReplaceAllString(title, " "))
	if s == "" {
		return "Export"
	}
	if r := []rune(s); len(r) > 31 {
		s = string(r[:31])
	}
	return s
}

// colName turns 0 into A and 26 into AA.
func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

// validXML drops what XML cannot hold at all, which would make the file refuse to open.
func validXML(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t', r == '\n', r == '\r':
			return r
		case r < 0x20, r == 0xFFFE, r == 0xFFFF, r == utf8.RuneError:
			return -1
		}
		return r
	}, s)
}

func escapeXML(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(validXML(s)))
	return b.String()
}

func textCell(b *strings.Builder, ref, text string, style int) {
	if r := []rune(text); len(r) > maxCell {
		text = string(r[:maxCell])
	}
	// An inline string is always text. Excel does not run it as a formula, whatever it says.
	fmt.Fprintf(b, `<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, escapeXML(text))
}

func writeXLSX(w io.Writer, records []Record, cols []string, title string) error {
	z := zip.NewWriter(w)
	part := func(name, body string) error {
		// A fixed time means the same data gives the same file.
		f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, body)
		return err
	}
	const head = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"
	files := []struct{ name, body string }{
		{"[Content_Types].xml", head + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`},
		{"_rels/.rels", head + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", head + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="` + escapeXML(sheetName(title)) + `" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", head + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"xl/styles.xml", head + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`},
	}
	for _, f := range files {
		if err := part(f.name, f.body); err != nil {
			return err
		}
	}
	var b strings.Builder
	b.WriteString(head + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if len(cols) > 0 {
		// Keep the header in view, and make it filterable.
		b.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	}
	b.WriteString(`<sheetData>`)
	if len(cols) > 0 {
		b.WriteString(`<row r="1">`)
		for i, c := range cols {
			textCell(&b, colName(i)+"1", c, 1)
		}
		b.WriteString(`</row>`)
	}
	for ri, r := range records {
		row := ri + 2
		fmt.Fprintf(&b, `<row r="%d">`, row)
		for i, c := range cols {
			ref := colName(i) + strconv.Itoa(row)
			switch v := r[c].(type) {
			case nil:
			case float64:
				fmt.Fprintf(&b, `<c r="%s"><v>%s</v></c>`, ref, strconv.FormatFloat(v, 'f', -1, 64))
			case bool:
				n := 0
				if v {
					n = 1
				}
				fmt.Fprintf(&b, `<c r="%s" t="b"><v>%d</v></c>`, ref, n)
			default:
				textCell(&b, ref, Text(v), 0)
			}
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData>`)
	if len(cols) > 0 {
		fmt.Fprintf(&b, `<autoFilter ref="A1:%s%d"/>`, colName(len(cols)-1), len(records)+1)
	}
	b.WriteString(`</worksheet>`)
	if err := part("xl/worksheets/sheet1.xml", b.String()); err != nil {
		return err
	}
	return z.Close()
}
