package auth_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// A refresh whose response was lost may be repeated within the grace window
// by the device holding the matching access token; anyone else reusing it
// still revokes the device.
func TestRefreshGraceOverHTTP(t *testing.T) {
	ctx := context.Background()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	revoked := ""
	h := (&auth.HTTP{Repository: repo, Policy: auth.NewAllowlist([]string{"1"}), PublicURL: "https://relay.example", Register: func(identity.Device) {}, Revoke: func(id string) { revoked = id }}).Handler()
	verifier := identity.Secret()
	e := auth.Enrollment{ID: identity.Secret(), Name: "Mac", Role: identity.Host, Challenge: auth.Challenge(verifier), Expires: time.Now().Add(time.Minute)}
	if err = repo.CreateEnrollment(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = repo.Approve(ctx, e.ID, auth.Principal{ID: "github:1", Login: "x"}); err != nil {
		t.Fatal(err)
	}
	_, c, err := repo.Claim(ctx, e.ID, verifier, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(token, bearer string) (int, protocol.Credentials) {
		r := httptest.NewRequest("POST", "https://relay.example/v1/auth/refresh", strings.NewReader(string(protocol.JSON(map[string]string{"refreshToken": token}))))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var next protocol.Credentials
		json.Unmarshal(w.Body.Bytes(), &next)
		return w.Code, next
	}
	code, first := refresh(c.RefreshToken, c.Token)
	if code != 200 {
		t.Fatal(code)
	}
	code, again := refresh(c.RefreshToken, c.Token)
	if code != 200 || again.RefreshToken != first.RefreshToken || again.Token != first.Token || again.Relay != "https://relay.example" || revoked != "" {
		t.Fatal("lost-response retry not idempotent", code, revoked)
	}
	if code, _ = refresh(c.RefreshToken, identity.Secret()); code != 401 || revoked != c.DeviceID {
		t.Fatal("reuse with a wrong proof not revoked", code)
	}
}
