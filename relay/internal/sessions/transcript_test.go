package sessions

import (
	"bytes"
	"encoding/json"
	"os"
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
