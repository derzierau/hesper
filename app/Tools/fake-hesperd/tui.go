package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// runTUI is a fake agent interface. It never runs a real agent; it draws
// something that looks and behaves like a full-screen TUI:
//
//	agent: a Claude-like screen (log lines, spinner, input box) at a calm pace
//	flood: redraws every cell every frame at --fps (perf stress)
//
// Both echo typed input on the last row as "❯ <input>" immediately, which is
// what the app's latency probe watches for.
func runTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	mode := fs.String("mode", "agent", "agent | flood")
	name := fs.String("name", "agent", "display name")
	fps := fs.Int("fps", 120, "frames per second (flood)")
	_ = fs.Parse(args)

	in := int(os.Stdin.Fd())
	restore, _ := makeRaw(in)
	defer restore()

	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	t := &tui{out: out, name: *name, mode: *mode}
	t.cols, t.rows, _ = getWinsize(in)
	if t.cols <= 0 {
		t.cols, t.rows = 80, 24
	}
	// The agent mode draws inline on the main screen like Claude Code: new
	// log lines scroll the screen (into the scrollback); flood is a
	// full-screen program on the alternate screen.
	if t.mode == "flood" {
		out.WriteString("\x1b[?1049h")
	}
	// Bracketed paste on, like Claude and Codex: a paste shows in the
	// input as ⟦text⟧ (tests see dropped files arrive as one paste).
	out.WriteString("\x1b[?25h\x1b[?2004h")
	t.redraw()

	winch := make(chan os.Signal, 4)
	signal.Notify(winch, syscall.SIGWINCH)
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	input := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				close(input)
				return
			}
			b := make([]byte, n)
			copy(b, buf[:n])
			input <- b
		}
	}()

	interval := 100 * time.Millisecond
	if *mode == "flood" && *fps > 0 {
		interval = time.Second / time.Duration(*fps)
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	logTick := time.NewTicker(450 * time.Millisecond)
	defer logTick.Stop()

	for {
		select {
		case <-term:
			if t.mode == "flood" {
				out.WriteString("\x1b[?1049l")
			}
			out.Flush()
			return nil
		case <-winch:
			t.cols, t.rows, _ = getWinsize(in)
			t.redraw()
		case b, ok := <-input:
			if !ok {
				return nil
			}
			t.handleInput(b)
		case <-tick.C:
			t.frame++
			if t.mode == "flood" {
				t.drawFlood()
			} else {
				t.drawSpinner()
			}
			t.drawPrompt()
			out.Flush()
		case <-logTick.C:
			if t.mode == "agent" {
				t.scrollIn(cannedLog[t.logN%len(cannedLog)])
				t.logN++
				t.drawPrompt()
				out.Flush()
			}
		}
	}
}

type tui struct {
	out        *bufio.Writer
	name, mode string
	cols, rows int
	frame      int
	logN       int
	log        []string
	input      []rune
	inPaste    bool
}

var cannedLog = []string{
	"\x1b[38;2;192;202;245m● Read(src/push/PushMessaging.kt)\x1b[0m",
	"  \x1b[38;2;86;95;137m⎿  Read 214 lines\x1b[0m",
	"\x1b[38;2;192;202;245m● Bash(./gradlew :core:push:test)\x1b[0m",
	"  \x1b[38;2;86;95;137m⎿  \x1b[38;2;158;206;106m214 tests completed, 0 failed\x1b[0m",
	"\x1b[38;2;192;202;245m● Update(PushMessaging.kt)\x1b[0m",
	"  \x1b[38;2;86;95;137m⎿  Added 18 lines, removed 3 lines\x1b[0m",
	"\x1b[38;2;122;162;247m● I'll wire the FCM token refresh into the provider next.\x1b[0m",
	"",
}

func (t *tui) handleInput(b []byte) {
	for len(b) > 0 {
		if bytes.HasPrefix(b, []byte("\x1b[200~")) {
			t.inPaste = true
			t.input = append(t.input, '⟦')
			b = b[6:]
			continue
		}
		if bytes.HasPrefix(b, []byte("\x1b[201~")) {
			t.inPaste = false
			t.input = append(t.input, '⟧')
			b = b[6:]
			continue
		}
		r, n := utf8.DecodeRune(b)
		if t.inPaste {
			b = b[n:]
			if r >= 0x20 {
				t.input = append(t.input, r)
			} else if r == '\n' || r == '\r' {
				t.input = append(t.input, '⏎')
			}
			continue
		}
		b = b[n:]
		switch {
		case r == '\r' || r == '\n':
			if len(t.input) > 0 {
				t.scrollIn("\x1b[38;2;224;175;104m> " + string(t.input) + "\x1b[0m")
			}
			t.input = t.input[:0]
		case r == 0x7f || r == 0x08:
			if len(t.input) > 0 {
				t.input = t.input[:len(t.input)-1]
			}
		case r == 0x1b:
			// ← → show as ‹ › in the input (tests see that plain arrows
			// reach the agent); other escape sequences are ignored.
			if len(b) >= 2 && (b[0] == '[' || b[0] == 'O') {
				switch b[1] {
				case 'C':
					t.input = append(t.input, '›')
				case 'D':
					t.input = append(t.input, '‹')
				}
			}
			b = nil
		case r >= 0x20:
			t.input = append(t.input, r)
		}
	}
	t.drawPrompt()
	t.out.Flush()
}

func (t *tui) addLog(line string) {
	t.log = append(t.log, line)
	if len(t.log) > 500 {
		t.log = t.log[len(t.log)-500:]
	}
}

// scrollIn adds a log line the way an inline TUI does: the log rows
// (1..rows-3, a region from the top) scroll up one, the top one into the
// terminal's scrollback, and the line appears at the bottom of the log.
func (t *tui) scrollIn(line string) {
	t.addLog(line)
	bottom := t.rows - 3
	if bottom < 1 {
		return
	}
	fmt.Fprintf(t.out, "\x1b[1;%dr\x1b[%d;1H\n\x1b[r", bottom, bottom)
	t.moveTo(bottom, 1)
	t.out.WriteString("\x1b[2K" + clipANSI(line, t.cols))
}

func (t *tui) moveTo(row, col int) { fmt.Fprintf(t.out, "\x1b[%d;%dH", row, col) }

func (t *tui) redraw() {
	t.out.WriteString("\x1b[0m\x1b[2J")
	if t.mode == "flood" {
		t.drawFlood()
	} else {
		t.drawLog()
		t.drawHeader()
		t.drawSpinner()
	}
	t.drawPrompt()
	t.out.Flush()
}

// drawHeader: the status line between the spinner and the prompt.
func (t *tui) drawHeader() {
	if t.rows < 4 {
		return
	}
	t.moveTo(t.rows-1, 1)
	header := fmt.Sprintf(" ✻ fake agent · %s · %dx%d", t.name, t.cols, t.rows)
	t.out.WriteString("\x1b[2K\x1b[38;2;187;154;247m" + clip(header, t.cols) + "\x1b[0m")
}

func (t *tui) drawLog() {
	top, bottom := 1, t.rows-3
	if bottom < top {
		return
	}
	n := bottom - top + 1
	start := len(t.log) - n
	for i := 0; i < n; i++ {
		t.moveTo(top+i, 1)
		t.out.WriteString("\x1b[2K")
		if j := start + i; j >= 0 && j < len(t.log) {
			t.out.WriteString(clipANSI(t.log[j], t.cols))
		}
	}
}

var spinner = []string{"·", "✢", "✳", "✶", "✻", "✽"}

func (t *tui) drawSpinner() {
	if t.rows < 4 {
		return
	}
	t.moveTo(t.rows-2, 1)
	s := fmt.Sprintf("\x1b[38;2;224;175;104m%s Working… (%ds · esc to interrupt)\x1b[0m\x1b[K", spinner[t.frame%len(spinner)], t.frame/10)
	t.out.WriteString(s)
}

func (t *tui) drawFlood() {
	last := t.rows - 1
	for r := 1; r <= last; r++ {
		t.moveTo(r, 1)
		c := (r*7 + t.frame) % 216
		fmt.Fprintf(t.out, "\x1b[38;5;%dm", 16+c)
		line := fmt.Sprintf("%05d %s %03d ", t.frame, t.name, r)
		pad := t.cols - len(line)
		if pad < 0 {
			line = line[:t.cols]
			pad = 0
		}
		t.out.WriteString(line)
		ch := byte('a' + (t.frame+r)%26)
		t.out.WriteString(strings.Repeat(string(ch), pad))
	}
	t.out.WriteString("\x1b[0m")
}

func (t *tui) drawPrompt() {
	t.moveTo(t.rows, 1)
	t.out.WriteString("\x1b[2K\x1b[38;2;122;162;247m❯\x1b[0m " + clip(string(t.input), t.cols-3))
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > n {
		r = r[len(r)-n:]
	}
	return string(r)
}

// clipANSI truncates visible characters to n, keeping escape sequences.
func clipANSI(s string, n int) string {
	var b strings.Builder
	visible := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		if visible >= n {
			continue
		}
		b.WriteRune(r)
		visible++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}
