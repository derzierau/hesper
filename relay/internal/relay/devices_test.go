package relay

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// fakeAdmin stands in for the transport: list returns the devices, revoke
// records the id and revokes it in the hub as Server.Revoke does.
type fakeAdmin struct {
	mu      sync.Mutex
	devices []identity.Device
	revoked []string
}

func (a *fakeAdmin) install(h *Hub) {
	h.SetDeviceAdmin(func(_ context.Context, owner string) ([]identity.Device, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		var result []identity.Device
		for _, d := range a.devices {
			if d.Owner == owner {
				result = append(result, d)
			}
		}
		return result, nil
	}, func(_ context.Context, owner, id string) error {
		a.mu.Lock()
		a.revoked = append(a.revoked, id)
		a.mu.Unlock()
		h.Revoke(id)
		return nil
	})
}
func (a *fakeAdmin) calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.revoked...)
}
func ask(t *testing.T, h *Hub, c *Connection, m protocol.Message) {
	t.Helper()
	m.Version = protocol.Version
	if err := h.Handle(c, m); err != nil {
		t.Fatal(err)
	}
}

func TestDevicesListsOnlyTheOwnersLiveDevices(t *testing.T) {
	host := device("host", "a", identity.Host)
	gone := device("gone", "a", identity.Host)
	gone.Revoked = true
	phone := device("phone", "a", identity.Controller)
	user := device("user", "a", identity.Controller)
	other := device("other", "b", identity.Host)
	all := []identity.Device{host, gone, phone, user, other}
	h := New(all, time.Second)
	defer h.Close()
	admin := &fakeAdmin{devices: all}
	admin.install(h)
	attach(t, h, host)
	c, p := attach(t, h, user)
	hello := next(t, p, "hello")
	var info struct{ Features []string }
	if json.Unmarshal(hello.Result, &info) != nil || len(info.Features) != 1 || info.Features[0] != "devices" {
		t.Fatalf("hello without features: %s", hello.Result)
	}
	ask(t, h, c, protocol.Message{Type: "devices", ID: "q"})
	m := next(t, p, "devices")
	if m.ID != "q" {
		t.Fatalf("devices answer lost its id: %+v", m)
	}
	got := map[string]protocol.Device{}
	for _, d := range m.Devices {
		got[d.ID] = d
	}
	if len(got) != 3 || !got["host"].Online || got["host"].Role != "host" || got["phone"].Online || !got["user"].Online || got["phone"].Role != "controller" || got["phone"].Name != "phone" {
		t.Fatalf("wrong devices: %+v", m.Devices)
	}
}

func TestRevokeIsOwnerBoundAndNeverSelf(t *testing.T) {
	host := device("host", "a", identity.Host)
	sibling := device("sibling", "a", identity.Controller)
	user := device("user", "a", identity.Controller)
	other := device("other", "b", identity.Host)
	all := []identity.Device{host, sibling, user, other}
	h := New(all, time.Second)
	defer h.Close()
	admin := &fakeAdmin{devices: all}
	admin.install(h)
	_, hostPeer := attach(t, h, host)
	_, siblingPeer := attach(t, h, sibling)
	attach(t, h, other)
	c, p := attach(t, h, user)
	for _, tc := range []struct{ id, code string }{{"other", "not_found"}, {"unknown", "not_found"}, {"user", "forbidden"}, {"", "invalid_request"}} {
		ask(t, h, c, protocol.Message{Type: "revoke", ID: "r-" + tc.id, DeviceID: tc.id})
		if m := next(t, p, "result"); m.ID != "r-"+tc.id || m.Error == nil || m.Error.Code != tc.code {
			t.Fatalf("revoke %q: %+v", tc.id, m)
		}
	}
	if calls := admin.calls(); len(calls) != 0 {
		t.Fatalf("refused revoke reached the repository: %v", calls)
	}
	ask(t, h, c, protocol.Message{Type: "revoke", ID: "s", DeviceID: "sibling"})
	if m := next(t, p, "result"); m.ID != "s" || m.Error != nil || string(m.Result) != "{}" {
		t.Fatalf("sibling revoke: %+v", m)
	}
	siblingPeer.mu.Lock()
	closed := siblingPeer.closed
	siblingPeer.mu.Unlock()
	if !closed {
		t.Fatal("revoked controller stayed connected")
	}
	ask(t, h, c, protocol.Message{Type: "revoke", ID: "h", DeviceID: "host"})
	if m := next(t, p, "machine.updated"); m.MachineID != "host" || !m.Removed {
		t.Fatalf("host removal not broadcast: %+v", m)
	}
	if m := next(t, p, "result"); m.ID != "h" || m.Error != nil {
		t.Fatalf("host revoke: %+v", m)
	}
	hostPeer.mu.Lock()
	closed = hostPeer.closed
	hostPeer.mu.Unlock()
	if !closed {
		t.Fatal("revoked host stayed connected")
	}
	if calls := admin.calls(); len(calls) != 2 || calls[0] != "sibling" || calls[1] != "host" {
		t.Fatalf("revoke calls: %v", calls)
	}
	ask(t, h, c, protocol.Message{Type: "revoke", ID: "again", DeviceID: "host"})
	if m := next(t, p, "result"); m.Error == nil || m.Error.Code != "not_found" {
		t.Fatalf("revoked twice: %+v", m)
	}
}

func TestDevicesNeedTheAdminAndAController(t *testing.T) {
	host := device("host", "a", identity.Host)
	user := device("user", "a", identity.Controller)
	h := New([]identity.Device{host, user}, time.Second)
	defer h.Close()
	hc, _ := attach(t, h, host)
	c, p := attach(t, h, user)
	if m := next(t, p, "hello"); json.Valid(m.Result) && string(m.Result) != "" {
		var info struct{ Features []string }
		json.Unmarshal(m.Result, &info)
		if len(info.Features) != 0 {
			t.Fatalf("features advertised without an admin: %s", m.Result)
		}
	}
	if err := h.Handle(c, protocol.Message{Version: protocol.Version, Type: "devices", ID: "x"}); err == nil {
		t.Fatal("devices answered without an admin")
	}
	(&fakeAdmin{}).install(h)
	if err := h.Handle(hc, protocol.Message{Version: protocol.Version, Type: "devices", ID: "x"}); err == nil {
		t.Fatal("a host listed devices")
	}
}
