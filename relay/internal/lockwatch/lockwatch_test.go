package lockwatch

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logs) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *logs) waitFor(t *testing.T, part string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range l.all() {
			if strings.Contains(line, part) {
				return line
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no log line with %q: %q", part, l.all())
	return ""
}

//go:noinline
func holdTooLong(m *Mutex, release <-chan struct{}) {
	m.Lock()
	<-release
	m.Unlock()
}

// A lock held past the limit is logged once, with its holder's stack,
// and its release after it.
func TestHeldTooLongNamesTheHolder(t *testing.T) {
	var m Mutex
	var l logs
	w := Start(Options{Limit: 50 * time.Millisecond, Every: 10 * time.Millisecond, Logf: l.logf}, Lock{Name: "the registry's lock", Mu: &m})
	defer w.Stop()
	release := make(chan struct{})
	go holdTooLong(&m, release)
	line := l.waitFor(t, "the registry's lock held for")
	if !strings.Contains(line, "lockwatch.holdTooLong") || !strings.Contains(line, "lockwatch_test.go:") {
		t.Fatalf("no holder in %q", line)
	}
	time.Sleep(100 * time.Millisecond) // more ticks while still held: no second report
	close(release)
	l.waitFor(t, "the registry's lock released")
	held := 0
	for _, line := range l.all() {
		if strings.Contains(line, "held for") {
			held++
		}
	}
	if held != 1 {
		t.Fatalf("%d reports of one hold: %q", held, l.all())
	}
}

// Short holds, many of them from many goroutines, are not logged.
func TestShortHoldsAreQuiet(t *testing.T) {
	var m Mutex
	var l logs
	w := Start(Options{Limit: time.Second, Every: 5 * time.Millisecond, Logf: l.logf}, Lock{Name: "x", Mu: &m})
	n := 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				m.Lock()
				n++
				m.Unlock()
				if m.TryLock() {
					n++
					m.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	w.Stop()
	if n < 8*2000 {
		t.Fatalf("n = %d", n)
	}
	if lines := l.all(); len(lines) != 0 {
		t.Fatalf("logged %q", lines)
	}
}
