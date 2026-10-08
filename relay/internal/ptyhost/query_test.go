package ptyhost

import (
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/internal/vt"
)

func TestFilterSplitsQueries(t *testing.T) {
	cases := []struct {
		in, pass string
		queries  []string
	}{
		{"plain \x1b[31mred\x1b[0m", "plain \x1b[31mred\x1b[0m", nil},
		{"a\x1b[6nb", "ab", []string{"\x1b[6n"}},
		{"\x1b[c\x1b[>c\x1b[?u\x1b[?2026$p\x1b[18t", "", []string{"\x1b[c", "\x1b[>c", "\x1b[?u", "\x1b[?2026$p", "\x1b[18t"}},
		{"\x1b]11;?\x07x\x1b]10;?\x1b\\", "x", []string{"\x1b]11;?\x07", "\x1b]10;?\x1b\\"}},
		{"\x1b]0;title\x07\x1b]8;;http://x\x1b\\link", "\x1b]0;title\x07\x1b]8;;http://x\x1b\\link", nil},
		{"\x1bP+q544e\x1b\\ok", "ok", []string{"\x1bP+q544e\x1b\\"}},
		{"\x1b_Gf=1;" + strings.Repeat("A", 600) + "\x1b\\z", "\x1b_Gf=1;" + strings.Repeat("A", 600) + "\x1b\\z", nil},
		{"\x1b]52;c;?\x07", "", []string{"\x1b]52;c;?\x07"}},
		{"\x1b]52;c;aGk=\x07", "\x1b]52;c;aGk=\x07", nil},
		{"\x1b[?1049h\x1b[2J\x1b[>1u\x1b[1 q", "\x1b[?1049h\x1b[2J\x1b[>1u\x1b[1 q", nil},
		{"\x1b\x1b[6n", "\x1b", []string{"\x1b[6n"}},
		{"\x1b]4;1;?;2;?\x07", "", []string{"\x1b]4;1;?;2;?\x07"}},
	}
	for _, c := range cases {
		// Whole, and split at every byte.
		for split := 0; split <= len(c.in); split++ {
			var f filter
			var pass strings.Builder
			var queries []string
			for _, part := range []string{c.in[:split], c.in[split:]} {
				f.feed([]byte(part), func(p []byte) { pass.Write(p) }, func(q []byte) { queries = append(queries, string(q)) })
			}
			if pass.String() != c.pass || strings.Join(queries, "|") != strings.Join(c.queries, "|") {
				t.Fatalf("%q split %d: pass %q queries %q", c.in, split, pass.String(), queries)
			}
		}
	}
}

func TestAnswers(t *testing.T) {
	s := vt.New(80, 24)
	s.WriteString("\x1b[3;4H\x1b[?2004h\x1b[>5u")
	for q, want := range map[string]string{
		"\x1b[6n":            "\x1b[3;4R",
		"\x1b[?6n":           "\x1b[?3;4R",
		"\x1b[5n":            "\x1b[0n",
		"\x1b[c":             "\x1b[?62;22c",
		"\x1b[?u":            "\x1b[?5u",
		"\x1b[?2004$p":       "\x1b[?2004;1$y",
		"\x1b[?1000$p":       "\x1b[?1000;2$y",
		"\x1b[?9999$p":       "\x1b[?9999;0$y",
		"\x1b[18t":           "\x1b[8;24;80t",
		"\x1b]11;?\x07":      "\x1b]11;rgb:1a1a/1b1b/2626\x07",
		"\x1b]4;196;?\x1b\\": "\x1b]4;196;rgb:ffff/0000/0000\x1b\\",
	} {
		if got := string(answer([]byte(q), s)); got != want {
			t.Errorf("%q: %q want %q", q, got, want)
		}
	}
}
