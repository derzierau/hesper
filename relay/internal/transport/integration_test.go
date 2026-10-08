package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/internal/transport"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
)

// counter answers requests with its name and counts the mutating ones.
type counter struct {
	name   string
	writes atomic.Int32
}

func (p *counter) handle(m protocol.Message) (any, error) {
	if m.Method != "agents.list" {
		p.writes.Add(1)
	}
	return map[string]string{"name": p.name}, nil
}

func TestRealWebSocketMultipleHostsPairingAndRevocation(t *testing.T) {
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
	admin := httptest.NewServer(app.AdminHandler())
	defer admin.Close()
	pair := func(owner string, role identity.Role) protocol.Credentials {
		t.Helper()
		invite, err := repo.Invite(ctx, identity.Invitation{Owner: owner, Role: role, Expires: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		credentials, err := client.Pair(ctx, server.URL, invite, string(role))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Pair(ctx, server.URL, invite, "replay"); err == nil {
			t.Fatal("invitation was replayed")
		}
		return credentials
	}
	first, second := pair("personal", identity.Host), pair("personal", identity.Host)
	controllerCredentials := pair("personal", identity.Controller)
	outsiderCredentials := pair("other", identity.Controller)
	startHost := func(c protocol.Credentials, p *counter) {
		t.Helper()
		startFakeHost(t, ctx, c, session.Snapshot{RuntimeID: p.name, Terminals: []session.Terminal{}}, p.handle)
	}
	a, b := &counter{name: "first"}, &counter{name: "second"}
	startHost(first, a)
	startHost(second, b)
	c, err := client.NewController(ctx, controllerCredentials)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for {
		select {
		case machines := <-c.Updates():
			if len(machines) == 2 && len(machines[0].Snapshot) > 0 && len(machines[1].Snapshot) > 0 {
				goto ready
			}
		case <-ctx.Done():
			t.Fatal("hosts never published their snapshots")
		}
	}
ready:
	for _, entry := range []struct {
		credentials protocol.Credentials
		text        string
	}{{first, "first"}, {second, "second"}} {
		raw, err := c.Request(ctx, entry.credentials.DeviceID, "agents.list", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Name string }
		if json.Unmarshal(raw, &got) != nil || got.Name != entry.text {
			t.Fatalf("wrong host: %s", raw)
		}
	}
	if _, err := c.Request(ctx, second.DeviceID, "agents.input", map[string]any{"id": "x", "text": "hello"}); err != nil {
		t.Fatal(err)
	}
	if a.writes.Load() != 0 || b.writes.Load() != 1 {
		t.Fatal("input delivered to the wrong machine or duplicated")
	}
	for _, method := range []string{"agents.answer", "agents.stop"} {
		if _, err := c.Request(ctx, second.DeviceID, method, map[string]any{"id": "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if a.writes.Load() != 0 || b.writes.Load() != 3 {
		t.Fatal("typed mutation routing failed")
	}
	outsider, err := client.NewController(ctx, outsiderCredentials)
	if err != nil {
		t.Fatal(err)
	}
	defer outsider.Close()
	machines, err := outsider.Machines(ctx)
	if err != nil || len(machines) != 0 {
		t.Fatal("inventory leaked across owners")
	}
	if _, err := outsider.Request(ctx, first.DeviceID, "agents.list", map[string]any{}); err == nil {
		t.Fatal("cross-owner request accepted")
	}
	res, err := http.Get(server.URL + "/v1/devices")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("admin API exposed on public listener")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, admin.URL+"/v1/devices/"+first.DeviceID, nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("revoke status %d", res.StatusCode)
	}
	if _, err := c.Request(ctx, first.DeviceID, "agents.list", map[string]any{}); err == nil {
		t.Fatal("revoked host still routable")
	}
	if link, err := client.Dial(ctx, first); err == nil {
		link.Close()
		t.Fatal("revoked credential reconnected")
	}
}
