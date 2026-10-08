package transport_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/internal/sessions"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Synthetic transcripts in the CLIs' formats.

func claudeLine(v map[string]any) string {
	data, _ := json.Marshal(v)
	return string(data) + "\n"
}

// writeClaude writes a Claude Code transcript of turns (prompt, answer,
// prompt, answer, …) under home's ~/.claude.
func writeClaude(t *testing.T, home, cwd, sid, title string, at time.Time, turns ...string) string {
	t.Helper()
	var b strings.Builder
	for i, text := range turns {
		ts := at.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano)
		base := map[string]any{"parentUuid": nil, "isSidechain": false, "userType": "external", "cwd": cwd, "sessionId": sid, "version": "2.1.290",
			"gitBranch": "main", "uuid": fmt.Sprintf("u-%d", i), "timestamp": ts, "entrypoint": "cli"}
		if i%2 == 0 {
			base["type"] = "user"
			base["message"] = map[string]any{"role": "user", "content": text}
		} else {
			base["type"] = "assistant"
			base["message"] = map[string]any{"id": fmt.Sprintf("msg_%d", i), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 5}}
		}
		b.WriteString(claudeLine(base))
	}
	if title != "" {
		b.WriteString(claudeLine(map[string]any{"type": "custom-title", "customTitle": title, "sessionId": sid}))
	}
	path := filepath.Join(home, ".claude", "projects", handoff.ClaudeSlug(cwd), sid+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	os.Chtimes(path, old, old)
	return path
}

// writeCodex writes a Codex rollout under home's ~/.codex.
func writeCodex(t *testing.T, home, cwd, sid string, at time.Time, prompt, answer string) string {
	t.Helper()
	ts := func(i int) string { return at.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano) }
	var b strings.Builder
	b.WriteString(claudeLine(map[string]any{"timestamp": ts(0), "ordinal": 0, "type": "session_meta", "payload": map[string]any{"id": sid, "session_id": sid,
		"timestamp": ts(0), "cwd": cwd, "originator": "codex-tui", "source": "cli", "cli_version": "0.160.1"}}))
	b.WriteString(claudeLine(map[string]any{"timestamp": ts(1), "ordinal": 1, "type": "response_item", "payload": map[string]any{"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": prompt}}}}))
	b.WriteString(claudeLine(map[string]any{"timestamp": ts(2), "ordinal": 2, "type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": answer}}}}))
	path := filepath.Join(home, ".codex", "sessions", at.Format("2006"), at.Format("01"), at.Format("02"), "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+sid+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	os.Chtimes(path, old, old)
	return path
}

func (n *node) sessions(t *testing.T, p wire.SessionSearchParams) []wire.Session {
	t.Helper()
	var res wire.SessionSearchResult
	if err := n.call(t, "sessions.search", p, &res); err != nil {
		t.Fatal(err)
	}
	return res.Items
}

func (n *node) sessionByID(t *testing.T, id string) (wire.Session, bool) {
	t.Helper()
	var d wire.SessionDetail
	if err := n.call(t, "sessions.show", wire.SessionIDParams{ID: id}, &d); err != nil {
		return wire.Session{}, false
	}
	return d.Session, true
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

// One shared history: each Mac indexes its own transcripts and every
// other Mac gets the entries (search works there, offline too); archive
// and delete travel as last-writer-wins fields; recent transcripts are
// mirrored; sessions resume on their home, move to another Mac with
// their uncommitted work when the home is reachable or from the mirror
// when it is not, and never resume while live. The relay sees nothing.
func TestSessionsSharedHistory(t *testing.T) {
	oldUndo := sessions.UndoWindow
	sessions.UndoWindow = 300 * time.Millisecond
	t.Cleanup(func() { sessions.UndoWindow = oldUndo })
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	M.waitLinked(t, "L")
	now := time.Now().Add(-time.Hour)
	const (
		sidA   = "aaaaaaaa-0000-4000-8000-00000000000a" // M, resumed on L with M offline
		sidB   = "019b0000-0000-7000-8000-00000000000b" // M (codex), moved to L with uncommitted work
		sidC   = "cccccccc-0000-4000-8000-00000000000c" // L
		sidD   = "dddddddd-0000-4000-8000-00000000000d" // M, resumed on M from L
		sidOld = "eeeeeeee-0000-4000-8000-00000000000e" // M, 30 days old: not mirrored, deleted
	)
	writeClaude(t, M.home, M.project, sidA, "SECRETTITLE-A", now, "Fix the SECRETWORD-flaky badge", "20 runs green; the race was in the SECRETWORD-debounce.")
	svc := filepath.Join(M.home, "projects", "SECRETPROJ-svc")
	os.MkdirAll(svc, 0o755)
	gitIn(t, svc, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(svc, "main.go"), []byte("package main\n"), 0o644)
	gitIn(t, svc, "add", ".")
	gitIn(t, svc, "commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(svc, "SECRETFILE-wip.txt"), []byte("uncommitted\n"), 0o644)
	writeCodex(t, M.home, svc, sidB, now, "Port the push provider to FCM", "The SECRETWORD-fcm client is in place.")
	writeClaude(t, L.home, L.project, sidC, "", now, "Laptop work on the rail", "Done on the laptop.")
	writeClaude(t, M.home, M.project, sidD, "", now, "Mini session to resume at home", "Waiting for you.")
	oldPath := writeClaude(t, M.home, M.project, sidOld, "", time.Now().Add(-30*24*time.Hour), "An old session", "Long ago.")
	old := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(oldPath, old, old)

	// Every entry on both Macs.
	for _, n := range []*node{L, M} {
		eventually(t, n.short+" lists all five", func() bool { return len(n.sessions(t, wire.SessionSearchParams{})) == 5 })
	}
	hits := L.sessions(t, wire.SessionSearchParams{Query: "SECRETWORD debounce"})
	if len(hits) != 1 || hits[0].ID != "M:claude:"+sidA || hits[0].Machine != "M" || hits[0].Title != "SECRETTITLE-A" || !strings.Contains(hits[0].Snippet, "[") {
		t.Fatalf("L's search for M's session: %+v", hits)
	}
	if got := L.sessions(t, wire.SessionSearchParams{Machines: []string{"M"}}); len(got) != 4 {
		t.Fatalf("machine filter: %d", len(got))
	}
	// The mirror: recent transcripts copied to L, the old one not.
	eventually(t, "L mirrors M's recent transcripts", func() bool {
		s, ok := L.sessionByID(t, "M:claude:"+sidA)
		return ok && len(s.Mirrored) == 2 && s.Mirrored[1] == "L"
	})
	if _, err := os.Stat(filepath.Join(L.state, "history", "mirror", "M", "claude", sidA+".jsonl.zst")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(L.state, "history", "mirror", "M", "claude", sidOld+".jsonl.zst")); err == nil {
		t.Fatal("a 30 day old transcript was mirrored")
	}
	var stats wire.SessionStats
	if err := L.call(t, "sessions.stats", nil, &stats); err != nil || stats.MirrorBytes == 0 || stats.Count != 5 || stats.ByMachine["M"] != 4 || stats.ByMachine["L"] != 1 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	// M sees that L holds a copy.
	eventually(t, "M knows L's mirror", func() bool {
		s, ok := M.sessionByID(t, "M:claude:"+sidA)
		return ok && len(s.Mirrored) == 2
	})

	// Archive on L → M.
	var arch wire.Session
	if err := L.call(t, "sessions.archive", wire.SessionArchiveParams{ID: "M:claude:" + sidOld, Archived: true}, &arch); err != nil || !arch.Archived {
		t.Fatalf("archive %+v %v", arch, err)
	}
	eventually(t, "M sees the archive", func() bool { s, _ := M.sessionByID(t, "M:claude:"+sidOld); return s.Archived })

	// Resume on its home, from L.
	var onM sessions.ResumeResult
	if err := L.call(t, "sessions.resume", wire.SessionResumeParams{ID: "M:claude:" + sidD}, &onM); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(onM.ID, "M/") || onM.SessionID != sidD {
		t.Fatalf("resumed %+v", onM.Agent)
	}
	eventually(t, "M started it with --resume", func() bool { return strings.Contains(M.argvs(onM.ID), `"--resume","`+sidD+`"`) })
	// Live now: never resumed twice; the app opens the agent.
	eventually(t, "L knows it is live", func() bool {
		s, _ := L.sessionByID(t, "M:claude:"+sidD)
		return s.Live != nil && s.Live.AgentID == onM.ID
	})
	err := L.call(t, "sessions.resume", wire.SessionResumeParams{ID: "M:claude:" + sidD}, nil)
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.CodeLive || we.AgentID != onM.ID {
		t.Fatalf("resume of a live session: %v", err)
	}
	// A fork is a copy: allowed while the session runs, also on another
	// Mac (the home packs it as it is).
	var forked sessions.ResumeResult
	if err := L.call(t, "sessions.fork", wire.SessionResumeParams{ID: "M:claude:" + sidD, Machine: "L"}, &forked); err != nil {
		t.Fatalf("fork of a live session to L: %v", err)
	}
	if !strings.HasPrefix(forked.ID, "L/") {
		t.Fatalf("forked %+v", forked.Agent)
	}
	eventually(t, "L forked it", func() bool { return strings.Contains(L.argvs(forked.ID), "--fork-session") })

	// Move B to L while M is reachable: the conversation and the
	// uncommitted work come along; ownership moves.
	var moved sessions.ResumeResult
	if err := L.call(t, "sessions.resume", wire.SessionResumeParams{ID: "M:codex:" + sidB, Machine: "L"}, &moved); err != nil {
		t.Fatal(err)
	}
	svcL := filepath.Join(L.home, "projects", "SECRETPROJ-svc")
	if !strings.HasPrefix(moved.ID, "L/") || moved.Project != svcL || moved.Note != "" {
		t.Fatalf("moved %+v", moved)
	}
	if data, err := os.ReadFile(filepath.Join(svcL, "SECRETFILE-wip.txt")); err != nil || string(data) != "uncommitted\n" {
		t.Fatalf("uncommitted work: %q %v", data, err)
	}
	if st := gitIn(t, svcL, "status", "--porcelain"); !strings.Contains(st, "SECRETFILE-wip.txt") {
		t.Fatalf("not uncommitted on L: %q", st)
	}
	eventually(t, "L resumed the codex session", func() bool {
		a := L.argvs(moved.ID)
		return strings.Contains(a, `"resume"`) && strings.Contains(a, sidB)
	})
	eventually(t, "L indexes the moved session as its own", func() bool {
		s, ok := L.sessionByID(t, "L:codex:"+sidB)
		return ok && !s.External && s.Cwd == svcL
	})
	eventually(t, "M's entry moved to L (both Macs)", func() bool {
		for _, n := range []*node{L, M} {
			got := n.sessions(t, wire.SessionSearchParams{Moved: true, Kinds: []string{"codex"}})
			found := false
			for _, s := range got {
				if s.ID == "M:codex:"+sidB && s.MovedTo == "L" {
					found = true
				}
			}
			if !found {
				return false
			}
		}
		return true
	})
	// The old entry is history: resuming it goes to the new home (live).
	if err := L.call(t, "sessions.resume", wire.SessionResumeParams{ID: "M:codex:" + sidB}, nil); !errors.As(err, &we) || we.Code != wire.CodeLive {
		t.Fatalf("resume of the moved entry: %v", err)
	}

	// Delete (a tombstone): M removes its file after the undo window.
	if err := L.call(t, "sessions.delete", wire.SessionDeleteParams{ID: "M:claude:" + sidOld}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "M removed the transcript", func() bool { _, err := os.Stat(oldPath); return os.IsNotExist(err) })
	for _, n := range []*node{L, M} {
		yes := true
		if got := n.sessions(t, wire.SessionSearchParams{Archived: &yes}); len(got) != 0 {
			t.Fatalf("%s lists the deleted session", n.short)
		}
	}

	// M goes away: L still searches M's sessions, and resumes A here from
	// its copy.
	M.stop()
	eventually(t, "L lost M", func() bool { return L.d.Fleet.Route("M") == "" })
	if got := L.sessions(t, wire.SessionSearchParams{Query: "flaky"}); len(got) != 1 || got[0].ID != "M:claude:"+sidA {
		t.Fatalf("offline search: %+v", got)
	}
	var here sessions.ResumeResult
	if err := L.call(t, "sessions.resume", wire.SessionResumeParams{ID: "M:claude:" + sidA, Machine: "L"}, &here); err != nil {
		t.Fatal(err)
	}
	if here.Note == "" || !strings.Contains(here.Note, "not reachable") || here.Project != L.project {
		t.Fatalf("offline resume %+v", here)
	}
	eventually(t, "L resumes A", func() bool { return strings.Contains(L.argvs(here.ID), `"--resume","`+sidA+`"`) })
	placed := filepath.Join(L.home, ".claude", "projects", handoff.ClaudeSlug(L.project), sidA+".jsonl")
	data, err := os.ReadFile(placed)
	if err != nil || !strings.Contains(string(data), `"cwd":"`+L.project+`"`) || strings.Contains(string(data), M.home) {
		t.Fatalf("placed transcript: %v %.300s", err, data)
	}

	// The relay saw none of it.
	if leaked := w.tap.sawAny("SECRETWORD", "SECRETTITLE", "SECRETPROJ", "SECRETFILE", sidA, sidB, sidC, sidD, "Fix the", "flaky"); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}
	if w.tap.count(`"method":"sessions.`) != 0 {
		t.Fatal("a sessions method went in plaintext")
	}
}

// The first sync of a large history: 2,000 entries replicate in batches;
// measured (go test -run SessionsReplicateThousands -v).
func TestSessionsReplicateThousands(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	const n = 2000
	start := time.Now()
	filler := strings.Repeat("lorem ipsum dolor sit amet consectetur ", 60) // ~2.3 KB per answer
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i)
		at := time.Now().Add(-time.Duration(i) * time.Hour)
		writeClaude(t, M.home, M.project, sid, "", at, fmt.Sprintf("task %d about widget-%d", i, i), filler+fmt.Sprintf("answer %d", i),
			"keep going "+filler, fmt.Sprintf("final %d ", i)+filler)
	}
	t.Logf("wrote %d transcripts in %v", n, time.Since(start))
	start = time.Now()
	var st wire.SessionStats
	eventually2(t, 3*time.Minute, "M indexed all", func() bool {
		M.call(t, "sessions.stats", nil, &st)
		return st.Count == n
	})
	indexed := time.Since(start)
	start = time.Now()
	eventually2(t, 3*time.Minute, "L has all", func() bool {
		L.call(t, "sessions.stats", nil, &st)
		return st.ByMachine["M"] == n
	})
	t.Logf("M indexed %d sessions in %v; L had them all %v later; L's index %.1f MB (%.2f MB per 1,000 sessions)", n, indexed, time.Since(start),
		float64(st.IndexBytes)/(1<<20), float64(st.IndexBytes)/(1<<20)/(n/1000.0))
	// Search over the replicated index.
	for _, q := range []string{"widget-1234", "lorem", "final 77"} {
		t0 := time.Now()
		got := L.sessions(t, wire.SessionSearchParams{Query: q})
		t.Logf("search %q: %d hits in %v (through the socket)", q, len(got), time.Since(t0))
		if len(got) == 0 {
			t.Fatalf("search %q found nothing", q)
		}
	}
}

func eventually2(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
