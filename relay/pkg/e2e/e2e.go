// Package e2e is the end-to-end channel between a controller and a host
// (docs/remote-shell-contract.md, Part N): the relay forwards it as opaque
// ciphertext and can neither read nor alter what travels inside.
//
// Handshake: Noise_IK_25519_ChaChaPoly_BLAKE2s (github.com/flynn/noise).
// The controller (initiator) knows the host's static X25519 key in advance:
// the host's transfer key, pinned in trusted-hosts.json when the controller
// first met the host and covered by part K's approval code. The host learns
// the controller's static key from the first message and accepts it only
// with a binding signature by the controller's approved P-256 device key
// (controllers.json). The prologue binds the session to the host's machine
// ID and the controller's device ID as the relay routes them.
//
// Transport: every request after the handshake is one relay request with
// method "e2e" whose params carry the session ID, an explicit counter and
// the ChaCha20-Poly1305 ciphertext of the inner request (method, params,
// part K signature); the host answers with the ciphertext of the inner
// result. Counters are explicit because the relay may drop a request (busy,
// timeout) and requests may be answered out of order; each side keeps a
// sliding replay window (as WireGuard does), so a frame is accepted at most
// once and never after it fell out of the window.
//
// Terminal streams opened through the channel get their own keys (Stream).
package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/flynn/noise"
)

const (
	// Version of the channel; both handshake payloads carry it.
	Version = 1
	// Name is the Noise protocol name.
	Name = "Noise_IK_25519_ChaChaPoly_BLAKE2s"
	// HelloMethod is the relay request that carries the handshake.
	HelloMethod = "e2e.hello"
	// FrameMethod is the relay request that carries one inner request.
	FrameMethod = "e2e"
	// Window is the replay window: how far behind the newest counter a frame
	// may arrive.
	Window = 1024
	// MaxSkew bounds the initiator's clock in the first message.
	MaxSkew = 2 * time.Minute
)

// Error codes the host answers in plaintext (outside the channel).
const (
	// CodeSession: the host has no such session (restarted, expired). The
	// frame was not decrypted, so nothing ran; the controller handshakes
	// again and resends.
	CodeSession = "e2e_session"
	// CodeIntegrity: a frame failed authentication (altered on the way, or
	// a replay); nothing ran.
	CodeIntegrity = "integrity"
	// CodeBinding: the controller's static key is not bound to its approved
	// device key.
	CodeBinding = "e2e_binding"
	// CodeRequired: plaintext refused (the host or the controller requires
	// the channel).
	CodeRequired = "e2e_required"
)

var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// Prologue binds a handshake to the host's machine ID and the controller's
// relay device ID.
func Prologue(machineID, controllerID string) []byte {
	// wire name: kept as "ghosty-e2e-v1" until the next relay deploy.
	return []byte("ghosty-e2e-v1\x00" + machineID + "\x00" + controllerID)
}

// BindingDigest is what a controller's device key signs to bind its static
// X25519 key: SHA-256 of "ghosty-e2e-bind-v1\n" + base64(static).
func BindingDigest(static []byte) [32]byte {
	// wire name: kept as "ghosty-e2e-bind-v1" until the next relay deploy.
	return sha256.Sum256([]byte("ghosty-e2e-bind-v1\n" + base64.StdEncoding.EncodeToString(static)))
}

// Hello is the params of e2e.hello: the first handshake message.
type Hello struct {
	V int    `json:"v"`
	H []byte `json:"h"`
}

// HelloResult is its result: the second handshake message.
type HelloResult struct {
	H []byte `json:"h"`
}

// Frame is the params of an e2e request.
type Frame struct {
	S string `json:"s"` // session ID
	N uint64 `json:"n"` // counter (the AEAD nonce)
	C []byte `json:"c"` // ciphertext
}

// FrameResult is the result of an e2e request.
type FrameResult struct {
	N uint64 `json:"n"`
	C []byte `json:"c"`
}

// Request is the plaintext of a frame: one host request as the controller
// signed it (part K).
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Auth   *protocol.Auth  `json:"auth,omitempty"`
}

// Response is the plaintext of a frame result. ID repeats the request's,
// so a relay cannot swap answers between requests.
type Response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *protocol.Error `json:"error,omitempty"`
}

// InitPayload travels encrypted in the first message.
type InitPayload struct {
	V  int   `json:"v"`
	TS int64 `json:"ts"` // Unix milliseconds
	// Binding is the base64 DER signature of BindingDigest(static) by the
	// controller's device key; empty without device keys.
	Binding string `json:"binding,omitempty"`
}

// RespPayload travels encrypted in the second message.
type RespPayload struct {
	V       int    `json:"v"`
	Session string `json:"session"`
	// Require: the host refuses plaintext for everything but observing.
	Require bool `json:"require,omitempty"`
}

// NewID returns 16 random bytes in hex.
func NewID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Session is an established channel. Seal encrypts toward the peer, Open
// decrypts from it; both are safe for concurrent use.
type Session struct {
	ID      string
	Peer    []byte // the peer's static key
	Created time.Time

	mu     sync.Mutex
	send   noise.Cipher
	recv   noise.Cipher
	next   uint64
	window replay
	ad     []byte
}

func newSession(id string, send, recv *noise.CipherState, peer []byte, now time.Time) *Session {
	// wire name: kept as "ghosty-e2e-v1" until the next relay deploy.
	return &Session{ID: id, Peer: bytes.Clone(peer), Created: now, send: send.Cipher(), recv: recv.Cipher(), ad: []byte("ghosty-e2e-v1\x00" + id)}
}

// Sent is how many frames this side sealed.
func (s *Session) Sent() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// Seal encrypts plaintext with the next counter.
func (s *Session) Seal(plaintext []byte) (uint64, []byte) {
	s.mu.Lock()
	n := s.next
	s.next++
	s.mu.Unlock()
	return n, s.send.Encrypt(nil, n, s.ad, plaintext)
}

// ErrReplay means a frame's counter was seen or fell out of the window.
var ErrReplay = errors.New("e2e: replayed or too old frame")

// ErrAuth means a frame did not authenticate.
var ErrAuth = errors.New("e2e: frame failed authentication")

// Open authenticates and decrypts a frame and records its counter.
func (s *Session) Open(n uint64, ciphertext []byte) ([]byte, error) {
	if n >= noise.MaxNonce {
		return nil, ErrReplay
	}
	s.mu.Lock()
	ok := s.window.fresh(n)
	s.mu.Unlock()
	if !ok {
		return nil, ErrReplay
	}
	plaintext, err := s.recv.Decrypt(nil, n, s.ad, ciphertext)
	if err != nil {
		return nil, ErrAuth
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Two goroutines may have decrypted the same counter at once.
	if !s.window.fresh(n) {
		return nil, ErrReplay
	}
	s.window.mark(n)
	return plaintext, nil
}

// replay is a sliding window of Window counters below the newest one.
type replay struct {
	started bool
	max     uint64
	bits    [Window / 64]uint64
}

func (w *replay) fresh(n uint64) bool {
	if !w.started || n > w.max {
		return true
	}
	if w.max-n >= Window {
		return false
	}
	return w.bits[(n%Window)/64]&(1<<(n%64)) == 0
}

func (w *replay) mark(n uint64) {
	if !w.started {
		w.started, w.max = true, n
		w.bits = [Window / 64]uint64{}
	} else if n > w.max {
		if n-w.max >= Window {
			w.bits = [Window / 64]uint64{}
		} else {
			for i := w.max + 1; i <= n; i++ {
				w.bits[(i%Window)/64] &^= 1 << (i % 64)
			}
		}
		w.max = n
	}
	w.bits[(n%Window)/64] |= 1 << (n % 64)
}

// Initiator is a controller's handshake in progress.
type Initiator struct {
	hs   *noise.HandshakeState
	host []byte
}

// Initiate starts a handshake with the host whose static key is hostStatic
// and returns the first message.
func Initiate(id *Identity, hostStatic []byte, machineID, controllerID string, now time.Time) (*Initiator, []byte, error) {
	if len(hostStatic) != 32 {
		return nil, nil, errors.New("e2e: host key must be 32 bytes")
	}
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeIK, Initiator: true,
		Prologue: Prologue(machineID, controllerID), StaticKeypair: noise.DHKey{Private: id.Private, Public: id.Public}, PeerStatic: hostStatic})
	if err != nil {
		return nil, nil, err
	}
	payload, _ := json.Marshal(InitPayload{V: Version, TS: now.UnixMilli(), Binding: id.Binding})
	msg, _, _, err := hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, err
	}
	return &Initiator{hs: hs, host: bytes.Clone(hostStatic)}, msg, nil
}

// Finish reads the host's answer. A wrong host key (or any change on the
// way) fails here: only the holder of the pinned key can answer.
func (i *Initiator) Finish(msg []byte, now time.Time) (*Session, RespPayload, error) {
	var p RespPayload
	plain, send, recv, err := i.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, p, fmt.Errorf("e2e: the host's handshake answer does not verify against its pinned key")
	}
	if send == nil || recv == nil {
		return nil, p, errors.New("e2e: handshake incomplete")
	}
	if err := json.Unmarshal(plain, &p); err != nil || p.V != Version || !validSession(p.Session) {
		return nil, p, errors.New("e2e: invalid handshake answer")
	}
	return newSession(p.Session, send, recv, i.host, now), p, nil
}

func validSession(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// ValidSession reports whether id has the form of a session ID.
func ValidSession(id string) bool { return validSession(id) }

// Responder is a host's handshake after reading the first message.
type Responder struct {
	hs *noise.HandshakeState
	// PeerStatic is the controller's static key; Payload its first payload.
	PeerStatic []byte
	Payload    InitPayload
	// Ephemeral is the controller's ephemeral key: a replayed first message
	// repeats it.
	Ephemeral []byte
}

// Respond reads a controller's first message with the host's static key.
func Respond(private, public []byte, msg []byte, machineID, controllerID string, now time.Time) (*Responder, error) {
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeIK, Initiator: false,
		Prologue: Prologue(machineID, controllerID), StaticKeypair: noise.DHKey{Private: private, Public: public}})
	if err != nil {
		return nil, err
	}
	plain, _, _, err := hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, errors.New("e2e: handshake does not verify")
	}
	var p InitPayload
	if err := json.Unmarshal(plain, &p); err != nil || p.V != Version {
		return nil, errors.New("e2e: invalid handshake payload")
	}
	if skew := now.Sub(time.UnixMilli(p.TS)); skew > MaxSkew || skew < -MaxSkew {
		return nil, errors.New("e2e: handshake timestamp outside ±2 minutes of the host's clock")
	}
	return &Responder{hs: hs, PeerStatic: bytes.Clone(hs.PeerStatic()), Payload: p, Ephemeral: bytes.Clone(hs.PeerEphemeral())}, nil
}

// Accept answers the handshake and returns the host's session.
func (r *Responder) Accept(sessionID string, require bool, now time.Time) (*Session, []byte, error) {
	payload, _ := json.Marshal(RespPayload{V: Version, Session: sessionID, Require: require})
	msg, recv, send, err := r.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, err
	}
	if send == nil || recv == nil {
		return nil, nil, errors.New("e2e: handshake incomplete")
	}
	return newSession(sessionID, send, recv, r.PeerStatic, now), msg, nil
}
