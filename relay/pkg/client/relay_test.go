package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRelayOrder(t *testing.T) {
	config, state := t.TempDir(), t.TempDir()
	t.Setenv(RelayEnv, "")
	check := func(flag, wantOrigin, wantSource string) {
		t.Helper()
		origin, source, err := ResolveRelay(flag, config, state)
		if err != nil || origin != wantOrigin || source != wantSource {
			t.Fatalf("ResolveRelay(%q) = %q, %q, %v; want %q, %q", flag, origin, source, err, wantOrigin, wantSource)
		}
	}

	// Nothing configured: a clear error naming what to do.
	if _, _, err := ResolveRelay("", config, state); !errors.Is(err, ErrNoRelay) || !strings.Contains(err.Error(), "--relay") || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("no relay: %v", err)
	}

	// Enrolled before the setting existed: the credentials' relay. The host
	// enrollment wins over the controller's.
	writeFile(t, filepath.Join(state, "controller.credentials.json"), `{"relay":"https://controller.example","deviceId":"d2","role":"controller","token":"t"}`)
	check("", "https://controller.example", RelayFromCredentials)
	writeFile(t, filepath.Join(state, "host.credentials.json"), `{"relay":"https://host.example","deviceId":"d1","role":"host","token":"t"}`)
	check("", "https://host.example", RelayFromCredentials)

	// settings.json beats the credentials; other settings are ignored.
	writeFile(t, filepath.Join(config, "settings.json"), `{"machine":"L","defaults":{"kind":"codex"},"relay":"https://settings.example/"}`)
	check("", "https://settings.example", RelayFromSettings)

	// $HESPER_RELAY beats settings.json.
	t.Setenv(RelayEnv, "https://env.example")
	check("", "https://env.example", RelayFromEnv)

	// --relay beats everything.
	check("https://flag.example", "https://flag.example", RelayFromFlag)
	check("http://localhost:8080", "http://localhost:8080", RelayFromFlag)
}

func TestResolveRelaySettingsWithoutRelayFallsThrough(t *testing.T) {
	config, state := t.TempDir(), t.TempDir()
	t.Setenv(RelayEnv, "")
	writeFile(t, filepath.Join(config, "settings.json"), `{"machine":"L"}`)
	if _, _, err := ResolveRelay("", config, state); !errors.Is(err, ErrNoRelay) {
		t.Fatalf("settings without relay: %v", err)
	}
	writeFile(t, filepath.Join(state, "host.credentials.json"), `{"relay":"https://host.example","role":"host"}`)
	if origin, source, err := ResolveRelay("", config, state); err != nil || origin != "https://host.example" || source != RelayFromCredentials {
		t.Fatalf("got %q %q %v", origin, source, err)
	}
}

func TestResolveRelayRejectsBadOrigins(t *testing.T) {
	config, state := t.TempDir(), t.TempDir()
	t.Setenv(RelayEnv, "")
	if _, _, err := ResolveRelay("http://remote.example", config, state); err == nil || !strings.Contains(err.Error(), "--relay") {
		t.Fatalf("plain HTTP flag: %v", err)
	}
	t.Setenv(RelayEnv, "relay.example.com")
	if _, _, err := ResolveRelay("", config, state); err == nil || !strings.Contains(err.Error(), RelayEnv) {
		t.Fatalf("env without scheme: %v", err)
	}
	t.Setenv(RelayEnv, "")
	writeFile(t, filepath.Join(config, "settings.json"), `{"relay": 3}`)
	if _, _, err := ResolveRelay("", config, state); err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("malformed settings: %v", err)
	}
}

func TestSignInHintWithoutRecordedRelay(t *testing.T) {
	err := &SignInRequiredError{Path: "/tmp/host.credentials.json", Role: "host", Err: errors.New("unauthorized")}
	msg := err.Error()
	if strings.Contains(msg, "--relay") || !strings.Contains(msg, "hesperctl login --role host") {
		t.Fatal(msg)
	}
}
