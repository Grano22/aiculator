// Package prompt asks questions in a terminal: yes or no, one choice, or several. In a real
// terminal it uses arrow keys and checkboxes. When input is piped it reads plain lines, so
// scripts and tests get the same questions.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// ErrCanceled means the person backed out, with Ctrl-C, Esc or q.
var ErrCanceled = errors.New("canceled")

// ErrNoInput means input ended before an answer came, as in a script with nothing piped in.
var ErrNoInput = errors.New("no answer: input ended")

// Option is one choice.
type Option struct {
	Label    string
	Detail   string // Shown dimmed, after the label.
	Selected bool   // Checked at the start, in a multi-select.
}

// Prompter asks questions on one input and output.
type Prompter struct {
	in  io.Reader
	out io.Writer
	// keys means read single keys. Otherwise it reads whole lines.
	keys  bool
	color bool
	lines *bufio.Reader
	raw   func() (restore func(), err error)
	// pending holds keys read after the last question ended. Fast typing or a paste delivers
	// several at once, and those are the answers to the questions that come next.
	pending []key
}

// New returns a prompter, using keys only when both ends are a real terminal.
func New(in, out *os.File) *Prompter {
	p := &Prompter{in: in, out: out, lines: bufio.NewReader(in)}
	if term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd())) {
		p.keys = true
		p.color = os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
		p.raw = func() (func(), error) {
			st, err := term.MakeRaw(int(in.Fd()))
			if err != nil {
				return nil, err
			}
			return func() { _ = term.Restore(int(in.Fd()), st) }, nil
		}
	}
	return p
}

// Lines returns a prompter that reads plain lines.
func Lines(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: in, out: out, lines: bufio.NewReader(in)}
}

func (p *Prompter) style(code, s string) string {
	if !p.color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Say prints a line of explanation.
func (p *Prompter) Say(format string, args ...any) {
	fmt.Fprintf(p.out, format+"\n", args...)
}

func (p *Prompter) readLine() (string, error) {
	s, err := p.lines.ReadString('\n')
	if err != nil && s == "" {
		if errors.Is(err, io.EOF) {
			return "", ErrNoInput
		}
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// Confirm asks a yes or no question. Enter takes the default.
func (p *Prompter) Confirm(question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	if p.keys {
		return p.confirmKeys(question, hint, def)
	}
	for {
		fmt.Fprintf(p.out, "? %s (%s) ", question, hint)
		s, err := p.readLine()
		if err != nil {
			return false, err
		}
		fmt.Fprintln(p.out)
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.Say("  Please answer y or n.")
	}
}

func (p *Prompter) word(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (p *Prompter) confirmKeys(question, hint string, def bool) (bool, error) {
	fmt.Fprintf(p.out, "%s %s %s ", p.style("36", "?"), p.style("1", question), p.style("2", "("+hint+")"))
	var answer bool
	err := p.withKeys(func(k key) (bool, error) {
		switch k {
		case keyEnter:
			answer = def
		case keyYes:
			answer = true
		case keyNo:
			answer = false
		case keyCancel:
			return true, ErrCanceled
		default:
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		fmt.Fprint(p.out, "\r\n")
		return false, err
	}
	fmt.Fprintf(p.out, "%s\n", p.style("36", p.word(answer)))
	return answer, nil
}

// Select asks for exactly one choice.
func (p *Prompter) Select(question string, options []Option, def int) (int, error) {
	if !p.keys {
		return p.selectLines(question, options, def)
	}
	cursor := def
	drawn := 0
	draw := func() {
		p.clear(drawn)
		drawn = p.drawList(question, p.style("2", "(arrows, Enter)"), options, cursor, nil)
	}
	draw()
	err := p.withKeys(func(k key) (bool, error) {
		switch k {
		case keyUp:
			cursor = (cursor - 1 + len(options)) % len(options)
		case keyDown:
			cursor = (cursor + 1) % len(options)
		case keyEnter:
			return true, nil
		case keyCancel:
			return true, ErrCanceled
		default:
			return false, nil
		}
		draw()
		return false, nil
	})
	p.clear(drawn)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(p.out, "%s %s %s\n", p.style("36", "?"), p.style("1", question), p.style("36", options[cursor].Label))
	return cursor, nil
}

// MultiSelect asks for any number of choices, and returns their positions in order.
func (p *Prompter) MultiSelect(question string, options []Option) ([]int, error) {
	if !p.keys {
		return p.multiLines(question, options)
	}
	checked := make([]bool, len(options))
	for i, o := range options {
		checked[i] = o.Selected
	}
	cursor, drawn := 0, 0
	draw := func() {
		p.clear(drawn)
		drawn = p.drawList(question, p.style("2", "(arrows, Space to toggle, a for all, Enter)"), options, cursor, checked)
	}
	draw()
	err := p.withKeys(func(k key) (bool, error) {
		switch k {
		case keyUp:
			cursor = (cursor - 1 + len(options)) % len(options)
		case keyDown:
			cursor = (cursor + 1) % len(options)
		case keySpace:
			checked[cursor] = !checked[cursor]
		case keyAll:
			all := true
			for _, c := range checked {
				all = all && c
			}
			for i := range checked {
				checked[i] = !all
			}
		case keyEnter:
			return true, nil
		case keyCancel:
			return true, ErrCanceled
		default:
			return false, nil
		}
		draw()
		return false, nil
	})
	p.clear(drawn)
	if err != nil {
		return nil, err
	}
	var out []int
	var names []string
	for i, c := range checked {
		if c {
			out = append(out, i)
			names = append(names, options[i].Label)
		}
	}
	summary := strings.Join(names, ", ")
	if summary == "" {
		summary = "none"
	}
	fmt.Fprintf(p.out, "%s %s %s\n", p.style("36", "?"), p.style("1", question), p.style("36", summary))
	return out, nil
}

// clear removes what was drawn, which is n lines above the cursor.
func (p *Prompter) clear(n int) {
	if n > 0 {
		fmt.Fprintf(p.out, "\x1b[%dA\r\x1b[J", n)
	}
}

// drawList writes the question and options, and returns how many lines that took. It uses
// \r\n because the terminal is in raw mode, and cuts lines to fit, because a wrapped line
// would throw off the count that the next redraw depends on.
func (p *Prompter) drawList(question, hint string, options []Option, cursor int, checked []bool) int {
	width := 100
	if f, ok := p.out.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 10 {
			width = w
		}
	}
	fmt.Fprintf(p.out, "%s %s %s\r\n", p.style("36", "?"), p.style("1", question), hint)
	for i, o := range options {
		mark := " "
		if i == cursor {
			mark = p.style("36", ">")
		}
		box := ""
		if checked != nil {
			box = "[ ] "
			if checked[i] {
				box = p.style("32", "[x]") + " "
			}
		}
		text := o.Label
		room := width - 8 - len(o.Label)
		detail := o.Detail
		if len(detail) > room {
			detail = detail[:max(room, 0)]
		}
		if detail != "" {
			text += "  " + p.style("2", detail)
		}
		fmt.Fprintf(p.out, "%s %s%s\r\n", mark, box, text)
	}
	return len(options) + 1
}

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keySpace
	keyEnter
	keyAll
	keyYes
	keyNo
	keyCancel
)

// parseKeys splits one read into keys. Fast typing or a paste can deliver several at once. An
// arrow's bytes arrive together, which is what tells it apart from a lone Esc.
func parseKeys(b []byte) []key {
	var out []key
	for len(b) > 0 {
		if b[0] == 0x1b && len(b) >= 3 && (b[1] == '[' || b[1] == 'O') {
			switch b[2] {
			case 'A':
				out = append(out, keyUp)
			case 'B':
				out = append(out, keyDown)
			default:
				out = append(out, keyNone)
			}
			b = b[3:]
			continue
		}
		switch b[0] {
		case 'k':
			out = append(out, keyUp)
		case 'j':
			out = append(out, keyDown)
		case ' ':
			out = append(out, keySpace)
		case '\r', '\n':
			out = append(out, keyEnter)
		case 'a', 'A':
			out = append(out, keyAll)
		case 'y', 'Y':
			out = append(out, keyYes)
		case 'n', 'N':
			out = append(out, keyNo)
		case 0x03, 0x1b, 'q', 'Q':
			out = append(out, keyCancel)
		default:
			out = append(out, keyNone)
		}
		b = b[1:]
	}
	return out
}

// withKeys hands each key to fn until it says it has finished, with the terminal in raw
// mode and the cursor hidden. The terminal is put back however this ends.
func (p *Prompter) withKeys(fn func(key) (bool, error)) error {
	if p.raw != nil {
		restore, err := p.raw()
		if err != nil {
			return err
		}
		defer restore()
	}
	fmt.Fprint(p.out, "\x1b[?25l")
	defer fmt.Fprint(p.out, "\x1b[?25h")
	buf := make([]byte, 64)
	for {
		for len(p.pending) > 0 {
			k := p.pending[0]
			p.pending = p.pending[1:]
			done, ferr := fn(k)
			if done || ferr != nil {
				return ferr
			}
		}
		n, err := p.in.Read(buf)
		p.pending = append(p.pending, parseKeys(buf[:n])...)
		if err != nil && len(p.pending) == 0 {
			if errors.Is(err, io.EOF) {
				return ErrNoInput
			}
			return err
		}
	}
}

func (p *Prompter) printNumbered(options []Option, checked []bool) {
	for i, o := range options {
		box := ""
		if checked != nil {
			box = "[ ] "
			if checked[i] {
				box = "[x] "
			}
		}
		line := fmt.Sprintf("  %d. %s%s", i+1, box, o.Label)
		if o.Detail != "" {
			line += "  " + o.Detail
		}
		p.Say("%s", line)
	}
}

func (p *Prompter) selectLines(question string, options []Option, def int) (int, error) {
	p.Say("? %s", question)
	p.printNumbered(options, nil)
	for {
		fmt.Fprintf(p.out, "Number (%d): ", def+1)
		s, err := p.readLine()
		if err != nil {
			return 0, err
		}
		fmt.Fprintln(p.out)
		if s == "" {
			return def, nil
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		p.Say("  Please give a number from 1 to %d.", len(options))
	}
}

func (p *Prompter) multiLines(question string, options []Option) ([]int, error) {
	checked := make([]bool, len(options))
	for i, o := range options {
		checked[i] = o.Selected
	}
	p.Say("? %s", question)
	p.printNumbered(options, checked)
	for {
		fmt.Fprint(p.out, "Numbers such as 1,3, or all or none (Enter keeps the marked ones): ")
		s, err := p.readLine()
		if err != nil {
			return nil, err
		}
		fmt.Fprintln(p.out)
		out, ok := parseChoices(s, len(options), checked)
		if ok {
			return out, nil
		}
		p.Say("  Please give numbers from 1 to %d.", len(options))
	}
}

func parseChoices(s string, n int, checked []bool) ([]int, bool) {
	out := []int{}
	switch strings.ToLower(s) {
	case "":
		for i, c := range checked {
			if c {
				out = append(out, i)
			}
		}
		return out, true
	case "none":
		return out, true
	case "all":
		for i := range n {
			out = append(out, i)
		}
		return out, true
	}
	seen := make([]bool, n)
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		v, err := strconv.Atoi(part)
		if err != nil || v < 1 || v > n {
			return nil, false
		}
		seen[v-1] = true
	}
	for i, on := range seen {
		if on {
			out = append(out, i)
		}
	}
	return out, true
}
