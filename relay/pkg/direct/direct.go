// Package direct is the direct path between a controller and a host that
// can reach each other without the relay (the same office or home network):
// a TCP connection secured with the same Noise IK handshake as the
// end-to-end channel (pkg/e2e, contract part N), on which the controller
// sends the same part K-signed requests and terminal streams it would
// otherwise send through the relay. The relay stays the rendezvous: a
// controller learns a host's addresses only inside the end-to-end channel
// (method direct.offer) and falls back to the relay whenever the direct
// path does not work. See docs/direct-path.md.
//
// Wire format. The controller opens a TCP connection and writes the magic
// "GHOSTYD1", then the first Noise message (Noise_IK_25519_ChaChaPoly_BLAKE2s,
// prologue "ghosty-direct-v1\0" + host machine ID) with a two-byte
// big-endian length. Its encrypted payload is Hello. The host answers the
// second Noise message (payload Welcome) the same way, or closes the
// connection without a word. After the handshake every record is a two-byte
// length and a Noise transport message (implicit counters: TCP keeps the
// order; any record that does not authenticate ends the connection). A
// record's plaintext is one flag byte (1: more records follow for this
// message) and a piece of a message; a message is one kind byte and its body.
package direct

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/flynn/noise"
)

const (
	// Version of the direct path; Hello and Welcome carry it.
	Version = 1
	// Magic starts every connection.
	// wire name: kept as "GHOSTYD1" until the next relay deploy.
	Magic = "GHOSTYD1"
	// MaxHandshake bounds each handshake message.
	MaxHandshake = 1024
	// MaxMessage bounds one message (the relay's limit is 1 MiB, so
	// everything that may fall back to the relay fits).
	MaxMessage = 2 << 20
	// maxRecord is the largest Noise transport message.
	maxRecord = 65535
	// maxPiece is the plaintext of one record without its flag byte.
	maxPiece = maxRecord - 16 - 1
	// MaxSkew bounds the controller's clock in Hello.
	MaxSkew = 2 * time.Minute
)

// Message kinds.
const (
	KindRequest  byte = 'q' // controller → host: Request
	KindResponse byte = 'r' // host → controller: e2e.Response
	KindPing     byte = 'p' // either side; the other answers KindPong with the same body
	KindPong     byte = 'o'
	KindData     byte = 'd' // terminal bytes on a stream connection
	KindResize   byte = 'z' // controller → host on a stream: columns, rows (uint16 BE each)
)

var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// Prologue binds a handshake to the host's machine ID and to the direct
// path (a handshake meant for the relay channel does not verify here).
func Prologue(machineID string) []byte {
	return []byte("ghosty-direct-v1\x00" + machineID)
}

// Hello is the controller's first handshake payload (encrypted to the
// host's static key). Device is its relay device ID, Binding the device
// key's signature of its static key (as in pkg/e2e). An RPC connection
// carries Token from a fresh direct.offer; a stream connection carries the
// terminal ticket's ID in Stream instead.
type Hello struct {
	V       int    `json:"v"`
	TS      int64  `json:"ts"`
	Device  string `json:"device"`
	Binding string `json:"binding"`
	Token   []byte `json:"token,omitempty"`
	Stream  string `json:"stream,omitempty"`
}

// Welcome is the host's answer. Require: the host refuses plaintext
// (--require-e2e), as in the channel's handshake.
type Welcome struct {
	V       int  `json:"v"`
	Require bool `json:"require,omitempty"`
}

// Request is one request on an RPC connection: the inner request of the
// end-to-end channel (part K signature included) and its deadline (Unix
// milliseconds), which the host caps.
type Request struct {
	e2e.Request
	Deadline int64 `json:"deadline,omitempty"`
}

// Offer is the result of direct.offer (sent only inside the end-to-end
// channel): the host's candidate addresses ("ip:port", link-local IPv6
// without a zone), a token for the RPC connection's Hello and when it
// expires (Unix milliseconds).
type Offer struct {
	V       int      `json:"v"`
	Addrs   []string `json:"addrs"`
	Token   []byte   `json:"token,omitempty"`
	Expires int64    `json:"expires,omitempty"`
	// Reason (no addresses): why the host does not listen, e.g. "firewall".
	Reason string `json:"reason,omitempty"`
}

// Conn is an established direct connection.
type Conn struct {
	raw     net.Conn
	wmu     sync.Mutex
	send    *noise.CipherState
	rmu     sync.Mutex
	recv    *noise.CipherState
	closed  chan struct{}
	once    sync.Once
	Welcome Welcome // controller side: the host's answer
}

func newConn(raw net.Conn, send, recv *noise.CipherState) *Conn {
	return &Conn{raw: raw, send: send, recv: recv, closed: make(chan struct{})}
}

// Close closes the connection (safe to call more than once).
func (c *Conn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.raw.Close()
}

// Done is closed once Close was called.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// LocalAddr and RemoteAddr are the TCP endpoints.
func (c *Conn) LocalAddr() net.Addr  { return c.raw.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr { return c.raw.RemoteAddr() }

// WriteMessage sends one message of the given kind, split into records.
func (c *Conn) WriteMessage(ctx context.Context, kind byte, body []byte) error {
	if 1+len(body) > MaxMessage {
		return errors.New("direct: message too large")
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.raw.SetWriteDeadline(deadline)
	msg := make([]byte, 0, 1+len(body))
	msg = append(append(msg, kind), body...)
	var out []byte
	for first := true; first || len(msg) > 0; first = false {
		n := min(len(msg), maxPiece)
		plain := make([]byte, 0, 1+n)
		flag := byte(0)
		if n < len(msg) {
			flag = 1
		}
		plain = append(append(plain, flag), msg[:n]...)
		msg = msg[n:]
		sealed, err := c.send.Encrypt(nil, nil, plain)
		if err != nil {
			c.Close()
			return err
		}
		out = binary.BigEndian.AppendUint16(out, uint16(len(sealed)))
		out = append(out, sealed...)
	}
	if _, err := c.raw.Write(out); err != nil {
		// A message cut short leaves the stream unusable.
		c.Close()
		return err
	}
	return nil
}

// ErrRecord means a record did not authenticate (altered, dropped,
// repeated or reordered on the way); the connection is closed.
var ErrRecord = errors.New("direct: record failed authentication")

// ReadMessage returns the next message. Any error closes the connection.
func (c *Conn) ReadMessage() (byte, []byte, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	var msg []byte
	var header [2]byte
	for {
		if _, err := io.ReadFull(c.raw, header[:]); err != nil {
			c.Close()
			return 0, nil, err
		}
		size := int(binary.BigEndian.Uint16(header[:]))
		if size < 17 {
			c.Close()
			return 0, nil, ErrRecord
		}
		sealed := make([]byte, size)
		if _, err := io.ReadFull(c.raw, sealed); err != nil {
			c.Close()
			return 0, nil, err
		}
		plain, err := c.recv.Decrypt(nil, nil, sealed)
		if err != nil || len(plain) == 0 || plain[0] > 1 {
			c.Close()
			return 0, nil, ErrRecord
		}
		if len(msg)+len(plain)-1 > MaxMessage {
			c.Close()
			return 0, nil, errors.New("direct: message too large")
		}
		msg = append(msg, plain[1:]...)
		if plain[0] == 0 {
			break
		}
	}
	if len(msg) == 0 {
		c.Close()
		return 0, nil, ErrRecord
	}
	return msg[0], msg[1:], nil
}

// DialConfig is what a controller needs to open a direct connection.
type DialConfig struct {
	MachineID string        // the host's relay device ID
	HostKey   []byte        // its pinned static key (trusted-hosts.json)
	Identity  *e2e.Identity // this controller's static key and binding
	Device    string        // this controller's relay device ID
	Token     []byte        // RPC: from direct.offer
	Stream    string        // stream: the terminal ticket's ID
}

// ErrHost means the other end did not prove it holds the pinned host key:
// a wrong address (or someone else) answered. Nothing was sent.
var ErrHost = errors.New("direct: the address did not answer as the pinned host")

// Dial connects to addr and runs the handshake within ctx's deadline.
func Dial(ctx context.Context, addr string, cfg DialConfig) (*Conn, error) {
	if len(cfg.HostKey) != 32 || cfg.Identity == nil || cfg.MachineID == "" || cfg.Device == "" {
		return nil, errors.New("direct: incomplete configuration")
	}
	dialer := net.Dialer{KeepAlive: 15 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	raw.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { raw.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	conn, err := clientHandshake(raw, cfg)
	if err != nil {
		raw.Close()
		return nil, err
	}
	raw.SetDeadline(time.Time{})
	return conn, nil
}

func clientHandshake(raw net.Conn, cfg DialConfig) (*Conn, error) {
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeIK, Initiator: true,
		Prologue: Prologue(cfg.MachineID), StaticKeypair: noise.DHKey{Private: cfg.Identity.Private, Public: cfg.Identity.Public}, PeerStatic: cfg.HostKey})
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(Hello{V: Version, TS: time.Now().UnixMilli(), Device: cfg.Device, Binding: cfg.Identity.Binding, Token: cfg.Token, Stream: cfg.Stream})
	msg1, _, _, err := hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, err
	}
	if len(msg1) > MaxHandshake {
		return nil, errors.New("direct: handshake too large")
	}
	out := append([]byte(Magic), binary.BigEndian.AppendUint16(nil, uint16(len(msg1)))...)
	if _, err := raw.Write(append(out, msg1...)); err != nil {
		return nil, err
	}
	msg2, err := readFrame(raw)
	if err != nil {
		// The host closes silently on any refusal.
		return nil, fmt.Errorf("direct: refused or no answer: %w", err)
	}
	plain, send, recv, err := hs.ReadMessage(nil, msg2)
	if err != nil || send == nil || recv == nil {
		return nil, ErrHost
	}
	var w Welcome
	if err := json.Unmarshal(plain, &w); err != nil || w.V != Version {
		return nil, ErrHost
	}
	c := newConn(raw, send, recv)
	c.Welcome = w
	return c, nil
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(header[:]))
	if size == 0 || size > MaxHandshake {
		return nil, errors.New("direct: invalid handshake size")
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Responder is the host's side of a handshake after the first message:
// Hello and the controller's keys are known, the controller is not yet
// verified. The host checks them and then calls Accept, or closes.
type Responder struct {
	raw        net.Conn
	hs         *noise.HandshakeState
	Hello      Hello
	PeerStatic []byte
	// Ephemeral is the controller's ephemeral key: a replayed first message
	// repeats it.
	Ephemeral []byte
}

// ErrHandshake is any malformed or undecryptable first message; the host
// closes the connection without an answer.
var ErrHandshake = errors.New("direct: invalid handshake")

// ReadHello reads the magic and the first message with the host's static
// key. The caller sets raw's deadline. Only the magic, a length and the
// Noise message are parsed before decryption; the payload is parsed only
// after it decrypted (and then strictly).
func ReadHello(raw net.Conn, private, public []byte, machineID string) (*Responder, error) {
	var magic [len(Magic)]byte
	if _, err := io.ReadFull(raw, magic[:]); err != nil || string(magic[:]) != Magic {
		return nil, ErrHandshake
	}
	msg1, err := readFrame(raw)
	if err != nil {
		return nil, ErrHandshake
	}
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeIK, Initiator: false,
		Prologue: Prologue(machineID), StaticKeypair: noise.DHKey{Private: private, Public: public}})
	if err != nil {
		return nil, err
	}
	plain, _, _, err := hs.ReadMessage(nil, msg1)
	if err != nil {
		return nil, ErrHandshake
	}
	var h Hello
	d := json.NewDecoder(bytes.NewReader(plain))
	d.DisallowUnknownFields()
	if err := d.Decode(&h); err != nil || h.V != Version || h.Device == "" || len(h.Device) > 128 || len(h.Binding) > 200 || len(h.Token) > 64 || len(h.Stream) > 128 ||
		(len(h.Token) == 0) == (h.Stream == "") {
		return nil, ErrHandshake
	}
	return &Responder{raw: raw, hs: hs, Hello: h, PeerStatic: bytes.Clone(hs.PeerStatic()), Ephemeral: bytes.Clone(hs.PeerEphemeral())}, nil
}

// Fresh reports whether Hello's timestamp is within MaxSkew of now.
func (r *Responder) Fresh(now time.Time) bool {
	skew := now.Sub(time.UnixMilli(r.Hello.TS))
	return skew <= MaxSkew && skew >= -MaxSkew
}

// Accept answers the handshake and returns the connection.
func (r *Responder) Accept(w Welcome) (*Conn, error) {
	payload, _ := json.Marshal(w)
	msg2, recv, send, err := r.hs.WriteMessage(nil, payload)
	if err != nil || send == nil || recv == nil {
		return nil, errors.New("direct: handshake incomplete")
	}
	out := binary.BigEndian.AppendUint16(nil, uint16(len(msg2)))
	if _, err := r.raw.Write(append(out, msg2...)); err != nil {
		return nil, err
	}
	return newConn(r.raw, send, recv), nil
}
