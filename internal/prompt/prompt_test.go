package prompt

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

var opts = []Option{{Label: "claude", Selected: true}, {Label: "opencode", Detail: "skills and MCP", Selected: true}, {Label: "pi"}}

func TestLinesConfirm(t *testing.T) {
	for in, want := range map[string]bool{"\n": true, "y\n": true, "YES\n": true, "n\n": false, "No\n": false, "maybe\ny\n": true} {
		var out bytes.Buffer
		got, err := Lines(strings.NewReader(in), &out).Confirm("Sure?", true)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if got, _ := Lines(strings.NewReader("\n"), io.Discard).Confirm("Sure?", false); got {
		t.Error("Enter should take the default of no")
	}
}

func TestLinesMultiSelect(t *testing.T) {
	for in, want := range map[string][]int{
		"\n": {0, 1}, "all\n": {0, 1, 2}, "none\n": {}, "3\n": {2}, "1, 3\n": {0, 2}, "3 1\n": {0, 2}, "9\n2\n": {1}, "x\n1\n": {0},
	} {
		got, err := Lines(strings.NewReader(in), io.Discard).MultiSelect("Which?", opts)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
}

func TestLinesSelect(t *testing.T) {
	for in, want := range map[string]int{"\n": 1, "3\n": 2, "0\n1\n": 0} {
		got, err := Lines(strings.NewReader(in), io.Discard).Select("One?", opts, 1)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
}

func TestEndOfInputIsAnErrorAndNotAYes(t *testing.T) {
	p := Lines(strings.NewReader(""), io.Discard)
	if _, err := p.Confirm("Sure?", true); !errors.Is(err, ErrNoInput) {
		t.Errorf("confirm: %v", err)
	}
	if _, err := p.MultiSelect("Which?", opts); !errors.Is(err, ErrNoInput) {
		t.Errorf("multi: %v", err)
	}
	if _, err := p.Select("One?", opts, 0); !errors.Is(err, ErrNoInput) {
		t.Errorf("select: %v", err)
	}
}

// keyReader hands over one key per read, as a terminal does.
type keyReader struct{ keys []string }

func (k *keyReader) Read(b []byte) (int, error) {
	if len(k.keys) == 0 {
		return 0, io.EOF
	}
	n := copy(b, k.keys[0])
	k.keys = k.keys[1:]
	return n, nil
}

func keyed(out io.Writer, keys ...string) *Prompter {
	return &Prompter{in: &keyReader{keys}, out: out, keys: true}
}

const (
	up    = "\x1b[A"
	down  = "\x1b[B"
	enter = "\r"
	space = " "
)

func TestKeysMultiSelect(t *testing.T) {
	for name, c := range map[string]struct {
		keys []string
		want []int
	}{
		"keep the defaults":    {[]string{enter}, []int{0, 1}},
		"toggle one off":       {[]string{space, enter}, []int{1}},
		"down and toggle on":   {[]string{down, down, space, enter}, []int{0, 1, 2}},
		"wraps upwards":        {[]string{up, space, enter}, []int{0, 1, 2}},
		"all on":               {[]string{"a", enter}, []int{0, 1, 2}},
		"all on, then all off": {[]string{"a", "a", enter}, nil},
		"vim keys":             {[]string{"j", space, "k", space, enter}, nil},
		"unknown keys ignored": {[]string{"x", "\x1b[C", enter}, []int{0, 1}},
	} {
		got, err := keyed(io.Discard, c.keys...).MultiSelect("Which?", opts)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

func TestKeysSelectAndConfirm(t *testing.T) {
	if got, err := keyed(io.Discard, down, enter).Select("One?", opts, 0); err != nil || got != 1 {
		t.Errorf("%d %v", got, err)
	}
	if got, err := keyed(io.Discard, up, enter).Select("One?", opts, 0); err != nil || got != 2 {
		t.Errorf("wrap: %d %v", got, err)
	}
	for keys, want := range map[string]bool{"y": true, "n": false, enter: true} {
		if got, err := keyed(io.Discard, keys).Confirm("Sure?", true); err != nil || got != want {
			t.Errorf("%q: %v %v", keys, got, err)
		}
	}
	if got, err := keyed(io.Discard, "x", "n").Confirm("Sure?", true); err != nil || got {
		t.Errorf("other keys should be ignored: %v %v", got, err)
	}
}

func TestEveryWayToBackOut(t *testing.T) {
	for _, k := range []string{"\x03", "\x1b", "q"} {
		if _, err := keyed(io.Discard, k).MultiSelect("Which?", opts); !errors.Is(err, ErrCanceled) {
			t.Errorf("multi %q: %v", k, err)
		}
		if _, err := keyed(io.Discard, k).Select("One?", opts, 0); !errors.Is(err, ErrCanceled) {
			t.Errorf("select %q: %v", k, err)
		}
		if _, err := keyed(io.Discard, k).Confirm("Sure?", true); !errors.Is(err, ErrCanceled) {
			t.Errorf("confirm %q: %v", k, err)
		}
	}
}

// The terminal must come back to normal however a question ends.
func TestCursorAndTerminalAreRestored(t *testing.T) {
	for _, k := range []string{enter, "\x03"} {
		var out bytes.Buffer
		restored := false
		p := keyed(&out, k)
		p.raw = func() (func(), error) { return func() { restored = true }, nil }
		_, _ = p.MultiSelect("Which?", opts)
		if !restored {
			t.Errorf("%q: raw mode was left on", k)
		}
		if !strings.Contains(out.String(), "\x1b[?25h") {
			t.Errorf("%q: the cursor stayed hidden", k)
		}
	}
}

func TestAnswerIsShownAfterChoosing(t *testing.T) {
	var out bytes.Buffer
	if _, err := keyed(&out, enter).MultiSelect("Which?", opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "claude, opencode\n") {
		t.Errorf("%q", out.String())
	}
}

// Fast typing or a paste delivers several keys in one read.
func TestSeveralKeysInOneRead(t *testing.T) {
	got, err := keyed(io.Discard, down+space+enter).MultiSelect("Which?", opts)
	if err != nil || !slices.Equal(got, []int{0}) {
		t.Errorf("%v %v", got, err)
	}
	if _, err := keyed(io.Discard, down+"\x03").Select("One?", opts, 0); !errors.Is(err, ErrCanceled) {
		t.Errorf("%v", err)
	}
}

// Keys after the end of one question answer the next. Losing them left the next question
// waiting forever for a key that had already been pressed.
func TestPastedKeysCarryOverToTheNextQuestion(t *testing.T) {
	p := keyed(io.Discard, down+space+enter+"n"+"y"+down+enter)
	got, err := p.MultiSelect("Which?", opts)
	if err != nil || !slices.Equal(got, []int{0}) {
		t.Fatalf("%v %v", got, err)
	}
	if a, err := p.Confirm("One?", true); err != nil || a {
		t.Errorf("first answer: %v %v", a, err)
	}
	if a, err := p.Confirm("Two?", false); err != nil || !a {
		t.Errorf("second answer: %v %v", a, err)
	}
	if i, err := p.Select("Three?", opts, 0); err != nil || i != 1 {
		t.Errorf("third answer: %d %v", i, err)
	}
	if _, err := p.Confirm("Four?", true); !errors.Is(err, ErrNoInput) {
		t.Errorf("with no keys left it should end, not hang: %v", err)
	}
}
