package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func wireCode(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code
	}
	return ""
}

func protocolCode(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// withOffers gives the harness's authorizer pairing offers on its clock.
func (h *authHarness) withOffers(allowShell bool) *Offers {
	o := &Offers{Now: h.clock}
	o.Configure("https://relay.example.com/", "mini-1", "mini", hostKeyForTest(), allowShell)
	h.a.Offers = o
	return o
}

func secretOf(t *testing.T, res OfferResult) []byte {
	t.Helper()
	code, err := devicekey.ParsePairingCode(res.Payload)
	if err != nil {
		t.Fatalf("payload %q: %v", res.Payload, err)
	}
	secret, _ := code.Secret()
	return secret
}

// askOffered sends devices.request with the proof for secret.
func (h *authHarness) askOffered(device string, s devicekey.Signer, name string, hardware bool, secret []byte) (ApprovalStatus, error) {
	key, strong := publicKeys(h.t, s)
	p := ApprovalRequest{Name: name, Key: key, StrongKey: strong, Hardware: hardware, Rights: []string{devicekey.Observe}}
	if secret != nil {
		p.OfferProof = devicekey.OfferProof(secret, "mini-1", name, key, strong, "")
	} else {
		p.OfferProof = "AAAA"
	}
	raw, err := h.a.RequestApproval(context.Background(), protocol.Message{Type: "request", ControllerID: device, Method: "devices.request", Params: protocol.JSON(p)})
	var status ApprovalStatus
	if err == nil {
		json.Unmarshal(raw, &status)
	}
	return status, err
}

func TestOfferLifecycle(t *testing.T) {
	h := newAuthHarness(t)
	if _, err := (&Offers{}).Create(nil); wireCode(err) != wire.CodeUnavailable {
		t.Fatalf("without the host role: %v", err)
	}
	o := h.withOffers(false)
	res, err := o.Create(nil)
	if err != nil {
		t.Fatal(err)
	}
	code, err := devicekey.ParsePairingCode(res.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if code.Relay != "https://relay.example.com" || code.Machine != "mini-1" || code.Name != "mini" ||
		!slices.Equal(code.Rights, DefaultOfferRights) || code.Expires != h.clock().Add(OfferTTL).Unix() || res.Expires != code.Expires || res.ID == "" {
		t.Fatalf("offer %+v code %+v", res, code)
	}
	if _, err := o.Create([]string{devicekey.Observe, devicekey.Shell}); wireCode(err) != wire.CodeInvalid {
		t.Fatalf("shell on a host without shells: %v", err)
	}
	if _, err := o.Create([]string{"root"}); wireCode(err) != wire.CodeInvalid {
		t.Fatalf("unknown right: %v", err)
	}
	shells := &Offers{}
	shells.Configure("r", "m", "n", hostKeyForTest(), true)
	if _, err := shells.Create([]string{devicekey.Shell}); err != nil {
		t.Fatalf("shell on a host with shells: %v", err)
	}

	// At most MaxOffers: a fourth ends the first.
	first := secretOf(t, res)
	var rest []OfferResult
	for range MaxOffers {
		r, err := o.Create([]string{devicekey.Observe})
		if err != nil {
			t.Fatal(err)
		}
		rest = append(rest, r)
	}
	laptop := &devicekey.Software{Dir: filepath.Join(h.dir, "laptop")}
	if _, err := h.askOffered("laptop-1", laptop, "laptop", false, first); protocolCode(err) != "forbidden" {
		t.Fatalf("the oldest offer stayed open: %v", err)
	}
	// Cancelled and expired offers approve nothing.
	if err := o.Cancel(rest[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := o.Cancel(rest[0].ID); wireCode(err) != wire.CodeNotFound {
		t.Fatalf("cancelled twice: %v", err)
	}
	if _, err := h.askOffered("laptop-1", laptop, "laptop", false, secretOf(t, rest[0])); protocolCode(err) != "forbidden" {
		t.Fatalf("cancelled offer: %v", err)
	}
	h.advance(OfferTTL)
	if _, err := h.askOffered("laptop-1", laptop, "laptop", false, secretOf(t, rest[1])); protocolCode(err) != "forbidden" {
		t.Fatalf("expired offer: %v", err)
	}

	// The socket's methods.
	if _, err, handled := o.Call("devices.offer", json.RawMessage(`{"rights":["observe"],"x":1}`)); !handled || wireCode(err) != wire.CodeInvalid {
		t.Fatalf("unknown field: %v %v", handled, err)
	}
	got, err, _ := o.Call("devices.offer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err, _ := o.Call("devices.offerCancel", json.RawMessage(`{"id":"`+got.(OfferResult).ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, handled := o.Call("devices.list", nil); handled {
		t.Fatal("devices.list handled")
	}
}

// A request with the proof of an open offer is approved at once with the
// offer's rights, uses the offer up and is announced; a bad proof leaves
// nothing waiting.
func TestApprovalByOffer(t *testing.T) {
	h := newAuthHarness(t)
	var notes []string
	var mu sync.Mutex
	done := make(chan struct{})
	h.a.Notify = func(title, body string) {
		mu.Lock()
		notes = append(notes, title+": "+body)
		mu.Unlock()
		done <- struct{}{}
	}
	o := h.withOffers(true)
	stop := make(chan struct{})
	defer close(stop)
	events := make(chan Paired, 4)
	go o.Watch(stop, func(method string, params any) error {
		if method != NotePaired {
			t.Errorf("event %s", method)
		}
		events <- params.(Paired)
		return nil
	})
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		o.mu.Lock()
		n := len(o.watchers)
		o.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher not registered")
		}
	}

	phone := &devicekey.Software{Dir: filepath.Join(h.dir, "phone")}
	if _, err := h.askOffered("phone-1", phone, "iPhone", false, nil); protocolCode(err) != "forbidden" {
		t.Fatalf("bad proof: %v", err)
	}
	if pending, _ := h.a.Store.PendingRequests(h.clock()); len(pending) != 0 {
		t.Fatalf("bad proof left %+v waiting", pending)
	}
	// A request waiting from before is replaced by the approval.
	if status, err := h.ask("phone-1", phone, "iPhone", "observe"); err != nil || status.Status != "pending" {
		t.Fatalf("plain request: %+v %v", status, err)
	}
	<-done
	res, err := o.Create([]string{devicekey.Observe, devicekey.Answer, devicekey.Shell})
	if err != nil {
		t.Fatal(err)
	}
	secret := secretOf(t, res)
	status, err := h.askOffered("phone-1", phone, "iPhone", false, secret)
	if err != nil || status.Status != "approved" || status.Name != "iPhone" || status.HostKey == "" ||
		!slices.Equal(status.Rights, []string{devicekey.Observe, devicekey.Answer}) {
		t.Fatalf("approval by offer: %+v %v (shell must be dropped for software keys)", status, err)
	}
	list, enforcing, err := h.a.Store.LoadControllers()
	if err != nil || !enforcing || len(list.Controllers) != 1 || list.Controllers[0].ApprovedBy != "offer" || list.Controllers[0].Device != "phone-1" {
		t.Fatalf("controllers %+v %v %v", list, enforcing, err)
	}
	if pending, _ := h.a.Store.PendingRequests(h.clock()); len(pending) != 0 {
		t.Fatalf("still waiting: %+v", pending)
	}
	select {
	case p := <-events:
		if p.Offer != res.ID || p.Device != "phone-1" || p.Name != "iPhone" || !slices.Equal(p.Rights, status.Rights) {
			t.Fatalf("event %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("no devices.paired")
	}
	<-done
	mu.Lock()
	if last := notes[len(notes)-1]; !strings.HasPrefix(last, "Hesper: iPhone added: ") {
		t.Fatalf("notification %q", last)
	}
	mu.Unlock()
	if !strings.Contains(h.audit(), `"detail":"rights observe,answer offer `+res.ID+`"`) || !strings.Contains(h.audit(), `"event":"device.offer.refused"`) {
		t.Fatalf("audit:\n%s", h.audit())
	}
	// The approved device's signed requests pass.
	if _, err := h.a.Authorize(context.Background(), h.signed("phone-1", phone, "agents.list", `{}`, false)); err != nil {
		t.Fatalf("signed request after approval: %v", err)
	}
	// Single use.
	if _, err := h.askOffered("phone-1", phone, "iPhone", false, secret); protocolCode(err) != "forbidden" {
		t.Fatalf("offer used twice: %v", err)
	}

	// Another device of the same name gets a number; hardware keys keep shell.
	res, _ = o.Create([]string{devicekey.Observe, devicekey.Shell})
	ipad := &devicekey.Software{Dir: filepath.Join(h.dir, "ipad")}
	status, err = h.askOffered("ipad-1", ipad, "iPhone", true, secretOf(t, res))
	if err != nil || status.Name != "iPhone 2" || !slices.Equal(status.Rights, []string{devicekey.Observe, devicekey.Shell}) {
		t.Fatalf("second device: %+v %v", status, err)
	}
	<-done
	// The proof covers the keys: another key with the same proof fails.
	res, _ = o.Create(nil)
	key, strong := publicKeys(t, phone)
	_, other := publicKeys(t, ipad)
	p := ApprovalRequest{Name: "iPhone", Key: key, StrongKey: other, Rights: []string{devicekey.Observe},
		OfferProof: devicekey.OfferProof(secretOf(t, res), "mini-1", "iPhone", key, strong, "")}
	if _, err := h.a.RequestApproval(context.Background(), protocol.Message{ControllerID: "phone-1", Method: "devices.request", Params: protocol.JSON(p)}); protocolCode(err) != "forbidden" {
		t.Fatalf("proof for other keys: %v", err)
	}
	// Without Offers every proof is refused.
	h.a.Offers = nil
	if _, err := h.askOffered("phone-1", phone, "iPhone", false, secretOf(t, res)); protocolCode(err) != "forbidden" {
		t.Fatalf("no offers: %v", err)
	}
}
