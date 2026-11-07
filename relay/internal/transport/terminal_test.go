package transport_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
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

// terminalTTL is the access-token lifetime of both stream ends.
const terminalTTL = 200 * time.Millisecond

// terminalWorld is a production-mode relay (allowlist policy, short-lived
// access tokens) with one paired host and controller terminal stream.
type terminalWorld struct {
	repo                  *storage.Bolt
	policy                *auth.Allowlist
	host, controller      identity.Device
	hostTerm, controlTerm *client.Terminal
}

func newTerminalWorld(t *testing.T, ctx context.Context) *terminalWorld {
	t.Helper()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	hub := relay.New(nil, time.Second)
	t.Cleanup(hub.Close)
	policy := auth.NewAllowlist([]string{"12345678"})
	hub.SetPolicy(policy.Allowed)
	app := transport.New(repo, hub)
	app.Policy = policy
	server := httptest.NewServer(app.PublicHandler())
	t.Cleanup(server.Close)
	t.Cleanup(app.Terminals.Close)
	issue := func(role identity.Role) (identity.Device, protocol.Credentials) {
		t.Helper()
		v := identity.Secret()
		e := auth.Enrollment{ID: identity.Secret(), Name: string(role), Role: role, Challenge: auth.Challenge(v), Expires: time.Now().Add(time.Minute)}
		if err := repo.CreateEnrollment(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := repo.Approve(ctx, e.ID, auth.Principal{ID: "github:12345678", Login: "octocat"}); err != nil {
			t.Fatal(err)
		}
		d, c, err := repo.Claim(ctx, e.ID, v, terminalTTL, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		c.Relay = server.URL
		return d, c
	}
	w := &terminalWorld{repo: repo, policy: policy}
	var hostCreds, controlCreds protocol.Credentials
	w.host, hostCreds = issue(identity.Host)
	w.controller, controlCreds = issue(identity.Controller)
	ticket := identity.Secret()
	if w.hostTerm, err = client.DialTerminal(ctx, hostCreds, ticket, w.controller.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.hostTerm.Close() })
	if w.controlTerm, err = client.DialTerminal(ctx, controlCreds, ticket, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.controlTerm.Close() })
	return w
}

// forwards checks that bytes still cross the stream in both directions.
func (w *terminalWorld) forwards(t *testing.T, ctx context.Context) {
	t.Helper()
	for _, dir := range []struct{ from, to *client.Terminal }{{w.controlTerm, w.hostTerm}, {w.hostTerm, w.controlTerm}} {
		want := []byte("still here")
		if err := dir.from.Write(ctx, want); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, got, err := dir.to.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("forwarded %q, want %q", got, want)
		}
	}
}

// pastExpiry waits until both access tokens have expired and checks the
// stream still forwards, so a later close is caused by the change under test.
func (w *terminalWorld) pastExpiry(t *testing.T, ctx context.Context) {
	t.Helper()
	time.Sleep(terminalTTL + 100*time.Millisecond)
	w.forwards(t, ctx)
}

// closesWithin reads the host end until the stream ends and fails if that
// takes longer than limit.
func (w *terminalWorld) closesWithin(t *testing.T, ctx context.Context, limit time.Duration) {
	t.Helper()
	start := time.Now()
	readCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for {
		if _, _, err := w.hostTerm.Read(readCtx); err != nil {
			break
		}
	}
	if readCtx.Err() != nil {
		t.Fatalf("stream still open %v after the change", time.Since(start).Round(time.Millisecond))
	}
}

func TestTerminalOutlivesTokenExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w := newTerminalWorld(t, ctx)
	// Past both tokens' expiry and at least one per-second repository check.
	time.Sleep(terminalTTL + 1500*time.Millisecond)
	w.forwards(t, ctx)
}

func TestTerminalClosesOnRevocation(t *testing.T) {
	for _, side := range []string{"controller", "host"} {
		t.Run(side, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			w := newTerminalWorld(t, ctx)
			w.pastExpiry(t, ctx)
			id := w.controller.ID
			if side == "host" {
				id = w.host.ID
			}
			// Revoke in the repository only: the stream's own check must
			// notice without the broker's Revoke callback.
			if err := w.repo.Revoke(ctx, id); err != nil {
				t.Fatal(err)
			}
			w.closesWithin(t, ctx, 1500*time.Millisecond)
		})
	}
}

func TestTerminalClosesOnPolicyChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w := newTerminalWorld(t, ctx)
	w.pastExpiry(t, ctx)
	// Without CloseDenied: the per-second policy check must close it.
	w.policy.Replace(nil)
	w.closesWithin(t, ctx, 1500*time.Millisecond)
}
