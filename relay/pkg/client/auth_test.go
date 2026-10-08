package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

func TestConcurrentFileRenewal(t *testing.T) {
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/auth/refresh" {
			t.Error("wrong endpoint")
		}
		var req struct{ RefreshToken string }
		json.NewDecoder(r.Body).Decode(&req)
		if req.RefreshToken != "old-refresh" {
			t.Error("wrong refresh token")
		}
		json.NewEncoder(w).Encode(protocol.Credentials{DeviceID: "device", Role: "host", Token: "new-access", RefreshToken: "new-refresh", ExpiresAt: time.Now().Add(15 * time.Minute)})
	}))
	defer h.Close()
	path := filepath.Join(t.TempDir(), "host.credentials.json")
	if err := SaveCredentials(path, protocol.Credentials{Relay: h.URL, DeviceID: "device", Role: "host", Token: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := FreshCredentials(context.Background(), path)
			if err != nil || c.Token != "new-access" {
				t.Error(c.DeviceID, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh called %d times", calls.Load())
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("credentials not private")
	}
}
