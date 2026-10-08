package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// login and pair have no built-in relay: with nothing configured they stop
// before any network request and say how to configure one.
func TestLoginAndPairWithoutRelay(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HESPER_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("HESPER_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("HESPER_RELAY", "")
	for _, args := range [][]string{
		{"login", "--name", "Mac", "--out", filepath.Join(dir, "host.credentials.json")},
		{"pair", "--name", "Mac", "--out", filepath.Join(dir, "pair.credentials.json"), "--invitation-file", writeTemp(t, dir, "inv", "token")},
	} {
		var stderr bytes.Buffer
		if code := execute(context.Background(), args, &stderr); code == 0 || !strings.Contains(stderr.String(), "no relay configured: pass --relay https://") || !strings.Contains(stderr.String(), "settings.json") {
			t.Fatalf("%s: exit %d, %q", args[0], code, stderr.String())
		}
	}
}

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
