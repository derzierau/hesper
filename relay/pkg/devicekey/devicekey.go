// Package devicekey signs and verifies controller requests with device keys
// (docs/remote-shell-contract.md, Part K).
//
// A controller has two P-256 keys: the device key signs ordinary mutating
// requests, the strong key (Touch ID on a Mac) signs shell.* requests. On
// macOS both live in the Secure Enclave behind the hesper-keys helper;
// elsewhere, and with HESPER_KEYS_SOFTWARE=1, they are software keys in
// files. Hosts keep the public keys of approved controllers and verify every
// request but snapshot.
//
// The signed message (one line each, "\n" separated, no trailing newline):
//
//	ghosty-req-v1
//	<host machine ID>
//	<method>
//	<ts: Unix milliseconds, decimal>
//	<nonce: 16 random bytes, standard base64 with padding>
//	<SHA-256 of the params bytes, lowercase hex>
//
// The signature is ECDSA P-256 over SHA-256(message), DER encoded and sent as
// standard base64. The params bytes are the request's params in the form
// encoding/json writes a json.RawMessage: compact, with <, >, &, U+2028 and
// U+2029 escaped (NormalizeParams). Senders sign and send that form; the
// relay re-encodes messages, which leaves it unchanged, and hosts normalize
// again before hashing, so a relay can neither change params nor break a
// signature by re-encoding them.
package devicekey

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Label starts every signed request message.
// wire name: kept as "ghosty-req-v1" until the next relay deploy.
const Label = "ghosty-req-v1"

// NonceBytes is the size of a request nonce.
const NonceBytes = 16

// MaxSignature bounds a DER P-256 signature (72 bytes) in base64.
const maxSignature = 128

// ErrInvalid is wrapped by every format error.
var ErrInvalid = errors.New("invalid device-key data")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// EncodePublicKey returns the standard base64 of the key's DER
// SubjectPublicKeyInfo, the form hesper-keys prints and controllers.json keeps.
func EncodePublicKey(key *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// ParsePublicKey accepts only a canonical encoding of a P-256 public key:
// standard base64 that re-encodes to the same text, of a DER
// SubjectPublicKeyInfo that re-marshals to the same bytes.
func ParsePublicKey(value string) (*ecdsa.PublicKey, []byte, error) {
	if len(value) == 0 || len(value) > 256 {
		return nil, nil, invalid("public key has an invalid length")
	}
	der, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || base64.StdEncoding.EncodeToString(der) != value {
		return nil, nil, invalid("public key is not canonical base64")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, nil, invalid("public key is not a SubjectPublicKeyInfo")
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, nil, invalid("public key is not ECDSA P-256")
	}
	again, err := x509.MarshalPKIXPublicKey(key)
	if err != nil || !bytes.Equal(again, der) {
		return nil, nil, invalid("public key is not canonical DER")
	}
	return key, der, nil
}

// NormalizeParams validates params and returns the bytes that are signed and
// sent: compact JSON with HTML-sensitive characters escaped, as encoding/json
// emits a json.RawMessage. Params must be one JSON value, valid UTF-8, with
// no object that repeats a key (parsers disagree on which one wins).
// Empty params are "{}".
func NormalizeParams(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	if len(raw) > protocol.MaxMessageBytes {
		return nil, invalid("params are too large")
	}
	if !utf8.Valid(raw) {
		return nil, invalid("params are not valid UTF-8")
	}
	if err := checkUnique(raw); err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, invalid("params are not JSON")
	}
	var out bytes.Buffer
	json.HTMLEscape(&out, compact.Bytes())
	return out.Bytes(), nil
}

// checkUnique fails on invalid JSON, trailing data or a repeated object key.
func checkUnique(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(depth int) error
	walk = func(depth int) error {
		if depth > 64 {
			return invalid("params are nested too deeply")
		}
		token, err := d.Token()
		if err != nil {
			return invalid("params are not JSON")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return invalid("params are not JSON")
				}
				name, ok := key.(string)
				if !ok {
					return invalid("params are not JSON")
				}
				if seen[name] {
					return invalid("params repeat the key %q", name)
				}
				seen[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return invalid("params are not JSON")
		}
		if _, err := d.Token(); err != nil {
			return invalid("params are not JSON")
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid("params hold more than one JSON value")
	}
	return nil
}

func validField(value string) bool {
	return protocol.ValidID(value) && utf8.ValidString(value) && !strings.ContainsAny(value, "\n\r\x00")
}

// Message is the exact text that is hashed and signed. params must already
// be normalized (NormalizeParams).
func Message(machineID, method string, ts int64, nonce string, params []byte) ([]byte, error) {
	if !validField(machineID) || !validField(method) {
		return nil, invalid("machine ID and method must be 1 to 128 characters without line breaks")
	}
	if ts <= 0 {
		return nil, invalid("timestamp must be positive")
	}
	if _, err := decodeNonce(nonce); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(params)
	return []byte(Label + "\n" + machineID + "\n" + method + "\n" + strconv.FormatInt(ts, 10) + "\n" + nonce + "\n" + hex.EncodeToString(sum[:])), nil
}

// Digest is SHA-256 of Message: what the device key signs.
func Digest(machineID, method string, ts int64, nonce string, params []byte) ([32]byte, error) {
	message, err := Message(machineID, method, ts, nonce, params)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(message), nil
}

func decodeNonce(nonce string) ([]byte, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(nonce)
	if err != nil || len(raw) != NonceBytes || base64.StdEncoding.EncodeToString(raw) != nonce {
		return nil, invalid("nonce must be %d bytes in canonical standard base64", NonceBytes)
	}
	return raw, nil
}

// NewNonce returns a fresh random nonce.
func NewNonce() (string, error) {
	var raw [NonceBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw[:]), nil
}

// Options select the key and the Touch ID prompt for one signature.
type Options struct {
	Strong bool
	Reason string // shown by Touch ID (strong key on a Mac only)
}

// SignRequest signs a request for machineID with the signer's device (or,
// with opts.Strong, strong) key. params must be normalized; they are the
// bytes to send.
func SignRequest(s Signer, device, machineID, method string, params []byte, opts Options, now time.Time) (*protocol.Auth, error) {
	if !validField(device) {
		return nil, invalid("device ID is invalid")
	}
	nonce, err := NewNonce()
	if err != nil {
		return nil, err
	}
	ts := now.UnixMilli()
	digest, err := Digest(machineID, method, ts, nonce, params)
	if err != nil {
		return nil, err
	}
	sig, err := s.Sign(digest[:], opts)
	if err != nil {
		return nil, err
	}
	return &protocol.Auth{Device: device, TS: ts, Nonce: nonce, Sig: base64.StdEncoding.EncodeToString(sig)}, nil
}

// ParsedAuth is a strictly validated protocol.Auth.
type ParsedAuth struct {
	Device string
	TS     int64
	Nonce  string
	Sig    []byte
}

// ParseAuth validates every field's format; it does not verify the signature.
func ParseAuth(a *protocol.Auth) (ParsedAuth, error) {
	if a == nil {
		return ParsedAuth{}, invalid("request is not signed")
	}
	if !validField(a.Device) {
		return ParsedAuth{}, invalid("auth.device is invalid")
	}
	if a.TS <= 0 {
		return ParsedAuth{}, invalid("auth.ts is invalid")
	}
	if _, err := decodeNonce(a.Nonce); err != nil {
		return ParsedAuth{}, err
	}
	if len(a.Sig) == 0 || len(a.Sig) > maxSignature {
		return ParsedAuth{}, invalid("auth.sig has an invalid length")
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(a.Sig)
	if err != nil || base64.StdEncoding.EncodeToString(sig) != a.Sig {
		return ParsedAuth{}, invalid("auth.sig is not canonical base64")
	}
	return ParsedAuth{Device: a.Device, TS: a.TS, Nonce: a.Nonce, Sig: sig}, nil
}

// Verify checks the signature of a request for machineID with key. params
// are the bytes as received; they are normalized first. Time window and
// replay are the host's checks.
func Verify(key *ecdsa.PublicKey, machineID, method string, params []byte, auth ParsedAuth) error {
	normalized, err := NormalizeParams(params)
	if err != nil {
		return err
	}
	digest, err := Digest(machineID, method, auth.TS, auth.Nonce, normalized)
	if err != nil {
		return err
	}
	if key == nil || !ecdsa.VerifyASN1(key, digest[:], auth.Sig) {
		return errors.New("signature does not verify")
	}
	return nil
}
