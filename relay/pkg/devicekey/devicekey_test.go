package devicekey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// Golden vector: a fixed key, request and signature. Other implementations
// (any other controller) must produce the same message and digest and accept the
// signature. Values were checked independently (Python hashlib).
const (
	goldenPublic = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEzl43zIIDgn77ReDsdfJBbUZPR4rW1lPcHfHSg8qfTIMtdAV9LOC4yz8Oa1V7H5Khq52cmq3h3IqTuH0vGkZM6g=="
	goldenRaw    = `{ "target": {"terminalId": "a7"}, "text": "y <ok> & go", "submit": true }`
	goldenParams = `{"target":{"terminalId":"a7"},"text":"y ` + bs + `u003cok` + bs + `u003e ` + bs + `u0026 go","submit":true}`
	// bs is a backslash and lineSep U+2028, spelled as bytes so that no
	// tool rewrites their escapes in this file.
	bs            = "\x5c"
	lineSep       = "\xe2\x80\xa8"
	goldenMachine = "mini-1"
	goldenMethod  = "input"
	goldenTS      = int64(1791036424000)
	goldenNonce   = "AAECAwQFBgcICQoLDA0ODw=="
	goldenMessage = "ghosty-req-v1\nmini-1\ninput\n1791036424000\nAAECAwQFBgcICQoLDA0ODw==\ncf71dc2fdfb8807e081aa51cee5b2647a0f8631b80a4edadeff209ac296f2496"
	goldenDigest  = "4c15802b986fa4a598295fbf7a1f78d25e5e6d387d2a1e0cfb3c0b129e3726f5"
	goldenSig     = "MEUCIAfH5o8PerLuc3Y4b93pYxeh1V+bsmlIv/rPN5BWFM3mAiEArRJ1eUaA/WZA1fV0W1B1qdJADbAAEd0NEOKe/mcWtGQ="
)

func goldenKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	// Golden vector: kept as "ghosty golden device key", like the "ghosty-req-v1"
	// label it signs.
	seed := sha256.Sum256([]byte("ghosty golden device key"))
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), seed[:])
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func goldenAuth(t *testing.T) ParsedAuth {
	t.Helper()
	a, err := ParseAuth(&protocol.Auth{Device: "laptop-1", TS: goldenTS, Nonce: goldenNonce, Sig: goldenSig})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestGoldenVector(t *testing.T) {
	key := goldenKey(t)
	public, err := EncodePublicKey(&key.PublicKey)
	if err != nil || public != goldenPublic {
		t.Fatalf("public key %s, %v", public, err)
	}
	params, err := NormalizeParams([]byte(goldenRaw))
	if err != nil || string(params) != goldenParams {
		t.Fatalf("params %s, %v", params, err)
	}
	message, err := Message(goldenMachine, goldenMethod, goldenTS, goldenNonce, params)
	if err != nil || string(message) != goldenMessage {
		t.Fatalf("message %q, %v", message, err)
	}
	digest, _ := Digest(goldenMachine, goldenMethod, goldenTS, goldenNonce, params)
	if hex.EncodeToString(digest[:]) != goldenDigest {
		t.Fatalf("digest %x", digest)
	}
	pub, _, err := ParsePublicKey(goldenPublic)
	if err != nil {
		t.Fatal(err)
	}
	// The host verifies the params as received: raw or normalized, the same.
	for _, received := range []string{goldenRaw, goldenParams} {
		if err := Verify(pub, goldenMachine, goldenMethod, []byte(received), goldenAuth(t)); err != nil {
			t.Fatalf("golden signature rejected for %s: %v", received, err)
		}
	}
}

// Changing anything the signature covers breaks it.
func TestTamperedRequestsDoNotVerify(t *testing.T) {
	pub, _, _ := ParsePublicKey(goldenPublic)
	auth := goldenAuth(t)
	verify := func(machine, method, params string, a ParsedAuth) error {
		return Verify(pub, machine, method, []byte(params), a)
	}
	cases := map[string]error{
		"method":       verify(goldenMachine, "key", goldenParams, auth),
		"params":       verify(goldenMachine, goldenMethod, strings.Replace(goldenParams, `"a7"`, `"a8"`, 1), auth),
		"param added":  verify(goldenMachine, goldenMethod, strings.Replace(goldenParams, `"submit":true`, `"submit":false`, 1), auth),
		"host machine": verify("mini-2", goldenMethod, goldenParams, auth),
		"ts": func() error {
			a := auth
			a.TS++
			return verify(goldenMachine, goldenMethod, goldenParams, a)
		}(),
		"nonce": func() error {
			a := auth
			a.Nonce = "AQECAwQFBgcICQoLDA0ODw=="
			return verify(goldenMachine, goldenMethod, goldenParams, a)
		}(),
		"signature": func() error {
			a := auth
			a.Sig = append([]byte(nil), a.Sig...)
			a.Sig[len(a.Sig)-1] ^= 1
			return verify(goldenMachine, goldenMethod, goldenParams, a)
		}(),
		"other key": func() error {
			other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			return Verify(&other.PublicKey, goldenMachine, goldenMethod, []byte(goldenParams), auth)
		}(),
		"duplicate key": verify(goldenMachine, goldenMethod, `{"target":{"terminalId":"a7"},"text":"y <ok> & go","submit":true,"submit":false}`, auth),
	}
	for name, err := range cases {
		if err == nil {
			t.Errorf("%s: tampered request verified", name)
		}
	}
}

func TestSignRequestRoundTrip(t *testing.T) {
	s := &Software{Dir: t.TempDir()}
	params, _ := NormalizeParams([]byte(`{"target":{"terminalId":"a1"},"key":"Enter"}`))
	for _, strong := range []bool{false, true} {
		auth, err := SignRequest(s, "laptop-1", "mini-1", "key", params, Options{Strong: strong}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseAuth(auth)
		if err != nil {
			t.Fatal(err)
		}
		pub, _ := s.PublicKey(strong)
		other, _ := s.PublicKey(!strong)
		if err := Verify(pub, "mini-1", "key", params, parsed); err != nil {
			t.Fatalf("strong=%v: %v", strong, err)
		}
		if Verify(other, "mini-1", "key", params, parsed) == nil {
			t.Fatalf("strong=%v: verified with the other key", strong)
		}
	}
	a, _ := SignRequest(s, "laptop-1", "mini-1", "key", params, Options{}, time.Now())
	b, _ := SignRequest(s, "laptop-1", "mini-1", "key", params, Options{}, time.Now())
	if a.Nonce == b.Nonce {
		t.Fatal("nonces repeat")
	}
}

func TestNormalizeParams(t *testing.T) {
	good := map[string]string{
		"":                          "{}",
		" {} ":                      "{}",
		`{"a" : [1, 2 ,{"b":3}]}`:   `{"a":[1,2,{"b":3}]}`,
		`null`:                      `null`,
		`{"a":{"x":1},"b":{"x":1}}`: `{"a":{"x":1},"b":{"x":1}}`,
		// <, >, & and U+2028/U+2029 (raw or escaped) end up escaped.
		`{"t":"<x>&"}`:                       `{"t":"` + bs + `u003cx` + bs + `u003e` + bs + `u0026"}`,
		`{"t":"` + lineSep + `"}`:            `{"t":"` + bs + `u2028"}`,
		`{"t":"` + bs + `u2029"}`:            `{"t":"` + bs + `u2029"}`,
		`{"t":"` + bs + `u003c` + bs + `n"}`: `{"t":"` + bs + `u003c` + bs + `n"}`,
	}
	for in, want := range good {
		got, err := NormalizeParams([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
		again, _ := NormalizeParams(got)
		if string(again) != string(got) {
			t.Errorf("%q: not idempotent", in)
		}
	}
	for _, bad := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":1}}`, `[{"x":1,"x":1}]`, "{\"a\":\"\xff\"}", `{}{}`, `{} 1`, `{`, `{"a":}`, `nul`, strings.Repeat("[", 70) + strings.Repeat("]", 70)} {
		if _, err := NormalizeParams([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseAuthIsStrict(t *testing.T) {
	ok := protocol.Auth{Device: "laptop-1", TS: goldenTS, Nonce: goldenNonce, Sig: goldenSig}
	if _, err := ParseAuth(&ok); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAuth(nil); err == nil {
		t.Fatal("nil auth accepted")
	}
	bad := []func(a *protocol.Auth){
		func(a *protocol.Auth) { a.Device = "" },
		func(a *protocol.Auth) { a.Device = "x\ny" },
		func(a *protocol.Auth) { a.TS = 0 },
		func(a *protocol.Auth) { a.TS = -5 },
		func(a *protocol.Auth) { a.Nonce = "AAECAwQFBgcICQoLDA0ODw" },   // no padding
		func(a *protocol.Auth) { a.Nonce = "AAECAwQFBgcICQoLDA0ODxA=" }, // 17 bytes
		func(a *protocol.Auth) { a.Nonce = "AAECAwQFBgcICQoLDA0ODx==" }, // non-canonical trailing bits
		func(a *protocol.Auth) { a.Nonce = "AAECAwQFBgcICQoLDA0OD_==" }, // URL alphabet
		func(a *protocol.Auth) { a.Sig = "" },
		func(a *protocol.Auth) { a.Sig = strings.Repeat("A", 200) },
		func(a *protocol.Auth) { a.Sig = goldenSig[:len(goldenSig)-1] },
		func(a *protocol.Auth) { a.Sig = " " + goldenSig },
	}
	for i, mutate := range bad {
		a := ok
		mutate(&a)
		if _, err := ParseAuth(&a); err == nil {
			t.Errorf("case %d accepted: %+v", i, a)
		}
	}
	if _, err := Message("mini\n1", "input", 1, goldenNonce, []byte("{}")); err == nil {
		t.Error("newline in machine ID accepted")
	}
	if _, err := Message("mini", "in\nput", 1, goldenNonce, []byte("{}")); err == nil {
		t.Error("newline in method accepted")
	}
}

func TestParsePublicKeyIsStrict(t *testing.T) {
	if _, _, err := ParsePublicKey(goldenPublic); err != nil {
		t.Fatal(err)
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der384, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	derRSA, _ := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	for _, bad := range []string{
		"", "not base64", goldenPublic[:len(goldenPublic)-2], " " + goldenPublic,
		strings.TrimRight(goldenPublic, "="),
		base64.StdEncoding.EncodeToString(der384), base64.StdEncoding.EncodeToString(derRSA),
		base64.StdEncoding.EncodeToString([]byte("garbage")),
	} {
		if _, _, err := ParsePublicKey(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMethodRights(t *testing.T) {
	for method, want := range map[string]string{
		"snapshot": Observe, "ping": Observe, "agents.list": Observe, "agents.link": Observe, "agents.export": Observe,
		"download": Observe, "job": Observe, "agents.answer": Answer, "agents.input": Type, "transfer": Transfer,
		"agents.spawn": Transfer, "agents.stop": Transfer, "agents.import": Transfer,
		"files.put": Type, "files.chunk": Type, "review.list": Observe, "review.diff": Observe,
	} {
		right, strong, ok := MethodRight(method, nil)
		if !ok || right != want || strong {
			t.Errorf("%s: %s %v %v", method, right, strong, ok)
		}
	}
	// Bring the folder: a folder's export, and its plan, transfer.
	for method, params := range map[string]string{"agents.export": `{"id":"folder:with:/Users/a/x"}`, "bring.plan": `{"path":"/Users/a/x"}`} {
		if right, _, ok := MethodRight(method, []byte(params)); !ok || right != Transfer {
			t.Errorf("%s %s: %s %v", method, params, right, ok)
		}
	}
	if right, _, ok := MethodRight("bring.probe", []byte(`{}`)); !ok || right != Observe {
		t.Errorf("bring.probe: %s %v", right, ok)
	}
	if right, _, _ := MethodRight("agents.export", []byte(`{"id":"a7f3k2"}`)); right != Observe {
		t.Errorf("an agent's export: %s", right)
	}
	// Projects: reading observes, sending a state or promoting transfers.
	for p, want := range map[string]string{`{}`: Observe, `{"state":null}`: Observe, `{"state":{"node":"n"}}`: Transfer, `garbage`: Transfer} {
		if r, _, ok := MethodRight("projects.sync", []byte(p)); !ok || r != want {
			t.Errorf("projects.sync %s needs %s", p, r)
		}
	}
	if r, _, _ := MethodRight("projects.promote", []byte(`{}`)); r != Transfer {
		t.Errorf("projects.promote needs %s", r)
	}
	for _, gone := range []string{"shell.open", "capture", "input", "terminal.open", "respond"} {
		if _, _, ok := MethodRight(gone, nil); ok {
			t.Errorf("%s has a right", gone)
		}
	}
	if r, _, _ := MethodRight("agents.attach", []byte(`{"mode":"ro","id":"x"}`)); r != Observe {
		t.Errorf("read-only attach needs %s", r)
	}
	for _, p := range []string{`{"mode":"rw"}`, `{}`, `garbage`, ``} {
		if r, _, _ := MethodRight("agents.attach", []byte(p)); r != Type {
			t.Errorf("attach %s needs %s", p, r)
		}
	}
	for p, shell := range map[string]bool{`{"kind":"shell"}`: true, `{"profile":"shell"}`: true, `{"profile":"my-shell"}`: true,
		`{"kind":"claude","profile":"shell"}`: false, `{"kind":"claude"}`: false, `{}`: false} {
		right, strong, _ := MethodRight("agents.spawn", []byte(p))
		if (right == Shell) != shell || strong != shell || NeedsStrong("agents.spawn", []byte(p)) != shell {
			t.Errorf("spawn %s: %s %v", p, right, strong)
		}
	}
}

func TestApprovalCode(t *testing.T) {
	hostKey := sha256.Sum256([]byte("host"))
	if code := ApprovalCode(hostKey[:], []byte("a"), []byte("b")); code != "ILY-TIQ" {
		t.Fatalf("code %s", code)
	}
	if ApprovalCode(hostKey[:], []byte("a"), []byte("c")) == "ILY-TIQ" {
		t.Fatal("strong key does not change the code")
	}
	for in, want := range map[string]string{"ily-tiq": "ILY-TIQ", "ILYTIQ": "ILY-TIQ", "ily tiq": "ILY-TIQ", "ILY-TI": "", "ILY-TI1": "", "lapt0p": "", "laptops": ""} {
		if got := NormalizeCode(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestSoftwareKeysPersistPrivately(t *testing.T) {
	dir := t.TempDir()
	a := &Software{Dir: dir}
	pa, err := a.PublicKey(false)
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := (&Software{Dir: dir}).PublicKey(false)
	if !pa.Equal(pb) {
		t.Fatal("key not persisted")
	}
	strong, _ := a.PublicKey(true)
	if strong.Equal(pa) {
		t.Fatal("strong key equals device key")
	}
	for _, name := range []string{DeviceFile, StrongFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s: %v %v", name, info.Mode(), err)
		}
	}
	os.Chmod(filepath.Join(dir, DeviceFile), 0644)
	if _, err := (&Software{Dir: dir}).PublicKey(false); err == nil {
		t.Fatal("a world-readable key was used")
	}
	os.WriteFile(filepath.Join(dir, "x.key"), []byte("not a key"), 0600)
	os.Rename(filepath.Join(dir, "x.key"), filepath.Join(dir, StrongFile))
	if _, err := (&Software{Dir: dir}).PublicKey(true); err == nil {
		t.Fatal("garbage key file accepted")
	}
}

// The Swift helper in software mode reads the same key files and signs
// digests the Go side verifies. Runs when dist/hesper-keys was built (make
// keys); never touches Secure Enclave keys or Touch ID.
func TestHelperInterop(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("hesper-keys is macOS only")
	}
	path, _ := filepath.Abs("../../dist/hesper-keys")
	if _, err := os.Stat(path); err != nil {
		t.Skip("dist/hesper-keys not built (make keys)")
	}
	t.Setenv("HESPER_KEYS_SOFTWARE", "1")
	dir := t.TempDir()
	software := &Software{Dir: dir}
	want, err := software.PublicKey(false) // Go creates the key, Swift reads it
	if err != nil {
		t.Fatal(err)
	}
	h := &Helper{Path: path, Dir: dir}
	if h.Hardware() {
		t.Fatal("software helper claims hardware")
	}
	got, err := h.PublicKey(false)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatal("helper and Go read different keys")
	}
	strong, err := h.PublicKey(true) // Swift creates it, Go reads it
	if err != nil {
		t.Fatal(err)
	}
	if goStrong, _ := software.PublicKey(true); !goStrong.Equal(strong) {
		t.Fatal("strong key differs")
	}
	params, _ := NormalizeParams([]byte(`{"text":"hi"}`))
	for _, opts := range []Options{{}, {Strong: true, Reason: "test"}} {
		auth, err := SignRequest(h, "laptop-1", "mini-1", "input", params, opts, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := ParseAuth(auth)
		key := want
		if opts.Strong {
			key = strong
		}
		if err := Verify(key, "mini-1", "input", params, parsed); err != nil {
			t.Fatalf("helper signature (strong=%v): %v", opts.Strong, err)
		}
	}
	// Raw digest input works too; malformed input is refused.
	digest := sha256.Sum256([]byte("x"))
	for input, ok := range map[string]bool{string(digest[:]): true, hex.EncodeToString(digest[:]): true, hex.EncodeToString(digest[:]) + "\n": true,
		"abc": false, hex.EncodeToString(digest[:]) + "\n\n": false, hex.EncodeToString(digest[:31]) + "zz": false} {
		cmd := exec.Command(path, "sign")
		cmd.Env = append(os.Environ(), "HESPER_STATE_DIR="+dir)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		if (err == nil) != ok {
			t.Errorf("input %q: %v %s", input, err, out)
			continue
		}
		if ok {
			sig, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
			if !ecdsa.VerifyASN1(want, digest[:], sig) {
				t.Errorf("input %q: signature does not verify", input)
			}
		}
	}
	// A world-readable key is refused by the helper as well.
	os.Chmod(filepath.Join(dir, DeviceFile), 0644)
	if _, err := (&Helper{Path: path, Dir: dir}).PublicKey(false); err == nil {
		t.Fatal("helper used a world-readable key")
	}
}

// Manual check of the Secure Enclave device key (no Touch ID: the strong key
// is not used): HESPER_KEYS_HARDWARE_DIR=/tmp/some-empty-dir go test -run
// TestHardwareDeviceKey ./pkg/devicekey.
func TestHardwareDeviceKey(t *testing.T) {
	dir := os.Getenv("HESPER_KEYS_HARDWARE_DIR")
	if dir == "" || runtime.GOOS != "darwin" {
		t.Skip("set HESPER_KEYS_HARDWARE_DIR to a scratch directory to check the Secure Enclave key")
	}
	path, _ := filepath.Abs("../../dist/hesper-keys")
	t.Setenv("HESPER_KEYS_SOFTWARE", "")
	h := &Helper{Path: path, Dir: dir}
	pub, err := h.PublicKey(false)
	if err != nil {
		t.Fatal(err)
	}
	params, _ := NormalizeParams([]byte(`{"text":"hi"}`))
	auth, err := SignRequest(h, "laptop-1", "mini-1", "input", params, Options{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := ParseAuth(auth)
	if err := Verify(pub, "mini-1", "input", params, parsed); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, DeviceFile)); strings.HasPrefix(string(data), "-----BEGIN") {
		t.Fatal("hardware mode wrote a software key")
	}
}
