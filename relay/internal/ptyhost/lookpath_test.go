package ptyhost

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The command is looked up on the agent's PATH (cfg.Env), directories with
// spaces included, never on the daemon's own PATH.
func TestLookPathUsesAgentPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "Android Studio.app", "Contents", "bin")
	os.MkdirAll(bin, 0o755)
	exe, _ := os.Executable()
	if err := os.Symlink(exe, filepath.Join(bin, "fakeagent")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "fakeagent"), []byte("#!/bin/sh\n"), 0o755) // in "." only
	t.Setenv("PATH", "/usr/bin:/bin")
	env := []string{"HOME=" + dir, "PATH=/nonexistent:" + bin + ":/usr/bin"}
	got, err := LookPath("fakeagent", env)
	if err != nil || got != filepath.Join(bin, "fakeagent") {
		t.Fatalf("LookPath = %q, %v", got, err)
	}
	// Not on the daemon's PATH: found only through the agent's.
	if _, err := exec.LookPath("fakeagent"); err == nil {
		t.Fatal("the daemon's PATH finds it: the test proves nothing")
	}
	// An empty entry is not the current directory.
	t.Chdir(dir)
	_, err = LookPath("fakeagent", []string{"PATH=:/usr/bin"})
	var nf *NotFoundError
	if !errors.As(err, &nf) || !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), `"fakeagent" not found on the agent's PATH (:/usr/bin)`) {
		t.Fatalf("err %v", err)
	}
	// No PATH in env: the daemon's.
	if got, err := LookPath("sh", nil); err != nil || got != "/bin/sh" {
		t.Fatalf("LookPath(sh, nil) = %q, %v", got, err)
	}

	// Start runs it from the agent's PATH.
	term, err := Start(Config{Argv: []string{"fakeagent"}, Env: append(env, "PTYHOST_HELPER=exit0"), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	<-term.Done()
	// And says plainly when it is not there.
	_, err = Start(Config{Argv: []string{"no-such-agent"}, Env: env})
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "Android Studio.app") {
		t.Fatalf("Start err %v", err)
	}
}
