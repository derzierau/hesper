package host

// Pairing offers (docs/iphone/onboarding.md; relay/docs/protocol.md,
// Pairing offers): the code `hesperctl phone` and Hesper.app's Settings ›
// iPhone show. Each holds a one-time secret; a devices.request that proves
// it (offerProof) is approved at once with the offer's rights. Offers live
// in hesperd's memory only, at most MaxOffers, OfferTTL each, single use;
// the local socket creates and cancels them (devices.offer,
// devices.offerCancel) and announces their use (devices.paired).

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

const (
	// MaxOffers bounds the open offers; a new one ends the oldest.
	MaxOffers = 3
	// OfferTTL is how long an offer stays open.
	OfferTTL = 5 * time.Minute
	// NotePaired is the local socket's notification when an offer is used.
	NotePaired = "devices.paired"
)

// DefaultOfferRights are an offer's rights when devices.offer names none.
var DefaultOfferRights = []string{devicekey.Observe, devicekey.Answer, devicekey.Type, devicekey.Transfer}

// OfferParams are devices.offer's params.
type OfferParams struct {
	Rights []string `json:"rights,omitempty"`
}

// OfferResult is devices.offer's result: the pairing code to show.
type OfferResult struct {
	Payload string `json:"payload"`
	Expires int64  `json:"expires"` // Unix seconds
	ID      string `json:"id"`
}

// Paired is devices.paired's params: an offer approved a device.
type Paired struct {
	Offer  string   `json:"offer"`
	Device string   `json:"device"`
	Name   string   `json:"name"`
	Rights []string `json:"rights"`
}

type offer struct {
	id      string
	secret  []byte
	rights  []string
	expires time.Time
}

// Offers are the open pairing offers. The zero value has no host: every
// call answers unavailable until Configure.
type Offers struct {
	Now  func() time.Time
	Rand io.Reader

	mu         sync.Mutex
	configured bool
	relay      string
	machine    string
	name       string
	hostKey    string
	allowShell bool
	open       []offer
	watchers   map[chan Paired]bool
}

// Configure sets what the pairing codes carry once the host role runs:
// the relay origin, this host's relay device ID and name, its X25519
// transfer key, and whether it offers shells (hesperd serve --allow-shell).
func (o *Offers) Configure(relay, machine, name string, hostKey []byte, allowShell bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.configured = true
	o.relay, o.machine, o.name = strings.TrimSuffix(relay, "/"), machine, name
	o.hostKey = base64.StdEncoding.EncodeToString(hostKey)
	o.allowShell = allowShell
}

func (o *Offers) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Offers) random(n int) ([]byte, error) {
	b := make([]byte, n)
	r := o.Rand
	if r == nil {
		r = rand.Reader
	}
	_, err := io.ReadFull(r, b)
	return b, err
}

// prune drops expired offers; o.mu is held.
func (o *Offers) prune(now time.Time) {
	o.open = slices.DeleteFunc(o.open, func(f offer) bool { return !now.Before(f.expires) })
}

// Create opens an offer with rights (DefaultOfferRights when empty). The
// shell right needs a host that offers shells.
func (o *Offers) Create(rights []string) (OfferResult, error) {
	if len(rights) == 0 {
		rights = DefaultOfferRights
	}
	if !validRights(rights) {
		return OfferResult{}, wire.Errorf(wire.CodeInvalid, "rights must be some of %s", strings.Join(devicekey.AllRights, ", "))
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.configured {
		return OfferResult{}, wire.Errorf(wire.CodeUnavailable, "pairing needs this Mac's host role (hesperctl login --role host)")
	}
	if slices.Contains(rights, devicekey.Shell) && !o.allowShell {
		return OfferResult{}, wire.Errorf(wire.CodeInvalid, "this Mac does not offer shells (hesperd serve --allow-shell)")
	}
	secret, err := o.random(devicekey.OfferSecretSize)
	if err != nil {
		return OfferResult{}, err
	}
	id, err := o.random(6)
	if err != nil {
		return OfferResult{}, err
	}
	now := o.now()
	f := offer{id: hex.EncodeToString(id), secret: secret, rights: SortRights(rights), expires: now.Add(OfferTTL)}
	o.prune(now)
	if len(o.open) >= MaxOffers {
		o.open = o.open[len(o.open)-MaxOffers+1:]
	}
	o.open = append(o.open, f)
	code := devicekey.PairingCode{V: 1, Relay: o.relay, Machine: o.machine, Name: o.name, HostKey: o.hostKey,
		Rights: f.rights, Offer: base64.RawURLEncoding.EncodeToString(secret), Expires: f.expires.Unix()}
	return OfferResult{Payload: code.Text(), Expires: f.expires.Unix(), ID: f.id}, nil
}

// Cancel ends an offer; an unknown or expired one is not_found.
func (o *Offers) Cancel(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prune(o.now())
	n := len(o.open)
	o.open = slices.DeleteFunc(o.open, func(f offer) bool { return f.id == id })
	if len(o.open) == n {
		return wire.Errorf(wire.CodeNotFound, "no open offer %q", id)
	}
	return nil
}

// use finds the open offer whose secret made proof for request p to
// machine and ends it. Every open offer's proof is computed and compared
// in constant time, so the time taken does not tell which one matched.
func (o *Offers) use(machine string, p ApprovalRequest) (offer, bool) {
	if o == nil {
		return offer{}, false
	}
	proof, err := base64.StdEncoding.Strict().DecodeString(p.OfferProof)
	if err != nil {
		return offer{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.configured {
		return offer{}, false
	}
	o.prune(o.now())
	match := -1
	for i, f := range o.open {
		want, _ := base64.StdEncoding.DecodeString(devicekey.OfferProof(f.secret, machine, p.Name, p.Key, p.StrongKey, p.E2EKey))
		if hmac.Equal(want, proof) {
			match = i
		}
	}
	if match < 0 {
		return offer{}, false
	}
	f := o.open[match]
	o.open = slices.Delete(o.open, match, match+1)
	return f, true
}

// paired announces an approval by offer to the watchers.
func (o *Offers) paired(p Paired) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for ch := range o.watchers {
		select {
		case ch <- p:
		default: // a watcher that does not read misses it
		}
	}
}

// Call runs devices.offer and devices.offerCancel (handled false: not
// one of them).
func (o *Offers) Call(method string, params json.RawMessage) (any, error, bool) {
	switch method {
	case "devices.offer":
		var p OfferParams
		if err := strictParams(params, &p); err != nil {
			return nil, err, true
		}
		res, err := o.Create(p.Rights)
		return res, err, true
	case "devices.offerCancel":
		var p struct {
			ID string `json:"id"`
		}
		if err := strictParams(params, &p); err != nil {
			return nil, err, true
		}
		return struct{}{}, o.Cancel(p.ID), true
	}
	return nil, nil, false
}

func strictParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	if err := strictJSON(raw, v); err != nil {
		return wire.Errorf(wire.CodeInvalid, "invalid params: %v", err)
	}
	return nil
}

// Watch sends devices.paired to an agents.subscribe connection until done
// closes or send fails.
func (o *Offers) Watch(done <-chan struct{}, send func(method string, params any) error) {
	ch := make(chan Paired, 8)
	o.mu.Lock()
	if o.watchers == nil {
		o.watchers = map[chan Paired]bool{}
	}
	o.watchers[ch] = true
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.watchers, ch)
		o.mu.Unlock()
	}()
	for {
		select {
		case <-done:
			return
		case p := <-ch:
			if send(NotePaired, p) != nil {
				return
			}
		}
	}
}
