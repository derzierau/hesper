package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/auth"
	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/internal/storage"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// browser drives the relay as one browser: it keeps the sign-in cookie
// and sends the relay's own Origin with Approve.
type browser struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func (b *browser) do(method, target, form string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://relay.example"+target, strings.NewReader(form))
	if b.cookie != nil {
		r.AddCookie(b.cookie)
	}
	if method == "POST" && strings.HasPrefix(target, "/auth/") {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://relay.example")
	}
	w := httptest.NewRecorder()
	b.h.ServeHTTP(w, r)
	if c := w.Result().Cookies(); len(c) > 0 {
		b.cookie = c[0]
	}
	return w
}

// enroll starts an enrollment, with the app callback when callback is set.
func (b *browser) enroll(callback bool) string {
	body := map[string]string{"name": "iPhone", "role": "controller", "challenge": auth.Challenge(identity.Secret())}
	if callback {
		body["callback"] = "hesper-ios://auth"
	}
	w := b.do("POST", "/v1/auth/enroll", string(protocol.JSON(body)))
	var e struct{ ID string }
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &e) != nil {
		b.t.Fatal(w.Code, w.Body)
	}
	return e.ID
}

// toGitHub opens the sign-in link and returns the state GitHub would get.
func (b *browser) toGitHub(id string) string {
	w := b.do("GET", "/auth/login?enrollment="+id, "")
	u, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != 303 {
		b.t.Fatal(w.Code, w.Body)
	}
	return u.Query().Get("state")
}

// approvalForm returns the form fields of the approval page.
func (b *browser) approvalForm(id string) string {
	w := b.do("GET", "/auth/github/callback?code=ok&state="+b.toGitHub(id), "")
	if w.Code != 200 {
		b.t.Fatal(w.Code, w.Body)
	}
	return url.Values{"state": {b.cookie.Value}, "enrollment": {id}}.Encode()
}

func pageTest(t *testing.T) (*storage.Bolt, *provider, func() *browser) {
	repo, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	p := &provider{owner: "github:12345678"}
	h := (&auth.HTTP{Repository: repo, Provider: p, Policy: auth.NewAllowlist([]string{"12345678"}), PublicURL: "https://relay.example", AppCallbacks: auth.DefaultAppCallbacks, Register: func(identity.Device) {}, Revoke: func(string) {}}).Handler()
	return repo, p, func() *browser { return &browser{t: t, h: h} }
}

var titlePattern = regexp.MustCompile(`<title>([^<]*)</title>`)

func page(t *testing.T, w *httptest.ResponseRecorder, status int, title string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content type %q: %s", ct, w.Body)
	}
	m := titlePattern.FindStringSubmatch(w.Body.String())
	if m == nil || !strings.HasPrefix(html.UnescapeString(m[1]), title) {
		t.Fatalf("title %q, want %q", m, title)
	}
}

func backToApp(t *testing.T, w *httptest.ResponseRecorder, id, status string) {
	t.Helper()
	u, err := url.Parse(w.Header().Get("Location"))
	if w.Code != 303 || err != nil || u.Scheme != "hesper-ios" || u.Query().Get("enrollment") != id || u.Query().Get("status") != status {
		t.Fatalf("want hesper-ios://auth with %s, got %d %q %s", status, w.Code, w.Header().Get("Location"), w.Body)
	}
}

func TestBrowserErrorPages(t *testing.T) {
	repo, p, open := pageTest(t)
	ctx := context.Background()

	b := open()
	page(t, b.do("GET", "/auth/login?enrollment=nope", ""), 400, "This sign-in has expired")

	// The callback in a browser without the sign-in cookie.
	b = open()
	state := b.toGitHub(b.enroll(false))
	page(t, open().do("GET", "/auth/github/callback?code=ok&state="+state, ""), 403, "This browser can't finish the sign-in")
	// Used twice.
	cookie := b.cookie
	b.do("GET", "/auth/github/callback?code=ok&state="+state, "")
	b.cookie = cookie
	page(t, b.do("GET", "/auth/github/callback?code=ok&state="+state, ""), 400, "This sign-in has expired")

	b = open()
	page(t, b.do("GET", "/auth/github/callback?error=access_denied&state="+b.toGitHub(b.enroll(false)), ""), 400, "GitHub sign-in was cancelled")

	p.owner = "github:999"
	b = open()
	w := b.do("GET", "/auth/github/callback?code=ok&state="+b.toGitHub(b.enroll(false)), "")
	page(t, w, 403, "octocat can't use this relay")
	p.owner = "github:12345678"

	// Approve from another site, without the cookie, and after the
	// enrollment ended.
	b = open()
	form := b.approvalForm(b.enroll(false))
	r := httptest.NewRequest("POST", "https://relay.example/auth/approve", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://attacker.example")
	r.AddCookie(b.cookie)
	w = httptest.NewRecorder()
	b.h.ServeHTTP(w, r)
	page(t, w, 403, "This approval came from another site")
	page(t, open().do("POST", "/auth/approve", form), 403, "This browser can't finish the sign-in")

	b = open()
	id := b.enroll(false)
	form = b.approvalForm(id)
	if err := repo.Approve(ctx, id, auth.Principal{ID: "github:other"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Approve(ctx, id, auth.Principal{ID: "github:12345678"}); err == nil {
		t.Fatal("approved twice")
	}
	page(t, b.do("POST", "/auth/approve", form), 200, "Already approved")

	page(t, open().do("GET", "/auth/nowhere", ""), 404, "Page not found")
}

func TestApproveTwice(t *testing.T) {
	_, _, open := pageTest(t)
	b := open()
	id := b.enroll(false)
	form := b.approvalForm(id)
	cookie := b.cookie
	w := b.do("POST", "/auth/approve", form)
	page(t, w, 200, "Device approved")
	if !strings.Contains(w.Body.String(), "Return to iPhone.") {
		t.Fatal(w.Body)
	}
	// A double click: the second POST still carries the cookie.
	b.cookie = cookie
	w = b.do("POST", "/auth/approve", form)
	page(t, w, 200, "Already approved")
	if !strings.Contains(w.Body.String(), "iPhone is already approved") {
		t.Fatal(w.Body)
	}
	// A reload after the cookie was cleared.
	b.cookie = nil
	page(t, b.do("POST", "/auth/approve", form), 200, "Already approved")

	// In the app's sheet the second tap returns to the app as approved.
	b = open()
	id = b.enroll(true)
	form = b.approvalForm(id)
	cookie = b.cookie
	backToApp(t, b.do("POST", "/auth/approve", form), id, "approved")
	b.cookie = cookie
	backToApp(t, b.do("POST", "/auth/approve", form), id, "approved")
}

func TestExpiredReturnsToApp(t *testing.T) {
	repo, _, open := pageTest(t)
	ctx := context.Background()
	gone := auth.Enrollment{ID: identity.Secret(), Name: "iPhone", Role: identity.Controller, Challenge: auth.Challenge(identity.Secret()), Callback: "hesper-ios://auth", Expires: time.Now().Add(-time.Second)}
	if err := repo.CreateEnrollment(ctx, gone); err != nil {
		t.Fatal(err)
	}
	backToApp(t, open().do("GET", "/auth/login?enrollment="+gone.ID, ""), gone.ID, "expired")

	// Approved meanwhile from another browser: the flow's Approve finds
	// the enrollment taken and still returns to the app.
	b := open()
	id := b.enroll(true)
	form := b.approvalForm(id)
	if err := repo.Approve(ctx, id, auth.Principal{ID: "github:12345678"}); err != nil {
		t.Fatal(err)
	}
	backToApp(t, b.do("POST", "/auth/approve", form), id, "approved")

	b = open()
	id = b.enroll(true)
	backToApp(t, b.do("GET", "/auth/github/callback?error=access_denied&state="+b.toGitHub(id), ""), id, "denied")
}

func TestAPIErrorsStayJSON(t *testing.T) {
	_, _, open := pageTest(t)
	b := open()
	for _, c := range []struct {
		body string
		code int
		json string
	}{
		{`{"id":"nope","verifier":"` + strings.Repeat("a", 43) + `"}`, 400, `{"code":"expired","message":"Authentication request rejected"}`},
		{`{`, 400, `{"code":"invalid_request","message":"Authentication request rejected"}`},
	} {
		w := b.do("POST", "/v1/auth/claim", c.body)
		if w.Code != c.code || w.Header().Get("Content-Type") != "application/json" || strings.TrimSpace(w.Body.String()) != c.json {
			t.Errorf("%s: %d %q %s", c.body, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
	}
}

func TestRateLimitPage(t *testing.T) {
	_, _, open := pageTest(t)
	b := open()
	for i := 0; i < 240; i++ {
		b.do("GET", "/auth/login?enrollment=nope", "")
	}
	w := b.do("GET", "/auth/login?enrollment=nope", "")
	page(t, w, 429, "Too many sign-ins")
	if w.Header().Get("Retry-After") != "60" {
		t.Fatal(w.Header())
	}
	w = b.do("POST", "/v1/auth/claim", "{}")
	if w.Code != 429 || strings.TrimSpace(w.Body.String()) != "Try again later" {
		t.Fatal("API rate limit changed", w.Code, w.Body)
	}
}

// The inline stylesheet is the only one, and the CSP allows exactly it.
func TestPageStyleMatchesCSP(t *testing.T) {
	_, _, open := pageTest(t)
	b := open()
	w := b.do("GET", "/auth/github/callback?code=ok&state="+b.toGitHub(b.enroll(true)), "")
	body := w.Body.String()
	styles := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindAllStringSubmatch(body, -1)
	if len(styles) != 1 {
		t.Fatal("want one stylesheet", body)
	}
	sum := sha256.Sum256([]byte(styles[0][1]))
	csp := w.Header().Get("Content-Security-Policy")
	want := "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; form-action 'self' hesper-ios:; frame-ancestors 'none'"
	if csp != want {
		t.Fatalf("CSP %q\nwant %q", csp, want)
	}
	for _, banned := range []string{"<script", " style=", "<link", "<img", "url("} {
		if strings.Contains(body, banned) {
			t.Errorf("page holds %q", banned)
		}
	}
}
