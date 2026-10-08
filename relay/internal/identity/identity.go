// Package identity owns enrollment and device authorization, independent of storage.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

type Role string

const Host Role = "host"
const Controller Role = "controller"

func (r Role) Valid() bool { return r == Host || r == Controller }

type Device struct {
	ID                  string    `json:"id"`
	Owner               string    `json:"owner"`
	Role                Role      `json:"role"`
	Name                string    `json:"name"`
	Revoked             bool      `json:"revoked"`
	CredentialExpiresAt time.Time `json:"-"`
}
type Invitation struct {
	Owner   string    `json:"owner"`
	Role    Role      `json:"role"`
	Expires time.Time `json:"expires"`
}
type Repository interface {
	Invite(context.Context, Invitation) (string, error)
	Redeem(context.Context, string, string) (Device, string, error)
	Authenticate(context.Context, string) (Device, error)
	Devices(context.Context, string) ([]Device, error)
	Revoke(context.Context, string) error
}

func Secret() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:])
}
func Hash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
