package devicekey

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// OpenPrivateFile checks the file it opened: a symlink is refused, not
// followed, and a FIFO is refused without blocking on it.
func TestOpenPrivateFileRefusesSymlinksAndFIFOs(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenPrivateFile(target)
	if err != nil {
		t.Fatalf("regular file: %v", err)
	}
	f.Close()
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenPrivateFile(target); err == nil {
		f.Close()
		t.Fatal("file readable by others opened")
	}
	if err := os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := OpenPrivateFile(link); err == nil {
		f.Close()
		t.Fatal("symlink followed")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := OpenPrivateFile(fifo)
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO opened")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}
