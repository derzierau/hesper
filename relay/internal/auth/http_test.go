package auth_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/relay"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/internal/transport"
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
func TestAppCallbackFlow(t *testing.T) {
	repo, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	p := &provider{owner: "github:12345678"}
	api := &auth.HTTP{Repository: repo, Provider: p, Policy: auth.NewAllowlist([]string{"12345678"}), PublicURL: "https://relay.example", AppCallbacks: auth.DefaultAppCallbacks, Register: func(identity.Device) {}, Revoke: func(string) {}}
	h := api.Handler()
	// Each step is what ASWebAuthenticationSession does: open
	// verificationURL, follow redirects, keep cookies, stop at the callback.
	var cookie *http.Cookie
	request := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if method == "POST" && strings.HasSuffix(target, "/auth/approve") {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://relay.example")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if c := w.Result().Cookies(); len(c) > 0 {
			cookie = c[0]
		}
		return w
	}
	signIn := func(name string) (string, string, *httptest.ResponseRecorder) {
		cookie = nil
		verifier := identity.Secret()
		w := request("POST", "https://relay.example/v1/auth/enroll", string(protocol.JSON(map[string]string{"name": name, "role": "controller", "challenge": auth.Challenge(verifier), "callback": "hesper-ios://auth"})))
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body)
		}
		var e struct{ ID, Code, VerificationURL string }
		json.Unmarshal(w.Body.Bytes(), &e)
		w = request("GET", e.VerificationURL, "")
		u, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != 303 || u.Host != "github.test" {
			t.Fatal(w.Code, u)
		}
		return e.ID, verifier, request("GET", "https://relay.example/auth/github/callback?code=ok&state="+u.Query().Get("state"), "")
	}
	back := func(w *httptest.ResponseRecorder, id, status string) {
		t.Helper()
		u, err := url.Parse(w.Header().Get("Location"))
		if w.Code != 303 || err != nil || u.Scheme != "hesper-ios" || u.Host != "auth" || u.Path != "" {
			t.Fatal(w.Code, w.Header().Get("Location"))
		}
		q := u.Query()
		want := url.Values{"enrollment": {id}, "status": {status}}
		if status == "approved" {
			want.Set("approved", "1")
		}
		if q.Encode() != want.Encode() {
			t.Fatalf("callback carries %v, want %v", q, want)
		}
	}

	id, verifier, w := signIn("iPhone")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<strong>iPhone</strong>") || !strings.Contains(w.Body.String(), id[:8]) {
		t.Fatal("approval page must name the device and show the code", w.Code, w.Body)
	}
	back(request("POST", "https://relay.example/auth/approve", "state="+cookie.Value), id, "approved")
	w = request("POST", "https://relay.example/v1/auth/claim", string(protocol.JSON(map[string]string{"id": id, "verifier": verifier})))
	var c protocol.Credentials
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil || c.Role != "controller" || c.Relay != "https://relay.example" {
		t.Fatal(w.Code, w.Body)
	}

	// An account the relay does not let in returns to the app too.
	p.owner = "github:999"
	id, _, w = signIn("iPhone")
	back(w, id, "not_allowed")
}
func TestDefaultAppCallbacks(t *testing.T) {
	dir := t.TempDir()
	load := func(extra string) auth.Config {
		t.Helper()
		path := filepath.Join(dir, "auth.json")
		os.WriteFile(path, []byte(`{"publicURL":"https://relay.example","githubClientId":"x","githubSecretFile":"s","allowedGitHubIds":["1"]`+extra+`}`), 0600)
		c, err := auth.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := load(""); len(c.AppCallbacks) != 1 || c.AppCallbacks[0] != "hesper-ios://auth" {
		t.Fatal(c.AppCallbacks)
	}
	if c := load(`,"appCallbacks":[]`); len(c.AppCallbacks) != 0 {
		t.Fatal(c.AppCallbacks)
	}
}
func claimed(t *testing.T, repo *storage.Bolt, owner string, role identity.Role) protocol.Credentials {
	t.Helper()
	ctx := context.Background()
	verifier := identity.Secret()
	e := auth.Enrollment{ID: identity.Secret(), Name: "iPhone", Role: role, Challenge: auth.Challenge(verifier), Expires: time.Now().Add(time.Minute)}
	if err := repo.CreateEnrollment(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := repo.Approve(ctx, e.ID, auth.Principal{ID: owner, Login: "octocat"}); err != nil {
		t.Fatal(err)
	}
	_, c, err := repo.Claim(ctx, e.ID, verifier, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestPushToken(t *testing.T) {
	repo, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	app := transport.New(repo, relay.New(nil, time.Second))
	app.Auth = &auth.HTTP{Repository: repo, Provider: &provider{}, Policy: auth.NewAllowlist([]string{"12345678"}), PublicURL: "https://relay.example"}
	h := app.PublicHandler()
	push := func(bearer, body string) int {
		r := httptest.NewRequest("POST", "https://relay.example/v1/devices/push", strings.NewReader(body))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	stored := func(id string) *identity.Push {
		devices, _ := repo.Devices(context.Background(), "")
		for _, d := range devices {
			if d.ID == id {
				return d.Push
			}
		}
		return nil
	}
	phone := claimed(t, repo, "github:12345678", identity.Controller)
	first := strings.Repeat("ab", 32)
	if code := push(phone.Token, `{"token":"`+first+`","environment":"sandbox"}`); code != 200 {
		t.Fatal(code)
	}
	second := strings.Repeat("CD", 32)
	if code := push(phone.Token, `{"token":"`+second+`","environment":"production"}`); code != 200 {
		t.Fatal(code)
	}
	if p := stored(phone.DeviceID); p == nil || p.Token != strings.ToLower(second) || p.Environment != "production" {
		t.Fatal("token not replaced", p)
	}
	for name, c := range map[string]struct {
		bearer, body string
		code         int
	}{
		"no bearer":   {"", `{"token":"` + first + `","environment":"sandbox"}`, 401},
		"bad bearer":  {"nope", `{"token":"` + first + `","environment":"sandbox"}`, 401},
		"environment": {phone.Token, `{"token":"` + first + `","environment":"dev"}`, 400},
		"short token": {phone.Token, `{"token":"abcd","environment":"sandbox"}`, 400},
		"not hex":     {phone.Token, `{"token":"` + strings.Repeat("zz", 32) + `","environment":"sandbox"}`, 400},
		"unknown":     {phone.Token, `{"token":"` + first + `","environment":"sandbox","x":1}`, 400},
		"host":        {claimed(t, repo, "github:12345678", identity.Host).Token, `{"token":"` + first + `","environment":"sandbox"}`, 403},
		"not allowed": {claimed(t, repo, "github:999", identity.Controller).Token, `{"token":"` + first + `","environment":"sandbox"}`, 401},
	} {
		if code := push(c.bearer, c.body); code != c.code {
			t.Errorf("%s: %d, want %d", name, code, c.code)
		}
	}
	if err := repo.Revoke(context.Background(), phone.DeviceID); err != nil {
		t.Fatal(err)
	}
	if p := stored(phone.DeviceID); p != nil {
		t.Fatal("revoke kept the push token")
	}
	if code := push(phone.Token, `{"token":"`+first+`","environment":"sandbox"}`); code != 401 {
		t.Fatal("revoked device registered a token", code)
	}
}
