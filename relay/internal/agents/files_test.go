package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// upload sends data through files.put / files.chunk on h's socket.
func (h *harness) upload(put FilePut, data []byte) (FileChunkResult, error) {
	h.t.Helper()
	if put.SHA256 == "" {
		put.SHA256 = sum(data)
	}
	var started FilePutResult
	if err := h.call("files.put", put, &started); err != nil {
		return FileChunkResult{}, err
	}
	if started.Chunk != FileChunk || !strings.HasPrefix(started.Upload, "at-") {
		h.t.Fatalf("files.put %+v", started)
	}
	for off := 0; ; off += FileChunk {
		end := min(off+FileChunk, len(data))
		last := end == len(data)
		var res FileChunkResult
		if err := h.call("files.chunk", FileChunkParams{Upload: started.Upload, Offset: int64(off), Data: data[off:end], Last: last}, &res); err != nil {
			return res, err
		}
		if last {
			return res, nil
		}
		if res.Received != int64(end) {
			h.t.Fatalf("received %d after %d", res.Received, end)
		}
	}
}

func wireErrCode(err error) string {
	if we, ok := err.(*wire.Error); ok {
		return we.Code
	}
	return ""
}

func TestFilesPutChunksIntoTheStateDir(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "hello"})
	data := bytes.Repeat([]byte("0123456789abcdef"), 1300*1024/16) // 1.3 MB: 3 chunks
	res, err := h.upload(FilePut{Agent: a.ID, Name: "shot one.png", Size: int64(len(data))}, data)
	if err != nil {
		t.Fatal(err)
	}
	_, local, _ := strings.Cut(a.ID, "/")
	want := filepath.Join(h.state, "attachments", local, "shot one.png")
	if res.Path != want || res.Size != int64(len(data)) {
		t.Fatalf("result %+v, want %s", res, want)
	}
	got, _ := os.ReadFile(res.Path)
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if st, _ := os.Stat(res.Path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if st, _ := os.Stat(filepath.Dir(res.Path)); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", st.Mode())
	}
	// The same name again: shot one-2.png; a bare local id works too.
	res2, err := h.upload(FilePut{Agent: local, Name: "shot one.png", Size: 3}, []byte("abc"))
	if err != nil || filepath.Base(res2.Path) != "shot one-2.png" {
		t.Fatalf("second %+v %v", res2, err)
	}
	// Nothing left behind.
	entries, _ := os.ReadDir(filepath.Dir(res.Path))
	if len(entries) != 2 || h.reg.Files().Pending() != 0 {
		t.Fatalf("left %v, %d pending", entries, h.reg.Files().Pending())
	}
	// An empty file is one empty last chunk.
	res3, err := h.upload(FilePut{Agent: a.ID, Name: "empty.txt", Size: 0}, nil)
	if err != nil || res3.Size != 0 {
		t.Fatalf("empty %+v %v", res3, err)
	}
	// A draft (no agent yet).
	res4, err := h.upload(FilePut{Machine: "L", Draft: "d-abc", Name: "notes.md", Size: 2}, []byte("hi"))
	if err != nil || res4.Path != filepath.Join(h.state, "attachments", "d-abc", "notes.md") {
		t.Fatalf("draft %+v %v", res4, err)
	}
}

func TestFilesPutRefusals(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "hello"})
	sha := sum([]byte("x"))
	for name, put := range map[string]FilePut{
		"too large":   {Agent: a.ID, Name: "big.bin", Size: MaxAttachment + 1, SHA256: sha},
		"bad sha":     {Agent: a.ID, Name: "a", Size: 1, SHA256: "ABC"},
		"no name":     {Agent: a.ID, Name: " \n ", Size: 1, SHA256: sha},
		"dots":        {Agent: a.ID, Name: "..", Size: 1, SHA256: sha},
		"long name":   {Agent: a.ID, Name: strings.Repeat("x", 121), Size: 1, SHA256: sha},
		"both":        {Agent: a.ID, Draft: "d-x", Name: "a", Size: 1, SHA256: sha},
		"neither":     {Name: "a", Size: 1, SHA256: sha},
		"bad draft":   {Machine: "L", Draft: "../up", Name: "a", Size: 1, SHA256: sha},
		"set by host": {Agent: a.ID, Name: "a", Size: 1, SHA256: sha, Upload: "x"},
	} {
		if err := h.call("files.put", put, nil); wireErrCode(err) != wire.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	err := h.call("files.put", FilePut{Agent: "L/nope00", Name: "a", Size: 1, SHA256: sha}, nil)
	if wireErrCode(err) != wire.CodeNotFound {
		t.Errorf("unknown agent: %v", err)
	}
	if err := h.call("files.put", FilePut{Agent: "Q/abc123", Name: "a", Size: 1, SHA256: sha}, nil); wireErrCode(err) != wire.CodeUnavailable {
		t.Errorf("remote without part R: %v", err)
	}
	if err := h.call("files.chunk", FileChunkParams{Upload: "at-none", Last: true}, nil); wireErrCode(err) != wire.CodeNotFound {
		t.Errorf("unknown upload: %v", err)
	}
	// The message names the file.
	err = h.call("files.put", FilePut{Agent: a.ID, Name: "big.bin", Size: MaxAttachment + 1, SHA256: sha}, nil)
	if err == nil || !strings.Contains(err.Error(), "big.bin is larger than 50 MB") {
		t.Errorf("size message: %v", err)
	}

	// A checksum mismatch discards the upload.
	data := []byte("hello world")
	if _, err := h.upload(FilePut{Agent: a.ID, Name: "x.txt", Size: int64(len(data)), SHA256: sum([]byte("other"))}, data); wireErrCode(err) != wire.CodeInvalid ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
	// A size mismatch too (fewer bytes than declared).
	if _, err := h.upload(FilePut{Agent: a.ID, Name: "x.txt", Size: 20, SHA256: sum(data)}, data); wireErrCode(err) != wire.CodeInvalid {
		t.Fatalf("short: %v", err)
	}
	// Out of order, and a short middle chunk.
	var started FilePutResult
	big := bytes.Repeat([]byte{7}, FileChunk+10)
	if err := h.call("files.put", FilePut{Agent: a.ID, Name: "o.bin", Size: int64(len(big)), SHA256: sum(big)}, &started); err != nil {
		t.Fatal(err)
	}
	err = h.call("files.chunk", FileChunkParams{Upload: started.Upload, Offset: FileChunk, Data: big[FileChunk:], Last: true}, nil)
	if wireErrCode(err) != wire.CodeInvalid || !strings.Contains(err.Error(), "expected offset 0") {
		t.Fatalf("out of order: %v", err)
	}
	if err := h.call("files.chunk", FileChunkParams{Upload: started.Upload, Data: big[:10]}, nil); wireErrCode(err) != wire.CodeInvalid {
		t.Fatalf("short middle chunk: %v", err)
	}
	// Still going: send it properly.
	if err := h.call("files.chunk", FileChunkParams{Upload: started.Upload, Data: big[:FileChunk]}, nil); err != nil {
		t.Fatal(err)
	}
	var res FileChunkResult
	if err := h.call("files.chunk", FileChunkParams{Upload: started.Upload, Offset: FileChunk, Data: big[FileChunk:], Last: true}, &res); err != nil {
		t.Fatal(err)
	}
	_, local, _ := strings.Cut(a.ID, "/")
	entries, _ := os.ReadDir(filepath.Join(h.state, "attachments", local))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload-") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestFilesUploadLimitAndExpiry(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "hello"})
	fs := h.reg.Files()
	now := time.Now()
	fs.mu.Lock()
	fs.now = func() time.Time { return now }
	fs.mu.Unlock()
	sha := sum([]byte("x"))
	for i := 0; i < maxUploads; i++ {
		if err := h.call("files.put", FilePut{Agent: a.ID, Name: "a", Size: 1, SHA256: sha}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.call("files.put", FilePut{Agent: a.ID, Name: "a", Size: 1, SHA256: sha}, nil); wireErrCode(err) != wire.CodeUnavailable {
		t.Fatalf("ninth upload: %v", err)
	}
	fs.mu.Lock()
	fs.now = func() time.Time { return now.Add(uploadIdle + time.Second) }
	fs.mu.Unlock()
	if err := h.call("files.put", FilePut{Agent: a.ID, Name: "a", Size: 1, SHA256: sha}, nil); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if fs.Pending() != 1 {
		t.Fatalf("%d pending", fs.Pending())
	}
}

func TestFilesProjectSetting(t *testing.T) {
	h := newHarness(t)
	time.Sleep(100 * time.Millisecond) // Serve has the listener
	h.close()
	writeJSON(t, filepath.Join(h.config, "settings.json"), map[string]any{"attachments": "project",
		"defaults": map[string]any{"kind": "claude", "kinds": map[string]string{"claude": "fake-claude", "shell": "fake-shell"}}})
	h.open()
	a := h.spawn(wire.SpawnParams{Task: "hello"})
	res, err := h.upload(FilePut{Agent: a.ID, Name: "a.txt", Size: 1}, []byte("x"))
	if err != nil || res.Path != filepath.Join(h.project, ".hesper", "attachments", "a.txt") {
		t.Fatalf("project %+v %v", res, err)
	}
	// Drafts always use the state dir.
	res, err = h.upload(FilePut{Machine: "L", Draft: "d-q", Name: "a.txt", Size: 1}, []byte("x"))
	if err != nil || res.Path != filepath.Join(h.state, "attachments", "d-q", "a.txt") {
		t.Fatalf("draft %+v %v", res, err)
	}
	h.close()
	writeJSON(t, filepath.Join(h.config, "settings.json"), map[string]any{"attachments": "repo"})
	if _, err := loadSettings(h.config); err == nil {
		t.Fatal("a bad attachments setting was accepted")
	}
}

func TestSanitizeName(t *testing.T) {
	for in, want := range map[string]string{
		"shot one.png":            "shot one.png",
		"  Bildschirmfoto ä.png ": "Bildschirmfoto ä.png",
		"../x":                    "x",
		"/etc/passwd":             "passwd",
		".hidden":                 "_hidden",
		"..":                      "",
		".":                       "",
		"a\nb\x00c.txt":           "abc.txt",
		"日本語.pdf":                 "日本語.pdf",
		"dir/":                    "dir",
		"":                        "",
	} {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
