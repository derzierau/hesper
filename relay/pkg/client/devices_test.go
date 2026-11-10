package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// fakeHub is a relay connection endpoint: it sends hello with the given
// result and an empty inventory, then answers devices and revoke.
type fakeHub struct {
	mu      sync.Mutex
	revoked []string
}

func (f *fakeHub) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}
func (f *fakeHub) start(t *testing.T, hello string) protocol.Credentials {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		send := func(m protocol.Message) { m.Version = protocol.Version; wsjson.Write(ctx, conn, m) }
		send(protocol.Message{Type: "hello", Result: []byte(hello)})
		send(protocol.Message{Type: "machines"})
		for {
			var m protocol.Message
			if wsjson.Read(ctx, conn, &m) != nil {
				return
			}
			switch m.Type {
			case "devices":
				send(protocol.Message{Type: "devices", ID: m.ID, Devices: []protocol.Device{{ID: "HOST_ID", Name: "Studio", Role: "host", Online: true}, {ID: "PHONE_ID", Name: "Phone", Role: "controller", Push: true}}})
			case "revoke":
				if m.DeviceID == "SELF_ID" {
					send(protocol.Message{Type: "result", ID: m.ID, Error: protocol.Err("forbidden", "This is the device you are using")})
					continue
				}
				f.mu.Lock()
				f.revoked = append(f.revoked, m.DeviceID)
				f.mu.Unlock()
				send(protocol.Message{Type: "result", ID: m.ID, Result: []byte("{}")})
			}
		}
	}))
	t.Cleanup(server.Close)
	return protocol.Credentials{Relay: server.URL, DeviceID: "SELF_ID", Role: "controller", Token: "TOKEN"}
}

func TestDevicesAndRevokeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hub := &fakeHub{}
	c, err := client.NewController(ctx, hub.start(t, `{"deviceId":"SELF_ID","role":"controller","features":["devices"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	devices, err := c.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0].ID != "HOST_ID" || !devices[0].Online || !devices[1].Push || devices[1].Role != "controller" {
		t.Fatalf("devices: %+v", devices)
	}
	if err := c.Revoke(ctx, "PHONE_ID"); err != nil {
		t.Fatal(err)
	}
	if calls := hub.calls(); len(calls) != 1 || calls[0] != "PHONE_ID" {
		t.Fatalf("revoked: %v", calls)
	}
	var perr *protocol.Error
	if err := c.Revoke(ctx, "SELF_ID"); !errors.As(err, &perr) || perr.Code != "forbidden" {
		t.Fatalf("self revoke: %v", err)
	}
}

func TestFeaturesFromHello(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, tc := range []struct {
		hello string
		want  bool
	}{
		{`{"deviceId":"SELF_ID","role":"controller","features":["devices"]}`, true},
		{`{"deviceId":"SELF_ID","role":"controller"}`, false},
	} {
		c, err := client.NewController(ctx, (&fakeHub{}).start(t, tc.hello))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.WaitHello(ctx); err != nil {
			t.Fatal(err)
		}
		if got := c.Supports("devices"); got != tc.want {
			t.Fatalf("%s: Supports(devices) = %v", tc.hello, got)
		}
		if got := c.Features(); tc.want && (len(got) != 1 || got[0] != "devices") {
			t.Fatalf("features: %v", got)
		}
		c.Close()
	}
}
