package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The relay is self-hosted and optional: Hesper has no built-in relay.
// Commands that talk to one (hesperctl login, pair) find its origin with
// ResolveRelay. hesperd itself never needs this: it uses the origin stored
// in each credentials file.

// RelayEnv names the environment variable that sets the relay origin.
const RelayEnv = "HESPER_RELAY"

// ErrNoRelay means no relay origin is configured anywhere.
var ErrNoRelay = errors.New("no relay configured: pass --relay https://… or set relay in ~/.config/hesper/settings.json (see relay/README.md)")

// Relay origin sources, in the order ResolveRelay tries them.
const (
	RelayFromFlag        = "flag"
	RelayFromEnv         = "env"
	RelayFromSettings    = "settings"
	RelayFromCredentials = "credentials"
)

// ResolveRelay picks the relay origin: flag (--relay) when set, else
// $HESPER_RELAY, else "relay" in configDir/settings.json, else the origin
// recorded in stateDir's host.credentials.json or controller.credentials.json
// (so Macs enrolled before the setting existed keep their relay), else
// ErrNoRelay. It returns the origin and where it came from (RelayFrom…).
func ResolveRelay(flag, configDir, stateDir string) (string, string, error) {
	pick := func(value, source string) (string, string, error) {
		u, err := Origin(value)
		if err != nil {
			return "", "", fmt.Errorf("relay %q (%s): %w", value, describeRelaySource(source, configDir), err)
		}
		return u.String(), source, nil
	}
	if v := strings.TrimSpace(flag); v != "" {
		return pick(v, RelayFromFlag)
	}
	if v := strings.TrimSpace(os.Getenv(RelayEnv)); v != "" {
		return pick(v, RelayFromEnv)
	}
	if configDir != "" {
		v, err := settingsRelay(filepath.Join(configDir, "settings.json"))
		if err != nil {
			return "", "", err
		}
		if v != "" {
			return pick(v, RelayFromSettings)
		}
	}
	if stateDir != "" {
		for _, role := range []string{"host", "controller"} {
			if v := credentialsRelay(filepath.Join(stateDir, role+".credentials.json")); v != "" {
				return pick(v, RelayFromCredentials)
			}
		}
	}
	return "", "", ErrNoRelay
}

func describeRelaySource(source, configDir string) string {
	switch source {
	case RelayFromFlag:
		return "--relay"
	case RelayFromEnv:
		return "$" + RelayEnv
	case RelayFromSettings:
		return filepath.Join(configDir, "settings.json")
	}
	return "stored credentials"
}

// settingsRelay reads "relay" from settings.json; a missing file is no
// setting.
func settingsRelay(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var s struct {
		Relay string `json:"relay"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return strings.TrimSpace(s.Relay), nil
}

// credentialsRelay is the relay origin a credentials file records, or "".
func credentialsRelay(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var c struct {
		Relay string `json:"relay"`
	}
	if json.Unmarshal(data, &c) != nil {
		return ""
	}
	return strings.TrimSpace(c.Relay)
}
