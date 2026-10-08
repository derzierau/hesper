package vt

import (
	"fmt"
	"slices"
	"testing"
)

func TestPlainLines(t *testing.T) {
	s := New(20, 3)
	for i := 1; i <= 5; i++ {
		s.WriteString(fmt.Sprintf("\x1b[1;3%dmline %d\x1b[0m  \r\n", i, i))
	}
	s.WriteString("中文 prompt")
	// History: lines 1..3; screen: 4, 5, the prompt.
	if got, want := s.PlainLines(0, 0), []string{"line 4", "line 5", "中文 prompt"}; !slices.Equal(got, want) {
		t.Fatalf("screen %q", got)
	}
	if got, want := s.PlainLines(1, 2), []string{"line 2", "line 3", "中文 prompt"}; !slices.Equal(got, want) {
		t.Fatalf("rows 1 scrollback 2: %q", got)
	}
	if got := s.PlainLines(10, 100); len(got) != 6 || got[0] != "line 1" {
		t.Fatalf("everything: %q", got)
	}
	// The alternate screen: its rows, the main screen's scrollback.
	s.WriteString("\x1b[?1049h\x1b[2;1Hfull screen")
	if got, want := s.PlainLines(0, 1), []string{"line 3", "", "full screen", ""}; !slices.Equal(got, want) {
		t.Fatalf("alt %q", got)
	}
}

func TestStripANSI(t *testing.T) {
	cases := map[string]string{
		"plain":                                  "plain",
		"\x1b[0;1;38;2;1;2;3mbold\x1b[0m!":       "bold!",
		"\x1b]8;;http://x\x07link\x1b]8;;\x1b\\": "link",
		"a\x1b(Bb\x1b=c":                         "abc",
		"tab\there\x1b":                          "tab\there",
	}
	for in, want := range cases {
		if got := StripANSI(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
