// Package auth owns human identity, access policy, device approval, and sessions.
// Providers establish identity; only the relay policy grants access to machines.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/derzierau/hesper/relay/internal/identity"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

type Principal struct {
	ID    string `json:"id"`
	Login string `json:"login"`
}
type Enrollment struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Role      identity.Role `json:"role"`
	Challenge string        `json:"challenge"`
	Callback  string        `json:"callback,omitempty"`
	Principal Principal     `json:"principal"`
	Expires   time.Time     `json:"expires"`
	Claimed   bool          `json:"claimed"`
}
type Flow struct {
	State        string    `json:"state"`
	EnrollmentID string    `json:"enrollmentId"`
	Verifier     string    `json:"verifier"`
	Principal    Principal `json:"principal"`
	Expires      time.Time `json:"expires"`
}

// RefreshGrace bounds how long after a rotation the same device may present
// the consumed refresh credential again (with the access token issued alongside
// it) and receive the identical successor, e.g. after the response was lost.
const RefreshGrace = 60 * time.Second

type ReplayError struct{ DeviceID string }

func (e *ReplayError) Error() string { return "refresh credential was reused" }

type Repository interface {
	CreateEnrollment(context.Context, Enrollment) error
	Enrollment(context.Context, string) (Enrollment, error)
	PutFlow(context.Context, Flow) error
	TakeFlow(context.Context, string) (Flow, error)
	Approve(context.Context, string, Principal) error
	Claim(context.Context, string, string, time.Duration, time.Duration) (identity.Device, protocol.Credentials, error)
	// Rotate consumes a refresh credential once and issues its single
	// successor. proof is the access token issued with that refresh credential
	// (or ""). Re-presenting a consumed refresh credential with its proof
	// within RefreshGrace, while the successor is still unused, returns the
	// same successor (a lost response); any other reuse revokes the device.
	Rotate(ctx context.Context, refresh, proof string, accessTTL time.Duration) (identity.Device, protocol.Credentials, error)
	PruneAuth(context.Context) error
}
type IdentityProvider interface {
	AuthorizationURL(state, verifier string) string
	Identify(context.Context, string, string) (Principal, error)
}
type AccessPolicy interface{ Allowed(string) bool }

func Challenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
