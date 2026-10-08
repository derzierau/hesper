package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/transfer"
)

type e2eWorld struct {
	h      *E2E
	signer *devicekey.Software
	id     *e2e.Identity
	dir    string
}

func newE2EWorld(t *testing.T, approved bool) *e2eWorld {
	t.Helper()
	key, err := transfer.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	w := &e2eWorld{dir: t.TempDir(), signer: &devicekey.Software{Dir: t.TempDir()}}
	w.h = &E2E{Key: key, Auth: &Authorizer{Store: Store{Dir: w.dir}, MachineID: "mini"}}
	if w.id, err = e2e.LoadIdentity(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := w.id.Bind(w.signer); err != nil {
		t.Fatal(err)
	}
	if approved {
		k, _ := w.signer.PublicKey(false)
		s, _ := w.signer.PublicKey(true)
		kb, _ := devicekey.EncodePublicKey(k)
		sb, _ := devicekey.EncodePublicKey(s)
		if err := w.h.Auth.Store.saveControllers(Controllers{Controllers: []Controller{{Device: "laptop", Name: "laptop", Key: kb, StrongKey: sb, Rights: []string{"observe", "shell"}, Approved: 1, ApprovedBy: "local"}}}); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *e2eWorld) hello(t *testing.T, device string) (*e2e.Session, []byte, error) {
	t.Helper()
	ini, msg, err := e2e.Initiate(w.id, w.h.Key.PublicKey().Bytes(), "mini", device, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := w.h.Hello(protocol.Message{ControllerID: device, Method: e2e.HelloMethod, Params: protocol.JSON(e2e.Hello{V: e2e.Version, H: msg})})
	if err != nil {
		return nil, msg, err
	}
	var answer e2e.HelloResult
	json.Unmarshal(raw, &answer)
	s, _, err := ini.Finish(answer.H, time.Now())
	return s, msg, err
}

func (w *e2eWorld) audit() string {
	data, _ := os.ReadFile(filepath.Join(w.dir, AuditFile))
	return string(data)
}

func TestE2EHelloChecksTheBindingOfApprovedDevices(t *testing.T) {
	w := newE2EWorld(t, true)
	if _, _, err := w.hello(t, "laptop"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.audit(), `"event":"e2e.session","device":"laptop","name":"laptop"`) {
		t.Fatalf("session not audited:\n%s", w.audit())
	}
	// A replayed first message is refused.
	_, msg, _ := w.hello(t, "laptop")
	_, err := w.h.Hello(protocol.Message{ControllerID: "laptop", Method: e2e.HelloMethod, Params: protocol.JSON(e2e.Hello{V: e2e.Version, H: msg})})
	if code(err) != "forbidden" || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replayed handshake: %v", err)
	}
	// A static key bound by another device key: e2e_binding.
	if err := w.id.Bind(&devicekey.Software{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.hello(t, "laptop"); code(err) != e2e.CodeBinding {
		t.Fatalf("foreign binding: %v", err)
	}
	// No binding at all.
	w.id.Binding = ""
	if _, _, err := w.hello(t, "laptop"); code(err) != e2e.CodeBinding {
		t.Fatalf("no binding: %v", err)
	}
	// An unknown device while enforcing.
	if _, _, err := w.hello(t, "phone"); code(err) != "forbidden" {
		t.Fatalf("unknown device: %v", err)
	}
	if !strings.Contains(w.audit(), `"event":"e2e.refused"`) {
		t.Fatalf("refusals not audited:\n%s", w.audit())
	}
}

func TestE2EHelloBeforeAnyApprovalAcceptsUnboundKeys(t *testing.T) {
	w := newE2EWorld(t, false)
	w.id.Binding = ""
	if _, _, err := w.hello(t, "laptop"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.audit(), "unbound key") {
		t.Fatalf("audit:\n%s", w.audit())
	}
	w.h.Auth.Require = true // --require-device-keys
	if _, _, err := w.hello(t, "laptop"); code(err) != "forbidden" {
		t.Fatalf("enforcing without approval: %v", err)
	}
}

func TestE2EOpenRefusesReplaysDuplicatesAndUnknownSessions(t *testing.T) {
	w := newE2EWorld(t, true)
	s, _, err := w.hello(t, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	frame := func(id string) protocol.Message {
		inner, _ := json.Marshal(e2e.Request{ID: id, Method: "capture", Params: json.RawMessage(`{"lines":3}`)})
		n, sealed := s.Seal(inner)
		return protocol.Message{ID: "relay-1", ControllerID: "laptop", Method: e2e.FrameMethod, Deadline: 42, Params: protocol.JSON(e2e.Frame{S: s.ID, N: n, C: sealed})}
	}
	m := frame("one")
	inner, session, innerID, err := w.h.Open(m)
	if err != nil || inner.Method != "capture" || string(inner.Params) != `{"lines":3}` || inner.ID != "relay-1" || inner.Deadline != 42 || innerID != "one" {
		t.Fatalf("open %+v %v", inner, err)
	}
	// The answer is sealed for the controller and names the request.
	var result e2e.FrameResult
	json.Unmarshal(w.h.Seal(session, innerID, json.RawMessage(`{"text":"screen"}`), nil), &result)
	plain, err := s.Open(result.N, result.C)
	if err != nil || !strings.Contains(string(plain), `"id":"one"`) || !strings.Contains(string(plain), "screen") {
		t.Fatalf("answer %s %v", plain, err)
	}
	if _, _, _, err := w.h.Open(m); code(err) != e2e.CodeIntegrity {
		t.Fatalf("replayed frame: %v", err)
	}
	if _, _, _, err := w.h.Open(frame("one")); code(err) != "duplicate_request" {
		t.Fatalf("repeated inner request: %v", err)
	}
	unknown := frame("two")
	var f e2e.Frame
	json.Unmarshal(unknown.Params, &f)
	f.S = e2e.NewID()
	unknown.Params = protocol.JSON(f)
	if _, _, _, err := w.h.Open(unknown); code(err) != e2e.CodeSession {
		t.Fatalf("unknown session: %v", err)
	}
	// Another controller cannot use this session.
	other := frame("three")
	other.ControllerID = "phone"
	if _, _, _, err := w.h.Open(other); code(err) != e2e.CodeSession {
		t.Fatalf("session of another device: %v", err)
	}
	// Errors travel sealed too.
	json.Unmarshal(w.h.Seal(session, "four", nil, protocol.Err("forbidden", "no")), &result)
	if plain, _ := s.Open(result.N, result.C); !strings.Contains(string(plain), `"code":"forbidden"`) {
		t.Fatalf("sealed error %s", plain)
	}
}

func TestRequireE2ECoversEverythingButObserving(t *testing.T) {
	for method, want := range map[string]bool{"snapshot": false, "ping": false, "devices.request": false, e2e.HelloMethod: false, "agents.list": true, "job": true,
		"agents.input": true, "agents.answer": true, "agents.link": true, "agents.attach": true, "transfer": true, "files.put": true, "files.chunk": true, "agents.spawn": true, "unknown": true} {
		if got := requiresE2E(protocol.Message{Method: method, Params: json.RawMessage(`{}`)}); got != want {
			t.Errorf("%s: %v", method, got)
		}
	}
}
