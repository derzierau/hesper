package relay

import (
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

type testPeer struct {
	messages chan protocol.Message
	mu       sync.Mutex
	closed   bool
}

func newPeer() *testPeer { return &testPeer{messages: make(chan protocol.Message, 64)} }
func (p *testPeer) Send(m protocol.Message) bool {
	select {
	case p.messages <- m:
		return true
	default:
		return false
	}
}
func (p *testPeer) Close() { p.mu.Lock(); p.closed = true; p.mu.Unlock() }
func next(t *testing.T, p *testPeer, kind string) protocol.Message {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case m := <-p.messages:
			if m.Type == kind {
				return m
			}
		case <-timer.C:
			t.Fatalf("waiting for %s", kind)
			return protocol.Message{}
		}
	}
}
func device(id, owner string, role identity.Role) identity.Device {
	return identity.Device{ID: id, Owner: owner, Role: role, Name: id}
}
func attach(t *testing.T, h *Hub, d identity.Device) (*Connection, *testPeer) {
	t.Helper()
	p := newPeer()
	c, err := h.Connect(d, p)
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}
func requestMessage(id, machine string) protocol.Message {
	return protocol.Message{Version: protocol.Version, Type: "request", ID: id, MachineID: machine, Method: "capture", Params: protocol.JSON(map[string]string{})}
}

func TestOwnerIsolationAndResponseProvenance(t *testing.T) {
	a := device("host-a", "a", identity.Host)
	b := device("host-b", "b", identity.Host)
	c := device("client", "a", identity.Controller)
	h := New([]identity.Device{a, b, c}, time.Second)
	defer h.Close()
	ha, pa := attach(t, h, a)
	hb, _ := attach(t, h, b)
	controller, pc := attach(t, h, c)
	if err := h.Handle(controller, requestMessage("denied", b.ID)); err != nil {
		t.Fatal(err)
	}
	if m := next(t, pc, "result"); m.Error == nil || m.Error.Code != "unavailable" {
		t.Fatalf("cross-owner routing: %+v", m)
	}
	if err := h.Handle(controller, requestMessage("ok", a.ID)); err != nil {
		t.Fatal(err)
	}
	routed := next(t, pa, "request")
	if routed.ID == "ok" || routed.Deadline == 0 {
		t.Fatal("relay must assign a fresh correlation ID and deadline")
	}
	if err := h.Handle(hb, protocol.Message{Version: protocol.Version, Type: "result", ID: routed.ID, Result: protocol.JSON("forged")}); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(ha, protocol.Message{Version: protocol.Version, Type: "result", ID: routed.ID, Result: protocol.JSON("correct")}); err != nil {
		t.Fatal(err)
	}
	m := next(t, pc, "result")
	if string(m.Result) != `"correct"` || m.ID != "ok" {
		t.Fatalf("wrong response: %+v", m)
	}
}
func TestReplacementAndRevocationCancelPending(t *testing.T) {
	d := device("host", "a", identity.Host)
	user := device("client", "a", identity.Controller)
	h := New([]identity.Device{d, user}, time.Second)
	defer h.Close()
	old, _ := attach(t, h, d)
	controller, pc := attach(t, h, user)
	h.Handle(controller, requestMessage("pending", d.ID))
	current, _ := attach(t, h, d)
	if m := next(t, pc, "result"); m.Error == nil || m.Error.Code != "connection_lost" {
		t.Fatalf("unexpected response %+v", m)
	}
	h.Disconnect(old)
	if h.connections[d.ID] != current {
		t.Fatal("old disconnect removed new connection")
	}
	h.Revoke(d.ID)
	if _, err := h.Connect(d, newPeer()); err == nil {
		t.Fatal("revoked device reconnected")
	}
	if err := h.Handle(old, protocol.Message{Version: protocol.Version, Type: "snapshot", Result: protocol.JSON("stale")}); err == nil {
		t.Fatal("stale connection published")
	}
}
func TestTimeoutAndDuplicateIDs(t *testing.T) {
	d := device("host", "a", identity.Host)
	user := device("client", "a", identity.Controller)
	h := New([]identity.Device{d, user}, 20*time.Millisecond)
	defer h.Close()
	attach(t, h, d)
	controller, pc := attach(t, h, user)
	m := requestMessage("one", d.ID)
	if err := h.Handle(controller, m); err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(controller, m); err == nil {
		t.Fatal("duplicate request accepted")
	}
	if got := next(t, pc, "result"); got.Error == nil || got.Error.Code != "timeout" {
		t.Fatalf("unexpected response %+v", got)
	}
}
