package e2e

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"

	"github.com/flynn/noise"
)

// Terminal streams opened through the channel are encrypted end to end too.
// The host draws a 32-byte secret for each stream and returns it inside the
// channel (terminal.open's result, field "e2e"); both sides derive one key
// per direction with HKDF-SHA256 (salt: the stream ID, info:
// "ghosty-stream-v1 host" or "ghosty-stream-v1 controller") and seal every
// relay frame with ChaCha20-Poly1305 (Noise's cipher function). The
// counter is implicit: frames arrive in order on one WebSocket, so any
// dropped, repeated, reordered or altered frame fails and ends the stream.
//
// Frame plaintext: one type byte, then the payload: StreamData (terminal
// bytes) or StreamResize (columns and rows, two big-endian uint16 each).
// Every frame is a binary WebSocket message; the relay forwards it as it
// forwards plaintext terminal bytes.

const (
	StreamData   byte = 0
	StreamResize byte = 1
	// StreamOverhead is the type byte plus the authentication tag.
	StreamOverhead = 1 + 16
	// StreamSecretBytes is the size of a stream's secret.
	StreamSecretBytes = 32
)

// NewStreamSecret returns a fresh stream secret.
func NewStreamSecret() []byte {
	secret := make([]byte, StreamSecretBytes)
	rand.Read(secret)
	return secret
}

// Stream seals and opens one terminal stream's frames. Seal and Open may
// run concurrently with each other; callers serialize their own direction
// (the order of Seal calls must be the order frames are written).
type Stream struct {
	sendMu, recvMu sync.Mutex
	send, recv     noise.Cipher
	sent, got      uint64
	ad             []byte
}

// NewStream derives a stream's keys; host is true on the host's side.
func NewStream(secret []byte, streamID string, host bool) (*Stream, error) {
	if len(secret) != StreamSecretBytes || streamID == "" {
		return nil, errors.New("e2e: invalid stream secret")
	}
	// wire names: kept as "ghosty-stream-v1 …" until the next relay deploy.
	key := func(side string) (noise.Cipher, error) {
		k, err := hkdf.Key(sha256.New, secret, []byte(streamID), "ghosty-stream-v1 "+side, 32)
		if err != nil {
			return nil, err
		}
		return noise.CipherChaChaPoly.Cipher([32]byte(k)), nil
	}
	fromHost, err := key("host")
	if err != nil {
		return nil, err
	}
	fromController, err := key("controller")
	if err != nil {
		return nil, err
	}
	s := &Stream{ad: []byte("ghosty-stream-v1\x00" + streamID)}
	if host {
		s.send, s.recv = fromHost, fromController
	} else {
		s.send, s.recv = fromController, fromHost
	}
	return s, nil
}

// Seal encrypts one frame of the given type.
func (s *Stream) Seal(kind byte, payload []byte) []byte {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	// One allocation: the plaintext is laid out in the frame's buffer and
	// encrypted in place.
	buf := make([]byte, 1+len(payload), 1+len(payload)+16)
	buf[0] = kind
	copy(buf[1:], payload)
	out := s.send.Encrypt(buf[:0], s.sent, s.ad, buf)
	s.sent++
	return out
}

// ErrStream means a stream frame did not authenticate or came out of order.
var ErrStream = errors.New("e2e: terminal frame failed authentication (altered, dropped or replayed on the way)")

// Open decrypts the next frame.
func (s *Stream) Open(frame []byte) (byte, []byte, error) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	plain, err := s.recv.Decrypt(nil, s.got, s.ad, frame)
	if err != nil || len(plain) == 0 {
		return 0, nil, ErrStream
	}
	s.got++
	return plain[0], plain[1:], nil
}

// ResizePayload encodes a resize.
func ResizePayload(columns, rows uint16) []byte {
	var b [4]byte
	binary.BigEndian.PutUint16(b[:2], columns)
	binary.BigEndian.PutUint16(b[2:], rows)
	return b[:]
}

// ParseResize decodes a resize payload.
func ParseResize(payload []byte) (columns, rows uint16, err error) {
	if len(payload) != 4 {
		return 0, 0, errors.New("e2e: invalid resize frame")
	}
	return binary.BigEndian.Uint16(payload[:2]), binary.BigEndian.Uint16(payload[2:]), nil
}
