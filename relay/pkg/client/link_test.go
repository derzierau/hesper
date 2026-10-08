package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

func TestOriginsAndCredentials(t *testing.T) {
	for _, value := range []string{"http://remote.example", "https://token@relay.example", "https://relay.example/path", "https://relay.example/?token=secret", "https://relay.example/#secret"} {
		if _, err := Origin(value); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	for _, value := range []string{"http://127.0.0.1:8787", "http://[::1]:8787", "https://relay.example"} {
		if _, err := Origin(value); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "controller.credentials.json")
	c := protocol.Credentials{Relay: "https://relay.example", DeviceID: "device", Role: "controller", Token: "secret"}
	if err := SaveCredentials(path, c); err != nil {
		t.Fatal(err)
	}
	if err := SaveCredentials(path, c); err == nil {
		t.Fatal("overwrote existing credentials")
	}
	got, err := LoadCredentials(path)
	if err != nil || got != c {
		t.Fatalf("round trip failed: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential file is not private")
	}
}
func TestPairingDoesNotForwardSecretsToRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if _, err := Pair(context.Background(), redirect.URL, "invitation", "phone"); err == nil {
		t.Fatal("redirected pairing succeeded")
	}
	if reached.Load() {
		t.Fatal("pairing request followed a redirect")
	}
}
