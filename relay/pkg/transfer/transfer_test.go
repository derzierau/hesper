package transfer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSealOpenRoundTripAndTamperRejection(t *testing.T) {
	host, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSender(host.PublicKey(), "ho-1")
	if err != nil {
		t.Fatal(err)
	}
	full := bytes.Repeat([]byte("a"), ChunkSize)
	first, err := s.Seal("code.bundle", 0, false, full)
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.Seal("code.bundle", 1, true, []byte("tail"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != ChunkSize+Overhead || bytes.Contains(first, []byte("aaaa")) {
		t.Fatal("chunk is not sealed")
	}
	o, err := NewOpener(host, s.EphemeralKey(), "ho-1", "code.bundle")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := o.Open(0, false, first); err != nil || !bytes.Equal(plain, full) {
		t.Fatal("first chunk did not round trip", err)
	}
	if plain, err := o.Open(1, true, last); err != nil || string(plain) != "tail" {
		t.Fatal("last chunk did not round trip", err)
	}
	if _, err := o.Open(1, false, last); err == nil {
		t.Fatal("last chunk accepted as a middle chunk (truncation)")
	}
	if _, err := o.Open(0, true, last); err == nil {
		t.Fatal("chunk accepted at another index")
	}
	tampered := append([]byte{}, last...)
	tampered[0] ^= 1
	if _, err := o.Open(1, true, tampered); err == nil {
		t.Fatal("tampered chunk accepted")
	}
	for _, other := range []struct{ upload, name string }{{"ho-2", "code.bundle"}, {"ho-1", "brief.md"}} {
		wrong, err := NewOpener(host, s.EphemeralKey(), other.upload, other.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wrong.Open(1, true, last); err == nil {
			t.Fatalf("chunk accepted for %+v", other)
		}
	}
	stranger, _ := GenerateKey()
	if o, err := NewOpener(stranger, s.EphemeralKey(), "ho-1", "code.bundle"); err == nil {
		if _, err := o.Open(1, true, last); err == nil {
			t.Fatal("another host decrypted the chunk")
		}
	}
	if _, err := s.Seal("code.bundle", 2, false, []byte("short")); err == nil {
		t.Fatal("short middle chunk accepted")
	}
}

func TestLoadOrCreateKeyIsPrivateAndStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "host.transfer.key")
	a, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key file must be 0600", err)
	}
	b, err := LoadOrCreateKey(path)
	if err != nil || !a.Equal(b) {
		t.Fatal("key changed between loads", err)
	}
	if p, err := ParsePublicKey(EncodePublicKey(a.PublicKey())); err != nil || !p.Equal(a.PublicKey()) {
		t.Fatal("public key encoding does not round trip")
	}
	os.Chmod(path, 0644)
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("world-readable key accepted")
	}
	if _, err := ParsePublicKey("c2hvcnQ="); err == nil {
		t.Fatal("short public key accepted")
	}
}

func TestDownloadRoundTripIsBoundToBothKeysAndDirection(t *testing.T) {
	host, _ := GenerateKey()
	requester, _ := GenerateKey()
	sealer, err := NewHostDownload(host, EncodePublicKey(requester.PublicKey()), "dl-1")
	if err != nil {
		t.Fatal(err)
	}
	full := bytes.Repeat([]byte("b"), ChunkSize)
	first, err := sealer.Seal("code.bundle", 0, false, full)
	if err != nil {
		t.Fatal(err)
	}
	last, err := sealer.Seal("code.bundle", 1, true, []byte("tail"))
	if err != nil {
		t.Fatal(err)
	}
	opener := NewRequesterDownload(requester, host.PublicKey(), "dl-1")
	if plain, err := opener.Open("code.bundle", 0, false, first); err != nil || !bytes.Equal(plain, full) {
		t.Fatal("first chunk did not round trip", err)
	}
	if plain, err := opener.Open("code.bundle", 1, true, last); err != nil || string(plain) != "tail" {
		t.Fatal("last chunk did not round trip", err)
	}
	for name, o := range map[string]*Download{
		"truncation":   opener,
		"other id":     NewRequesterDownload(requester, host.PublicKey(), "dl-2"),
		"other host":   NewRequesterDownload(requester, requester.PublicKey(), "dl-1"),
		"other reader": func() *Download { k, _ := GenerateKey(); return NewRequesterDownload(k, host.PublicKey(), "dl-1") }(),
	} {
		if _, err := o.Open("code.bundle", 1, name != "truncation", last); err == nil {
			t.Fatalf("%s: chunk accepted", name)
		}
	}
	if _, err := opener.Open("brief.md", 1, true, last); err == nil {
		t.Fatal("chunk accepted for another file")
	}
	// The same keys and names in the upload direction never open a download.
	if o, err := NewOpener(host, EncodePublicKey(requester.PublicKey()), "dl-1", "code.bundle"); err != nil {
		t.Fatal(err)
	} else if _, err := o.Open(1, true, last); err == nil {
		t.Fatal("a download chunk opened as an upload chunk")
	}
	if _, err := sealer.Seal("code.bundle", 2, false, []byte("short")); err == nil {
		t.Fatal("short middle chunk accepted")
	}
	if _, err := NewHostDownload(host, "bad", "dl-1"); err == nil {
		t.Fatal("invalid requester key accepted")
	}
}
