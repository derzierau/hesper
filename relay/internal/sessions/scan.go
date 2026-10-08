package sessions

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The scanner: transcripts are found by walking Claude's projects folder
// (~/.claude/projects/<folder>/<session>.jsonl) and Codex's sessions
// (~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl[.zst]) and archive
// (~/.codex/archived_sessions). Each file has a checkpoint (size, mtime,
// byte offset after its last complete line, the parse state); a file
// whose size and mtime are unchanged is not opened, an appended one is
// read from its offset only. .zst files are re-read whole (streamed) when
// they change. The first full index runs newest first (the last two weeks
// are searchable within seconds) with a small pool of workers in macOS's
// background band (CPU and disk throttled); later passes look at recent
// folders every ScanEvery and walk everything every FullScanEvery.

type fileInfo struct {
	path     string
	kind     string
	sid      string
	size     int64
	mtime    int64 // ns
	zst      bool
	archived bool // Codex's archived_sessions
}

type checkpoint struct {
	size, mtime, offset int64
	key                 string
	state               []byte
}

type scanner struct {
	s    *Service
	wake chan struct{}

	mu      sync.Mutex
	ckpts   map[string]*checkpoint
	threads map[string]string // Codex thread names by session id
	thrSig  string
	cwdProj map[string]string // cwd → project id
	working map[string]bool   // files being read

	loaded   bool
	lastFull time.Time
	// Stats of the last full pass (benchmarks).
	LastFull    time.Duration
	LastBytes   int64
	firstPassed atomic.Bool
	// readTotal counts transcript bytes read (tests, benchmarks).
	readTotal atomic.Int64
}

func newScanner(s *Service) *scanner {
	return &scanner{s: s, wake: make(chan struct{}, 1), ckpts: map[string]*checkpoint{}, threads: map[string]string{},
		cwdProj: map[string]string{}, working: map[string]bool{}}
}

// Poke asks for a pass now (a transcript was placed or changed).
func (sc *scanner) poke() {
	select {
	case sc.wake <- struct{}{}:
	default:
	}
}

func (sc *scanner) run() {
	sc.pass(true)
	t := time.NewTicker(sc.s.opt.ScanEvery)
	defer t.Stop()
	for {
		select {
		case <-sc.s.stop:
			return
		case <-t.C:
		case <-sc.wake:
		}
		full := sc.s.opt.Now().Sub(sc.lastFull) >= sc.s.opt.FullScanEvery
		sc.pass(full)
		sc.s.refreshLive()
	}
}

func (sc *scanner) loadCheckpoints() {
	if sc.loaded {
		return
	}
	sc.loaded = true
	rows, err := sc.s.db.r.Query(`SELECT path, size, mtime, offset, key, state FROM files`)
	if err != nil {
		return
	}
	defer rows.Close()
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for rows.Next() {
		var path string
		var c checkpoint
		if rows.Scan(&path, &c.size, &c.mtime, &c.offset, &c.key, &c.state) == nil {
			sc.ckpts[path] = &c
		}
	}
}

// list finds the transcripts: everything (full) or the recently active
// folders (Claude's project folders; Codex's last three days).
func (sc *scanner) list(full bool) []fileInfo {
	var out []fileInfo
	add := func(path, kind string, archived bool) {
		name := filepath.Base(path)
		var sid string
		zst := strings.HasSuffix(name, ".jsonl.zst")
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".zst"), ".jsonl")
		switch kind {
		case wire.KindClaude:
			sid = base
		case wire.KindCodex:
			if !strings.HasPrefix(base, "rollout-") || len(base) < 36 {
				return
			}
			sid = base[len(base)-36:]
		}
		if !sidRE.MatchString(sid) {
			return
		}
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() {
			return
		}
		out = append(out, fileInfo{path: path, kind: kind, sid: sid, size: st.Size(), mtime: st.ModTime().UnixNano(), zst: zst, archived: archived})
	}
	claude := filepath.Join(sc.s.opt.ClaudeHome, "projects")
	dirs, _ := os.ReadDir(claude)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(claude, d.Name()))
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".jsonl") {
				add(filepath.Join(claude, d.Name(), f.Name()), wire.KindClaude, false)
			}
		}
	}
	codex := filepath.Join(sc.s.opt.CodexHome, "sessions")
	rollout := func(dir string, archived bool) {
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			n := f.Name()
			if !f.IsDir() && strings.HasPrefix(n, "rollout-") && (strings.HasSuffix(n, ".jsonl") || strings.HasSuffix(n, ".jsonl.zst")) {
				add(filepath.Join(dir, n), wire.KindCodex, archived)
			}
		}
	}
	if full {
		years, _ := os.ReadDir(codex)
		for _, y := range years {
			months, _ := os.ReadDir(filepath.Join(codex, y.Name()))
			for _, m := range months {
				days, _ := os.ReadDir(filepath.Join(codex, y.Name(), m.Name()))
				for _, d := range days {
					rollout(filepath.Join(codex, y.Name(), m.Name(), d.Name()), false)
				}
			}
		}
		rollout(filepath.Join(sc.s.opt.CodexHome, "archived_sessions"), true)
	} else {
		now := sc.s.opt.Now()
		seen := map[string]bool{}
		for _, t := range []time.Time{now, now.UTC(), now.Add(-24 * time.Hour), now.UTC().Add(-24 * time.Hour), now.Add(-48 * time.Hour)} {
			dir := filepath.Join(codex, t.Format("2006"), t.Format("01"), t.Format("02"))
			if !seen[dir] {
				seen[dir] = true
				rollout(dir, false)
			}
		}
	}
	return out
}

// afterTranscript, when set (tests), runs on a pass's worker after each
// transcript it read.
var afterTranscript func(*Service)

// pass looks at the transcripts and reads what changed.
func (sc *scanner) pass(full bool) {
	start := time.Now()
	sc.loadCheckpoints()
	sc.loadThreads()
	files := sc.list(full)
	sc.mu.Lock()
	var needs []fileInfo
	present := map[string]bool{}
	for _, f := range files {
		present[f.path] = true
		c := sc.ckpts[f.path]
		if c != nil && c.size == f.size && c.mtime == f.mtime {
			continue
		}
		needs = append(needs, f)
	}
	sc.mu.Unlock()
	// Newest first: the last weeks are searchable while the rest indexes.
	sort.Slice(needs, func(i, j int) bool { return needs[i].mtime > needs[j].mtime })
	first := !sc.firstPassed.Load()
	var done atomic.Int64
	var bytesRead atomic.Int64
	total := len(needs)
	report := func(force bool) {}
	if total > 0 && (first || total > 20) {
		var lastReport atomic.Int64
		report = func(force bool) {
			now := time.Now().UnixMilli()
			if force || now-lastReport.Load() >= 500 {
				lastReport.Store(now)
				sc.s.setIndexing(int(done.Load()), total)
			}
		}
		report(true)
	}
	work := make(chan fileInfo)
	workers := 1
	if total > 8 {
		workers = sc.s.opt.Workers
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var th *throttle
			if !sc.s.opt.Foreground {
				defer lockThread()()
				th = newThrottle(sc.s)
			}
			for f := range work {
				// Extra workers step aside while agents are working.
				for i > 0 && !sc.s.opt.Foreground && sc.s.busy() {
					select {
					case <-sc.s.stop:
						return
					case <-time.After(500 * time.Millisecond):
					}
				}
				th.begin(f)
				n, err := sc.file(f, th)
				if err != nil && !errors.Is(err, errClosed) {
					sc.s.opt.Logf("history: %s: %v", f.path, err)
				}
				bytesRead.Add(n)
				done.Add(1)
				report(false)
				if afterTranscript != nil {
					afterTranscript(sc.s)
				}
				th.slice()
			}
		}(i)
	}
feed:
	for _, f := range needs {
		select {
		case <-sc.s.stop:
			break feed
		case work <- f:
		}
	}
	close(work)
	wg.Wait()
	if total > 0 {
		report(true)
	}
	if full {
		sc.vanished(present)
		sc.lastFull = sc.s.opt.Now()
		sc.LastFull, sc.LastBytes = time.Since(start), bytesRead.Load()
		if first {
			sc.s.opt.Logf("history: indexed %d transcripts (%d MB read) in %v", total, bytesRead.Load()>>20, time.Since(start).Round(time.Millisecond))
		}
		sc.firstPassed.Store(true)
	}
}

// vanished: transcripts gone since the last full pass. Their checkpoints
// go; a session whose transcript is gone (and was not moved, e.g. by
// Codex's archive, which this pass found at its new place) becomes a
// tombstone.
func (sc *scanner) vanished(present map[string]bool) {
	sc.mu.Lock()
	var gone []string
	gk := map[string]string{}
	for path, c := range sc.ckpts {
		if !present[path] {
			gone = append(gone, path)
			gk[path] = c.key
		}
	}
	sc.mu.Unlock()
	for _, path := range gone {
		key := gk[path]
		sc.s.db.write(func(tx sqlTx) error {
			_, err := tx.Exec(`DELETE FROM files WHERE path = ?`, path)
			return err
		})
		sc.mu.Lock()
		delete(sc.ckpts, path)
		sc.mu.Unlock()
		sc.s.edit(key, func(r *Record, st Stamp) bool {
			if r.Deleted || r.Meta.Path != path {
				return false
			}
			r.Deleted, r.DeletedS = true, st
			return true
		})
	}
}

// loadThreads reads Codex's thread names (session_index.jsonl) when the
// file changed, and retitles sessions whose name changed.
func (sc *scanner) loadThreads() {
	path := filepath.Join(sc.s.opt.CodexHome, "session_index.jsonl")
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	sig := strconv.FormatInt(st.Size(), 10) + ":" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	if sig == sc.thrSig {
		return
	}
	sc.thrSig = sig
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	names := map[string]string{}
	stamps := map[string]string{}
	readLines(f, func(line []byte) {
		var e struct {
			ID      string `json:"id"`
			Name    string `json:"thread_name"`
			Updated string `json:"updated_at"`
		}
		if json.Unmarshal(line, &e) == nil && e.ID != "" && e.Updated >= stamps[e.ID] {
			names[e.ID], stamps[e.ID] = e.Name, e.Updated
		}
	}, nil)
	sc.mu.Lock()
	old := sc.threads
	sc.threads = names
	sc.mu.Unlock()
	for id, name := range names {
		if old[id] == name || name == "" || !sc.firstPassed.Load() {
			continue
		}
		title := clip(oneLine(name), maxTitle)
		sc.s.updateLocal(wire.KindCodex, id, func(r *Record) bool {
			if r.Meta.Title == title {
				return false
			}
			r.Meta.Title = title
			return true
		})
	}
}

// pauseEvery is how much readLines reads between pauses (the throttle's
// rest and the check for Close).
const pauseEvery = 1 << 20

// readLines calls fn with every complete line of r (without its newline)
// and returns the bytes those lines took; a last line without a newline
// is left for later. pause, when set, runs after every pauseEvery bytes
// read, also inside a long line (an error stops the reading: Close
// interrupts a pass within about that much reading).
func readLines(r io.Reader, fn func(line []byte), pause func() error) (int64, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var consumed, sincePause, pending int64
	var long []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			pending += int64(len(chunk))
			// A line of up to maxLine: still a place to stop.
			if sincePause += int64(len(chunk)); pause != nil && sincePause >= pauseEvery {
				sincePause = 0
				if err := pause(); err != nil {
					return consumed, err
				}
			}
			if !tooLong {
				if len(long)+len(chunk) > maxLine {
					tooLong, long = true, nil
				} else {
					long = append(long, chunk...)
				}
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			return consumed, nil
		}
		if err != nil {
			return consumed, err
		}
		n := pending + int64(len(chunk))
		if !tooLong {
			if long != nil {
				long = append(long, chunk...)
				fn(bytes.TrimRight(long, "\r\n"))
			} else {
				fn(bytes.TrimRight(chunk, "\r\n"))
			}
		}
		long, tooLong, pending = nil, false, 0
		consumed += n
		sincePause += int64(len(chunk)) // a long line's earlier chunks counted above
		if pause != nil && sincePause >= pauseEvery {
			sincePause = 0
			if err := pause(); err != nil {
				return consumed, err
			}
		}
	}
}

// file reads one transcript (from its checkpoint) and writes its entry.
func (sc *scanner) file(f fileInfo, th *throttle) (int64, error) {
	s := sc.s
	key := s.db.node + ":" + f.kind + ":" + f.sid
	sc.mu.Lock()
	if sc.working[f.path] {
		sc.mu.Unlock()
		return 0, nil
	}
	sc.working[f.path] = true
	c := sc.ckpts[f.path]
	thread := sc.threads[f.sid]
	sc.mu.Unlock()
	defer func() {
		sc.mu.Lock()
		delete(sc.working, f.path)
		sc.mu.Unlock()
	}()
	acc := &Accum{Kind: f.kind, SessionID: f.sid}
	var offset int64
	if c != nil && !f.zst && c.key == key && f.size >= c.offset && len(c.state) > 0 {
		if json.Unmarshal(c.state, acc) == nil {
			offset = c.offset
			if rw, _ := s.db.get(key); rw != nil {
				acc.Prompts = splitPieces(rw.rec.Meta.Prompts)
				acc.Answers = splitPieces(rw.rec.Meta.Answers)
			} else {
				acc, offset = &Accum{Kind: f.kind, SessionID: f.sid}, 0
			}
		} else {
			acc = &Accum{Kind: f.kind, SessionID: f.sid}
		}
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	var src io.Reader = fh
	if f.zst {
		dec, err := zstd.NewReader(fh, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
		if err != nil {
			return 0, err
		}
		defer dec.Close()
		src = dec
	} else if offset > 0 {
		if _, err := fh.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
	}
	line := acc.claudeLineIn
	if f.kind == wire.KindCodex {
		line = acc.codexLineIn
	}
	pause := func() error {
		th.slice()
		select {
		case <-s.stop:
			return errClosed
		default:
			return nil
		}
	}
	n, err := readLines(src, line, pause)
	sc.readTotal.Add(n)
	if err != nil {
		return n, err
	}
	end := offset + n
	if f.zst {
		end = f.size
	}
	if thread != "" {
		acc.ThreadName = thread
	}
	meta := sc.meta(acc, f)
	state, _ := json.Marshal(acc)
	err = sc.store(key, f, meta, end, state)
	return n, err
}

func splitPieces(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, pieceSep)
}

// meta is an entry's transcript fields.
func (sc *scanner) meta(acc *Accum, f fileInfo) Meta {
	s := sc.s
	last := acc.LastActivity
	if last == 0 {
		last = f.mtime / int64(time.Millisecond)
	}
	started := acc.StartedAt
	if started == 0 {
		started = last
	}
	m := Meta{Cwd: acc.Cwd, ProjectID: sc.project(acc.Cwd), Branch: acc.Branch, Title: validText(acc.Title()),
		FirstPrompt: validText(acc.FirstPrompt), LastUser: validText(acc.LastUser), LastAssistant: validText(acc.LastAssistant),
		Todos: acc.todos(), Turns: acc.Turns, Tokens: acc.Tokens, StartedAt: started, LastActivity: last, Origin: acc.Origin,
		External: !s.isOwned(acc.Kind, f.sid), Size: f.size, Version: strconv.FormatInt(f.size, 10) + ":" + strconv.FormatInt(f.mtime, 10),
		Path: f.path, UserHome: s.opt.UserHome,
		Prompts: validText(strings.Join(acc.Prompts, pieceSep)), Answers: validText(strings.Join(acc.Answers, pieceSep))}
	m.Live = s.liveOf(acc.Kind, f.sid, f.mtime)
	return m
}

// project maps a folder to its project (as agents' folders are mapped).
func (sc *scanner) project(cwd string) string {
	if cwd == "" {
		return ""
	}
	sc.mu.Lock()
	id, ok := sc.cwdProj[cwd]
	sc.mu.Unlock()
	if ok {
		return id
	}
	if p := sc.s.projectReg(); p != nil {
		id = p.Resolve(cwd)
	}
	sc.mu.Lock()
	sc.cwdProj[cwd] = id
	sc.mu.Unlock()
	return id
}

// store writes an entry and its file's checkpoint in one transaction.
func (sc *scanner) store(key string, f fileInfo, meta Meta, offset int64, state []byte) error {
	s := sc.s
	changed := false
	var rec Record
	err := s.db.write(func(tx sqlTx) error {
		// Ownership may have been claimed after meta was captured. Recheck
		// inside the serialized write so a stale scan cannot undo a resume.
		meta.External = !s.isOwned(f.kind, f.sid)
		old, err := getTx(tx, key)
		if err != nil {
			return err
		}
		if old != nil {
			rec = old.rec
			rec.Mirrors = cloneMirrors(old.rec.Mirrors)
		} else {
			rec = Record{Node: s.db.node, Kind: f.kind, SID: f.sid}
		}
		rec.Home = s.machine()
		if old == nil || !sameMeta(&old.rec.Meta, &meta) || old.rec.Home != rec.Home {
			rec.Meta, rec.MetaS = meta, s.clock.tick()
			if old == nil && f.archived {
				rec.Archived, rec.ArchivedS = true, rec.MetaS
			}
			if err := s.db.putTx(tx, old, &rec); err != nil {
				return err
			}
			changed = true
		}
		_, err = tx.Exec(`INSERT INTO files (path, size, mtime, offset, key, state) VALUES (?, ?, ?, ?, ?, ?)
 ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime, offset = excluded.offset, key = excluded.key, state = excluded.state`,
			f.path, f.size, f.mtime, offset, key, state)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrConnDone) {
			return errClosed
		}
		return err
	}
	sc.mu.Lock()
	sc.ckpts[f.path] = &checkpoint{size: f.size, mtime: f.mtime, offset: offset, key: key, state: state}
	sc.mu.Unlock()
	if changed {
		s.noteChange(key, s.visible(&rec))
	}
	return nil
}

func sameMeta(a, b *Meta) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
