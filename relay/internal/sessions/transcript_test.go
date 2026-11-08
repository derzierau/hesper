package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fixtureItems parses a fixture line by line the way the transcript
// reader does, returning the items and each line's byte offset.
func fixtureItems(t *testing.T, kind, path string) ([]wire.TranscriptItem, []int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var items []wire.TranscriptItem
	var offs []int64
	var off int64
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		offs = append(offs, off)
		items = append(items, itemsFrom(kind, bytes.TrimSuffix(line, []byte("\n")), off)...)
		off += int64(len(line))
	}
	return items, offs
}

func ok(b bool) *bool { return &b }

func sameItems(t *testing.T, got, want []wire.TranscriptItem) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", "  ")
		w, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("items:\n got %s\nwant %s", g, w)
	}
}

func TestClaudeItems(t *testing.T) {
	got, offs := fixtureItems(t, wire.KindClaude, "testdata/transcript_claude.jsonl")
	at := func(s string) int64 { return parseTime("2026-10-01T10:00:" + s + ".000Z") }
	want := []wire.TranscriptItem{
		{Seq: offs[1], At: at("00"), Kind: wire.ItemUser, Text: "Fix the yak's flaky badge"},
		{Seq: offs[4], At: at("03"), Kind: wire.ItemAssistant, Text: "Looking at the badge."},
		{Seq: offs[5], At: at("04"), Kind: wire.ItemTool, Tool: "Read", Summary: "Badge.swift", ID: "toolu_01"},
		{Seq: offs[6], At: at("05"), Kind: wire.ItemResult, Of: "toolu_01", Text: "1\tstruct Badge {}\n", OK: ok(true)},
		{Seq: offs[7], At: at("06"), Kind: wire.ItemTool, Tool: "Bash", Summary: "swift test --filter Badge", ID: "toolu_02"},
		{Seq: offs[8], At: at("07"), Kind: wire.ItemResult, Of: "toolu_02", Text: "error: build failed\n[image]", OK: ok(false)},
		{Seq: offs[12], At: at("11"), Kind: wire.ItemSystem, Text: "Context compacted"},
		{Seq: offs[15], At: at("14"), Kind: wire.ItemUser, Text: "Only the rail badge,\nnot the tab bar."},
		{Seq: offs[16], At: at("15"), Kind: wire.ItemTool, Tool: "Grep", Summary: "RailBadge", ID: "toolu_03"},
	}
	sameItems(t, got, want)
}

func TestClaudeResultBlocks(t *testing.T) {
	line := `{"type":"user","timestamp":"2026-10-01T10:00:00.000Z","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_a","content":"first"},` +
		`{"type":"tool_result","tool_use_id":"toolu_b","content":[{"type":"text","text":"second"},{"type":"text","text":"half"}],"is_error":false},` +
		`{"type":"tool_result","tool_use_id":"toolu_c","is_error":true}]}}`
	at := parseTime("2026-10-01T10:00:00.000Z")
	sameItems(t, itemsFrom(wire.KindClaude, []byte(line), 42), []wire.TranscriptItem{
		{Seq: 42, At: at, Kind: wire.ItemResult, Of: "toolu_a", Text: "first", OK: ok(true)},
		{Seq: 43, At: at, Kind: wire.ItemResult, Of: "toolu_b", Text: "second\nhalf", OK: ok(true)},
		{Seq: 44, At: at, Kind: wire.ItemResult, Of: "toolu_c", OK: ok(false)},
	})

	long := strings.Repeat("x\n", 100)
	raw, _ := json.Marshal(long)
	line = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_d","content":` + string(raw) + `}]}}`
	items := itemsFrom(wire.KindClaude, []byte(line), 7)
	if len(items) != 1 || !items[0].Truncated || strings.Count(items[0].Text, "\n") != 59 || items[0].At != 0 {
		t.Fatalf("long result not clipped: %+v", items)
	}

	for _, bad := range []string{``, `not json`, `{"type":"user"}`, `{"type":"assistant","message":{"content":"plain"}}`} {
		if items := itemsFrom(wire.KindClaude, []byte(bad), 0); len(items) != 0 {
			t.Errorf("%q: items %+v", bad, items)
		}
	}
}

// The phone keys items by seq: the items of one line get off, off+1, …
func TestClaudeMultiResultSeq(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_a","content":"a"},` +
		`{"type":"tool_result","tool_use_id":"toolu_b","content":"b"},` +
		`{"type":"tool_result","tool_use_id":"toolu_c","content":"c"}]}}`
	const off = 1000
	items := itemsFrom(wire.KindClaude, []byte(line), off)
	if len(items) != 3 {
		t.Fatalf("items: %+v", items)
	}
	for i, it := range items {
		if it.Seq != off+int64(i) {
			t.Errorf("item %d (%s): seq %d, want %d", i, it.Of, it.Seq, off+int64(i))
		}
	}
	if last := items[2].Seq; last >= off+int64(len(line)) {
		t.Errorf("seq %d reaches the next line", last)
	}
}

func TestCodexItems(t *testing.T) {
	got, offs := fixtureItems(t, wire.KindCodex, "testdata/transcript_codex.jsonl")
	at := func(s string) int64 { return parseTime("2026-10-02T09:00:" + s + ".000Z") }
	want := []wire.TranscriptItem{
		{Seq: offs[3], At: at("01"), Kind: wire.ItemUser, Text: "Add FCM push for the yak app"},
		{Seq: offs[5], At: at("03"), Kind: wire.ItemTool, Tool: "shell", Summary: "bash -lc go test ./push/...", ID: "call_01"},
		{Seq: offs[6], At: at("04"), Kind: wire.ItemResult, Of: "call_01", Text: "ok  \tpush\t0.12s\n", OK: ok(true)},
		{Seq: offs[8], At: at("06"), Kind: wire.ItemAssistant, Text: "FCM push is in; the tests pass."},
	}
	sameItems(t, got, want)

	if items := itemsFrom("shell", []byte(`{"type":"user","message":{"content":"hi"}}`), 0); len(items) != 0 {
		t.Errorf("unknown kind: items %+v", items)
	}
}

func TestToolSummary(t *testing.T) {
	long := strings.Repeat("é", 100)
	for _, c := range []struct{ name, input, want string }{
		{"Read", `{"file_path":"/work/yak/Sources/Badge.swift"}`, "Badge.swift"},
		{"Edit", `{"file_path":"/work/yak/a.go","old_string":"x"}`, "a.go"},
		{"Write", `{"path":"/work/yak/notes.md"}`, "notes.md"},
		{"NotebookEdit", `{"notebook_path":"/work/yak/plot.ipynb"}`, "plot.ipynb"},
		{"Read", `{}`, ""},
		{"Bash", `{"command":"  make test\nmake check"}`, "make test"},
		{"Bash", `{"command":"` + long + `"}`, strings.Repeat("é", 79) + "…"},
		{"Bash", `{"command":"` + strings.Repeat("é", 80) + `"}`, strings.Repeat("é", 80)},
		{"shell", `{"command":["bash","-lc","ls\npwd"]}`, "bash -lc ls"},
		{"shell", `{"command":"ls -la"}`, "ls -la"},
		{"Grep", `{"pattern":"func Test","path":"."}`, "func Test"},
		{"Glob", `{"pattern":"**/*.swift"}`, "**/*.swift"},
		{"Agent", `{"description":"Find badge views","prompt":"…"}`, "Find badge views"},
		{"Task", `{"description":"Audit"}`, "Audit"},
		{"WebFetch", `{"url":"https://example.com"}`, ""},
		{"Read", `not json`, ""},
		{"Bash", ``, ""},
	} {
		if got := toolSummary(c.name, json.RawMessage(c.input)); got != c.want {
			t.Errorf("toolSummary(%s, %s) = %q, want %q", c.name, c.input, got, c.want)
		}
	}
}

func TestClipResult(t *testing.T) {
	lines := make([]string, 61)
	for i := range lines {
		lines[i] = "line"
	}
	text, cut := clipResult(strings.Join(lines, "\n"))
	if !cut || text != strings.Join(lines[:60], "\n") {
		t.Errorf("61 lines: cut %v, %d lines", cut, strings.Count(text, "\n")+1)
	}

	text, cut = clipResult(strings.Join(lines[:60], "\n") + "\n")
	if cut || strings.Count(text, "\n") != 60 {
		t.Errorf("60 lines and a newline: cut %v", cut)
	}

	text, cut = clipResult(strings.Repeat("a", 5*1024))
	if !cut || len(text) != 4096 {
		t.Errorf("5 KiB: cut %v, %d bytes", cut, len(text))
	}

	// A cut never splits a rune.
	text, cut = clipResult("a" + strings.Repeat("é", 3000))
	if !cut || len(text) != 4095 || !strings.HasSuffix(text, "é") {
		t.Errorf("runes: cut %v, %d bytes", cut, len(text))
	}

	for _, s := range []string{"", "short", "two\nlines"} {
		if text, cut := clipResult(s); cut || text != s {
			t.Errorf("%q: got %q, cut %v", s, text, cut)
		}
	}
}

// lineOf is the offset of the line that holds seq.
func lineOf(offs []int64, seq int64) int64 {
	var line int64
	for _, o := range offs {
		if o <= seq {
			line = o
		}
	}
	return line
}

// fromLine is items from the line at off on.
func fromLine(items []wire.TranscriptItem, off int64) []wire.TranscriptItem {
	for i, it := range items {
		if it.Seq >= off {
			return items[i:]
		}
	}
	return nil
}

func TestTranscriptPage(t *testing.T) {
	for _, c := range []struct{ kind, path string }{
		{wire.KindClaude, "testdata/transcript_claude.jsonl"},
		{wire.KindCodex, "testdata/transcript_codex.jsonl"},
	} {
		all, offs := fixtureItems(t, c.kind, c.path)
		f, err := openTranscript(c.path, c.kind)
		if err != nil {
			t.Fatal(err)
		}
		// Whole lines, from the line of the 3rd last item on.
		p, err := f.page(0, 3)
		if err != nil {
			t.Fatal(err)
		}
		if !p.More {
			t.Errorf("%s: limit 3: More false", c.kind)
		}
		sameItems(t, p.Items, fromLine(all, lineOf(offs, all[len(all)-3].Seq)))
		p, err = f.page(0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if p.More {
			t.Errorf("%s: limit 100: More true", c.kind)
		}
		sameItems(t, p.Items, all)
	}
}

func TestTranscriptPageBefore(t *testing.T) {
	const path = "testdata/transcript_claude.jsonl"
	all, offs := fixtureItems(t, wire.KindClaude, path)
	f, err := openTranscript(path, wire.KindClaude)
	if err != nil {
		t.Fatal(err)
	}
	// An offset inside a line: the lines before that line only.
	line := lineOf(offs, all[len(all)/2].Seq)
	var want []wire.TranscriptItem
	for _, it := range all {
		if it.Seq < line {
			want = append(want, it)
		}
	}
	p, err := f.page(line+1, 100)
	if err != nil {
		t.Fatal(err)
	}
	sameItems(t, p.Items, want)
	if p.More {
		t.Error("before a middle line, limit 100: More true")
	}
	// The first line's offset: nothing before it.
	p, err = f.page(all[0].Seq, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 0 || p.More {
		t.Errorf("before the first line: %d items, More %v", len(p.Items), p.More)
	}
}

func TestTranscriptPathShell(t *testing.T) {
	s := &Service{}
	for _, a := range []wire.Agent{{ID: "a1", Kind: wire.KindShell}, {ID: "a2", Kind: wire.KindClaude}} {
		var we *wire.Error
		if _, err := s.TranscriptPath(a); !errors.As(err, &we) || we.Code != wire.CodeUnavailable {
			t.Errorf("%s: TranscriptPath err %v, want unavailable", a.ID, err)
		}
		if _, err := s.Transcript(context.Background(), a, 0, 0); !errors.As(err, &we) || we.Code != wire.CodeUnavailable {
			t.Errorf("%s: Transcript err %v, want unavailable", a.ID, err)
		}
	}
}

// A Codex rollout the index has not seen yet is found by its glob, and
// a page after the file grew holds the new line.
func TestTranscriptGlob(t *testing.T) {
	e := newEnv(t)
	s := e.open(Options{})
	const sid = "0199a000-0000-7000-8000-00000000c0de"
	data, err := os.ReadFile("testdata/transcript_codex.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.codex, "sessions", "2026", "11", "08")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-08T10-00-00-"+sid+".jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	a := wire.Agent{ID: "a1", Kind: wire.KindCodex, SessionID: sid}
	if got, err := s.TranscriptPath(a); err != nil || got != path {
		t.Fatalf("TranscriptPath = %q, %v; want %q", got, err, path)
	}
	p, err := s.Transcript(context.Background(), a, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := fixtureItems(t, wire.KindCodex, "testdata/transcript_codex.jsonl")
	sameItems(t, p.Items, all)

	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(`{"timestamp":"2026-10-02T09:00:08.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"later"}]}}` + "\n")
	fh.Close()
	p, err = s.Transcript(context.Background(), a, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Text != "later" || p.Items[0].Seq != int64(len(data)) || !p.More {
		t.Errorf("after the append: %+v", p)
	}
}
