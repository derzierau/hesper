package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
)

// TrustFile is trusted-hosts.json on a controller: each host's X25519 key,
// pinned when the controller first met it (pair-host, the first handoff or
// the first channel) and refused when it changes, until `hesperctl trust
// --reset`. One key per host serves transfers, approval codes and the
// channel's handshake.
const TrustFile = "trusted-hosts.json"

// TrustedHost is one pinned host. E2E is when this controller first spoke
// to it through the channel (Unix seconds): from then on it never falls
// back to plaintext with that host, whatever the relay says about the
// host's capabilities.
type TrustedHost struct {
	Name   string `json:"name"`
	Key    string `json:"key"`
	Pinned int64  `json:"pinned"`
	E2E    int64  `json:"e2e,omitempty"`
	// Direct is when this controller last reached the host on the direct
	// path (Unix seconds, updated at most hourly).
	Direct int64 `json:"direct,omitempty"`
}

// Trust is the content of trusted-hosts.json.
type Trust struct {
	Hosts map[string]TrustedHost `json:"hosts"`
}

// LoadTrust reads path; a missing file is empty.
func LoadTrust(path string) (Trust, error) {
	trust := Trust{Hosts: map[string]TrustedHost{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return trust, nil
	}
	if err != nil {
		return trust, err
	}
	if err := json.Unmarshal(data, &trust); err != nil {
		return trust, fmt.Errorf("%s: %w", path, err)
	}
	if trust.Hosts == nil {
		trust.Hosts = map[string]TrustedHost{}
	}
	return trust, nil
}

// SaveTrust writes path (mode 0600).
func SaveTrust(path string, trust Trust) error {
	data, _ := json.MarshalIndent(trust, "", "  ")
	return writeAtomic(path, append(data, '\n'))
}

// UpdateTrust runs change on the current pins under an exclusive lock
// (trusted-hosts.lock next to path) and saves them when change reports a
// change, so a fleet sync and a command never lose each other's pins.
func UpdateTrust(path string, change func(*Trust) (bool, error)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), "trusted-hosts.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	trust, err := LoadTrust(path)
	if err != nil {
		return err
	}
	changed, err := change(&trust)
	if err != nil || !changed {
		return err
	}
	return SaveTrust(path, trust)
}

// KeyChanged is the hard refusal for a host whose key differs from the
// pinned one (code key_changed).
func KeyChanged(short string, pinned TrustedHost) error {
	return protocol.Err("key_changed", fmt.Sprintf("%s presents a different host key than the one pinned on %s: someone between the machines (the relay) may be intercepting. If %s was reinstalled, compare its key there, run `hesperctl trust --reset %s` and pair again",
		short, time.Unix(pinned.Pinned, 0).Format("2006-01-02"), short, short))
}
