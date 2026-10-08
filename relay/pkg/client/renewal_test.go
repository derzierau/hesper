package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// failingTransport stands in for the network: it returns err instead of a
// response, after optionally delivering the request (a lost response).
type failingTransport struct {
	deliver bool
	err     error
}

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.deliver {
		if res, err := http.DefaultTransport.RoundTrip(r); err == nil {
			res.Body.Close()
		}
	}
	return nil, f.err
}

func useAuthClient(t *testing.T, c *http.Client) {
	t.Helper()
	previous := authHTTP
	authHTTP = func() *http.Client { return c }
	t.Cleanup(func() { authHTTP = previous })
}

func expiring(relay string) protocol.Credentials {
	return protocol.Credentials{Relay: relay, DeviceID: "device", Role: "host", Token: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(10 * time.Second), RefreshExpiresAt: time.Now().Add(time.Hour)}
}

func savedFile(t *testing.T, c protocol.Credentials) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host.credentials.json")
	if err := SaveCredentials(path, c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, data
}

func addrNotAvailable() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.EADDRNOTAVAIL)}
}

// Every failure but the relay's explicit rejection keeps the credentials file
// byte for byte and reports a postponed renewal.
func TestTransientRenewalFailuresKeepTheCredential(t *testing.T) {
	hijack := func(reset bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			if tcp, ok := conn.(*net.TCPConn); ok && reset {
				tcp.SetLinger(0) // closes with RST: "connection reset by peer"
			}
			conn.Close()
		}
	}
	status := func(code int, contentType, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			w.WriteHeader(code)
			w.Write([]byte(body))
		}
	}
	cases := []struct {
		name      string
		handler   http.HandlerFunc
		transport http.RoundTripper
		timeout   time.Duration
	}{
		{name: "read error EADDRNOTAVAIL before sending", transport: failingTransport{err: addrNotAvailable()}},
		{name: "read error EADDRNOTAVAIL after delivery", transport: failingTransport{deliver: true, err: addrNotAvailable()}, handler: status(200, "application/json", `{}`)},
		{name: "DNS failure", transport: failingTransport{err: &net.DNSError{Err: "no such host", Name: "relay.example", IsNotFound: true}}},
		{name: "connection reset", handler: hijack(true)},
		{name: "EOF", handler: hijack(false)},
		{name: "timeout", timeout: 100 * time.Millisecond, handler: func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}},
		{name: "500", handler: status(500, "text/plain", "internal error")},
		{name: "502 proxy page", handler: status(502, "text/html", "<html>Bad Gateway</html>")},
		{name: "503 JSON", handler: status(503, "application/json", `{"code":"busy","message":"later"}`)},
		{name: "429", handler: status(429, "text/plain", "Try again later")},
		{name: "401 without the relay's error", handler: status(401, "text/html", "<html>proxy login</html>")},
		{name: "401 with another code", handler: status(401, "application/json", `{"code":"invalid_token","message":"x"}`)},
		{name: "400", handler: status(400, "application/json", `{"code":"invalid_request","message":"x"}`)},
		{name: "malformed 200", handler: status(200, "application/json", `{"token":`)},
		{name: "incomplete 200", handler: status(200, "application/json", `{"deviceId":"device","role":"host","token":"new-access"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relay := "http://127.0.0.1:9"
			if tc.handler != nil {
				server := httptest.NewServer(tc.handler)
				defer server.Close()
				relay = server.URL
			}
			h := authHTTP()
			if tc.transport != nil {
				h.Transport = tc.transport
			}
			if tc.timeout > 0 {
				h.Timeout = tc.timeout
			}
			useAuthClient(t, h)
			path, before := savedFile(t, expiring(relay))
			c, err := FreshCredentials(context.Background(), path)
			if !errors.Is(err, ErrRenewalPostponed) || errors.Is(err, ErrSignInRequired) {
				t.Fatalf("not postponed: %v", err)
			}
			if c.RefreshToken != "old-refresh" || c.Token != "old-access" {
				t.Fatal("returned credentials changed", c)
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Fatalf("credentials file changed:\n%s", after)
			}
		})
	}
}

func TestExplicitRejectionRequiresSignInWithTheCommand(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		role   string
	}{{401, "unauthorized", "host"}, {403, "not_allowed", "controller"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			json.NewEncoder(w).Encode(protocol.Err(tc.code, "Authentication request rejected"))
		}))
		c := expiring(server.URL)
		c.Role = tc.role
		path, before := savedFile(t, c)
		_, err := FreshCredentials(context.Background(), path)
		server.Close()
		var signIn *SignInRequiredError
		if !errors.Is(err, ErrSignInRequired) || !errors.As(err, &signIn) || errors.Is(err, ErrRenewalPostponed) {
			t.Fatalf("%d not a sign-in error: %v", tc.status, err)
		}
		want := "rm " + path + " && hesperctl login --relay " + server.URL + " --role " + tc.role + " --name \"$(hostname -s)"
		if !strings.Contains(err.Error(), want) || !strings.HasSuffix(err.Error(), "--out "+path) {
			t.Fatalf("message lacks the command %q:\n%s", want, err)
		}
		// The file is left alone, so a relay misconfiguration that is fixed
		// later does not cost the credential either.
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Fatal("rejected credential rewritten")
		}
	}
}

func TestMissingRefreshCredentialNamesTheCommand(t *testing.T) {
	c := expiring("https://relay.example")
	c.RefreshToken = ""
	path, _ := savedFile(t, c)
	_, err := FreshCredentials(context.Background(), path)
	if !errors.Is(err, ErrSignInRequired) || !strings.Contains(err.Error(), "hesperctl login --relay https://relay.example --role host") {
		t.Fatal(err)
	}
}

func TestRenewalSendsTheAccessTokenAsProof(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer old-access" {
			t.Error("missing proof", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(protocol.Credentials{DeviceID: "device", Role: "host", Token: "new-access", RefreshToken: "new-refresh", ExpiresAt: time.Now().Add(15 * time.Minute)})
	}))
	defer server.Close()
	path, _ := savedFile(t, expiring(server.URL))
	c, err := FreshCredentials(context.Background(), path)
	if err != nil || c.RefreshToken != "new-refresh" {
		t.Fatal(c, err)
	}
	if stored, _ := LoadCredentials(path); stored.RefreshToken != "new-refresh" || stored.Relay != server.URL {
		t.Fatal("renewed pair not saved", stored)
	}
}

// When the renewed pair cannot be saved, the old one stays on disk (and is
// presented again within the relay's grace window).
func TestFailedSaveKeepsTheOldCredential(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(protocol.Credentials{DeviceID: "device", Role: "host", Token: "new-access", RefreshToken: "new-refresh", ExpiresAt: time.Now().Add(15 * time.Minute)})
	}))
	defer server.Close()
	path, before := savedFile(t, expiring(server.URL))
	// The lock file must exist before the directory turns read-only.
	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if _, err := FreshCredentials(context.Background(), path); !errors.Is(err, ErrRenewalPostponed) {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("credentials file changed although the save failed")
	}
}
