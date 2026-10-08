package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// new --project takes folders relative to the current directory (this
// Mac's); another Mac's are sent as given.
func TestNewRelativeProject(t *testing.T) {
	w := newMoreWorld(t)
	t.Chdir(filepath.Dir(w.project))
	var a wire.Agent
	if err := json.Unmarshal([]byte(w.out("new", "--json", "--project", "./"+filepath.Base(w.project), "hi")), &a); err != nil {
		t.Fatal(err)
	}
	if a.Project != w.project {
		t.Fatalf("project %q, want %q", a.Project, w.project)
	}
	t.Chdir(w.project)
	if err := json.Unmarshal([]byte(w.out("new", "--json", "--project", ".", "--machine", "L", "hi")), &a); err != nil {
		t.Fatal(err)
	}
	if a.Project != w.project {
		t.Fatalf("project with --machine of this Mac %q", a.Project)
	}
	// Another machine: as given (this daemon has no remote: unavailable,
	// and the relative folder is not made absolute here).
	if code := w.code("new", "--project", ".", "--machine", "mini", "hi"); code != exitUnavailable {
		t.Fatalf("another machine: exit %d", code)
	}
}

// result with nothing yet: exit 0 both ways; text prints nothing on
// stdout, --json has message and summary null.
func TestResultNothingYet(t *testing.T) {
	w := newMoreWorld(t)
	a := w.spawn("hi")
	if out := w.out("result", a.ID); out != "" {
		t.Fatalf("result text: %q", out)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(w.out("result", a.ID, "--json")), &res); err != nil {
		t.Fatal(err)
	}
	if m, ok := res["message"]; !ok || m != nil || res["summary"] != nil || res["id"] != a.ID {
		t.Fatalf("result --json %v", res)
	}
	w.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "Stop", Payload: json.RawMessage(`{"last_assistant_message":"Done."}`)})
	if err := json.Unmarshal([]byte(w.out("result", a.ID, "--json")), &res); err != nil || res["message"] != "Done." {
		t.Fatalf("result --json %v %v", err, res)
	}
}

// Inside an agent, hesperctl names its agent only to that agent's own
// hesperd (HESPER_SOCKET): another daemon sees a person.
func TestCallerOnlyToOwnDaemon(t *testing.T) {
	w := newMoreWorld(t)
	t.Setenv("HESPER_AGENT_ID", "X/unknown")
	t.Setenv("HESPER_MACHINE", "")
	t.Setenv("HESPER_SOCKET", filepath.Join(t.TempDir(), "other.sock"))
	var a wire.Agent
	if err := json.Unmarshal([]byte(w.out("new", "--json", "--project", w.project, "hi")), &a); err != nil {
		t.Fatal(err)
	}
	if a.Parent != "" {
		t.Fatalf("started on another daemon as a child of %q", a.Parent)
	}
	// To its own daemon the (unknown) agent is named, and refused.
	t.Setenv("HESPER_SOCKET", w.sock)
	if code := w.code("new", "--project", w.project, "hi"); code != exitForbidden {
		t.Fatalf("own daemon, unknown agent: exit %d", code)
	}
	if got := callerFor(filepath.Dir(w.sock) + "/./" + filepath.Base(w.sock)); got != "X/unknown" {
		t.Fatalf("same socket, other spelling: %q", got)
	}
	t.Setenv("HESPER_AGENT_ID", "")
	if got := callerFor(w.sock); got != "" {
		t.Fatalf("outside an agent: %q", got)
	}
}

// A shell started with a TASK runs it. new --track --wait returns once
// the command is done (idle); without --track new --wait waits for the
// shell to exit (TASK; exit).
func TestNewShellTaskWait(t *testing.T) {
	reg, sock, project := daemonWith(t, map[string]wire.Profile{"sh": {Kind: wire.KindShell, Argv: []string{"/bin/sh", "-i"}}})
	w := &moreWorld{t, reg, sock, project}
	var res struct {
		Agent wire.Agent `json:"agent"`
	}
	if err := json.Unmarshal([]byte(w.out("new", "--json", "--profile", "sh", "--track", "--project", project, "--wait", "--timeout", "20s", "sleep 1; echo polish-$((2+3))")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Agent.State != wire.StateIdle || !res.Agent.Track {
		t.Fatalf("settled as %+v", res.Agent)
	}
	if out := w.out("screen", res.Agent.ID); !strings.Contains(out, "polish-5") {
		t.Fatalf("new --track --wait returned before the command was done:\n%s", out)
	}
	// Untracked: idle all along, so --wait waits for the exit.
	res.Agent = wire.Agent{}
	if err := json.Unmarshal([]byte(w.out("new", "--json", "--profile", "sh", "--project", project, "--wait", "--timeout", "20s", "sleep 1; echo plain-$((2+3)); exit")), &res); err != nil {
		t.Fatal(err)
	}
	if res.Agent.State != wire.StateExited || res.Agent.Track {
		t.Fatalf("untracked shell settled as %+v", res.Agent)
	}
	if out := w.out("screen", res.Agent.ID); !strings.Contains(out, "plain-5") {
		t.Fatalf("untracked new --wait:\n%s", out)
	}
	// Without exit it waits on: the timeout ends it.
	if code := w.code("new", "--profile", "sh", "--project", project, "--wait", "--timeout", "1500ms", "echo hi"); code != exitTimeout {
		t.Fatalf("untracked shell that stays: exit %d", code)
	}
}
