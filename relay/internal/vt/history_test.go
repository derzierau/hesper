package vt

import (
	"fmt"
	"strings"
	"testing"
)

func TestHistoryCapturesLinesLeavingTheTop(t *testing.T) {
	s := New(20, 3)
	for i := 1; i <= 5; i++ {
		s.WriteString(fmt.Sprintf("line %d\r\n", i))
	}
	// 5 lines and the cursor on a 6th: lines 1..3 scrolled off.
	if s.HistoryLen() != 3 || s.HistoryTotal() != 3 {
		t.Fatalf("history %d total %d", s.HistoryLen(), s.HistoryTotal())
	}
	for i := range 3 {
		if got := s.HistoryLine(i); got != fmt.Sprintf("line %d", i+1) {
			t.Fatalf("line %d: %q", i, got)
		}
	}
	if got := s.Text(0); !strings.HasPrefix(got, "line 4") {
		t.Fatalf("screen %q", got)
	}
}

func TestHistoryKeepsAttributes(t *testing.T) {
	s := New(20, 1)
	s.WriteString("\x1b[31mred\x1b[0m plain\r\n")
	if got := s.HistoryLine(0); got != SGR(Attr{FG: Indexed(1)})+"red"+SGR(Attr{})+" plain" {
		t.Fatalf("%q", got)
	}
}

func TestHistoryIgnoresClearAndAltScreenAndRegions(t *testing.T) {
	s := New(10, 3)
	s.WriteString("a\r\nb\r\nc")
	s.WriteString("\x1b[2J") // clearing the screen is not scrolling
	if s.HistoryLen() != 0 {
		t.Fatalf("clear added %d lines", s.HistoryLen())
	}
	s.WriteString("\x1b[?1049h")
	for range 10 {
		s.WriteString("alt\r\n")
	}
	if s.HistoryLen() != 0 {
		t.Fatalf("alt screen added %d lines", s.HistoryLen())
	}
	s.WriteString("\x1b[?1049l")
	// A region below the top (a pinned header) scrolls without history.
	s.WriteString("\x1b[2;3r\x1b[3;1H\nx\nx\n\x1b[r")
	if s.HistoryLen() != 0 {
		t.Fatalf("inner region added %d lines", s.HistoryLen())
	}
	s.WriteString("\x1b[3;1H\r\n\r\n")
	if s.HistoryLen() != 2 {
		t.Fatalf("history %d", s.HistoryLen())
	}
	s.WriteString("\x1b[3J") // erase saved lines
	if s.HistoryLen() != 0 || s.HistoryTotal() != 2 {
		t.Fatalf("after ED 3: %d (total %d)", s.HistoryLen(), s.HistoryTotal())
	}
	s.WriteString("\x1bc") // a full reset keeps (the now empty) scrollback, and counting
	s.WriteString("1\r\n2\r\n3\r\n4")
	if s.HistoryLen() != 1 || s.HistoryTotal() != 3 {
		t.Fatalf("after reset: %d (total %d)", s.HistoryLen(), s.HistoryTotal())
	}
}

func TestHistoryIsBounded(t *testing.T) {
	s := New(10, 2)
	s.SetHistoryLimit(100, 1<<20)
	for i := range 1000 {
		s.WriteString(fmt.Sprintf("%d\r\n", i))
	}
	if s.HistoryLen() != 100 || s.HistoryLine(0) != "899" || s.HistoryLine(99) != "998" || s.HistoryTotal() != 999 {
		t.Fatalf("len %d first %q last %q total %d", s.HistoryLen(), s.HistoryLine(0), s.HistoryLine(99), s.HistoryTotal())
	}
	b := New(100, 2)
	b.SetHistoryLimit(1000, 500)
	for range 100 {
		b.WriteString(strings.Repeat("x", 50) + "\r\n")
	}
	if b.hist.bytes > 500 || b.HistoryLen() != 10 {
		t.Fatalf("bytes %d len %d", b.hist.bytes, b.HistoryLen())
	}
	n := New(10, 2)
	n.SetHistoryLimit(0, 0)
	n.WriteString("a\r\nb\r\nc\r\n")
	if n.HistoryLen() != 0 {
		t.Fatal("history kept with limit 0")
	}
}

func TestClipANSI(t *testing.T) {
	red := SGR(Attr{FG: Indexed(1)})
	if got := ClipANSI(red+"hello"+SGR(Attr{})+" world", 7); got != red+"hello"+SGR(Attr{})+" w" {
		t.Fatalf("%q", got)
	}
	if got := ClipANSI("ab世界", 3); got != "ab" {
		t.Fatalf("wide: %q", got)
	}
	if got := ClipANSI("abc", 0); got != "abc" {
		t.Fatalf("%q", got)
	}
}

func BenchmarkHistoryPush(b *testing.B) {
	s := New(120, 40)
	line := []byte("\x1b[32m● Bash(./gradlew :core:push:test)\x1b[0m 214 tests completed, 0 failed\r\n")
	b.ResetTimer()
	for range b.N {
		s.Write(line)
	}
}
