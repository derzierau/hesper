package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// moreWorld is a test daemon with ctl (run) and code (execute: the exit
// code) on its socket.
type moreWorld struct {
	t       *testing.T
	reg     *agents.Registry
	sock    string
	project string
}

func newMoreWorld(t *testing.T) *moreWorld {
	reg, sock, project := daemon(t)
	return &moreWorld{t, reg, sock, project}
}

func (w *moreWorld) args(args []string) []string {
	return append([]string{args[0], "--daemon-socket", w.sock}, args[1:]...)
}

func (w *moreWorld) ctl(args ...string) error {
	return run(context.Background(), w.args(args))
}

func (w *moreWorld) code(args ...string) int {
	return execute(context.Background(), w.args(args), io.Discard)
}

// out runs a command and returns its stdout; it must succeed.
func (w *moreWorld) out(args ...string) string {
	w.t.Helper()
	var err error
	out := capture(w.t, func() { err = w.ctl(args...) })
	if err != nil {
		w.t.Fatalf("%v: %v", args, err)
	}
	return out
}

// spawn starts a cat agent and waits until it runs.
func (w *moreWorld) spawn(task string) wire.Agent {
	w.t.Helper()
	var a wire.Agent
	if err := json.Unmarshal([]byte(w.out("new", "--json", "--project", w.project, task)), &a); err != nil {
		w.t.Fatal(err)
	}
	eventually(w.t, "running", func() bool { g, _ := w.reg.Get(a.ID); return g.PID != 0 })
	return a
}

func (w *moreWorld) state(id string) string {
	g, err := w.reg.Get(id)
	if err != nil {
		return "gone"
	}
	return g.State
}

func (w *moreWorld) approval(id string) {
	w.t.Helper()
	w.reg.Hook(wire.HookParams{Agent: id, Source: "claude", Event: "PermissionRequest", Payload: json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"git push"}}`)})
	if s := w.state(id); s != wire.StateApproval {
		w.t.Fatalf("state %s", s)
	}
}

func (w *moreWorld) hook(id, event string) {
	w.reg.Hook(wire.HookParams{Agent: id, Source: "claude", Event: event, Payload: json.RawMessage(`{}`)})
}

func TestSendKeysScreenShow(t *testing.T) {
	w := newMoreWorld(t)
	a := w.spawn("first")
	if err := w.ctl("send", a.ID, "--raw", "abc"); err != nil {
		t.Fatal(err)
	}
	if err := w.ctl("send", a.ID, "--key", "backspace", "--key", "x", "--key", "enter"); err != nil {
		t.Fatal(err)
	}
	// cat echoes the line, then prints it: "abx" twice.
	eventually(t, "keys", func() bool { return strings.Count(screenOf(w.reg, a.ID), "abx") == 2 })
	if code := w.code("send", a.ID, "--key", "hyper-x"); code != exitUsage {
		t.Fatalf("unknown key: exit %d", code)
	}
	if code := w.code("send", a.ID); code != exitUsage {
		t.Fatalf("nothing to send: exit %d", code)
	}

	out := w.out("screen", a.ID)
	if !strings.Contains(out, "abx\nabx") || strings.Contains(out, "\x1b") || strings.HasSuffix(out, "\n\n") {
		t.Fatalf("screen:\n%q", out)
	}
	var res wire.ScreenResult
	if err := json.Unmarshal([]byte(w.out("screen", a.ID, "--rows", "3", "--json")), &res); err != nil {
		t.Fatal(err)
	}
	// --rows: the last rows with text (the blank screen below is dropped).
	if res.Cols == 0 || res.Rows == 0 || !strings.HasSuffix(res.Text, "abx\nabx") || strings.Count(res.Text, "\n") > 2 || res.Cursor == nil {
		t.Fatalf("screen --json %+v", res)
	}
	if out := w.out("screen", a.ID, "--rows", "1"); out != "abx\n" {
		t.Fatalf("screen --rows 1: %q", out)
	}
	if code := w.code("screen", "nope00"); code != exitNotFound {
		t.Fatalf("unknown agent: exit %d", code)
	}

	w.approval(a.ID)
	out = w.out("show", a.ID)
	for _, want := range []string{a.ID, "approval", "Bash", "git push", "1. allow  2. always  3. deny"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show lacks %q:\n%s", want, out)
		}
	}
	var d agentDetail
	if err := json.Unmarshal([]byte(w.out("show", a.ID, "--json")), &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != a.ID || len(d.Choices) != 3 || d.Choices[2].Decision != wire.Deny || d.Attention == nil {
		t.Fatalf("show --json %+v", d)
	}
}

func TestAnswerAndChoose(t *testing.T) {
	w := newMoreWorld(t)
	a := w.spawn("answer me")
	w.approval(a.ID)
	if code := w.code("answer", a.ID, "maybe"); code != exitUsage {
		t.Fatalf("bad decision: exit %d", code)
	}
	if code := w.code("answer", a.ID, "allow", "--message", "x"); code != exitUsage {
		t.Fatalf("message without deny: exit %d", code)
	}
	if err := w.ctl("answer", a.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	if s := w.state(a.ID); s != wire.StateWorking {
		t.Fatalf("after allow: %s", s)
	}
	w.approval(a.ID)
	if err := w.ctl("answer", a.ID, "deny", "--message", "try another way"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "deny message", func() bool { return strings.Contains(screenOf(w.reg, a.ID), "try another way") })

	// choose: an approval's third choice is deny.
	w.approval(a.ID)
	if code := w.code("choose", a.ID, "4"); code != exitUsage {
		t.Fatalf("choice 4 of 3: exit %d", code)
	}
	if err := w.ctl("choose", a.ID, "3"); err != nil {
		t.Fatal(err)
	}
	if s := w.state(a.ID); s != wire.StateIdle {
		t.Fatalf("after choose deny: %s", s)
	}
	// A question with numbered options: its number is typed.
	w.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "PreToolUse",
		Payload: json.RawMessage(`{"tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which tone? 1. Formal 2. Casual 3. Pirate"}]}}`)})
	if s := w.state(a.ID); s != wire.StateQuestion {
		t.Fatalf("question: %s", s)
	}
	if out := w.out("show", a.ID); !strings.Contains(out, "Which tone?") || !strings.Contains(out, "3. Pirate") {
		t.Fatalf("show question:\n%s", out)
	}
	threes := strings.Count(screenOf(w.reg, a.ID), "3")
	if err := w.ctl("choose", a.ID, "3"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "typed 3", func() bool { return strings.Count(screenOf(w.reg, a.ID), "3") > threes })
	// Nothing to choose while working.
	w.hook(a.ID, "UserPromptSubmit")
	if code := w.code("choose", a.ID, "1"); code != exitError {
		t.Fatalf("no choices: exit %d", code)
	}
}

func TestChoices(t *testing.T) {
	q := func(detail string, options ...string) wire.Agent {
		return wire.Agent{State: wire.StateQuestion, Attention: &wire.Attention{Kind: "question", Detail: detail, Options: options}}
	}
	titles := func(a wire.Agent) []string {
		var out []string
		for _, c := range choices(a) {
			out = append(out, c.Title+"|"+c.Decision+c.Keys)
		}
		return out
	}
	cases := []struct {
		a    wire.Agent
		want []string
	}{
		{wire.Agent{State: wire.StateApproval, Attention: &wire.Attention{Kind: "approval", Options: []string{"deny", "allow"}}}, []string{"allow|allow", "deny|deny"}},
		{wire.Agent{State: wire.StateApproval}, []string{"allow|allow", "deny|deny"}},
		{q("Trust ~/x?", "trust", "exit"), []string{"trust|trust", "exit|exit"}},
		{q("Pick: 1) Red, or 2) Blue"), []string{"Red|1", "Blue|2"}},
		{q("[1] A [2] B [3] C"), []string{"A|1", "B|2", "C|3"}},
		{q("Do it in 2 steps. 1. now"), nil},
		{q("Open the agent to log in"), nil},
		{wire.Agent{State: wire.StateWorking}, nil},
	}
	for _, c := range cases {
		if got := titles(c.a); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.a.Attention, got, c.want)
		}
	}
	if q, opts, ok := numberedOptions("Which tone?\n1. Formal\n2. Casual;"); !ok || q != "Which tone?" || !slices.Equal(opts, []string{"Formal", "Casual"}) {
		t.Errorf("numbered: %q %q %v", q, opts, ok)
	}
}

func TestCloseKillBackgroundTidy(t *testing.T) {
	w := newMoreWorld(t)
	a, b, c, d := w.spawn("one"), w.spawn("two"), w.spawn("three"), w.spawn("four")
	if err := w.ctl("background", a.ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := w.reg.Get(a.ID); !g.Background {
		t.Fatal("not in the background")
	}
	if err := w.ctl("background", a.ID, "--off"); err != nil {
		t.Fatal(err)
	}
	if g, _ := w.reg.Get(a.ID); g.Background {
		t.Fatal("still in the background")
	}
	if err := w.ctl("kill", b.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "killed", func() bool {
		g, _ := w.reg.Get(b.ID)
		return g.State == wire.StateExited && g.Ended == wire.EndedKilled
	})

	// tidy: done c and the killed b go; working a and approval d stay.
	w.hook(c.ID, "Stop")
	w.hook(a.ID, "UserPromptSubmit")
	w.approval(d.ID)
	if s := w.state(c.ID); s != wire.StateDone {
		t.Fatalf("c: %s", s)
	}
	out := w.out("tidy", "--dry-run")
	if !strings.Contains(out, b.ID) || !strings.Contains(out, c.ID) || strings.Contains(out, a.ID) || strings.Contains(out, d.ID) || w.state(c.ID) == "gone" {
		t.Fatalf("tidy --dry-run:\n%s", out)
	}
	var list []closed
	if err := json.Unmarshal([]byte(w.out("tidy", "--json")), &list); err != nil || len(list) != 2 {
		t.Fatalf("tidy --json %v %+v", err, list)
	}
	// tidy returns once they are gone.
	if w.state(b.ID) != "gone" || w.state(c.ID) != "gone" {
		t.Fatalf("tidy returned before the agents left: b %s, c %s", w.state(b.ID), w.state(c.ID))
	}
	if w.state(a.ID) == "gone" || w.state(d.ID) == "gone" {
		t.Fatal("tidy closed a working or waiting agent")
	}
	if err := json.Unmarshal([]byte(w.out("close", a.ID, d.ID, "--json")), &list); err != nil || len(list) != 2 || list[0].ID != a.ID {
		t.Fatalf("close --json %v %+v", err, list)
	}
	// close returns once they are gone (a running one is interrupted and
	// ended first).
	if n := len(w.reg.List()); n != 0 {
		t.Fatalf("close returned with %d agents listed", n)
	}
	e := w.spawn("five")
	if err := w.ctl("close", "--no-wait", e.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "closed --no-wait", func() bool { return len(w.reg.List()) == 0 })
	if code := w.code("close", a.ID); code != exitNotFound {
		t.Fatalf("close a closed agent: exit %d", code)
	}
}

func TestLastRows(t *testing.T) {
	for _, c := range []struct {
		text string
		n    int
		want string
	}{
		{"a\nb\n\n\n", 0, "a\nb"},
		{"a\nb\n\n\n", 1, "b"},
		{"a\nb\n  \n", 5, "a\nb"},
		{"x\n\ny\n\n", 2, "\ny"},
		{"\n\n", 3, ""},
	} {
		if got := lastRows(c.text, c.n); got != c.want {
			t.Errorf("lastRows(%q, %d) = %q, want %q", c.text, c.n, got, c.want)
		}
	}
}

func TestWait(t *testing.T) {
	w := newMoreWorld(t)
	a, b := w.spawn("one"), w.spawn("two")
	w.hook(a.ID, "UserPromptSubmit")
	w.hook(b.ID, "UserPromptSubmit")

	// Already there: at once.
	if out := w.out("wait", a.ID, "--until", "working"); !strings.Contains(out, a.ID+"\tworking") {
		t.Fatalf("wait working: %q", out)
	}
	// Timeout: 6.
	start := time.Now()
	if code := w.code("wait", a.ID, "--until", "needs-you", "--timeout", "200ms"); code != exitTimeout {
		t.Fatalf("timeout: exit %d", code)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout took too long")
	}
	// --any: the first that needs you.
	done := make(chan string)
	go func() {
		var err error
		out := capture(t, func() { err = w.ctl("wait", a.ID, b.ID, "--any", "--until", "needs-you", "--json", "--timeout", "10s") })
		if err != nil {
			out = err.Error()
		}
		done <- out
	}()
	time.Sleep(300 * time.Millisecond)
	w.approval(b.ID)
	var got []wire.Agent
	if out := <-done; json.Unmarshal([]byte(out), &got) != nil || len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("wait --any: %s", out)
	}
	// --all with --next: b is in approval now, which --next ignores; a
	// finishes, then b.
	errc := make(chan error)
	go func() { errc <- w.ctl("wait", a.ID, b.ID, "--next", "--until", "finished", "--timeout", "10s") }()
	time.Sleep(300 * time.Millisecond)
	w.hook(a.ID, "Stop")
	select {
	case err := <-errc:
		t.Fatalf("wait --all returned with b waiting: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := w.ctl("answer", b.ID, "deny"); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	// The agent goes away: 3.
	codes := make(chan int)
	go func() { codes <- w.code("wait", a.ID, "--until", "working", "--timeout", "10s") }()
	time.Sleep(300 * time.Millisecond)
	if err := w.ctl("close", a.ID); err != nil {
		t.Fatal(err)
	}
	if code := <-codes; code != exitNotFound {
		t.Fatalf("gone: exit %d", code)
	}
	// settled: what new --wait waits for (b is idle now).
	if out := w.out("wait", b.ID, "--until", "settled", "--timeout", "5s"); !strings.Contains(out, b.ID+"\t") {
		t.Fatalf("wait --until settled: %q", out)
	}
	if states, _ := waitStates("settled"); !slices.Equal(states, settledStates) {
		t.Fatalf("settled %v", states)
	}
	if code := w.code("wait", b.ID, "--until", "sleepy"); code != exitUsage {
		t.Fatalf("bad condition: exit %d", code)
	}
}

func TestEvents(t *testing.T) {
	w := newMoreWorld(t)
	a, b := w.spawn("one"), w.spawn("two")
	ctx, cancel := context.WithCancel(context.Background())
	var err error
	out := capture(t, func() {
		go func() {
			time.Sleep(300 * time.Millisecond)
			w.hook(a.ID, "UserPromptSubmit")
			w.hook(b.ID, "UserPromptSubmit")
			time.Sleep(300 * time.Millisecond)
			cancel()
		}()
		err = run(ctx, w.args([]string{"events", "--agent", a.ID, "--kinds", "agents"}))
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	states := []string{}
	for _, l := range lines {
		var n struct {
			Method string       `json:"method"`
			Params wire.Changed `json:"params"`
		}
		if err := json.Unmarshal([]byte(l), &n); err != nil || n.Method != wire.NoteChanged || n.Params.Agent.ID != a.ID {
			t.Fatalf("event %q: %v", l, err)
		}
		states = append(states, n.Params.Agent.State)
	}
	if states[len(states)-1] != wire.StateWorking {
		t.Fatalf("states %v", states)
	}
	if code := w.code("events", "--kinds", "agents,weather"); code != exitUsage {
		t.Fatalf("bad kind: exit %d", code)
	}
}

func TestAttachFile(t *testing.T) {
	w := newMoreWorld(t)
	a := w.spawn("files")
	dir := t.TempDir()
	img := filepath.Join(dir, "shot one.png")
	doc := filepath.Join(dir, "notes.txt")
	os.WriteFile(img, []byte("not really a png"), 0o600)
	os.WriteFile(doc, []byte("hello notes"), 0o600)

	// This Mac's agent: the paths themselves, escaped, an image per paste.
	out := w.out("attach-file", a.ID, img, doc)
	want := strings.ReplaceAll(img, " ", `\ `) + " " + doc
	if strings.TrimSpace(out) != want {
		t.Fatalf("attach-file: %q, want %q", out, want)
	}
	eventually(t, "pasted", func() bool { return strings.Contains(screenOf(w.reg, a.ID), "notes.txt") })

	// --upload: a copy stored by hesperd.
	var res struct {
		Files []attached `json:"files"`
	}
	if err := json.Unmarshal([]byte(w.out("attach-file", a.ID, doc, "--upload", "--no-paste", "--json")), &res); err != nil || len(res.Files) != 1 {
		t.Fatalf("--upload %v %+v", err, res)
	}
	f := res.Files[0]
	if data, err := os.ReadFile(f.AgentPath); !f.Uploaded || f.AgentPath == doc || err != nil || string(data) != "hello notes" {
		t.Fatalf("uploaded %+v: %q %v", f, data, err)
	}
	if code := w.code("attach-file", a.ID, filepath.Join(dir, "missing")); code != exitNotFound {
		t.Fatalf("missing file: exit %d", code)
	}
	if got := dropPastes([]string{"/a.png", "/b c.txt", "/d.JPG", "/e"}); !slices.Equal(got, []string{"/a.png", " /d.JPG", ` /b\ c.txt /e`}) {
		t.Fatalf("pastes %q", got)
	}
	if got := shellQuote("/x/it's\n"); got != `$'/x/it\'s\n'` {
		t.Fatalf("quote %q", got)
	}
}
