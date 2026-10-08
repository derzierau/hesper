package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/internal/transport"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

func TestProductionGatesAndActiveExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	hub := relay.New(nil, time.Second)
	defer hub.Close()
	policy := auth.NewAllowlist([]string{"12345678"})
	hub.SetPolicy(policy.Allowed)
	app := transport.New(repo, hub)
	app.Policy = policy
	app.Auth = &auth.HTTP{Repository: repo, Policy: policy, Register: hub.Register, Revoke: hub.Revoke}
	server := httptest.NewServer(app.PublicHandler())
	defer server.Close()
	app.Auth.PublicURL = server.URL
	res, err := http.Post(server.URL+"/v1/pair", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("production pairing exposed")
	}
	admin := httptest.NewRecorder()
	app.AdminHandler().ServeHTTP(admin, httptest.NewRequest("POST", "/v1/invitations", strings.NewReader(`{}`)))
	if admin.Code != 403 {
		t.Fatal("production invitations exposed")
	}
	inv, _ := repo.Invite(ctx, identity.Invitation{Owner: "github:12345678", Role: identity.Controller, Expires: time.Now().Add(time.Minute)})
	d, token, _ := repo.Redeem(ctx, inv, "legacy")
	hub.Register(d)
	if l, err := client.Dial(ctx, protocol.Credentials{Relay: server.URL, Token: token}); err == nil {
		l.Close()
		t.Fatal("legacy credential accepted")
	}
	issue := func(ttl time.Duration) protocol.Credentials {
		t.Helper()
		v := identity.Secret()
		e := auth.Enrollment{ID: identity.Secret(), Name: "test", Role: identity.Controller, Challenge: auth.Challenge(v), Expires: time.Now().Add(time.Minute)}
		if err := repo.CreateEnrollment(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := repo.Approve(ctx, e.ID, auth.Principal{ID: "github:12345678", Login: "derzierau"}); err != nil {
			t.Fatal(err)
		}
		d, c, err := repo.Claim(ctx, e.ID, v, ttl, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		hub.Register(d)
		c.Relay = server.URL
		return c
	}
	expired := issue(150 * time.Millisecond)
	link, err := client.Dial(ctx, expired)
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := link.Read(ctx); err != nil {
			break
		}
	}
	link.Close()
	if ctx.Err() != nil {
		t.Fatal("active socket did not expire")
	}
	if l, err := client.Dial(ctx, expired); err == nil {
		l.Close()
		t.Fatal("expired credential accepted")
	}
	valid := issue(time.Minute)
	link, err = client.Dial(ctx, valid)
	if err != nil {
		t.Fatal(err)
	}
	policy.Replace(nil)
	hub.SetPolicy(policy.Allowed)
	for {
		if _, err := link.Read(ctx); err != nil {
			break
		}
	}
	link.Close()
	if ctx.Err() != nil {
		t.Fatal("allowlist removal did not disconnect")
	}
	if l, err := client.Dial(ctx, valid); err == nil {
		l.Close()
		t.Fatal("removed user accepted")
	}
}
