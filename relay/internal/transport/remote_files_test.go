package transport_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// sendFile uploads data through n's socket (files.put, files.chunk), as
// the app does.
func sendFile(t *testing.T, n *node, put agents.FilePut, data []byte) (agents.FileChunkResult, error) {
	t.Helper()
	if put.SHA256 == "" {
		h := sha256.Sum256(data)
		put.SHA256 = hex.EncodeToString(h[:])
	}
	put.Size = int64(len(data))
	c := n.client(t)
	var started agents.FilePutResult
	if err := c.Call(t.Context(), "files.put", put, &started); err != nil {
		return agents.FileChunkResult{}, err
	}
	for off := 0; ; off += started.Chunk {
		end := min(off+started.Chunk, len(data))
		last := end == len(data)
		var res agents.FileChunkResult
		if err := c.Call(t.Context(), "files.chunk", agents.FileChunkParams{Upload: started.Upload, Offset: int64(off), Data: data[off:end], Last: last}, &res); err != nil {
			return res, err
		}
		if last {
			return res, nil
		}
	}
}

// A file dropped on a remote agent: L's daemon seals it for M, M stores
// it in its state directory and answers with its own path.
func TestRemoteFilePutLandsOnTheAgentsMachine(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	var a wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: "hello"}, &a); err != nil {
		t.Fatal(err)
	}
	marker := []byte("SECRET-ATTACHMENT-9931 ")
	data := bytes.Repeat(marker, 1200*1024/len(marker)) // ~1.2 MB: three chunks
	res, err := sendFile(t, L, agents.FilePut{Agent: a.ID, Name: "Bildschirmfoto 1.png"}, data)
	if err != nil {
		t.Fatal(err)
	}
	_, local, _ := strings.Cut(a.ID, "/")
	if want := filepath.Join(M.state, "attachments", local, "Bildschirmfoto 1.png"); res.Path != want || res.Size != int64(len(data)) {
		t.Fatalf("result %+v, want %s", res, want)
	}
	if got, _ := os.ReadFile(res.Path); !bytes.Equal(got, data) {
		t.Fatal("the stored file differs")
	}
	// A draft for M (no agent yet).
	res, err = sendFile(t, L, agents.FilePut{Machine: "M", Draft: "d-remote1", Name: "brief.md"}, []byte("SECRET-ATTACHMENT-9931 brief"))
	if err != nil || res.Path != filepath.Join(M.state, "attachments", "d-remote1", "brief.md") {
		t.Fatalf("draft %+v %v", res, err)
	}
	// A checksum that does not match is refused, nothing stored.
	_, err = sendFile(t, L, agents.FilePut{Agent: a.ID, Name: "bad.txt", SHA256: strings.Repeat("0", 64)}, []byte("SECRET-ATTACHMENT-9931 bad"))
	if wireCode(err) != wire.CodeInvalid || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("mismatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(M.state, "attachments", local, "bad.txt")); err == nil {
		t.Fatal("a file with the wrong checksum was stored")
	}
	// Too large: refused before anything travels.
	err = L.call(t, "files.put", agents.FilePut{Agent: a.ID, Name: "big.bin", Size: agents.MaxAttachment + 1, SHA256: strings.Repeat("0", 64)}, nil)
	if wireCode(err) != wire.CodeInvalid || !strings.Contains(err.Error(), "larger than 50 MB") {
		t.Fatalf("too large: %v", err)
	}
	// The relay saw neither the content nor the name nor the methods.
	if leaked := w.tap.sawAny("SECRET-ATTACHMENT-9931", "Bildschirmfoto", "brief.md"); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}
	if w.tap.count(`"method":"files.`) != 0 {
		t.Fatal("a files method went in plaintext")
	}
}

func TestRemoteFilePutNeedsTheTypeRight(t *testing.T) {
	w := newWorld(t, worldOptions{rights: []string{"observe"}})
	w.L.waitLinked(t, "M")
	var a wire.Agent
	if err := w.M.call(t, "agents.spawn", wire.SpawnParams{Project: w.M.project, Task: "hello"}, &a); err != nil {
		t.Fatal(err)
	}
	id := localOn(a.ID, "M")
	w.L.waitAgent(t, id, func(wire.Agent) bool { return true })
	_, err := sendFile(t, w.L, agents.FilePut{Agent: id, Name: "a.txt"}, []byte("SECRET-ATTACHMENT-9931"))
	if wireCode(err) != wire.CodeForbidden {
		t.Fatalf("files.put by an observer: %v", err)
	}
}
