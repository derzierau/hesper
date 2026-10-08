package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// lostResponse delivers the request to the relay and then fails like the
// laptop did: the relay rotated, but the answer never arrived.
type lostResponse struct {
	mu   sync.Mutex
	lost protocol.Credentials
}

func (l *lostResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		l.mu.Lock()
		json.NewDecoder(res.Body).Decode(&l.lost)
		l.mu.Unlock()
		res.Body.Close()
	}
	return nil, &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.EADDRNOTAVAIL)}
}

type relay struct {
	repo    *storage.Bolt
	url     string
	mu      sync.Mutex
	revoked []string
}

func (r *relay) revokedDevices() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.revoked...)
}

// newRelay runs the relay's real refresh endpoint and storage, and enrolls a
// host whose access token is about to expire.
func newRelay(t *testing.T) (*relay, protocol.Credentials) {
	t.Helper()
	ctx := context.Background()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	r := &relay{repo: repo}
	api := &auth.HTTP{Repository: repo, Policy: auth.NewAllowlist([]string{"12345678"}), PublicURL: "http://relay.test",
		Register: func(identity.Device) {},
		Revoke:   func(id string) { r.mu.Lock(); r.revoked = append(r.revoked, id); r.mu.Unlock() }}
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	r.url = server.URL
	verifier := identity.Secret()
	e := auth.Enrollment{ID: identity.Secret(), Name: "laptop", Role: identity.Host, Challenge: auth.Challenge(verifier), Expires: time.Now().Add(time.Minute)}
	if err := repo.CreateEnrollment(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := repo.Approve(ctx, e.ID, auth.Principal{ID: "github:12345678", Login: "x"}); err != nil {
		t.Fatal(err)
	}
	_, c, err := repo.Claim(ctx, e.ID, verifier, 30*time.Second, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.Relay = server.URL
	return r, c
}

func TestLostResponseAfterRotationRecoversWithinGrace(t *testing.T) {
	r, c := newRelay(t)
	path := filepath.Join(t.TempDir(), "host.credentials.json")
	if err := client.SaveCredentials(path, c); err != nil {
		t.Fatal(err)
	}
	lost := &lostResponse{}
	client.UseAuthTransport(t, lost)
	if _, err := client.FreshCredentials(context.Background(), path); !errors.Is(err, client.ErrRenewalPostponed) {
		t.Fatal("lost response not retryable:", err)
	}
	if lost.lost.RefreshToken == "" {
		t.Fatal("the relay did not rotate")
	}
	if stored, _ := client.LoadCredentials(path); stored.RefreshToken != c.RefreshToken {
		t.Fatal("credential lost after a transient failure")
	}
	// The network is back.
	client.UseAuthTransport(t, http.DefaultTransport)
	next, err := client.FreshCredentials(context.Background(), path)
	if err != nil {
		t.Fatal("retry within grace failed:", err)
	}
	if next.RefreshToken != lost.lost.RefreshToken || next.Token != lost.lost.Token {
		t.Fatal("retry did not return the same successor pair")
	}
	if len(r.revokedDevices()) != 0 {
		t.Fatal("retry within grace revoked the device")
	}
	if _, err := r.repo.Authenticate(context.Background(), next.Token); err != nil {
		t.Fatal("successor access token not valid:", err)
	}
	// The family continues normally.
	third, err := client.Refresh(context.Background(), next)
	if err != nil || third.RefreshToken == next.RefreshToken {
		t.Fatal("successor did not rotate:", err)
	}
	// And the consumed original is reuse now that its successor was used.
	if _, err := client.Refresh(context.Background(), c); !client.RenewalRejected(err) {
		t.Fatal("reuse after the successor was used not rejected:", err)
	}
	if got := r.revokedDevices(); len(got) != 1 || got[0] != c.DeviceID {
		t.Fatal("reuse did not revoke the device", got)
	}
}

func TestRejectedRefreshAgainstTheRelayRequiresSignIn(t *testing.T) {
	_, c := newRelay(t)
	c.RefreshToken = identity.Secret() // unknown to the relay
	path := filepath.Join(t.TempDir(), "host.credentials.json")
	if err := client.SaveCredentials(path, c); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FreshCredentials(context.Background(), path); !errors.Is(err, client.ErrSignInRequired) {
		t.Fatal(err)
	}
}
