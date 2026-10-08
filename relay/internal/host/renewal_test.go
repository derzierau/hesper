package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

func expiringHostFile(t *testing.T, relay string) (string, protocol.Credentials) {
	t.Helper()
	c := protocol.Credentials{Relay: relay, DeviceID: "laptop", Role: "host", Token: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute), RefreshExpiresAt: time.Now().Add(time.Hour)}
	path := filepath.Join(t.TempDir(), "host.credentials.json")
	if err := client.SaveCredentials(path, c); err != nil {
		t.Fatal(err)
	}
	return path, c
}

// The host outlives transient renewal failures (it used to exit and lose its
// sign-in) and connects once the relay renews.
func TestHostKeepsRunningAcrossTransientRenewalFailures(t *testing.T) {
	var refreshes atomic.Int32
	connected := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/refresh":
			switch refreshes.Add(1) {
			case 1: // response lost on the way back
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
			case 2:
				http.Error(w, "bad gateway", 502)
			default:
				json.NewEncoder(w).Encode(protocol.Credentials{DeviceID: "laptop", Role: "host", Token: "new-access", RefreshToken: "new-refresh", ExpiresAt: time.Now().Add(15 * time.Minute)})
			}
		case "/v1/connect":
			if r.Header.Get("Authorization") != "Bearer new-access" {
				http.Error(w, "unexpected token", 400)
				return
			}
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
			if err != nil {
				return
			}
			defer c.CloseNow()
			select {
			case connected <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	path, _ := expiringHostFile(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &Runner{Service: &Service{}, CredentialsPath: path, Credentials: protocol.Credentials{Role: "host"}, PollInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case err := <-done:
		t.Fatal("host stopped on a transient renewal failure:", err)
	case <-connected:
	case <-time.After(20 * time.Second):
		t.Fatal("host never connected")
	}
	if refreshes.Load() != 3 {
		t.Fatal("unexpected renewal attempts", refreshes.Load())
	}
	if stored, _ := client.LoadCredentials(path); stored.RefreshToken != "new-refresh" {
		t.Fatal("renewed pair not saved")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHostStopsOnRejectionNamingTheLoginCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(protocol.Err("unauthorized", "Authentication request rejected"))
	}))
	defer server.Close()
	path, _ := expiringHostFile(t, server.URL)
	before, _ := os.ReadFile(path)
	runner := &Runner{Service: &Service{}, CredentialsPath: path, Credentials: protocol.Credentials{Role: "host"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := runner.Run(ctx)
	if !errors.Is(err, client.ErrSignInRequired) || !strings.Contains(err.Error(), "hesperctl login --relay "+server.URL+" --role host") || !strings.Contains(err.Error(), "--out "+path) {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("credentials file changed")
	}
}
