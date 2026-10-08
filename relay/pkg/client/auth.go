package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"golang.org/x/sys/unix"
)

type Enrollment struct {
	ID              string    `json:"id"`
	Code            string    `json:"code"`
	VerificationURL string    `json:"verificationURL"`
	Expires         time.Time `json:"expires"`
	Interval        int       `json:"interval"`
}

func authPost(ctx context.Context, origin, path string, body, result any) error {
	return authPostBearer(ctx, origin, path, "", body, result)
}

// statusError is a non-success HTTP answer. It unwraps to the relay's
// protocol error when the body carried one, so errors.As keeps finding it.
type statusError struct {
	status int
	fault  *protocol.Error
}

func (e *statusError) Error() string {
	if e.fault != nil {
		return fmt.Sprintf("%s (HTTP %d)", e.fault.Error(), e.status)
	}
	return fmt.Sprintf("authentication failed (HTTP %d)", e.status)
}
func (e *statusError) Unwrap() error {
	if e.fault == nil {
		return nil
	}
	return e.fault
}

// authHTTP builds the client for authentication requests; tests replace it
// to inject transport failures.
var authHTTP = func() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func authPostBearer(ctx context.Context, origin, path, bearer string, body, result any) error {
	u, err := Origin(origin)
	if err != nil {
		return err
	}
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(protocol.JSON(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := authHTTP().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 && res.StatusCode != 201 {
		var fault protocol.Error
		if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&fault) == nil && fault.Code != "" {
			return &statusError{status: res.StatusCode, fault: &fault}
		}
		return &statusError{status: res.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(result)
}

// BeginLogin holds the proof on the device; only its SHA-256 challenge leaves it.
// Native apps open VerificationURL in their system authentication browser.
func BeginLogin(ctx context.Context, origin, name, role, callback string) (Enrollment, string, error) {
	var e Enrollment
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return e, "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(verifier))
	err := authPost(ctx, origin, "/v1/auth/enroll", map[string]string{"name": name, "role": role, "callback": callback, "challenge": base64.RawURLEncoding.EncodeToString(hash[:])}, &e)
	return e, verifier, err
}
func ClaimLogin(ctx context.Context, origin, id, verifier string) (protocol.Credentials, error) {
	var c protocol.Credentials
	err := authPost(ctx, origin, "/v1/auth/claim", map[string]string{"id": id, "verifier": verifier}, &c)
	c.Relay = origin
	return c, err
}

// Refresh rotates c's refresh credential once. It sends the access token issued
// with that refresh credential as proof for the relay's lost-response grace:
// re-presenting a just-rotated refresh credential with that proof, shortly
// after, returns the same successor pair instead of counting as reuse.
// Persist the result before using it; FreshCredentials does both and
// classifies failures.
func Refresh(ctx context.Context, c protocol.Credentials) (protocol.Credentials, error) {
	var next protocol.Credentials
	if c.RefreshToken == "" {
		return next, fmt.Errorf("no refresh credential")
	}
	if err := authPostBearer(ctx, c.Relay, "/v1/auth/refresh", c.Token, map[string]string{"refreshToken": c.RefreshToken}, &next); err != nil {
		return protocol.Credentials{}, err
	}
	next.Relay = c.Relay
	if !protocol.ValidID(next.Token) || !protocol.ValidID(next.RefreshToken) || next.DeviceID != c.DeviceID || next.Role != c.Role {
		return protocol.Credentials{}, fmt.Errorf("relay returned an incomplete credential")
	}
	return next, nil
}

// RenewalRejected reports whether err is the relay's explicit refusal of a
// refresh credential: 401 "unauthorized" (invalid, expired, reused or revoked)
// or 403 "not_allowed" (owner removed from the allowlist), each with the
// relay's JSON error body. Nothing else proves the credential is dead:
// transport errors, timeouts, 429, 5xx, proxy pages and malformed answers
// are all retried with the same credential.
func RenewalRejected(err error) bool {
	var s *statusError
	if !errors.As(err, &s) || s.fault == nil {
		return false
	}
	return (s.status == 401 && s.fault.Code == "unauthorized") || (s.status == 403 && s.fault.Code == "not_allowed")
}

// ErrRenewalPostponed means renewal failed without the relay rejecting the
// credential (network, timeout, 5xx, 429, malformed answer, failed save). The
// credentials file is unchanged; retry later. A retry shortly after a lost
// response gets the same successor pair from the relay's grace window.
var ErrRenewalPostponed = errors.New("credential renewal postponed; kept the refresh credential to retry")

// ErrRenewalNotSent is the former name of ErrRenewalPostponed.
var ErrRenewalNotSent = ErrRenewalPostponed

// ErrSignInRequired matches (errors.Is) every SignInRequiredError.
var ErrSignInRequired = errors.New("sign in again")

// SignInRequiredError means the relay refused the device's credential; only a
// new browser login helps. Its message names the exact command to run.
type SignInRequiredError struct {
	Path  string
	Relay string
	Role  string
	Err   error
}

func (e *SignInRequiredError) Error() string {
	role, name := "host", `"$(hostname -s)"`
	if e.Role != "host" {
		role, name = "controller", `"$(hostname -s) controller"`
	}
	relay := e.Relay
	if relay == "" {
		relay = "https://relay.olezierau.de"
	}
	path := shellQuote(e.Path)
	return fmt.Sprintf("the relay rejected this device's credential (%v); sign in again: rm %s && hesperctl login --relay %s --role %s --name %s --out %s", e.Err, path, shellQuote(relay), role, name, path)
}
func (e *SignInRequiredError) Unwrap() error        { return e.Err }
func (e *SignInRequiredError) Is(target error) bool { return target == ErrSignInRequired }

// SignInRequired wraps cause for the credentials file at path.
func SignInRequired(path string, c protocol.Credentials, cause error) error {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return &SignInRequiredError{Path: path, Relay: c.Relay, Role: c.Role, Err: cause}
}

func shellQuote(s string) string {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-:+@=,", r)) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

// FreshCredentials serializes renewal across processes, rereads under the
// lock, and atomically replaces the protected file. The lock file stays.
//
// The file is replaced only by a successfully renewed pair, durably (temp
// file, fsync, rename, directory fsync). Every failure leaves it as it was:
// transient failures return ErrRenewalPostponed together with the stored
// credentials, and an explicit relay rejection returns a SignInRequiredError.
func FreshCredentials(ctx context.Context, path string) (protocol.Credentials, error) {
	var c protocol.Credentials
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return c, err
	}
	defer lock.Close()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return c, err
		}
		select {
		case <-ctx.Done():
			return c, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	c, err = LoadCredentials(path)
	if err != nil {
		return c, err
	}
	if c.ExpiresAt.IsZero() || time.Until(c.ExpiresAt) > time.Minute {
		return c, nil
	}
	if c.RefreshToken == "" {
		return c, SignInRequired(path, c, errors.New("no refresh credential"))
	}
	next, err := Refresh(ctx, c)
	if err != nil {
		if RenewalRejected(err) {
			return c, SignInRequired(path, c, err)
		}
		return c, fmt.Errorf("%w: %v", ErrRenewalPostponed, err)
	}
	if err = replaceCredentials(path, next); err != nil {
		// The relay already rotated. The old pair stays on disk; presenting it
		// again within the relay's grace window returns this same pair.
		return c, fmt.Errorf("%w: renewed credential could not be saved: %v", ErrRenewalPostponed, err)
	}
	return next, nil
}
func replaceCredentials(path string, c protocol.Credentials) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hesper-credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = json.NewEncoder(f).Encode(c); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
