package devicekey

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Pairing offers (docs/iphone/onboarding.md, Interfaces; relay/docs/
// protocol.md, Pairing offers): a host shows a pairing code with a
// one-time secret, the controller that scanned it proves the secret in
// devices.request and is approved at once.

// PairingPrefix starts every pairing code.
const PairingPrefix = "HESPER1:"

// OfferSecretSize is the length of an offer's secret in bytes.
const OfferSecretSize = 16

// PairingCode is the JSON inside a pairing code, in the code's field order.
type PairingCode struct {
	V       int      `json:"v"`
	Relay   string   `json:"relay"`   // the relay's origin
	Machine string   `json:"machine"` // the host's relay device ID
	Name    string   `json:"name"`    // the host's name
	HostKey string   `json:"hostKey"` // base64 X25519 transfer key
	Rights  []string `json:"rights"`
	Offer   string   `json:"offer"`   // the secret, base64url without padding
	Expires int64    `json:"expires"` // Unix seconds
}

// Text is the code as shown: PairingPrefix and base64url (no padding) of
// the compact JSON.
func (p PairingCode) Text() string {
	data, _ := json.Marshal(p)
	return PairingPrefix + base64.RawURLEncoding.EncodeToString(data)
}

// Secret is the offer's secret.
func (p PairingCode) Secret() ([]byte, error) {
	secret, err := base64.RawURLEncoding.Strict().DecodeString(p.Offer)
	if err != nil || len(secret) != OfferSecretSize {
		return nil, errors.New("the offer is not 16 bytes of base64url")
	}
	return secret, nil
}

// ErrNotPairingCode means the text is not a Hesper pairing code.
var ErrNotPairingCode = errors.New("not a Hesper pairing code")

// ParsePairingCode reads a pairing code. Anything but version 1 with every
// field is not one.
func ParsePairingCode(text string) (PairingCode, error) {
	var p PairingCode
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), PairingPrefix)
	if !ok {
		return p, ErrNotPairingCode
	}
	data, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || json.Unmarshal(data, &p) != nil || p.V != 1 {
		return PairingCode{}, ErrNotPairingCode
	}
	key, err := base64.StdEncoding.Strict().DecodeString(p.HostKey)
	if p.Relay == "" || p.Machine == "" || p.Name == "" || err != nil || len(key) != 32 || len(p.Rights) == 0 || p.Expires <= 0 {
		return PairingCode{}, ErrNotPairingCode
	}
	if _, err := p.Secret(); err != nil {
		return PairingCode{}, ErrNotPairingCode
	}
	return p, nil
}

// OfferProof is devices.request's offerProof: standard base64 of
// HMAC-SHA256 keyed with the offer's secret over "hesper-offer-v1", the
// host's machine ID and the request's name, key, strongKey and e2eKey
// (empty without one), joined by "\n". The secret itself never travels,
// so a relay that sees the request cannot use it for keys of its own.
func OfferProof(secret []byte, machine, name, key, strongKey, e2eKey string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strings.Join([]string{"hesper-offer-v1", machine, name, key, strongKey, e2eKey}, "\n")))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
