package host

// Request authorization by device keys (docs/remote-shell-contract.md,
// Part K). The relay is not trusted to say who sent a request: every request
// except snapshot carries a signature by a controller device key, and the
// host checks it against its own allowlist (controllers.json), the right the
// method needs, a ±60 s time window and a nonce it has not seen in the last
// 10 minutes (persisted across restarts).
//
// Enforcement: on as soon as controllers.json exists (the first approval
// creates it; it stays when the last device is revoked, so revoking never
// reopens the host), or with Require (hesperd serve --require-device-keys).
// Until then requests are allowed and audited as unauthenticated, so
// existing setups keep working until their devices are approved; shell
// methods are refused regardless.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/e2e"
	"github.com/derzierau/hesper/relay/pkg/protocol"
)

const (
	// MaxSkew is how far a request's timestamp may be from the host's clock.
	MaxSkew = 60 * time.Second
	// NonceTTL is how long a nonce is remembered.
	NonceTTL = 10 * time.Minute
	// maxNonces bounds the replay cache; beyond it requests are refused.
	maxNonces = 200_000
)

// Caller is who sent a request, as far as the host can tell.
type Caller struct {
	Device   string   // the controller's relay device ID
	Name     string   // its name in controllers.json ("" when not verified)
	Rights   []string // what it may do
	Strong   bool     // signed with the strong (Touch ID) key
	Hardware bool     // its keys are in a Secure Enclave (its claim at approval)
	// Verified is true when a device signature was checked; false for
	// snapshot and while enforcement is off.
	Verified bool
	// E2E is true when the request came through the end-to-end channel;
	// Direct when it came on the direct path (docs/direct-path.md).
	E2E    bool
	Direct bool
}

// Has reports whether the caller holds right.
func (c Caller) Has(right string) bool { return slices.Contains(c.Rights, right) }

type callerKey struct{}

// WithCaller returns ctx carrying c; Runner puts the authorized caller into
// the context of every operation.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the caller Runner authorized for this operation.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// Authorizer checks requests against the host's approved devices.
type Authorizer struct {
	Store     Store
	MachineID string // this host's relay device ID, part of every signed message
	HostKey   []byte // this host's X25519 transfer public key, for approval codes
	Require   bool   // enforce even with an empty allowlist
	// Notify (optional) tells the user at this machine that a device asks
	// for approval (MacNotify on macOS).
	Notify func(title, body string)
	Now    func() time.Time

	mu     sync.Mutex
	nonces *nonceCache
	asked  map[string]time.Time
	recent []time.Time
	noted  map[string]time.Time
}

func (a *Authorizer) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Audit appends an audit line (see Store.Audit).
func (a *Authorizer) Audit(event string, fields map[string]any) { a.Store.Audit(event, fields) }

func (a *Authorizer) refuse(ctx context.Context, m protocol.Message, device, name, detail, message string) error {
	if device == "" {
		device = m.ControllerID
	}
	a.Audit("request.refused", map[string]any{"device": device, "name": name, "method": m.Method, "ok": false, "detail": detail, "e2e": ViaE2E(ctx), "route": RouteOf(ctx)})
	return protocol.Err("forbidden", message)
}

// Enforcing reports whether requests must be signed by an approved device.
func (a *Authorizer) Enforcing() (bool, error) {
	_, exists, err := a.Store.LoadControllers()
	if err != nil {
		return true, err
	}
	return a.Require || exists, nil
}

// Authorize decides whether m may run. It returns a protocol error with
// code "forbidden" (and writes an audit line) when it may not.
func (a *Authorizer) Authorize(ctx context.Context, m protocol.Message) (Caller, error) {
	now := a.now()
	if devicekey.Unsigned(m.Method) {
		// Snapshots carry metadata, never terminal output; every routed
		// controller may read them (the fleet needs them). A ping answers
		// nothing but that the host is there.
		return Caller{Device: m.ControllerID, Rights: []string{devicekey.Observe}, E2E: ViaE2E(ctx), Direct: ViaDirect(ctx)}, nil
	}
	right, strong, known := devicekey.MethodRight(m.Method, m.Params)
	list, exists, err := a.Store.LoadControllers()
	if err != nil {
		return Caller{}, a.refuse(ctx, m, "", "", "allowlist unreadable: "+err.Error(), "This host's device allowlist (controllers.json) is damaged; fix or remove it on the host")
	}
	if !a.Require && !exists {
		if right == devicekey.Shell {
			return Caller{}, a.refuse(ctx, m, "", "", "shell without approved devices", "Shells need an approved device: run hesperctl pair-host, then hesperctl approve on the host")
		}
		a.noteUnauthenticated(ctx, m, now)
		return Caller{Device: m.ControllerID, Rights: []string{devicekey.Observe, devicekey.Answer, devicekey.Type, devicekey.Transfer}, E2E: ViaE2E(ctx), Direct: ViaDirect(ctx)}, nil
	}
	if !known {
		return Caller{}, a.refuse(ctx, m, "", "", "method not in the rights table", "Unknown method")
	}
	if a.MachineID == "" {
		return Caller{}, a.refuse(ctx, m, "", "", "host has no machine ID", "Host cannot verify requests")
	}
	auth, err := devicekey.ParseAuth(m.Auth)
	if err != nil {
		if m.Auth == nil {
			return Caller{}, a.refuse(ctx, m, "", "", "unsigned request", "Request is not signed by a device key: approve this device on the host (hesperctl pair-host)")
		}
		return Caller{}, a.refuse(ctx, m, "", "", "malformed auth: "+err.Error(), "Malformed request signature")
	}
	if auth.Device != m.ControllerID {
		return Caller{}, a.refuse(ctx, m, auth.Device, "", "auth.device differs from the routed controller "+m.ControllerID, "Request signature is for another device")
	}
	var c *Controller
	for i := range list.Controllers {
		if list.Controllers[i].Device == auth.Device {
			c = &list.Controllers[i]
		}
	}
	if c == nil {
		return Caller{}, a.refuse(ctx, m, auth.Device, "", "device not approved", "This device is not approved on this host: run hesperctl pair-host")
	}
	if !slices.Contains(c.Rights, right) {
		return Caller{}, a.refuse(ctx, m, c.Device, c.Name, "missing right "+right, fmt.Sprintf("%s may not %s on this host (right %q)", c.Name, m.Method, right))
	}
	if skew := now.Sub(time.UnixMilli(auth.TS)); skew > MaxSkew || skew < -MaxSkew {
		return Caller{}, a.refuse(ctx, m, c.Device, c.Name, "timestamp off by "+skew.Round(time.Second).String(), "Request timestamp is outside ±60 s of the host's clock (check both clocks, or Touch ID took too long)")
	}
	key, _, err := devicekey.ParsePublicKey(c.Key)
	if err != nil {
		return Caller{}, a.refuse(ctx, m, c.Device, c.Name, "stored key invalid", "Host cannot verify requests")
	}
	strongKey, _, err := devicekey.ParsePublicKey(c.StrongKey)
	if err != nil {
		return Caller{}, a.refuse(ctx, m, c.Device, c.Name, "stored strong key invalid", "Host cannot verify requests")
	}
	usedStrong := false
	if strong {
		err = devicekey.Verify(strongKey, a.MachineID, m.Method, m.Params, auth)
		usedStrong = err == nil
	} else if err = devicekey.Verify(key, a.MachineID, m.Method, m.Params, auth); err != nil {
		// Any request may be signed with the strong key.
		if devicekey.Verify(strongKey, a.MachineID, m.Method, m.Params, auth) == nil {
			err, usedStrong = nil, true
		}
	}
	if err != nil {
		detail := "bad signature"
		if strong {
			detail = "bad or missing strong-key signature"
		}
		if errors.Is(err, devicekey.ErrInvalid) {
			detail = err.Error()
		}
		return Caller{}, a.refuse(ctx, m, c.Device, c.Name, detail, "Request signature does not verify")
	}
	a.mu.Lock()
	if a.nonces == nil {
		a.nonces, err = openNonces(a.Store.path(NoncesFile), now)
	}
	if err == nil {
		err = a.nonces.add(c.Device+" "+auth.Nonce, now)
	}
	a.mu.Unlock()
	if errors.Is(err, errReplay) {
		return Caller{}, a.refuse(ctx, m, c.Device, c.Name, "replayed nonce", "Request was already received (replay)")
	}
	if err != nil {
		return Caller{}, a.refuse(ctx, m, c.Device, c.Name, "replay cache: "+err.Error(), "Host cannot record the request nonce")
	}
	if right != devicekey.Observe && m.Method != "transfer" && m.Method != "files.chunk" {
		detail := "right " + right
		if usedStrong {
			detail += ", strong key"
		}
		a.Audit("request", map[string]any{"device": c.Device, "name": c.Name, "method": m.Method, "ok": true, "detail": detail, "e2e": ViaE2E(ctx), "route": RouteOf(ctx)})
	}
	return Caller{Device: c.Device, Name: c.Name, Rights: slices.Clone(c.Rights), Strong: usedStrong, Hardware: c.Hardware, Verified: true, E2E: ViaE2E(ctx), Direct: ViaDirect(ctx)}, nil
}

// noteUnauthenticated audits an allowed request while enforcement is off,
// once a minute per device and method.
func (a *Authorizer) noteUnauthenticated(ctx context.Context, m protocol.Message, now time.Time) {
	a.mu.Lock()
	if a.noted == nil {
		a.noted = map[string]time.Time{}
	}
	key := m.ControllerID + " " + m.Method
	if t, ok := a.noted[key]; ok && now.Sub(t) < time.Minute {
		a.mu.Unlock()
		return
	}
	a.noted[key] = now
	a.mu.Unlock()
	a.Audit("request.unauthenticated", map[string]any{"device": m.ControllerID, "method": m.Method, "ok": true,
		"detail": "unauthenticated request (enforcement off: approve devices with hesperctl approve)", "e2e": ViaE2E(ctx), "route": RouteOf(ctx)})
}

// ApprovalRequest is the params of devices.request.
type ApprovalRequest struct {
	Name      string   `json:"name"`
	Key       string   `json:"key"`
	StrongKey string   `json:"strongKey"`
	Hardware  bool     `json:"hardware"`
	Rights    []string `json:"rights"`
	// E2EKey and E2EBinding (optional, part N): the controller's
	// end-to-end static key and the device key's signature of it.
	E2EKey     string `json:"e2eKey,omitempty"`
	E2EBinding string `json:"e2eBinding,omitempty"`
}

// ApprovalStatus is the result of devices.request.
type ApprovalStatus struct {
	Status  string   `json:"status"` // "pending" or "approved"
	Code    string   `json:"code,omitempty"`
	Expires int64    `json:"expires,omitempty"`
	Name    string   `json:"name,omitempty"`
	Rights  []string `json:"rights,omitempty"`
	HostKey string   `json:"hostKey"` // base64 X25519 key the code is derived from
}

// RequestApproval handles devices.request: unauthenticated by design, it
// can only add a pending request (at most MaxPending, rate limited) that
// someone at this machine has to approve. It never grants anything itself.
func (a *Authorizer) RequestApproval(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	now := a.now()
	var p ApprovalRequest
	if err := params(m.Params, &p); err != nil {
		return nil, err
	}
	if !deviceID.MatchString(m.ControllerID) {
		return nil, protocol.Err("invalid_request", "Invalid controller")
	}
	if !ValidName(p.Name) {
		return nil, protocol.Err("invalid_request", "name must be 1 to 40 printable characters")
	}
	_, keyDER, err := devicekey.ParsePublicKey(p.Key)
	if err != nil {
		return nil, protocol.Err("invalid_request", "key must be a base64 P-256 SubjectPublicKeyInfo")
	}
	_, strongDER, err := devicekey.ParsePublicKey(p.StrongKey)
	if err != nil || p.Key == p.StrongKey {
		return nil, protocol.Err("invalid_request", "strongKey must be a second base64 P-256 SubjectPublicKeyInfo")
	}
	if !validRights(p.Rights) {
		return nil, protocol.Err("invalid_request", "rights must be some of "+strings.Join(devicekey.AllRights, ", "))
	}
	if p.E2EKey != "" || p.E2EBinding != "" {
		deviceKey, _, _ := devicekey.ParsePublicKey(p.Key)
		static, err := base64.StdEncoding.Strict().DecodeString(p.E2EKey)
		if err != nil || len(static) != 32 || e2e.VerifyBinding(deviceKey, static, p.E2EBinding) != nil {
			return nil, protocol.Err("invalid_request", "e2eKey must be a base64 X25519 key signed (e2eBinding) by key")
		}
	}
	if len(a.HostKey) != 32 {
		return nil, protocol.Err("unsupported", "Host has no transfer key for approval codes")
	}
	hostKey := base64.StdEncoding.EncodeToString(a.HostKey)
	list, _, err := a.Store.LoadControllers()
	if err != nil {
		return nil, protocol.Err("unavailable", "Host device allowlist is damaged")
	}
	for _, c := range list.Controllers {
		if c.Device == m.ControllerID && c.Key == p.Key && c.StrongKey == p.StrongKey && !slices.ContainsFunc(p.Rights, func(r string) bool { return !slices.Contains(c.Rights, r) }) {
			return protocol.JSON(ApprovalStatus{Status: "approved", Name: c.Name, Rights: c.Rights, HostKey: hostKey}), nil
		}
	}
	// Asking again with the same keys and rights (a controller waiting for
	// the answer) neither notifies again nor extends the request.
	if f, err := a.Store.pendingState(now); err == nil {
		for _, d := range f.Denied {
			if d.Device == m.ControllerID || d.Key == p.Key {
				return nil, protocol.Err("forbidden", "The host denied this device; ask again in 10 minutes")
			}
		}
		for _, q := range f.Pending {
			if q.Device == m.ControllerID && q.Key == p.Key && q.StrongKey == p.StrongKey && q.Hardware == p.Hardware &&
				q.Name == p.Name && slices.Equal(q.Rights, SortRights(p.Rights)) {
				return protocol.JSON(ApprovalStatus{Status: "pending", Code: q.Code, Expires: q.Expires, HostKey: hostKey}), nil
			}
		}
	}
	a.mu.Lock()
	if a.asked == nil {
		a.asked = map[string]time.Time{}
	}
	a.recent = slices.DeleteFunc(a.recent, func(t time.Time) bool { return now.Sub(t) > time.Minute })
	last, seen := a.asked[m.ControllerID]
	limited := (seen && now.Sub(last) < 5*time.Second) || len(a.recent) >= 10
	if !limited {
		a.asked[m.ControllerID] = now
		a.recent = append(a.recent, now)
	}
	a.mu.Unlock()
	if limited {
		return nil, protocol.Err("busy", "Too many approval requests; try again in a minute")
	}
	code := devicekey.ApprovalCode(a.HostKey, keyDER, strongDER)
	pending := Pending{Device: m.ControllerID, Name: p.Name, Key: p.Key, StrongKey: p.StrongKey, Hardware: p.Hardware, E2EKey: p.E2EKey,
		Rights: SortRights(p.Rights), Code: code, Requested: now.Unix(), Expires: now.Add(PendingTTL).Unix()}
	if err := a.Store.addPending(pending, now); errors.Is(err, ErrDenied) {
		return nil, protocol.Err("forbidden", "The host denied this device; ask again in 10 minutes")
	} else if errors.Is(err, ErrPendingFull) {
		a.Audit("device.request.refused", map[string]any{"device": m.ControllerID, "name": p.Name, "ok": false, "detail": "too many pending"})
		return nil, protocol.Err("busy", "Too many devices are waiting for approval on this host")
	} else if err != nil {
		return nil, protocol.Err("unavailable", "Host cannot record the request")
	}
	a.Audit("device.requested", map[string]any{"device": m.ControllerID, "name": p.Name, "ok": true,
		"detail": fmt.Sprintf("code %s rights %s hardware %t", code, strings.Join(pending.Rights, ","), p.Hardware)})
	if a.Notify != nil {
		go a.Notify("Hesper: device waiting for approval", fmt.Sprintf("%q asks for %s. Code %s. Approve: C-a A or hesperctl approve %s", p.Name, strings.Join(pending.Rights, ", "), code, code))
	}
	return protocol.JSON(ApprovalStatus{Status: "pending", Code: code, Expires: pending.Expires, HostKey: hostKey}), nil
}

// MacNotify shows a macOS notification. Texts are passed as arguments,
// never spliced into the AppleScript source.
func MacNotify(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exec.CommandContext(ctx, "osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "--", title, body).Run()
}

var errReplay = errors.New("replayed nonce")

// nonceCache remembers nonces for NonceTTL in memory and in an append-only
// file ("<expiry unix ms> <device> <nonce>" per line), so a restarted host
// still refuses a replay. A line is written before the request runs.
type nonceCache struct {
	path  string
	seen  map[string]int64
	lines int
	file  *os.File
}

func openNonces(path string, now time.Time) (*nonceCache, error) {
	c := &nonceCache{path: path, seen: map[string]int64{}}
	if data, err := devicekey.ReadPrivateFile(path); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) != 3 {
				continue // a line cut short by a crash
			}
			expiry, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil || expiry <= now.UnixMilli() {
				continue
			}
			c.seen[fields[1]+" "+fields[2]] = expiry
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := c.rewrite(now); err != nil {
		return nil, err
	}
	return c, nil
}

// rewrite drops expired entries and starts a fresh file.
func (c *nonceCache) rewrite(now time.Time) error {
	var b strings.Builder
	for key, expiry := range c.seen {
		if expiry <= now.UnixMilli() {
			delete(c.seen, key)
			continue
		}
		fmt.Fprintf(&b, "%d %s\n", expiry, key)
	}
	if c.file != nil {
		c.file.Close()
		c.file = nil
	}
	if err := writeAtomic(c.path, []byte(b.String())); err != nil {
		return err
	}
	f, err := os.OpenFile(c.path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	c.file, c.lines = f, len(c.seen)
	return nil
}

func (c *nonceCache) add(key string, now time.Time) error {
	if expiry, ok := c.seen[key]; ok && expiry > now.UnixMilli() {
		return errReplay
	}
	if c.lines > 4096 && c.lines > 2*len(c.seen) {
		if err := c.rewrite(now); err != nil {
			return err
		}
	}
	if len(c.seen) >= maxNonces {
		if err := c.rewrite(now); err != nil {
			return err
		}
		if len(c.seen) >= maxNonces {
			return errors.New("too many recent requests")
		}
	}
	expiry := now.Add(NonceTTL).UnixMilli()
	if _, err := fmt.Fprintf(c.file, "%d %s\n", expiry, key); err != nil {
		return err
	}
	c.seen[key] = expiry
	c.lines++
	return nil
}
