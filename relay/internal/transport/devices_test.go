package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/internal/transport"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// TestRevokeOverTheHubEndsTheDevice revokes a host from its owner's
// controller: its credential stops working, its terminal stream closes and
// another owner's device is out of reach.
func TestRevokeOverTheHubEndsTheDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	hub := relay.New(nil, time.Second)
	defer hub.Close()
	app := transport.New(repo, hub)
	server := httptest.NewServer(app.PublicHandler())
	defer server.Close()
	pair := func(owner string, role identity.Role, name string) protocol.Credentials {
		t.Helper()
		invite, err := repo.Invite(ctx, identity.Invitation{Owner: owner, Role: role, Expires: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		c, err := client.Pair(ctx, server.URL, invite, name)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	host := pair("personal", identity.Host, "studio")
	user := pair("personal", identity.Controller, "laptop")
	outsider := pair("other", identity.Host, "elsewhere")

	link, err := client.Dial(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()
	read := func(kind string) protocol.Message {
		t.Helper()
		for {
			m, err := link.Read(ctx)
			if err != nil {
				t.Fatalf("waiting for %s: %v", kind, err)
			}
			if m.Type == kind {
				return m
			}
		}
	}
	if hello := read("hello"); string(hello.Result) == "" || !strings.Contains(string(hello.Result), `"features":["devices"]`) {
		t.Fatalf("hello: %s", hello.Result)
	}
	link.Send(ctx, protocol.Message{Type: "devices", ID: "d"})
	m := read("devices")
	if len(m.Devices) != 2 {
		t.Fatalf("devices: %+v", m.Devices)
	}

	// The host holds a terminal stream open, waiting for its controller.
	stream := websocketStream(t, ctx, server.URL, host.Token, user.DeviceID)
	defer stream.CloseNow()

	link.Send(ctx, protocol.Message{Type: "revoke", ID: "x", DeviceID: outsider.DeviceID})
	if m := read("result"); m.Error == nil || m.Error.Code != "not_found" {
		t.Fatalf("cross-owner revoke: %+v", m)
	}
	link.Send(ctx, protocol.Message{Type: "revoke", ID: "r", DeviceID: host.DeviceID})
	if m := read("result"); m.ID != "r" || m.Error != nil {
		t.Fatalf("revoke: %+v", m)
	}
	if _, err := repo.Authenticate(ctx, host.Token); err == nil {
		t.Fatal("revoked token still authenticates")
	}
	if _, err := repo.Authenticate(ctx, outsider.Token); err != nil {
		t.Fatalf("outsider touched: %v", err)
	}
	if l, err := client.Dial(ctx, host); err == nil {
		l.Close()
		t.Fatal("revoked host reconnected")
	}
	readCtx, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	if _, _, err := stream.Read(readCtx); err == nil || readCtx.Err() != nil {
		t.Fatalf("terminal stream outlived the revoke: %v", err)
	}
}

// websocketStream opens the host side of a terminal stream.
func websocketStream(t *testing.T, ctx context.Context, url, token, controller string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http")+"/v1/terminal", &websocket.DialOptions{Subprotocols: []string{protocol.TerminalProtocol}, HTTPHeader: http.Header{
		"Authorization":       {"Bearer " + token},
		"X-Ghosty-Stream":     {identity.Secret()},
		"X-Ghosty-Controller": {controller},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatalf("terminal registration: %v", err)
	}
	return conn
}
