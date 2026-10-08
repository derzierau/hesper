package host

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestExpiredAndMalformedRequestsNeverRun(t *testing.T) {
	reg, project := testRegistry(t)
	s := &Service{Agents: reg}
	exec := func(method, params string, deadline time.Duration) error {
		_, err := s.Execute(context.Background(), protocol.Message{Method: method, Params: json.RawMessage(params), Deadline: time.Now().Add(deadline).UnixMilli()})
		return err
	}
	spawn := `{"project":"` + project + `","kind":"shell"}`
	if err := exec("agents.spawn", spawn, -time.Second); code(err) != "expired" {
		t.Fatalf("expired: %v", err)
	}
	if err := exec("agents.spawn", `{"project":"`+project+`","shell":"malicious"}`, time.Second); code(err) != "invalid_request" {
		t.Fatalf("unknown field: %v", err)
	}
	if err := exec("agents.nope", `{}`, time.Second); code(err) != "unsupported" {
		t.Fatalf("unknown method: %v", err)
	}
	// A shell needs --allow-shell (and the shell right, Touch ID).
	if err := exec("agents.spawn", spawn, time.Second); code(err) != "forbidden" {
		t.Fatalf("shell without --allow-shell: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Fatalf("something ran: %+v", reg.List())
	}
	// Ids of another machine are not this one's.
	if err := exec("agents.stop", `{"id":"M/abc123"}`, time.Second); code(err) != wire.CodeNotFound {
		t.Fatalf("other machine's id: %v", err)
	}
}

// The relay sees no names, tasks or attention texts: only ids, kinds,
// states and attention kinds.
func TestSnapshotCarriesNoTaskText(t *testing.T) {
	reg, project := testRegistry(t)
	s := &Service{Agents: reg}
	a, err := reg.Spawn(wire.SpawnParams{Project: project, Task: "SECRET-TASK please approve", Name: "SECRET-NAME"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Stop(a.ID)
	state, _ := s.Snapshot(context.Background())
	data, _ := json.Marshal(state)
	if len(state.Terminals) != 1 || state.Terminals[0].Target.TerminalID != localID(a.ID) || state.Short != "mini" || !state.Capabilities.Agents {
		t.Fatalf("snapshot %s", data)
	}
	for _, secret := range []string{"SECRET", project} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("snapshot shows %q: %s", secret, data)
		}
	}
}
