package e2e

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

func hostKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func handshake(t *testing.T, id *Identity, host *ecdh.PrivateKey) (*Session, *Session, *Responder) {
	t.Helper()
	now := time.Now()
	ini, msg1, err := Initiate(id, host.PublicKey().Bytes(), "mini", "laptop", now)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Respond(host.Bytes(), host.PublicKey().Bytes(), msg1, "mini", "laptop", now)
	if err != nil {
		t.Fatal(err)
	}
	hs, msg2, err := r.Accept(NewID(), true, now)
	if err != nil {
		t.Fatal(err)
	}
	cs, p, err := ini.Finish(msg2, now)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Require || p.Session != hs.ID || cs.ID != hs.ID {
		t.Fatalf("payload %+v", p)
	}
	return cs, hs, r
}

func TestHandshakeBindsBothStaticKeysAndCarriesFrames(t *testing.T) {
	signer := &devicekey.Software{Dir: t.TempDir()}
	id, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := id.Bind(signer); err != nil {
		t.Fatal(err)
	}
	host := hostKey(t)
	controller, hostSide, r := handshake(t, id, host)
	if !bytes.Equal(r.PeerStatic, id.Public) {
		t.Fatal("the host did not learn the controller's static key")
	}
	device, _ := signer.PublicKey(false)
	if err := VerifyBinding(device, r.PeerStatic, r.Payload.Binding); err != nil {
		t.Fatalf("binding: %v", err)
	}
	// Bound to the device key only: another device's key does not verify,
	// nor does the binding of another static key.
	other, _ := (&devicekey.Software{Dir: t.TempDir()}).PublicKey(false)
	if VerifyBinding(other, r.PeerStatic, r.Payload.Binding) == nil {
		t.Fatal("binding verified with another device key")
	}
	if VerifyBinding(device, hostKey(t).PublicKey().Bytes(), r.Payload.Binding) == nil {
		t.Fatal("binding verified for another static key")
	}
	n, sealed := controller.Seal([]byte(`{"method":"input","text":"secret"}`))
	if bytes.Contains(sealed, []byte("secret")) {
		t.Fatal("plaintext in the frame")
	}
	plain, err := hostSide.Open(n, sealed)
	if err != nil || !strings.Contains(string(plain), "secret") {
		t.Fatalf("open: %q %v", plain, err)
	}
	// Answers go the other way with the other key.
	m, answer := hostSide.Seal([]byte("ok"))
	if _, err := hostSide.Open(m, answer); err == nil {
		t.Fatal("a side opened its own frame")
	}
	if got, err := controller.Open(m, answer); err != nil || string(got) != "ok" {
		t.Fatalf("answer %q %v", got, err)
	}
}

func TestReplayedAndTamperedFramesAreRefused(t *testing.T) {
	id, _ := LoadIdentity(t.TempDir())
	controller, host, _ := handshake(t, id, hostKey(t))
	n, sealed := controller.Seal([]byte("one"))
	if _, err := host.Open(n, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Open(n, sealed); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay: %v", err)
	}
	for i := range sealed {
		n2, frame := controller.Seal([]byte("two"))
		frame[i%len(frame)] ^= 0x80
		if _, err := host.Open(n2, frame); !errors.Is(err, ErrAuth) {
			t.Fatalf("flipped byte %d: %v", i, err)
		}
		// The untouched frame still opens: tampering does not burn it.
		frame[i%len(frame)] ^= 0x80
		if _, err := host.Open(n2, frame); err != nil {
			t.Fatalf("original after tampering: %v", err)
		}
	}
	// A frame moved to another counter does not open.
	n3, frame := controller.Seal([]byte("three"))
	if _, err := host.Open(n3+5, frame); !errors.Is(err, ErrAuth) {
		t.Fatalf("moved counter: %v", err)
	}
	// Out of order within the window is fine, each only once; older than
	// the window is refused.
	var frames [][]byte
	var counters []uint64
	for range Window + 10 {
		c, f := controller.Seal([]byte("x"))
		counters, frames = append(counters, c), append(frames, f)
	}
	last := len(frames) - 1
	if _, err := host.Open(counters[last], frames[last]); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Open(counters[last-3], frames[last-3]); err != nil {
		t.Fatalf("within the window: %v", err)
	}
	if _, err := host.Open(counters[0], frames[0]); !errors.Is(err, ErrReplay) {
		t.Fatalf("older than the window: %v", err)
	}
}

func TestWrongHostKeyOrRoutingFailsTheHandshake(t *testing.T) {
	id, _ := LoadIdentity(t.TempDir())
	host, impostor := hostKey(t), hostKey(t)
	now := time.Now()
	// The controller encrypts to the pinned key: an impostor cannot read
	// the first message.
	_, msg1, _ := Initiate(id, host.PublicKey().Bytes(), "mini", "laptop", now)
	if _, err := Respond(impostor.Bytes(), impostor.PublicKey().Bytes(), msg1, "mini", "laptop", now); err == nil {
		t.Fatal("an impostor read the handshake")
	}
	// A relay that routes the handshake as another controller or to
	// another host breaks the prologue.
	if _, err := Respond(host.Bytes(), host.PublicKey().Bytes(), msg1, "mini", "phone", now); err == nil {
		t.Fatal("handshake accepted for another controller")
	}
	if _, err := Respond(host.Bytes(), host.PublicKey().Bytes(), msg1, "studio", "laptop", now); err == nil {
		t.Fatal("handshake accepted by another machine ID")
	}
	// An answer made with any key but the pinned one does not verify.
	ini, msg1, _ := Initiate(id, impostor.PublicKey().Bytes(), "mini", "laptop", now)
	r, err := Respond(impostor.Bytes(), impostor.PublicKey().Bytes(), msg1, "mini", "laptop", now)
	if err != nil {
		t.Fatal(err)
	}
	_, msg2, _ := r.Accept(NewID(), false, now)
	ini2, _, _ := Initiate(id, host.PublicKey().Bytes(), "mini", "laptop", now)
	if _, _, err := ini2.Finish(msg2, now); err == nil {
		t.Fatal("accepted an answer from another key")
	}
	if _, _, err := ini.Finish(msg2, now); err != nil {
		t.Fatalf("the impostor's own answer to its own handshake: %v", err)
	}
	// Stale handshakes are refused.
	_, old, _ := Initiate(id, host.PublicKey().Bytes(), "mini", "laptop", now.Add(-time.Hour))
	if _, err := Respond(host.Bytes(), host.PublicKey().Bytes(), old, "mini", "laptop", now); err == nil {
		t.Fatal("stale handshake accepted")
	}
	// A tampered first message fails.
	_, msg1, _ = Initiate(id, host.PublicKey().Bytes(), "mini", "laptop", now)
	msg1[len(msg1)-1] ^= 1
	if _, err := Respond(host.Bytes(), host.PublicKey().Bytes(), msg1, "mini", "laptop", now); err == nil {
		t.Fatal("tampered handshake accepted")
	}
}

func TestStreamFramesAreSealedInOrder(t *testing.T) {
	secret := NewStreamSecret()
	controller, _ := NewStream(secret, "stream-1", false)
	host, _ := NewStream(secret, "stream-1", true)
	a := controller.Seal(StreamData, []byte("typed secret"))
	b := controller.Seal(StreamResize, ResizePayload(120, 40))
	if bytes.Contains(a, []byte("typed secret")) {
		t.Fatal("plaintext in a stream frame")
	}
	if _, _, err := host.Open(b); !errors.Is(err, ErrStream) {
		t.Fatal("a reordered frame opened")
	}
	kind, payload, err := host.Open(a)
	if err != nil || kind != StreamData || string(payload) != "typed secret" {
		t.Fatalf("%d %q %v", kind, payload, err)
	}
	if _, _, err := host.Open(a); err == nil {
		t.Fatal("a replayed frame opened")
	}
	kind, payload, err = host.Open(b)
	if columns, rows, _ := ParseResize(payload); err != nil || kind != StreamResize || columns != 120 || rows != 40 {
		t.Fatalf("resize %d %v", kind, err)
	}
	c := controller.Seal(StreamData, []byte("x"))
	c[0] ^= 1
	if _, _, err := host.Open(c); err == nil {
		t.Fatal("a flipped bit opened")
	}
	// Other streams' keys (another ID or secret) do not open it.
	other, _ := NewStream(secret, "stream-2", true)
	if _, _, err := other.Open(controller.Seal(StreamData, []byte("y"))); err == nil {
		t.Fatal("another stream opened the frame")
	}
	// The host's frames are sealed with the other direction's key.
	h := host.Seal(StreamData, []byte("out"))
	if _, _, err := host.Open(h); err == nil {
		t.Fatal("the host opened its own frame")
	}
	if _, payload, err := controller.Open(h); err != nil || string(payload) != "out" {
		t.Fatalf("host frame %q %v", payload, err)
	}
}

func BenchmarkStreamSealOpen(b *testing.B) {
	secret := NewStreamSecret()
	controller, _ := NewStream(secret, "stream-1", false)
	host, _ := NewStream(secret, "stream-1", true)
	frame := bytes.Repeat([]byte("row of a frame "), 20) // a typical echo frame
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := controller.Open(host.Seal(StreamData, frame)); err != nil {
			b.Fatal(err)
		}
	}
}

func TestIdentityFileIsPrivateAndStable(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dir, KeyFile))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := a.Bind(&devicekey.Software{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	b, err := LoadIdentity(dir)
	if err != nil || !bytes.Equal(a.Public, b.Public) || b.Binding != a.Binding {
		t.Fatal("the key or its binding changed on reload")
	}
	os.Chmod(filepath.Join(dir, KeyFile), 0644)
	if _, err := LoadIdentity(dir); err == nil {
		t.Fatal("a readable key file was accepted")
	}
}

// The largest request there is (a 512 KiB transfer chunk with its
// signature) still fits in one relay message inside the channel.
func TestLargestRequestFitsInARelayMessage(t *testing.T) {
	id, _ := LoadIdentity(t.TempDir())
	controller, _, _ := handshake(t, id, hostKey(t))
	chunk := make([]byte, 512*1024+16)
	params, _ := json.Marshal(map[string]any{"upload": strings.Repeat("u", 64), "name": "transcript.jsonl", "offset": 1 << 28, "data": base64.StdEncoding.EncodeToString(chunk),
		"last": true, "sha256": strings.Repeat("f", 64), "epk": base64.StdEncoding.EncodeToString(make([]byte, 32))})
	inner, _ := json.Marshal(Request{ID: NewID(), Method: "transfer", Params: params,
		Auth: &protocol.Auth{Device: strings.Repeat("d", 43), TS: time.Now().UnixMilli(), Nonce: base64.StdEncoding.EncodeToString(make([]byte, 16)), Sig: strings.Repeat("s", 96)}})
	n, sealed := controller.Seal(inner)
	outer, _ := json.Marshal(protocol.Message{Version: protocol.Version, Type: "request", ID: strings.Repeat("i", 64), MachineID: strings.Repeat("m", 43),
		ControllerID: strings.Repeat("c", 43), Deadline: time.Now().UnixMilli(), Method: FrameMethod, Params: protocol.JSON(Frame{S: NewID(), N: n, C: sealed})})
	if len(outer) > protocol.MaxMessageBytes-32*1024 {
		t.Fatalf("a full transfer chunk takes %d bytes in the channel (limit %d)", len(outer), protocol.MaxMessageBytes)
	}
}
