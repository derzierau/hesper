package auth_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

type provider struct{ owner string }

func (p *provider) AuthorizationURL(state, verifier string) string {
	return "https://github.test/authorize?state=" + state
}
func (p *provider) Identify(context.Context, string, string) (auth.Principal, error) {
	return auth.Principal{ID: p.owner, Login: "octocat"}, nil
}
func TestBrowserApprovalAndRotation(t *testing.T) {
	repo, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	p := &provider{owner: "github:12345678"}
	registered := ""
	revoked := ""
	api := &auth.HTTP{Repository: repo, Provider: p, Policy: auth.NewAllowlist([]string{"12345678"}), PublicURL: "https://relay.example", Register: func(d identity.Device) { registered = d.ID }, Revoke: func(id string) { revoked = id }}
	h := api.Handler()
	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://relay.example"+path, strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if path == "/auth/approve" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://relay.example")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	verifier := identity.Secret()
	w := request("POST", "/v1/auth/enroll", string(protocol.JSON(map[string]string{"name": "Mac <script>", "role": "host", "challenge": auth.Challenge(verifier)})), nil)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var e struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &e)
	claim := string(protocol.JSON(map[string]string{"id": e.ID, "verifier": verifier}))
	if w = request("POST", "/v1/auth/claim", claim, nil); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	w = request("GET", "/auth/login?enrollment="+e.ID, "", nil)
	cookie := w.Result().Cookies()[0]
	u, _ := url.Parse(w.Header().Get("Location"))
	state := u.Query().Get("state")
	if w = request("GET", "/auth/github/callback?code=ok&state="+state, "", nil); w.Code != 403 {
		t.Fatal("unbound callback accepted")
	}
	w = request("GET", "/auth/github/callback?code=ok&state="+state, "", cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), "Mac <script>") {
		t.Fatal(w.Code, w.Body)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("approval form must preserve same-origin POST Origin, got %q", got)
	}
	approvalCookie := w.Result().Cookies()[0]
	for _, origin := range []string{"null", "https://attacker.example"} {
		r := httptest.NewRequest("POST", "https://relay.example/auth/approve", strings.NewReader("state="+approvalCookie.Value))
		r.AddCookie(approvalCookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		rejected := httptest.NewRecorder()
		h.ServeHTTP(rejected, r)
		if rejected.Code != 403 {
			t.Fatalf("accepted origin %q", origin)
		}
	}
	if w = request("POST", "/v1/auth/claim", claim, nil); w.Code != 409 {
		t.Fatal("callback approved without consent")
	}
	if w = request("GET", "/auth/github/callback?code=ok&state="+state, "", cookie); w.Code != 400 {
		t.Fatal("callback replay accepted")
	}
	w = request("POST", "/auth/approve", "state="+approvalCookie.Value, approvalCookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = request("POST", "/v1/auth/claim", claim, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var c protocol.Credentials
	json.Unmarshal(w.Body.Bytes(), &c)
	if registered != c.DeviceID || c.RefreshToken == "" || time.Until(c.ExpiresAt) > 16*time.Minute {
		t.Fatal("invalid credential")
	}
	d, err := repo.Authenticate(context.Background(), c.Token)
	if err != nil || d.Owner != "github:12345678" {
		t.Fatal(d, err)
	}
	if w = request("POST", "/v1/auth/claim", claim, nil); w.Code != 400 {
		t.Fatal("claim replay accepted")
	}
	refresh := string(protocol.JSON(map[string]string{"refreshToken": c.RefreshToken}))
	w = request("POST", "/v1/auth/refresh", refresh, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var next protocol.Credentials
	json.Unmarshal(w.Body.Bytes(), &next)
	if !next.RefreshExpiresAt.Equal(c.RefreshExpiresAt) {
		t.Fatal("rotation extended absolute expiry")
	}
	if w = request("POST", "/v1/auth/refresh", refresh, nil); w.Code != 401 || revoked != c.DeviceID {
		t.Fatal("replay not revoked")
	}
	if _, err = repo.Authenticate(context.Background(), next.Token); err == nil {
		t.Fatal("replay left new token valid")
	}
	// A different GitHub identity cannot approve, even with a valid browser state.
	p.owner = "github:999"
	w = request("GET", "/auth/login?enrollment="+e.ID, "", nil)
	e2 := auth.Enrollment{ID: identity.Secret(), Name: "other", Role: identity.Controller, Challenge: auth.Challenge(verifier), Expires: time.Now().Add(time.Minute)}
	if err = repo.CreateEnrollment(context.Background(), e2); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/auth/login?enrollment="+e2.ID, "", nil)
	cookie = w.Result().Cookies()[0]
	u, _ = url.Parse(w.Header().Get("Location"))
	w = request("GET", "/auth/github/callback?code=ok&state="+u.Query().Get("state"), "", cookie)
	if w.Code != 403 {
		t.Fatal("unlisted account accepted")
	}
}
func TestEnrollmentValidation(t *testing.T) {
	repo, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	h := (&auth.HTTP{Repository: repo, PublicURL: "https://relay.example"}).Handler()
	for _, body := range []string{`{"name":"x","role":"admin","challenge":"x"}`, `{"name":"x","role":"host","challenge":"` + auth.Challenge(identity.Secret()) + `","callback":"https://evil.example"}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/auth/enroll", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/v1/auth/enroll", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGitHubIdentityExchange(t *testing.T) {
	g := &auth.GitHub{ClientID: "client", Secret: "secret", Callback: "https://relay.example/auth/github/callback"}
	g.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"id":12345678,"login":"renamed-user"}`
		if r.URL.Host == "github.com" {
			r.ParseForm()
			if r.PostForm.Get("code_verifier") != "proof" || r.PostForm.Get("client_secret") != "secret" {
				t.Fatal("missing exchange proof")
			}
			body = `{"access_token":"github-token"}`
		} else if r.Header.Get("Authorization") != "Bearer github-token" {
			t.Fatal("missing GitHub authorization")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	u, _ := url.Parse(g.AuthorizationURL("state", "proof"))
	if u.Query().Get("scope") != "" || u.Query().Get("code_challenge") != auth.Challenge("proof") {
		t.Fatal(u)
	}
	p, err := g.Identify(context.Background(), "code", "proof")
	if err != nil || p.ID != "github:12345678" {
		t.Fatal(p, err)
	}
}
