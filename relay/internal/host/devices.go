package host

// Approved controller devices (docs/remote-shell-contract.md, Part K): the
// host's allowlist controllers.json, the pending approval requests
// pending-approvals.json and the audit log, all in the host's state
// directory (~/.local/state/hesper). hesperd writes pending requests;
// `hesperctl approve` and `hesperctl devices-local` (run on the host)
// decide them. Every read-modify-write holds an exclusive flock on
// devices.lock; files are replaced atomically and are mode 0600.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
)

const (
	ControllersFile = "controllers.json"
	PendingFile     = "pending-approvals.json"
	AuditFile       = "audit.log"
	NoncesFile      = "nonces.log"
	lockFile        = "devices.lock"

	// PendingTTL is how long a request for approval waits.
	PendingTTL = 10 * time.Minute
	// MaxPending bounds the requests waiting at once.
	MaxPending = 5
	// maxAuditBytes rotates audit.log to audit.log.1.
	maxAuditBytes = 10 << 20
)

var deviceID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Controller is one approved device in controllers.json.
type Controller struct {
	Device     string   `json:"device"`
	Name       string   `json:"name"`
	Key        string   `json:"key"`
	StrongKey  string   `json:"strongKey"`
	Hardware   bool     `json:"hardware"`
	Rights     []string `json:"rights"`
	Approved   int64    `json:"approved"`
	ApprovedBy string   `json:"approvedBy"`
	// E2EKey is the device's end-to-end static key (base64 X25519) as it
	// was bound at approval (informational: every handshake checks the
	// binding signature against Key).
	E2EKey string `json:"e2eKey,omitempty"`
}

// Controllers is controllers.json.
type Controllers struct {
	Version     int          `json:"version"`
	Controllers []Controller `json:"controllers"`
}

// Pending is a controller waiting for local approval.
type Pending struct {
	Device    string   `json:"device"`
	Name      string   `json:"name"`
	Key       string   `json:"key"`
	StrongKey string   `json:"strongKey"`
	Hardware  bool     `json:"hardware"`
	Rights    []string `json:"rights"`
	Code      string   `json:"code"`
	Requested int64    `json:"requested"`
	Expires   int64    `json:"expires"`
	E2EKey    string   `json:"e2eKey,omitempty"`
}

type pendingFile struct {
	Denied  []Denied  `json:"denied,omitempty"`
	Version int       `json:"version"`
	Pending []Pending `json:"pending"`
}

// ValidName: 1 to 40 printable characters, no leading or trailing space.
func ValidName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > 40 || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// validRights: known, no repeats, at least one.
func validRights(rights []string) bool {
	if len(rights) == 0 || len(rights) > len(devicekey.AllRights) {
		return false
	}
	seen := map[string]bool{}
	for _, r := range rights {
		if !devicekey.ValidRight(r) || seen[r] {
			return false
		}
		seen[r] = true
	}
	return true
}

// SortRights orders rights as devicekey.AllRights.
func SortRights(rights []string) []string {
	out := []string{}
	for _, r := range devicekey.AllRights {
		if slices.Contains(rights, r) {
			out = append(out, r)
		}
	}
	return out
}

func (c Controller) validate() error {
	if !deviceID.MatchString(c.Device) {
		return fmt.Errorf("controller %q: invalid device ID", c.Name)
	}
	if !ValidName(c.Name) {
		return fmt.Errorf("controller %q: invalid name", c.Name)
	}
	if _, _, err := devicekey.ParsePublicKey(c.Key); err != nil {
		return fmt.Errorf("controller %q: key: %w", c.Name, err)
	}
	if _, _, err := devicekey.ParsePublicKey(c.StrongKey); err != nil {
		return fmt.Errorf("controller %q: strongKey: %w", c.Name, err)
	}
	if c.Key == c.StrongKey {
		return fmt.Errorf("controller %q: key and strongKey must differ", c.Name)
	}
	if !validRights(c.Rights) {
		return fmt.Errorf("controller %q: invalid rights", c.Name)
	}
	return nil
}

// strictJSON decodes data into v: valid UTF-8, no repeated keys, no unknown
// fields, nothing after the value.
func strictJSON(data []byte, v any) error {
	if _, err := devicekey.NormalizeParams(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// Store is the host's device state in Dir.
type Store struct {
	Dir string
}

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

// lock takes the exclusive devices lock.
func (s Store) lock() (func(), error) {
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.path(lockFile), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// LoadControllers reads controllers.json. A missing file is an empty list
// (exists false). Anything invalid is an error: callers then refuse every
// request (fail closed).
func (s Store) LoadControllers() (list Controllers, exists bool, err error) {
	data, err := devicekey.ReadPrivateFile(s.path(ControllersFile))
	if errors.Is(err, os.ErrNotExist) {
		return Controllers{Version: 1}, false, nil
	}
	if err != nil {
		return list, true, err
	}
	if err := strictJSON(data, &list); err != nil {
		return list, true, fmt.Errorf("%s: %w", ControllersFile, err)
	}
	if list.Version != 1 {
		return list, true, fmt.Errorf("%s: unsupported version %d", ControllersFile, list.Version)
	}
	devices, names := map[string]bool{}, map[string]bool{}
	for _, c := range list.Controllers {
		if err := c.validate(); err != nil {
			return list, true, fmt.Errorf("%s: %w", ControllersFile, err)
		}
		if devices[c.Device] || names[strings.ToLower(c.Name)] {
			return list, true, fmt.Errorf("%s: %q appears twice", ControllersFile, c.Name)
		}
		devices[c.Device], names[strings.ToLower(c.Name)] = true, true
	}
	return list, true, nil
}

func (s Store) saveControllers(list Controllers) error {
	list.Version = 1
	if list.Controllers == nil {
		list.Controllers = []Controller{}
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	return writeAtomic(s.path(ControllersFile), append(data, '\n'))
}

// Denied remembers a denied request for PendingTTL, so a controller that
// keeps asking does not show up again right away.
type Denied struct {
	Device string `json:"device"`
	Key    string `json:"key"`
	Until  int64  `json:"until"`
}

// loadPending returns the requests and denials that have not expired. A
// damaged file is treated as empty: pending requests grant nothing.
func (s Store) loadPending(now time.Time) pendingFile {
	out := pendingFile{Version: 1, Pending: []Pending{}}
	data, err := devicekey.ReadPrivateFile(s.path(PendingFile))
	if err != nil {
		return out
	}
	var f pendingFile
	if strictJSON(data, &f) != nil || f.Version != 1 {
		return out
	}
	for _, p := range f.Pending {
		if p.Expires > now.Unix() {
			out.Pending = append(out.Pending, p)
		}
	}
	for _, d := range f.Denied {
		if d.Until > now.Unix() {
			out.Denied = append(out.Denied, d)
		}
	}
	return out
}

func (s Store) savePending(f pendingFile) error {
	f.Version = 1
	if f.Pending == nil {
		f.Pending = []Pending{}
	}
	data, _ := json.MarshalIndent(f, "", "  ")
	return writeAtomic(s.path(PendingFile), append(data, '\n'))
}

// PendingRequests lists the requests waiting for approval.
func (s Store) PendingRequests(now time.Time) ([]Pending, error) {
	f, err := s.pendingState(now)
	return f.Pending, err
}

func (s Store) pendingState(now time.Time) (pendingFile, error) {
	unlock, err := s.lock()
	if err != nil {
		return pendingFile{}, err
	}
	defer unlock()
	return s.loadPending(now), nil
}

var (
	// ErrPendingFull means MaxPending requests already wait.
	ErrPendingFull = errors.New("too many devices are waiting for approval")
	// ErrDenied means this device's key was denied within PendingTTL.
	ErrDenied = errors.New("the request was denied on the host")
)

// addPending records p, replacing an earlier request of the same device.
func (s Store) addPending(p Pending, now time.Time) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	f := s.loadPending(now)
	for _, d := range f.Denied {
		if d.Device == p.Device || d.Key == p.Key {
			return ErrDenied
		}
	}
	f.Pending = slices.DeleteFunc(f.Pending, func(q Pending) bool { return q.Device == p.Device })
	if len(f.Pending) >= MaxPending {
		return ErrPendingFull
	}
	f.Pending = append(f.Pending, p)
	return s.savePending(f)
}

// findPending matches a code ("ABC-DEF", any case and spacing) or a name.
func findPending(list []Pending, query string) (int, error) {
	code := devicekey.NormalizeCode(query)
	match := -1
	for i, p := range list {
		if (code != "" && p.Code == code) || strings.EqualFold(p.Name, query) {
			if match >= 0 {
				return -1, fmt.Errorf("%q matches more than one waiting device; use its code", query)
			}
			match = i
		}
	}
	if match < 0 {
		return -1, fmt.Errorf("no device waiting for approval matches %q", query)
	}
	return match, nil
}

// ApproveOptions adjust an approval.
type ApproveOptions struct {
	Rights             []string // instead of the requested rights
	Name               string   // instead of the requested name
	AllowSoftwareShell bool     // grant shell to a device without hardware keys
	By                 string   // approvedBy (default "local")
}

// Approve moves a pending request into controllers.json. A device that
// does not claim hardware keys never gets the shell right unless
// AllowSoftwareShell. The returned note says what was changed.
func (s Store) Approve(query string, opts ApproveOptions, now time.Time) (Controller, string, error) {
	unlock, err := s.lock()
	if err != nil {
		return Controller{}, "", err
	}
	defer unlock()
	f := s.loadPending(now)
	i, err := findPending(f.Pending, query)
	if err != nil {
		return Controller{}, "", err
	}
	p := f.Pending[i]
	rights := p.Rights
	if opts.Rights != nil {
		rights = opts.Rights
	}
	note := ""
	if !p.Hardware && slices.Contains(rights, devicekey.Shell) && !opts.AllowSoftwareShell {
		rights = slices.DeleteFunc(slices.Clone(rights), func(r string) bool { return r == devicekey.Shell })
		note = "shell not granted: the device has no hardware keys (--allow-software-shell grants it)"
	}
	if !validRights(rights) {
		return Controller{}, "", fmt.Errorf("invalid or empty rights %v (known: %s)", rights, strings.Join(devicekey.AllRights, ", "))
	}
	name := p.Name
	if opts.Name != "" {
		name = opts.Name
	}
	by := opts.By
	if by == "" {
		by = "local"
	}
	c := Controller{Device: p.Device, Name: name, Key: p.Key, StrongKey: p.StrongKey, Hardware: p.Hardware,
		Rights: SortRights(rights), Approved: now.Unix(), ApprovedBy: by, E2EKey: p.E2EKey}
	if err := c.validate(); err != nil {
		return Controller{}, "", err
	}
	list, _, err := s.LoadControllers()
	if err != nil {
		return Controller{}, "", err
	}
	kept := []Controller{}
	for _, old := range list.Controllers {
		if old.Device == c.Device {
			continue // the same device with new keys or rights
		}
		if strings.EqualFold(old.Name, c.Name) {
			return Controller{}, "", fmt.Errorf("a controller named %q exists; revoke it first or approve with --name", old.Name)
		}
		kept = append(kept, old)
	}
	list.Controllers = append(kept, c)
	if err := s.saveControllers(list); err != nil {
		return Controller{}, "", err
	}
	f.Pending = slices.Delete(f.Pending, i, i+1)
	if err := s.savePending(f); err != nil {
		return c, note, err
	}
	s.Audit("device.approved", map[string]any{"device": c.Device, "name": c.Name, "ok": true,
		"detail": "rights " + strings.Join(c.Rights, ",") + " code " + p.Code})
	return c, note, nil
}

// Deny drops a pending request.
func (s Store) Deny(query string, now time.Time) (Pending, error) {
	unlock, err := s.lock()
	if err != nil {
		return Pending{}, err
	}
	defer unlock()
	f := s.loadPending(now)
	i, err := findPending(f.Pending, query)
	if err != nil {
		return Pending{}, err
	}
	p := f.Pending[i]
	f.Pending = slices.Delete(f.Pending, i, i+1)
	f.Denied = append(f.Denied, Denied{Device: p.Device, Key: p.Key, Until: now.Add(PendingTTL).Unix()})
	if err := s.savePending(f); err != nil {
		return p, err
	}
	s.Audit("device.denied", map[string]any{"device": p.Device, "name": p.Name, "ok": true, "detail": "code " + p.Code})
	return p, nil
}

// Revoke removes an approved controller by name or device ID. Its requests
// are refused from the next one on.
func (s Store) Revoke(query string) (Controller, error) {
	unlock, err := s.lock()
	if err != nil {
		return Controller{}, err
	}
	defer unlock()
	list, _, err := s.LoadControllers()
	if err != nil {
		return Controller{}, err
	}
	match := -1
	for i, c := range list.Controllers {
		if c.Device == query || strings.EqualFold(c.Name, query) {
			if match >= 0 {
				return Controller{}, fmt.Errorf("%q matches more than one controller; use its device ID", query)
			}
			match = i
		}
	}
	if match < 0 {
		return Controller{}, fmt.Errorf("no approved controller %q", query)
	}
	c := list.Controllers[match]
	list.Controllers = slices.Delete(list.Controllers, match, match+1)
	if err := s.saveControllers(list); err != nil {
		return c, err
	}
	s.Audit("device.revoked", map[string]any{"device": c.Device, "name": c.Name, "ok": true})
	return c, nil
}

var auditMu sync.Mutex

// Audit appends one JSON line {at, event, device, name, method, ok, detail}
// to audit.log (mode 0600; rotated to audit.log.1 at 10 MiB). fields may
// set device, name, method, ok, detail and e2e; other keys are ignored. Failures
// to write are reported on stderr: auditing never blocks a refusal.
func (s Store) Audit(event string, fields map[string]any) {
	entry := struct {
		At     string `json:"at"`
		Event  string `json:"event"`
		Device string `json:"device,omitempty"`
		Name   string `json:"name,omitempty"`
		Method string `json:"method,omitempty"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail,omitempty"`
		// E2E: whether the request (or session) used the end-to-end
		// channel; absent for events without a request.
		E2E *bool `json:"e2e,omitempty"`
		// Route: "direct" or "relay", how the request reached the host.
		Route string `json:"route,omitempty"`
	}{At: time.Now().UTC().Format(time.RFC3339Nano), Event: event}
	text := func(key string) string {
		v, _ := fields[key].(string)
		if len(v) > 512 {
			v = v[:512]
		}
		return strings.ToValidUTF8(v, "?")
	}
	entry.Device, entry.Name, entry.Method, entry.Detail, entry.Route = text("device"), text("name"), text("method"), text("detail"), text("route")
	entry.OK, _ = fields["ok"].(bool)
	if viaE2E, ok := fields["e2e"].(bool); ok {
		entry.E2E = &viaE2E
	}
	line, _ := json.Marshal(entry)
	auditMu.Lock()
	defer auditMu.Unlock()
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		return
	}
	path := s.path(AuditFile)
	if info, err := os.Stat(path); err == nil && info.Size() > maxAuditBytes {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
	}
}

// writeAtomic replaces path with data (mode 0600) through a synced
// temporary file and rename.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
