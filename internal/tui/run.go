package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Grano22/aiculator/pkg/observe"
)

// Fetch gets the latest snapshot.
type Fetch func(ctx context.Context) (observe.Snapshot, error)

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

// fit cuts a painted line to the terminal width, counting only visible characters, so a
// long line cannot wrap and throw off the whole screen.
func fit(line string, width int) string {
	if len([]rune(ansiSeq.ReplaceAllString(line, ""))) <= width {
		return line
	}
	var b strings.Builder
	seen := 0
	for i := 0; i < len(line); {
		if loc := ansiSeq.FindStringIndex(line[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(line[i : i+loc[1]])
			i += loc[1]
			continue
		}
		r := []rune(line[i:])[0]
		if seen < width {
			b.WriteRune(r)
			seen++
		}
		i += len(string(r))
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

func draw(out io.Writer, text string, width int) {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(fit(line, width))
		b.WriteString("\x1b[K") // Clear what the last frame left to the right.
	}
	b.WriteString("\x1b[J")
	_, _ = io.WriteString(out, b.String())
}

// Run shows the dashboard until the user quits or ctx ends. It needs a real terminal.
func Run(ctx context.Context, fetch Fetch, interval time.Duration, color bool) error {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("the dashboard needs a terminal; use --once or --json: %w", err)
	}
	// However this ends, the terminal goes back to how it was.
	defer func() { _ = term.Restore(fd, old) }()
	out := os.Stdout
	_, _ = io.WriteString(out, "\x1b[?1049h\x1b[?25l")
	defer func() { _, _ = io.WriteString(out, "\x1b[?25h\x1b[?1049l") }()

	keys := make(chan string, 16)
	go readKeys(os.Stdin, keys)

	var snap observe.Snapshot
	o := Options{Color: color}
	refresh := func() {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		got, err := fetch(c)
		if err != nil {
			o.Stale = err.Error()
			return
		}
		snap, o.Stale = got, ""
	}
	paint := func() {
		o.Width, o.Height = ScreenSize(term.GetSize(int(out.Fd())))
		draw(out, Render(snap, o), o.Width)
	}
	refresh()
	paint()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if !o.Paused {
				refresh()
			}
			paint()
		case k, ok := <-keys:
			if !ok {
				return nil
			}
			if handleKey(k, &o) {
				return nil
			}
			paint()
		}
	}
}

// ScreenSize falls back to a usable size. Some terminals report 0x0 with no error, which
// taken at face value would cut every line to nothing and draw a blank screen.
func ScreenSize(w, h int, err error) (int, int) {
	if err != nil || w <= 0 || h <= 0 {
		return 100, 40
	}
	return w, h
}

// handleKey applies one key and reports whether to quit. In raw mode Ctrl-C arrives as
// a byte rather than a signal, so it has to be handled here.
func handleKey(k string, o *Options) (quit bool) {
	switch k {
	case "q", "Q", "\x03", "\x04":
		return true
	case "1", "2", "3", "4", "5":
		o.View, o.Scroll = View(k[0]-'1'), 0
	case "\t", "l", "\x1b[C":
		o.View, o.Scroll = (o.View+1)%View(len(viewNames)), 0
	case "\x1b[Z", "h", "\x1b[D":
		o.View, o.Scroll = (o.View+View(len(viewNames))-1)%View(len(viewNames)), 0
	case "j", "\x1b[B":
		o.Scroll++
	case "k", "\x1b[A":
		o.Scroll = max(o.Scroll-1, 0)
	case "p", " ":
		o.Paused = !o.Paused
	}
	return false
}

func readKeys(in io.Reader, keys chan<- string) {
	defer close(keys)
	buf := make([]byte, 16)
	for {
		n, err := in.Read(buf)
		if err != nil {
			return
		}
		select {
		case keys <- string(buf[:n]):
		default: // Nobody is reading; a dropped key does no harm.
		}
	}
}
