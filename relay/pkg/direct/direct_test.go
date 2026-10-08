package direct

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/flynn/noise"
)

func TestEligibleAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"10.0.0.12": true, "172.16.0.1": true, "172.31.255.254": true, "192.168.1.20": true, "169.254.3.4": true,
		"fd12:3456::1": true, "fe80::1": true, "fe80::1%en0": true,
		"8.8.8.8": false, "172.32.0.1": false, "100.64.0.1": false, "127.0.0.1": false, "0.0.0.0": false,
		"::": false, "::1": false, "2001:db8::1": false, "2a00:1450::1": false, "224.0.0.1": false, "ff02::1": false,
		"::ffff:10.0.0.1": true, "::ffff:8.8.8.8": false,
	} {
		if got := Eligible(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Eligible(%s) = %v", addr, got)
		}
	}
	for _, a := range LocalAddrs() {
		if !Eligible(a.WithZone("")) {
			t.Errorf("LocalAddrs lists %s", a)
		}
	}
}

func TestCandidatesFilterOrderAndZones(t *testing.T) {
	got := Candidates([]string{"[fe80::1]:7000", "8.8.8.8:7000", "[fd00::5]:7000", "10.0.0.2:7000", "127.0.0.1:7000", "10.0.0.3:0", "nonsense", "169.254.1.1:7000"},
		nil, []string{"en0", "en1"}, 8)
	want := []string{"10.0.0.2:7000", "[fd00::5]:7000", "169.254.1.1:7000", "[fe80::1%en0]:7000", "[fe80::1%en1]:7000"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("candidates %v", got)
	}
	if got := Candidates([]string{"127.0.0.1:7000"}, func(a netip.Addr) bool { return a.IsLoopback() }, nil, 8); len(got) != 1 {
		t.Fatalf("loopback allowed by the caller: %v", got)
	}
	if got := Candidates([]string{"10.0.0.1:1", "10.0.0.2:1", "10.0.0.3:1"}, nil, nil, 2); len(got) != 2 {
		t.Fatalf("not capped: %v", got)
	}
}

type world struct {
	host     *ecdh.PrivateKey
	id       *e2e.Identity
	listener net.Listener
}

func newWorld(t *testing.T) *world {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := e2e.LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Bind(&devicekey.Software{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return &world{host: key, id: id, listener: l}
}

func (w *world) config() DialConfig {
	return DialConfig{MachineID: "mini", HostKey: w.host.PublicKey().Bytes(), Identity: w.id, Device: "laptop", Token: []byte("0123456789abcdef")}
}

// serve accepts one connection and answers its handshake with accept
// deciding (nil: close silently).
func (w *world) serve(t *testing.T, accept func(*Responder) bool) <-chan *Conn {
	t.Helper()
	out := make(chan *Conn, 1)
	go func() {
		defer close(out)
		raw, err := w.listener.Accept()
		if err != nil {
			return
		}
		raw.SetDeadline(time.Now().Add(3 * time.Second))
		r, err := ReadHello(raw, w.host.Bytes(), w.host.PublicKey().Bytes(), "mini")
		if err != nil || !accept(r) {
			raw.Close()
			return
		}
		c, err := r.Accept(Welcome{V: Version, Require: true})
		if err != nil {
			raw.Close()
			return
		}
		raw.SetDeadline(time.Time{})
		out <- c
	}()
	return out
}

func TestHandshakeAndMessagesBothWays(t *testing.T) {
	w := newWorld(t)
	var hello Hello
	var peer []byte
	served := w.serve(t, func(r *Responder) bool {
		hello, peer = r.Hello, r.PeerStatic
		return r.Fresh(time.Now())
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := Dial(ctx, w.listener.Addr().String(), w.config())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := <-served
	if h == nil {
		t.Fatal("host side failed")
	}
	defer h.Close()
	if hello.Device != "laptop" || !bytes.Equal(peer, w.id.Public) || hello.Binding != w.id.Binding || !c.Welcome.Require {
		t.Fatalf("hello %+v welcome %+v", hello, c.Welcome)
	}
	// A message larger than one record is split and reassembled.
	big := bytes.Repeat([]byte("0123456789"), 150_000)
	go c.WriteMessage(ctx, KindRequest, big)
	kind, body, err := h.ReadMessage()
	if err != nil || kind != KindRequest || !bytes.Equal(body, big) {
		t.Fatalf("big message: %c %d %v", kind, len(body), err)
	}
	if err := h.WriteMessage(ctx, KindResponse, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if kind, body, err := c.ReadMessage(); err != nil || kind != KindResponse || string(body) != "ok" {
		t.Fatalf("answer %c %q %v", kind, body, err)
	}
	if err := c.WriteMessage(ctx, KindData, bytes.Repeat([]byte{1}, MaxMessage)); err == nil {
		t.Fatal("an oversized message was sent")
	}
}

// Only the holder of the pinned key can answer: the controller pinned
// another key, so the host cannot even read the first message and closes;
// something that answers garbage fails as ErrHost.
func TestPinMismatchAndImpostors(t *testing.T) {
	w := newWorld(t)
	served := w.serve(t, func(*Responder) bool { return true })
	cfg := w.config()
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	cfg.HostKey = other.PublicKey().Bytes()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := Dial(ctx, w.listener.Addr().String(), cfg); err == nil {
		t.Fatal("connected to a host with another key")
	}
	if c := <-served; c != nil {
		t.Fatal("the host accepted a handshake for another key")
	}
	// An impostor answering with bytes of its own.
	go func() {
		raw, err := w.listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		io.ReadFull(raw, make([]byte, len(Magic)+2))
		junk := make([]byte, 48)
		rand.Read(junk)
		raw.Write(append(binary.BigEndian.AppendUint16(nil, 48), junk...))
		time.Sleep(time.Second)
	}()
	if _, err := Dial(ctx, w.listener.Addr().String(), w.config()); !errors.Is(err, ErrHost) {
		t.Fatalf("impostor: %v", err)
	}
}

// A record altered on the way ends the connection.
func TestTamperedRecordsEndTheConnection(t *testing.T) {
	w := newWorld(t)
	// A proxy that flips one bit in the first record after the handshake.
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	go func() {
		down, err := front.Accept()
		if err != nil {
			return
		}
		up, err := net.Dial("tcp", w.listener.Addr().String())
		if err != nil {
			return
		}
		go io.Copy(down, up)
		head := make([]byte, len(Magic)+2)
		io.ReadFull(down, head)
		msg1 := make([]byte, binary.BigEndian.Uint16(head[len(Magic):]))
		io.ReadFull(down, msg1)
		up.Write(append(head, msg1...))
		record := make([]byte, 2)
		io.ReadFull(down, record)
		body := make([]byte, binary.BigEndian.Uint16(record))
		io.ReadFull(down, body)
		body[len(body)/2] ^= 1
		up.Write(append(record, body...))
		io.Copy(up, down)
	}()
	served := w.serve(t, func(*Responder) bool { return true })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := Dial(ctx, front.Addr().String(), w.config())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := <-served
	if err := c.WriteMessage(ctx, KindRequest, []byte(`{"id":"1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.ReadMessage(); !errors.Is(err, ErrRecord) {
		t.Fatalf("tampered record: %v", err)
	}
	select {
	case <-h.Done():
	default:
		t.Fatal("the connection stayed open")
	}
}

// What a stranger can send before the handshake verifies: the magic, a
// length and one Noise message of at most MaxHandshake bytes. Anything else
// is refused before any further parsing.
func TestHandshakeLimitsAndStrictHello(t *testing.T) {
	w := newWorld(t)
	try := func(data []byte) error {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		go func() { client.Write(data); client.Close() }()
		server.SetDeadline(time.Now().Add(time.Second))
		_, err := ReadHello(server, w.host.Bytes(), w.host.PublicKey().Bytes(), "mini")
		return err
	}
	if err := try([]byte("GET / HTTP/1.1\r\n\r\n")); !errors.Is(err, ErrHandshake) {
		t.Fatalf("http: %v", err)
	}
	if err := try(append([]byte(Magic), 0xff, 0xff)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("oversized: %v", err)
	}
	junk := make([]byte, 200)
	rand.Read(junk)
	if err := try(append(append([]byte(Magic), 0, 200), junk...)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("junk: %v", err)
	}
	// A well-formed handshake whose payload carries unknown fields, or both
	// a token and a stream, or neither, is refused too.
	first := func(payload any) []byte {
		hs, _ := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeIK, Initiator: true, Prologue: Prologue("mini"),
			StaticKeypair: noise.DHKey{Private: w.id.Private, Public: w.id.Public}, PeerStatic: w.host.PublicKey().Bytes()})
		body, _ := json.Marshal(payload)
		msg, _, _, _ := hs.WriteMessage(nil, body)
		return append(append([]byte(Magic), binary.BigEndian.AppendUint16(nil, uint16(len(msg)))...), msg...)
	}
	now := time.Now().UnixMilli()
	for name, payload := range map[string]any{
		"unknown field": map[string]any{"v": 1, "ts": now, "device": "laptop", "token": []byte("x"), "admin": true},
		"both":          Hello{V: 1, TS: now, Device: "laptop", Token: []byte("x"), Stream: "s"},
		"neither":       Hello{V: 1, TS: now, Device: "laptop"},
		"version":       Hello{V: 2, TS: now, Device: "laptop", Token: []byte("x")},
		"no device":     Hello{V: 1, TS: now, Token: []byte("x")},
	} {
		if err := try(first(payload)); !errors.Is(err, ErrHandshake) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := try(first(Hello{V: 1, TS: now, Device: "laptop", Token: []byte("x")})); err != nil {
		t.Fatalf("valid hello: %v", err)
	}
	// Another machine ID in the prologue does not verify.
	hs, _ := noise.NewHandshakeState(noise.Config{CipherSuite: suite, Pattern: noise.HandshakeIK, Initiator: true, Prologue: Prologue("other"),
		StaticKeypair: noise.DHKey{Private: w.id.Private, Public: w.id.Public}, PeerStatic: w.host.PublicKey().Bytes()})
	body, _ := json.Marshal(Hello{V: 1, TS: now, Device: "laptop", Token: []byte("x")})
	msg, _, _, _ := hs.WriteMessage(nil, body)
	if err := try(append(append([]byte(Magic), binary.BigEndian.AppendUint16(nil, uint16(len(msg)))...), msg...)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("other machine: %v", err)
	}
}
