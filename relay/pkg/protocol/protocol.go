// Package protocol defines the versioned wire contract shared by hosts and clients.
// The relay routes opaque RPC payloads; session-provider details live on the host.
package protocol

import (
	"encoding/json"
	"errors"
	"time"
)

const Version = 2
const MaxMessageBytes = 1 << 20

// wire name: kept as "ghosty.v2" until the next relay deploy.
const Subprotocol = "ghosty.v2"

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string        { return e.Code + ": " + e.Message }
func Err(code, message string) *Error { return &Error{code, message} }
func PublicError(err error) *Error {
	var p *Error
	if errors.As(err, &p) {
		return p
	}
	return Err("internal", "Operation failed")
}

type Message struct {
	ControllerID string          `json:"controllerId,omitempty"`
	Version      int             `json:"v"`
	Type         string          `json:"type"`
	ID           string          `json:"id,omitempty"`
	MachineID    string          `json:"machineId,omitempty"`
	Method       string          `json:"method,omitempty"`
	Deadline     int64           `json:"deadline,omitempty"`
	Params       json.RawMessage `json:"params,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        *Error          `json:"error,omitempty"`
	Removed      bool            `json:"removed,omitempty"`
	Machines     []Machine       `json:"machines,omitempty"`
	// Auth is the controller's device-key signature of a request
	// (pkg/devicekey). The relay forwards it unchanged; hosts verify it.
	Auth *Auth `json:"auth,omitempty"`
	// E2E marks, inside one process, a result that traveled through the
	// end-to-end channel (pkg/e2e). It is never sent.
	E2E bool `json:"-"`
	// Route ("direct" or "relay") and DirectAddr (the direct connection's
	// address) say, inside one process, how a fleet sync forwarded a
	// request. Never sent.
	Route      string `json:"-"`
	DirectAddr string `json:"-"`
}

// Auth proves that a request comes from an approved controller device:
// Device is the controller's relay device ID, TS the signing time (Unix
// milliseconds), Nonce 16 random bytes (standard base64) and Sig the
// base64 DER ECDSA P-256 signature (see pkg/devicekey for the message).
type Auth struct {
	Device string `json:"device"`
	TS     int64  `json:"ts"`
	Nonce  string `json:"nonce"`
	Sig    string `json:"sig"`
}
type Machine struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Online   bool            `json:"online"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}
type PairRequest struct {
	Invitation string `json:"invitation"`
	Name       string `json:"name"`
}
type Credentials struct {
	Relay            string    `json:"relay"`
	DeviceID         string    `json:"deviceId"`
	Role             string    `json:"role"`
	Token            string    `json:"token"`
	RefreshToken     string    `json:"refreshToken,omitempty"`
	ExpiresAt        time.Time `json:"expiresAt,omitempty"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt,omitempty"`
}

func JSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
func ValidID(value string) bool      { return len(value) > 0 && len(value) <= 128 }
