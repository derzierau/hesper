package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// sizer changes what the published snapshot shows: the size of one shell
// agent's PTY.
type sizer struct {
	t   *testing.T
	reg *agents.Registry
	id  string
}

func (p sizer) set(cols int) {
	term, err := p.reg.Term(p.id)
	if err != nil {
		p.t.Fatal(err)
	}
	term.Resize(cols, 30)
}

// A change event is published at once (no fixed debounce), a burst of
// events is one or two publications, and nothing is sent when nothing
// changed.
func TestChangeEventsPublishAtOnceAndCoalesce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conns := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		conns <- c
		<-r.Context().Done()
	}))
	defer server.Close()
	reg, project := testRegistry(t)
	shell, err := reg.Spawn(wire.SpawnParams{Project: project, Task: "work"})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Stop(shell.ID)
	provider := sizer{t, reg, shell.ID}
	provider.set(80)
	events := make(chan struct{}, 1)
	runner := &Runner{Service: &Service{Agents: reg}, PollInterval: time.Hour, Events: events,
		Credentials: protocol.Credentials{Relay: server.URL, DeviceID: "mini-1", Role: "host", Token: "t"}}
	link, err := client.Dial(ctx, runner.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); runner.Serve(ctx, link) }()
	defer func() { cancel(); link.Close(); <-done }()
	relay := <-conns
	defer relay.CloseNow()
	snapshots := make(chan int, 16)
	go func() {
		for {
			var m protocol.Message
			if wsjson.Read(ctx, relay, &m) != nil {
				close(snapshots)
				return
			}
			var s session.Snapshot
			if m.Type == "snapshot" && json.Unmarshal(m.Result, &s) == nil && len(s.Terminals) == 1 {
				snapshots <- s.Terminals[0].Columns
			}
		}
	}()
	next := func(wait time.Duration) (int, bool) {
		select {
		case s, ok := <-snapshots:
			return s, ok
		case <-time.After(wait):
			return 0, false
		}
	}
	if s, _ := next(5 * time.Second); s != 80 {
		t.Fatalf("initial snapshot %d", s)
	}
	signal := func() {
		select {
		case events <- struct{}{}:
		default:
		}
	}
	for i := 0; i < 3; i++ {
		time.Sleep(PublishGap)
		provider.set([]int{81, 82, 83}[i])
		started := time.Now()
		signal()
		s, _ := next(time.Second)
		if s != []int{81, 82, 83}[i] {
			t.Fatalf("change %d published as %d", i, s)
		}
		if took := time.Since(started); took > 100*time.Millisecond {
			t.Fatalf("change published after %v", took)
		}
	}
	// A burst: a hook per agent, several agents at once. Do not sleep
	// between events: scheduler delays can stretch a nominal 1 ms sleep
	// beyond PublishGap on loaded runners, turning this into multiple bursts.
	time.Sleep(PublishGap)
	for i := 0; i < 20; i++ {
		provider.set([]int{90, 91}[i%2])
		signal()
	}
	provider.set(99)
	signal()
	count := 0
	for {
		s, ok := next(300 * time.Millisecond)
		if !ok {
			break
		}
		count++
		if s == 99 {
			break
		}
	}
	if count == 0 || count > 2 {
		t.Fatalf("a burst of 21 events made %d publications", count)
	}
	// Nothing changed: nothing is sent.
	signal()
	if s, ok := next(200 * time.Millisecond); ok {
		t.Fatalf("an unchanged snapshot was published again: %d", s)
	}
}
