package sessions

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/ptyhost"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Performance budgets of the shared history (contract, "As built — shared
// history (data)"), checked on synthetic data in every run and measured
// on the real transcripts with HESPER_HISTORY_BENCH=1 (read-only: the
// index goes to a temp folder).

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

// synthetic fills an index with n sessions (half of them another Mac's)
// with realistic text sizes.
func synthetic(t testing.TB, s *Service, n int) {
	words := strings.Fields("ticker debounce badge rail widget push provider fcm token refresh cache invalidate layout grid shelf band project " +
		"group sidebar wall tile focus attach daemon relay mirror replica session resume fork brief archive delete search index sqlite fts")
	r := rand.New(rand.NewSource(1))
	text := func(nw int) string {
		var b strings.Builder
		for i := 0; i < nw; i++ {
			b.WriteString(words[r.Intn(len(words))])
			b.WriteByte(' ')
		}
		return b.String()
	}
	now := time.Now().UnixMilli()
	var batch []*Record
	for i := 0; i < n; i++ {
		node := s.db.node
		if i%2 == 1 {
			node = "n-00000000000000mm"
		}
		var prompts, answers []string
		for j := 0; j < 8; j++ {
			prompts = addPiece(prompts, clip(text(60), maxPiece), maxPrompts)
			answers = addPiece(answers, clip(text(90), maxAnswerPiece), maxAnswers)
		}
		rec := &Record{Node: node, Home: "M", Kind: []string{"claude", "codex"}[i%2], SID: fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i),
			Meta: Meta{Cwd: fmt.Sprintf("/Users/x/projects/p%d", i%40), ProjectID: fmt.Sprintf("p-%016x", i%40), Branch: "main", Title: text(6),
				FirstPrompt: prompts[0], LastUser: prompts[len(prompts)-1], LastAssistant: answers[len(answers)-1], Turns: 8, Tokens: 1000,
				StartedAt: now - int64(i)*3600_000, LastActivity: now - int64(i)*3600_000 + 60_000, Origin: "cli", External: i%3 == 0,
				Size: 100000, Version: "1:1", Path: "/x", Prompts: strings.Join(prompts, pieceSep), Answers: strings.Join(answers, pieceSep)},
			MetaS: Stamp{T: now, N: node}}
		batch = append(batch, rec)
		if len(batch) == 200 {
			if _, err := s.apply(batch, false); err != nil {
				t.Fatal(err)
			}
			batch = nil
		}
	}
	if len(batch) > 0 {
		if _, err := s.apply(batch, false); err != nil {
			t.Fatal(err)
		}
	}
}

var benchQueries = []wire.SessionSearchParams{
	{}, {Query: "debounce"}, {Query: "flaky badge"}, {Query: "fts sqlite index"}, {Query: "wid"},
	{Kinds: []string{"codex"}}, {Machines: []string{"M"}}, {ProjectID: "p-0000000000000007"},
	{Query: "resume", Kinds: []string{"claude"}}, {Query: "mirror replica", Limit: 20},
}

func measureSearch(t testing.TB, s *Service, rounds int) (p50, p99 time.Duration) {
	var lat []time.Duration
	for i := 0; i < rounds; i++ {
		q := benchQueries[i%len(benchQueries)]
		t0 := time.Now()
		if _, err := s.Search(q); err != nil {
			t.Fatal(err)
		}
		lat = append(lat, time.Since(t0))
	}
	return percentile(lat, 0.5), percentile(lat, 0.99)
}

// Search over 2,000 sessions: p50 ≤ 5 ms, p99 ≤ 20 ms (budgets: enforced
// with HESPER_BUDGETS=1, budget_test.go).
func TestSearchBudget(t *testing.T) {
	e := newEnv(t)
	s := e.open(Options{ScanEvery: time.Hour, FullScanEvery: time.Hour})
	synthetic(t, s, 2000)
	p50, p99 := measureSearch(t, s, rounds(1000))
	st, _ := s.Stats()
	t.Logf("search over %d sessions: p50 %v, p99 %v; index %.1f MB (%.2f MB per 1,000)", st.Count, p50, p99,
		float64(st.IndexBytes)/(1<<20), float64(st.IndexBytes)/(1<<20)/2)
	if p50 > 5*time.Millisecond || p99 > 20*time.Millisecond {
		overBudget(t, "search p50 %v / p99 %v over 5 ms / 20 ms", p50, p99)
	}
	// sessions.show of another Mac's session (no git here): ≤ 2 ms.
	var lat []time.Duration
	for i := 1; i < 400; i += 2 {
		id := fmt.Sprintf("M:codex:%08x-0000-4000-8000-%012x", i, i)
		t0 := time.Now()
		rw, err := s.find(id)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.toWire(&rw.rec)
		lat = append(lat, time.Since(t0))
	}
	t.Logf("show without git: p50 %v, p99 %v", percentile(lat, 0.5), percentile(lat, 0.99))
	if percentile(lat, 0.5) > 2*time.Millisecond {
		overBudget(t, "show p50 %v over 2 ms", percentile(lat, 0.5))
	}
}

// An append to a large transcript is indexed reading only the new bytes:
// ≤ 10 ms.
func TestIncrementalBudget(t *testing.T) {
	e := newEnv(t)
	path := filepath.Join(e.claude, "projects", "-w", claudeSID+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	f, _ := os.Create(path)
	w := bufio.NewWriter(f)
	big := strings.Repeat("x", 4000)
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(w, `{"type":"user","sessionId":"%s","cwd":"/w","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":[{"tool_use_id":"t","type":"tool_result","content":"%s"}]}}`+"\n", claudeSID, big)
	}
	w.Flush()
	f.Close()
	s := e.open(Options{ScanEvery: time.Hour, FullScanEvery: time.Hour})
	waitFor(t, "first pass", func() bool { return s.scan.firstPassed.Load() })
	fi := func() fileInfo {
		st, _ := os.Stat(path)
		return fileInfo{path: path, kind: "claude", sid: claudeSID, size: st.Size(), mtime: st.ModTime().UnixNano()}
	}
	var lat []time.Duration
	for i := 0; i < 50; i++ {
		fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		fmt.Fprintf(fh, `{"type":"user","sessionId":"%s","cwd":"/w","timestamp":"2026-10-01T10:%02d:00Z","message":{"role":"user","content":"next step %d"}}`+"\n", claudeSID, i%60, i)
		fh.Close()
		t0 := time.Now()
		n, err := s.scan.file(fi(), nil)
		if err != nil || n == 0 || n > 400 {
			t.Fatalf("read %d bytes: %v", n, err)
		}
		lat = append(lat, time.Since(t0))
	}
	st, _ := os.Stat(path)
	t.Logf("append to a %.1f MB transcript indexed in p50 %v, p99 %v", float64(st.Size())/(1<<20), percentile(lat, 0.5), percentile(lat, 0.99))
	if percentile(lat, 0.5) > 10*time.Millisecond {
		overBudget(t, "incremental p50 %v over 10 ms", percentile(lat, 0.5))
	}
}

// keyProbe types into a fake shell under ptyhost and times the echo.
type keyProbe struct {
	tm     *ptyhost.Term
	client net.Conn
	r      *bufio.Reader
}

func newKeyProbe(t testing.TB) *keyProbe {
	exe, _ := os.Executable()
	tm, err := ptyhost.Start(ptyhost.Config{Argv: []string{exe, "shell"}, Env: append(os.Environ(), "AGENTS_FAKE=1"), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	fds, _ := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	sf, cf := os.NewFile(uintptr(fds[0]), "s"), os.NewFile(uintptr(fds[1]), "c")
	server, _ := net.FileConn(sf)
	client, _ := net.FileConn(cf)
	sf.Close()
	cf.Close()
	go tm.Attach(server, bufio.NewReader(server), wire.AttachRequest{Mode: wire.ModeRW})
	p := &keyProbe{tm: tm, client: client, r: bufio.NewReader(client)}
	wire.ReadLine(p.r)
	wire.ReadFrame(p.r, nil)
	time.Sleep(300 * time.Millisecond)
	return p
}

func (p *keyProbe) one(t testing.TB) time.Duration {
	t0 := time.Now()
	wire.WriteFrame(p.client, wire.FrameData, []byte("k"))
	for {
		typ, data, err := wire.ReadFrame(p.r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if typ == wire.FrameData && bytes.Contains(data, []byte("k")) {
			return time.Since(t0)
		}
	}
}

func (p *keyProbe) close() { p.tm.Signal(syscall.SIGKILL); <-p.tm.Done() }

// The real transcripts (HESPER_HISTORY_BENCH=1): a first full index in
// the background band, timed, with keystrokes through a PTY measured
// before and while it runs; then search and show over the result.
func TestRealHistoryBenchmark(t *testing.T) {
	if os.Getenv("HESPER_HISTORY_BENCH") == "" {
		t.Skip("HESPER_HISTORY_BENCH=1 indexes the real ~/.claude and ~/.codex (read-only) into a temp index")
	}
	home, _ := os.UserHomeDir()
	state := os.Getenv("HESPER_HISTORY_BENCH_STATE")
	if state == "" {
		state = t.TempDir()
	}
	probe := newKeyProbe(t)
	defer probe.close()
	var idle []time.Duration
	for i := 0; i < 400; i++ {
		idle = append(idle, probe.one(t))
		time.Sleep(5 * time.Millisecond)
	}
	opt := Options{StateDir: state, ClaudeHome: filepath.Join(home, ".claude"), CodexHome: filepath.Join(home, ".codex"), UserHome: home,
		Machine: "L", ScanEvery: time.Hour, FullScanEvery: time.Hour, Logf: t.Logf}
	if os.Getenv("HESPER_HISTORY_BENCH_BUSY") != "" {
		opt.Busy = func() bool { return true } // as while agents work: background band
	}
	sc := &scanner{s: &Service{opt: opt}}
	sc.s.opt.Now = time.Now
	files := sc.list(true)
	recent := 0
	var totalBytes int64
	for _, f := range files {
		totalBytes += f.size
		if time.Since(time.Unix(0, f.mtime)) < 14*24*time.Hour {
			recent++
		}
	}
	t.Logf("%d transcripts, %.2f GB; %d of the last 14 days", len(files), float64(totalBytes)/(1<<30), recent)
	start := time.Now()
	s, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var during, duringOld []time.Duration
	var recentAt time.Duration
	for !s.scan.firstPassed.Load() {
		k := probe.one(t)
		during = append(during, k)
		if recentAt > 0 {
			duringOld = append(duringOld, k)
		}
		s.mu.Lock()
		done := s.indexing.Done
		s.mu.Unlock()
		if recentAt == 0 && done >= recent {
			recentAt = time.Since(start)
		}
		if limit, _ := time.ParseDuration(os.Getenv("HESPER_HISTORY_BENCH_LIMIT")); limit > 0 && time.Since(start) > limit {
			el := time.Since(start)
			t.Logf("stopped after %v: %d of %d transcripts, %.2f GB read (%.1f MB/s); the last 14 days searchable after %v",
				el.Round(time.Millisecond), done, len(files), float64(s.scan.readTotal.Load())/(1<<30),
				float64(s.scan.readTotal.Load())/(1<<20)/el.Seconds(), recentAt.Round(time.Millisecond))
			t.Logf("keystroke echo through a PTY: idle p50 %v p99 %v; while indexing p50 %v p99 %v (%d samples)",
				percentile(idle, 0.5), percentile(idle, 0.99), percentile(during, 0.5), percentile(during, 0.99), len(during))
			t.Logf("  while indexing older transcripts (background band): p50 %v p99 %v (%d samples)", percentile(duringOld, 0.5), percentile(duringOld, 0.99), len(duringOld))
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	full := time.Since(start)
	st, _ := s.Stats()
	t.Logf("first full index: %v (the last 14 days searchable after %v), %.2f GB read, %d sessions, index %.1f MB (%.2f MB per 1,000 sessions)",
		full.Round(time.Millisecond), recentAt.Round(time.Millisecond), float64(s.scan.readTotal.Load())/(1<<30), st.Count,
		float64(st.IndexBytes)/(1<<20), float64(st.IndexBytes)/(1<<20)/(float64(st.Count)/1000))
	t.Logf("keystroke echo through a PTY: idle p50 %v p99 %v; while indexing p50 %v p99 %v (%d samples)",
		percentile(idle, 0.5), percentile(idle, 0.99), percentile(during, 0.5), percentile(during, 0.99), len(during))
	t.Logf("  while indexing older transcripts (background band): p50 %v p99 %v (%d samples)", percentile(duringOld, 0.5), percentile(duringOld, 0.99), len(duringOld))
	// Incremental pass over everything (nothing changed): stats only.
	t0 := time.Now()
	s.scan.pass(true)
	t.Logf("full pass with nothing new: %v", time.Since(t0).Round(time.Microsecond))
	if st.Count < 2000 {
		synthetic(t, s, 2000-st.Count+100)
	}
	st, _ = s.Stats()
	p50, p99 := measureSearch(t, s, 2000)
	t.Logf("search over %d sessions: p50 %v, p99 %v", st.Count, p50, p99)
	var lat, latGit []time.Duration
	res, _ := s.Search(wire.SessionSearchParams{Machines: []string{"L"}, Limit: 50})
	for _, it := range res.Items {
		s.Show(it.ID) // fills the git cache
	}
	for i := 0; i < 5; i++ {
		for _, it := range res.Items {
			t1 := time.Now()
			s.Show(it.ID)
			latGit = append(latGit, time.Since(t1))
			t2 := time.Now()
			rw, _ := s.find(it.ID)
			_ = s.toWire(&rw.rec)
			lat = append(lat, time.Since(t2))
		}
	}
	t.Logf("show: without git p50 %v p99 %v; with the cached git state p50 %v p99 %v", percentile(lat, 0.5), percentile(lat, 0.99),
		percentile(latGit, 0.5), percentile(latGit, 0.99))
}
