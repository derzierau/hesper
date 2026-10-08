package vt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Ground truth: tmux itself. Each case's bytes are written raw (no output
// processing) into a pane of a private tmux server; the emulator fed the
// same bytes must show what tmux's capture of the pane shows (loaded into
// a second emulator), cell by cell, with the same cursor and modes.

type tmuxServer struct {
	socket string
	dir    string
}

func newTmux(t *testing.T) *tmuxServer {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "hesper-vt-")
	if err != nil {
		t.Fatal(err)
	}
	s := &tmuxServer{socket: filepath.Join(dir, "s"), dir: dir}
	s.run(t, "-f", "/dev/null", "new-session", "-d", "-s", "keep", "exec sleep 600")
	t.Cleanup(func() {
		exec.Command("tmux", "-S", s.socket, "kill-server").Run()
		os.RemoveAll(dir)
	})
	return s
}

func (s *tmuxServer) run(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-S", s.socket}, args...)...)
	cmd.Env = append(os.Environ(), "TMUX=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v %s", args, err, out)
	}
	return string(out)
}

// truth writes data into a fresh w×h pane and returns tmux's screen of it
// loaded into an emulator.
func (s *tmuxServer) truth(t *testing.T, name string, w, h int, data []byte) *Screen {
	t.Helper()
	file := filepath.Join(s.dir, name)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	s.run(t, "new-session", "-d", "-s", name, "-x", fmt.Sprint(w), "-y", fmt.Sprint(h),
		"stty raw -echo; cat '"+file+"'; tmux wait-for -S '"+name+"'; exec sleep 600")
	s.run(t, "wait-for", name)
	return s.capture(t, name)
}

func (s *tmuxServer) capture(t *testing.T, target string) *Screen {
	t.Helper()
	line := strings.TrimSpace(s.run(t, "display-message", "-p", "-t", target, SeedFormat))
	seed, ok := ParseSeed(line)
	if !ok {
		t.Fatalf("seed %q", line)
	}
	rows := strings.Split(strings.TrimSuffix(s.run(t, "capture-pane", "-p", "-e", "-N", "-t", target), "\n"), "\n")
	got := New(1, 1)
	got.Load(rows, seed)
	return got
}

func row(s *Screen, y int) string { r, _ := s.RowANSI(y, 0); return r }

// same reports the first difference between two screens.
func same(want, got *Screen) string {
	if want.w != got.w || want.h != got.h {
		return fmt.Sprintf("size %dx%d, want %dx%d", got.w, got.h, want.w, want.h)
	}
	for y := range want.h {
		for x := range want.w {
			a, b := want.grid[y][x], got.grid[y][x]
			if a != b {
				return fmt.Sprintf("row %d col %d: got %+v want %+v\n got  %q\n want %q\n got  %q\n want %q", y, x, b, a,
					got.Text(y), want.Text(y), row(got, y), row(want, y))
			}
		}
	}
	if want.x != got.x || want.y != got.y {
		return fmt.Sprintf("cursor %d,%d want %d,%d", got.x, got.y, want.x, want.y)
	}
	wm, gm := want.Modes(), got.Modes()
	gm.Sync = false
	// What a tmux capture cannot carry (the host never loaded these).
	gm.Mouse, gm.MouseFormat, gm.Focus, gm.Keyboard, gm.CursorStyle = 0, 0, false, 0, 0
	wm.Mouse, wm.MouseFormat, wm.Focus, wm.Keyboard, wm.CursorStyle = 0, 0, false, 0, 0
	if wm != gm {
		return fmt.Sprintf("modes %+v want %+v", gm, wm)
	}
	return ""
}

var cases = []struct {
	name string
	w, h int
	data string
}{
	{"text-wrap-scroll", 20, 5, "hello\r\nworld\r\n" + strings.Repeat("0123456789", 5) + "\r\nline4\r\nline5\r\nline6\r\nend"},
	{"sgr", 60, 6, "\x1b[1mbold\x1b[22m \x1b[2mdim\x1b[0m \x1b[3mit\x1b[23m \x1b[4mul\x1b[24m \x1b[4:3mcurly\x1b[4:0m \x1b[21mdbl\x1b[0m \x1b[7mrev\x1b[27m \x1b[9mstrike\x1b[29m\r\n" +
		"\x1b[31mred\x1b[39m \x1b[42mgreen-bg\x1b[49m \x1b[91mbright\x1b[0m \x1b[38;5;208m208\x1b[48;5;17mon17\x1b[0m \x1b[38;2;10;200;30mrgb\x1b[0m \x1b[38:2::1:2:3mcolon\x1b[0m \x1b[38:5:99mc99\x1b[m\r\n" +
		"\x1b[1;4;31;44mmix\x1b[0m \x1b[5mblink\x1b[25m \x1b[8mhid\x1b[28m \x1b[53mover\x1b[55m end"},
	{"wide", 12, 6, "漢字テスト\r\n😀x😀\r\nabcdefghijk😀\r\n" + "é ñ\r\nab漢cd\x1b[1;3Hx\r\n" + "❤️|❤|\U0001F44D\U0001F3FD|"},
	{"wide-edge", 5, 4, "abcd漢\r\n\x1b[3;1H漢字漢\x1b[4;2Hx"},
	{"cursor", 30, 10, "\x1b[5;10Hmid\x1b[2Aup\x1b[3Bdn\x1b[4Dleft\x1b[10Cright\x1b[1;1Htop\x1b[8Gcha\x1b[9dvpa\x1b[3`hpa" +
		"\x1b7\x1b[10;20Hsaved\x1b8back\x1b[s\x1b[2;2Hxx\x1b[uyy\x1b[2Ecnl\x1b[1Fcpl"},
	{"erase-bce", 20, 6, "AAAAAAAAAAAAAAAAAAAA\r\nBBBBBBBBBBBBBBBBBBBB\r\nCCCCCCCCCCCCCCCCCCCC\r\nDDDDDDDDDDDDDDDDDDDD\r\nEEEEEEEEEEEEEEEEEEEE" +
		"\x1b[44m\x1b[1;5H\x1b[K\x1b[2;5H\x1b[1K\x1b[3;3H\x1b[2K\x1b[4;5H\x1b[3X\x1b[0m\x1b[5;10H\x1b[1J"},
	{"erase-screen", 20, 6, "1\r\n2\r\n3\r\n4\r\n5\x1b[3;1H\x1b[0J\x1b[1;1H\x1b[45m\x1b[2J\x1b[0mx"},
	{"insert-delete", 20, 6, "0123456789\x1b[1;3H\x1b[2@\x1b[1;8H\x1b[3P\r\n" + "abcdefghij\x1b[2;3H\x1b[4hXY\x1b[4l" +
		"\r\nl3\r\nl4\r\nl5\r\nl6\x1b[3;1H\x1b[2L\x1b[5;1H\x1b[1M"},
	{"scroll-region", 20, 8, "r1\r\nr2\r\nr3\r\nr4\r\nr5\r\nr6\r\nr7\r\nr8\x1b[3;6r\x1b[6;1H\nnew1\nnew2\x1b[3;1H\x1bMrev\x1b[2Sx\x1b[1Ty\x1b[r\x1b[8;1Hlast"},
	{"origin", 20, 8, "\x1b[3;6r\x1b[?6h\x1b[1;1Ho1\x1b[10;1Ho2\x1b[?6l\x1b[r\x1b[8;8Hplain"},
	{"alt-screen", 20, 5, "main1\r\nmain2\x1b[?1049h\x1b[1;1Halt\x1b[2;2Hscreen\x1b[?1049lback"},
	{"alt-47", 20, 5, "keep\x1b[?47hgone\x1b[?47l!"},
	{"alt-stays", 20, 5, "main\x1b[?1049h\x1b[H\x1b[2Jinside alt\x1b[3;3Hcur"},
	{"linedraw", 20, 4, "\x1b(0lqqqk\r\nx   x\r\nmqqqj\x1b(B ok\x1b)0\x0eqq\x0f!"},
	{"tabs", 40, 4, "a\tb\tc\r\n\x1b[1;5H\x1bH\r\n\tx\x1b[3g\r\n1\t2\x1b[2;30H\x1b[2Zz"},
	{"modes", 20, 4, "\x1b[?1h\x1b=\x1b[?2004h\x1b[?25lx"},
	{"modes-off", 20, 4, "\x1b[?1h\x1b=\x1b[?2004h\x1b[?25l\x1b[?1l\x1b>\x1b[?2004l\x1b[?25hy"},
	{"autowrap-off", 10, 4, "\x1b[?7l0123456789ABC\r\n\x1b[?7h0123456789ABC"},
	{"rep-decaln", 10, 4, "\x1b#8\x1b[2;1Hx\x1b[4b"},
	{"osc-dcs-ignored", 30, 4, "\x1b]0;title\x07a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\b\x1bPq#0\x1b\\c\x1b[>4;1md\x1b[ 2qe\x1b[?2026hf\x1b[?2026l"},
	// An Ink-style (Claude Code) redraw: erase the previous frame line by
	// line, then draw the new one, inside a synchronized update.
	{"ink-redraw", 40, 10, "\x1b[?2026h\x1b[38;2;215;119;87m✻\x1b[39m Thinking…\r\n\x1b[2m  esc to interrupt\x1b[22m\r\n" +
		"\x1b[?2026l\x1b[?2026h\x1b[2K\x1b[1A\x1b[2K\x1b[G\x1b[38;2;215;119;87m✶\x1b[39m Writing…\r\n\x1b[2m  esc to interrupt\x1b[22m\r\n" +
		"\x1b[48;5;236m> \x1b[0m\x1b[7m \x1b[27m\x1b[?2026l"},
	// A ratatui-style (Codex) inline viewport: history inserted above it
	// through a scroll region and reverse index.
	{"codex-insert", 30, 10, "\x1b[7;1H\x1b[1m› \x1b[0mprompt\x1b[8;1H\x1b[2mstatus\x1b[0m" +
		"\x1b[1;6r\x1b[6;1H\r\nhistory one\r\nhistory two\x1b[r\x1b[1;5r\x1b[1;1H\x1bM\x1bMtop\x1b[r\x1b[7;9H"},
	{"zwj-flags", 20, 4, "\U0001F469\u200d\U0001F4BB|\U0001F1E9\U0001F1EA|\U0001F44D\U0001F3FD|x\r\n\u263a\ufe0f|\u2603|\u00e9\u0301\u0302|"},
	{"utf8-invalid", 20, 3, "a\xffb\xe2\x82c\xc3\xa9"},
}

func TestEmulatorMatchesTmux(t *testing.T) {
	s := newTmux(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := s.truth(t, c.name, c.w, c.h, []byte(c.data))
			got := New(c.w, c.h)
			got.WriteString(c.data)
			if diff := same(want, got); diff != "" {
				t.Fatal(diff)
			}
			// Fed a byte at a time (output arrives in arbitrary chunks).
			split := New(c.w, c.h)
			for i := 0; i < len(c.data); i++ {
				split.Write([]byte{c.data[i]})
			}
			if diff := same(got, split); diff != "" {
				t.Fatal("split: " + diff)
			}
		})
	}
}

// After a capture is loaded, further output continues from tmux's state.
func TestLoadThenContinue(t *testing.T) {
	s := newTmux(t)
	first := "\x1b[1;31mred line\r\n\x1b[3;10r\x1b[5;1Hin region\x1b[?1h"
	rest := "\x1b[0m\r\nmore\r\nand more\r\n\x1b[4;4Hx\x1b[r\x1b[10;1Hend"
	file := filepath.Join(s.dir, "cont")
	os.WriteFile(file, []byte(first), 0600)
	os.WriteFile(file+"2", []byte(rest), 0600)
	s.run(t, "new-session", "-d", "-s", "cont", "-x", "20", "-y", "10",
		"stty raw -echo; cat '"+file+"'; tmux wait-for -S one; tmux wait-for two; cat '"+file+"2'; tmux wait-for -S three; exec sleep 600")
	s.run(t, "wait-for", "one")
	got := s.capture(t, "cont")
	s.run(t, "wait-for", "-S", "two")
	s.run(t, "wait-for", "three")
	got.WriteString(rest)
	if diff := same(s.capture(t, "cont"), got); diff != "" {
		t.Fatal(diff)
	}
}

// A pane resized on the host: the next load takes the new size.
func TestLoadAfterResize(t *testing.T) {
	s := newTmux(t)
	want := s.truth(t, "resize", 30, 8, []byte("\x1b[44mblue\x1b[0m wide 漢字\r\nsecond line that is long"))
	s.run(t, "resize-window", "-t", "resize", "-x", "12", "-y", "5")
	time.Sleep(100 * time.Millisecond)
	got := s.capture(t, "resize")
	if w, h := got.Size(); w != 12 || h != 5 || want.w != 30 {
		t.Fatalf("size %dx%d", w, h)
	}
	if !strings.Contains(got.Text(0)+got.Text(1)+got.Text(2), "second line") {
		t.Fatalf("content lost: %q", got.Text(0))
	}
}

func TestLeavingAnUnknownAlternateScreenAsksForResync(t *testing.T) {
	s := New(10, 3)
	s.Load([]string{"alt"}, Seed{Width: 10, Height: 3, Alt: true, Wrap: true, Top: 0, Bottom: 2})
	if s.Resync || !s.Modes().Alt {
		t.Fatal("loaded")
	}
	s.WriteString("\x1b[?1049l")
	if !s.Resync || s.Modes().Alt {
		t.Fatal("no resync request")
	}
}

func TestRowANSIClips(t *testing.T) {
	s := New(10, 1)
	s.WriteString("ab漢\x1b[44mcd\x1b[0m  ")
	for limit, want := range map[int]string{0: "ab漢\x1b[0;44mcd", 3: "ab", 4: "ab漢", 5: "ab漢\x1b[0;44mc"} {
		if got, end := s.RowANSI(0, limit); got != want || limit > 0 && end > limit {
			t.Errorf("limit %d: %q %d, want %q", limit, got, end, want)
		}
	}
	if _, end := s.RowANSI(0, 0); end != 6 {
		t.Errorf("end %d", end)
	}
}

func TestSGRRoundTrip(t *testing.T) {
	for _, a := range []Attr{{}, {Flags: Bold | Italic, Underline: 3, FG: Indexed(1), BG: Indexed(12)}, {FG: Indexed(200), BG: RGB(1, 2, 3), UL: RGB(9, 8, 7), Underline: 1},
		{Flags: Dim | Reverse | Strike | Hidden | Blink | Overline, Underline: 2}} {
		s := New(5, 1)
		s.WriteString(SGR(a) + "x")
		if got := s.Row(0)[0].A; got != a {
			t.Errorf("%q: %+v, want %+v", SGR(a), got, a)
		}
	}
}

func TestRuneWidth(t *testing.T) {
	for r, w := range map[rune]int{'a': 1, 'é': 1, '́': 0, '漢': 2, '😀': 2, '✻': 1, '✳': 1, '─': 1, '‍': 0, '️': 0, '한': 2, '❤': 1, '⏺': 1, '⎿': 1} {
		if got := RuneWidth(r); got != w {
			t.Errorf("%U: %d, want %d", r, got, w)
		}
	}
}

// The width the emulator gives characters agents print is tmux's: each is
// printed alone into a pane and tmux's cursor tells its width.
func TestWidthsMatchTmux(t *testing.T) {
	s := newTmux(t)
	probes := []string{"a", "漢", "😀", "✻", "✶", "✢", "✳", "·", "⏺", "⎿", "❯", "›", "…", "•", "→", "▌", "█", "░", "─", "╭", "⚡", "✔", "✓", "⚠",
		"⚠️", "❤️", "☺️", "👍\U0001F3FD", "👩‍💻", "🇩🇪", "é", "한", "ｱ", "Ａ", "🤖", "🧠", "📁", "⏳", "⌛", "✅", "❌", "🔥"}
	for i, p := range probes {
		name := fmt.Sprintf("w%d", i)
		screen := s.truth(t, name, 10, 2, []byte(p))
		want, _ := screen.Cursor()
		got := New(10, 2)
		got.WriteString(p)
		if x, _ := got.Cursor(); x != want {
			t.Errorf("%q (%U): emulator advances %d, tmux %d", p, []rune(p), x, want)
		}
	}
}

// Whatever a program writes, the emulator never panics and keeps its
// invariants (cursor inside, wide characters whole).
func TestArbitraryOutput(t *testing.T) {
	tokens := []string{"a", "漢", "😀", "́", "‍", "️", "\r", "\n", "\b", "\t", "\x1b[", "\x1b", "1", "9", ";", ":", "?", ">",
		"m", "H", "J", "K", "@", "P", "L", "M", "S", "T", "X", "b", "r", "h", "l", "1049", "47", "6", "7", "2004", "\x1b7", "\x1b8", "\x1bM",
		"\x1bD", "\x1bc", "\x1b(0", "\x1b(B", "\x0e", "\x0f", "\x1b]0;", "\x07", "\x1bP", "\x1b\\", "\xff", "\xe2\x82", "99999", "\x1b#8", "s", "u", "Z", "I", "g"}
	seed := uint64(1)
	next := func(n int) int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return int(seed>>33) % n
	}
	for range 3000 {
		s := New(1+next(12), 1+next(6))
		var b strings.Builder
		for range next(80) {
			b.WriteString(tokens[next(len(tokens))])
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic %v on %q (%dx%d)", r, b.String(), s.w, s.h)
				}
			}()
			s.WriteString(b.String())
			for y := range s.h {
				s.RowANSI(y, 0)
				s.RowANSI(y, 3)
				row := s.Row(y)
				for x, c := range row {
					if c.Width == 2 && (x+1 >= s.w || row[x+1].Width != 0) || c.Width == 0 && (x == 0 || row[x-1].Width != 2) {
						t.Fatalf("broken wide character at %d,%d on %q: %+v", x, y, b.String(), row)
					}
				}
			}
			if x, y := s.Cursor(); x < 0 || x >= s.w || y < 0 || y >= s.h {
				t.Fatalf("cursor %d,%d outside %dx%d on %q", x, y, s.w, s.h, b.String())
			}
		}()
	}
}

func BenchmarkFeed(b *testing.B) {
	frame := []byte("\x1b[?2026h\x1b[2K\x1b[1A\x1b[2K\x1b[G\x1b[38;2;215;119;87m✶\x1b[39m Writing a fairly long line of agent output here…\r\n\x1b[2m  esc to interrupt\x1b[22m\r\n\x1b[?2026l")
	s := New(120, 40)
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		s.Write(frame)
	}
}

func BenchmarkRowANSI(b *testing.B) {
	s := New(120, 1)
	s.WriteString("\x1b[1;31mred\x1b[0m plain \x1b[38;2;1;2;3mrgb\x1b[0m " + strings.Repeat("text ", 20))
	for b.Loop() {
		_, _ = s.RowANSI(0, 0)
	}
}
