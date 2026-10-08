package auth

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// HTTP owns browser transactions. Repository owns atomic, single-use credentials.
// Neither GitHub tokens nor relay credentials are put in browser redirects.
type HTTP struct {
	Repository   Repository
	Provider     IdentityProvider
	Policy       AccessPolicy
	PublicURL    string
	AppCallbacks []string
	Register     func(identity.Device)
	Revoke       func(string)
	mu           sync.Mutex
	window       time.Time
	requests     int
}

func (s *HTTP) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/enroll", s.enroll)
	mux.HandleFunc("POST /v1/auth/claim", s.claim)
	mux.HandleFunc("POST /v1/auth/refresh", s.refresh)
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/github/callback", s.callback)
	mux.HandleFunc("POST /auth/approve", s.approve)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		s.mu.Lock()
		if time.Since(s.window) >= time.Minute {
			s.window = time.Now()
			s.requests = 0
		}
		s.requests++
		allowed := s.requests <= 240
		s.mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "Try again later", 429)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code string) {
	jsonReply(w, status, protocol.Err(code, "Authentication request rejected"))
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Origin") != "" {
		fail(w, 403, "forbidden")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		fail(w, 400, "invalid_request")
		return false
	}
	return true
}

var challengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (s *HTTP) enroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string        `json:"name"`
		Role      identity.Role `json:"role"`
		Challenge string        `json:"challenge"`
		Callback  string        `json:"callback"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 128 || (req.Role != identity.Host && req.Role != identity.Controller) || !challengePattern.MatchString(req.Challenge) {
		fail(w, 400, "invalid_request")
		return
	}
	if req.Callback != "" {
		ok := false
		for _, c := range s.AppCallbacks {
			if c == req.Callback {
				ok = true
			}
		}
		if !ok {
			fail(w, 400, "invalid_callback")
			return
		}
	}
	e := Enrollment{ID: identity.Secret(), Name: req.Name, Role: req.Role, Challenge: req.Challenge, Callback: req.Callback, Expires: time.Now().Add(10 * time.Minute)}
	if s.Repository.CreateEnrollment(r.Context(), e) != nil {
		fail(w, 503, "busy")
		return
	}
	jsonReply(w, 201, map[string]any{"id": e.ID, "code": e.ID[:8], "verificationURL": s.PublicURL + "/auth/login?enrollment=" + url.QueryEscape(e.ID), "expires": e.Expires, "interval": 5})
}

// wire name: the "ghosty_auth" cookie is kept until the next relay deploy.
func (s *HTTP) cookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{Name: "ghosty_auth", Value: state, Path: "/auth/", HttpOnly: true, Secure: strings.HasPrefix(s.PublicURL, "https:"), SameSite: http.SameSiteLaxMode, MaxAge: 600})
}
func bound(r *http.Request, state string) bool {
	c, err := r.Cookie("ghosty_auth")
	return err == nil && state != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) == 1
}
func (s *HTTP) login(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("enrollment")
	if _, err := s.Repository.Enrollment(r.Context(), id); err != nil {
		fail(w, 400, "expired")
		return
	}
	f := Flow{State: identity.Secret(), EnrollmentID: id, Verifier: identity.Secret(), Expires: time.Now().Add(10 * time.Minute)}
	if s.Repository.PutFlow(r.Context(), f) != nil {
		fail(w, 503, "busy")
		return
	}
	s.cookie(w, f.State)
	http.Redirect(w, r, s.Provider.AuthorizationURL(f.State, f.Verifier), http.StatusSeeOther)
}

var approvalPage = template.Must(template.New("approve").Parse(`<!doctype html><html><head><title>Approve Hesper device</title></head><body><h1>Approve device</h1><p>Signed in as {{.Login}}.</p><p>Device: <strong>{{.Name}}</strong> ({{.Role}})</p><p>Compare this code with your device: <strong>{{.Code}}</strong></p><p>Approve only if you started this sign-in on that device.</p><form method="post" action="/auth/approve"><input type="hidden" name="state" value="{{.State}}"><button type="submit">Approve this device</button></form></body></html>`))

func (s *HTTP) callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if !bound(r, state) {
		fail(w, 403, "invalid_state")
		return
	}
	f, err := s.Repository.TakeFlow(r.Context(), state)
	if err != nil || f.Verifier == "" || f.Principal.ID != "" {
		fail(w, 400, "expired")
		return
	}
	if r.URL.Query().Get("code") == "" {
		fail(w, 400, "denied")
		return
	}
	p, err := s.Provider.Identify(r.Context(), r.URL.Query().Get("code"), f.Verifier)
	if err != nil || !s.Policy.Allowed(p.ID) {
		fail(w, 403, "not_allowed")
		return
	}
	e, err := s.Repository.Enrollment(r.Context(), f.EnrollmentID)
	if err != nil {
		fail(w, 400, "expired")
		return
	}
	f.State = identity.Secret()
	f.Verifier = ""
	f.Principal = p
	f.Expires = time.Now().Add(5 * time.Minute)
	if s.Repository.PutFlow(r.Context(), f) != nil {
		fail(w, 503, "busy")
		return
	}
	s.cookie(w, f.State)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Preserve the Origin header on same-origin form POSTs without leaking
	// OAuth callback URLs to other sites. no-referrer makes form Origin null.
	w.Header().Set("Referrer-Policy", "same-origin")
	_ = approvalPage.Execute(w, map[string]string{"Login": p.Login, "Name": e.Name, "Role": string(e.Role), "Code": e.ID[:8], "State": f.State})
}
func (s *HTTP) approve(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.PublicURL {
		fail(w, 403, "invalid_origin")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil || !bound(r, r.PostForm.Get("state")) {
		fail(w, 403, "invalid_state")
		return
	}
	f, err := s.Repository.TakeFlow(r.Context(), r.PostForm.Get("state"))
	if err != nil || f.Verifier != "" || !s.Policy.Allowed(f.Principal.ID) {
		fail(w, 403, "not_allowed")
		return
	}
	e, err := s.Repository.Enrollment(r.Context(), f.EnrollmentID)
	if err != nil || s.Repository.Approve(r.Context(), f.EnrollmentID, f.Principal) != nil {
		fail(w, 400, "expired")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "ghosty_auth", Path: "/auth/", HttpOnly: true, Secure: strings.HasPrefix(s.PublicURL, "https:"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if e.Callback != "" {
		u, _ := url.Parse(e.Callback)
		q := u.Query()
		q.Set("enrollment", e.ID)
		q.Set("approved", "1")
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<!doctype html><title>Hesper approved</title><h1>Device approved</h1><p>Return to your device. You can close this window.</p>")
}
func (s *HTTP) claim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id"`
		Verifier string `json:"verifier"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	e, err := s.Repository.Enrollment(r.Context(), req.ID)
	if err != nil {
		fail(w, 400, "expired")
		return
	}
	if len(req.Verifier) < 43 || len(req.Verifier) > 128 || subtle.ConstantTimeCompare([]byte(e.Challenge), []byte(Challenge(req.Verifier))) != 1 {
		fail(w, 403, "invalid_proof")
		return
	}
	if e.Principal.ID == "" {
		fail(w, 409, "authorization_pending")
		return
	}
	if !s.Policy.Allowed(e.Principal.ID) {
		fail(w, 403, "not_allowed")
		return
	}
	d, c, err := s.Repository.Claim(r.Context(), req.ID, req.Verifier, 15*time.Minute, 30*24*time.Hour)
	if err != nil {
		fail(w, 400, "expired")
		return
	}
	s.Register(d)
	c.Relay = s.PublicURL
	jsonReply(w, 200, c)
}
func (s *HTTP) refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// The access token issued with this refresh credential, when the client
	// sends it, proves the same device for the lost-response grace.
	proof, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		proof = ""
	}
	d, c, err := s.Repository.Rotate(r.Context(), req.RefreshToken, proof, 15*time.Minute)
	var replay *ReplayError
	if errors.As(err, &replay) {
		s.Revoke(replay.DeviceID)
	}
	if err != nil {
		fail(w, 401, "unauthorized")
		return
	}
	if !s.Policy.Allowed(d.Owner) {
		s.Revoke(d.ID)
		fail(w, 403, "not_allowed")
		return
	}
	c.Relay = s.PublicURL
	jsonReply(w, 200, c)
}
