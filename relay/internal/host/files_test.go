package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/transfer"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestFilesPutSealedChunksAndAudit(t *testing.T) {
	reg, project := testRegistry(t)
	a, err := reg.Spawn(wire.SpawnParams{Project: project, Task: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := transfer.GenerateKey()
	var mu sync.Mutex
	var audit []string
	s := &Service{Agents: reg, Handoff: &Handoff{Key: key}, Audit: func(event string, f map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(f)
		audit = append(audit, event+" "+string(b))
	}}
	ctx := WithCaller(context.Background(), Caller{Device: "laptop-1", Name: "laptop", Rights: []string{devicekey.Type}, Verified: true})
	exec := func(method string, params any) (json.RawMessage, error) {
		return s.Execute(ctx, protocol.Message{Method: method, Params: protocol.JSON(params), Deadline: time.Now().Add(20 * time.Second).UnixMilli()})
	}
	data := bytes.Repeat([]byte("SECRET-CONTENT "), 50000) // 750 KB: two chunks
	h := sha256.Sum256(data)
	sender, _ := transfer.NewSender(key.PublicKey(), "at-0011223344556677")
	raw, err := exec("files.put", map[string]any{"agent": localID(a.ID), "name": "notes.txt", "size": len(data), "sha256": hex.EncodeToString(h[:]),
		"upload": "at-0011223344556677", "epk": sender.EphemeralKey()})
	if err != nil {
		t.Fatal(err)
	}
	var put struct {
		Upload string
		Chunk  int
	}
	json.Unmarshal(raw, &put)
	if put.Upload != "at-0011223344556677" || put.Chunk != transfer.ChunkSize {
		t.Fatalf("put %s", raw)
	}
	first, _ := sender.Seal("notes.txt", 0, false, data[:transfer.ChunkSize])
	if _, err := exec("files.chunk", map[string]any{"upload": put.Upload, "offset": 0, "data": first, "last": false}); err != nil {
		t.Fatal(err)
	}
	last, _ := sender.Seal("notes.txt", 1, true, data[transfer.ChunkSize:])
	raw, err = exec("files.chunk", map[string]any{"upload": put.Upload, "offset": transfer.ChunkSize, "data": last, "last": true})
	if err != nil {
		t.Fatal(err)
	}
	var res struct{ Path string }
	json.Unmarshal(raw, &res)
	if got, _ := os.ReadFile(res.Path); !bytes.Equal(got, data) || !strings.Contains(res.Path, "/attachments/"+localID(a.ID)+"/notes.txt") {
		t.Fatalf("stored at %s", res.Path)
	}
	mu.Lock()
	lines := strings.Join(audit, "\n")
	mu.Unlock()
	if len(audit) != 1 || !strings.Contains(lines, `"ok":true`) || !strings.Contains(lines, "laptop-1") || strings.Contains(lines, "notes.txt") || strings.Contains(lines, "SECRET") {
		t.Fatalf("audit %s", lines)
	}

	// A chunk that does not open (sealed for another name) ends the
	// upload, audited as failed.
	sender2, _ := transfer.NewSender(key.PublicKey(), "at-2")
	if _, err := exec("files.put", map[string]any{"agent": localID(a.ID), "name": "x.txt", "size": 3, "sha256": hex.EncodeToString(h[:]),
		"upload": "at-2", "epk": sender2.EphemeralKey()}); err != nil {
		t.Fatal(err)
	}
	bad, _ := sender2.Seal("y.txt", 0, true, []byte("abc"))
	if _, err := exec("files.chunk", map[string]any{"upload": "at-2", "offset": 0, "data": bad, "last": true}); err == nil {
		t.Fatal("a chunk sealed for another file was accepted")
	}
	mu.Lock()
	if len(audit) != 2 || !strings.Contains(audit[1], `"ok":false`) {
		t.Fatalf("audit %v", audit)
	}
	mu.Unlock()
	// Plain (unsealed) chunks never reach a host upload; a local upload
	// is not reachable as a host one either.
	if reg.Files().Pending() != 0 {
		t.Fatalf("%d pending", reg.Files().Pending())
	}
	// No transfer key: no attachments.
	noKey := &Service{Agents: reg}
	if _, err := noKey.Execute(ctx, protocol.Message{Method: "files.put", Params: protocol.JSON(map[string]any{}), Deadline: time.Now().Add(time.Minute).UnixMilli()}); err == nil ||
		!strings.Contains(err.Error(), "no transfer key") {
		t.Fatalf("without a key: %v", err)
	}
}

func TestFilesPutShellNeedsTheShellRight(t *testing.T) {
	reg, project := testRegistry(t)
	sh, err := reg.Spawn(wire.SpawnParams{Project: project, Kind: wire.KindShell})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := transfer.GenerateKey()
	s := &Service{Agents: reg, Handoff: &Handoff{Key: key}, AllowShell: true}
	ctx := WithCaller(context.Background(), Caller{Device: "d", Rights: []string{devicekey.Type}, Verified: true})
	sender, _ := transfer.NewSender(key.PublicKey(), "at-3")
	_, err = s.Execute(ctx, protocol.Message{Method: "files.put", Deadline: time.Now().Add(time.Minute).UnixMilli(), Params: protocol.JSON(map[string]any{
		"agent": localID(sh.ID), "name": "a", "size": 1, "sha256": strings.Repeat("0", 64), "upload": "at-3", "epk": sender.EphemeralKey()})})
	if pe, ok := err.(*protocol.Error); !ok || pe.Code != "not_found" { // shells are invisible without the right
		t.Fatalf("files.put on a shell without the shell right: %v", err)
	}
}

func TestFilesNeedTheTypeRight(t *testing.T) {
	h := newAuthHarness(t)
	watcher := &devicekey.Software{Dir: t.TempDir()}
	h.approve("watch-1", watcher, "watcher", ApproveOptions{}, "observe")
	typist := &devicekey.Software{Dir: t.TempDir()}
	h.approve("type-1", typist, "typist", ApproveOptions{}, "observe", "type")
	for _, method := range []string{"files.put", "files.chunk"} {
		if _, err := h.a.Authorize(context.Background(), h.signed("watch-1", watcher, method, `{"upload":"at-1"}`, false)); !forbidden(err) {
			t.Errorf("%s by an observer: %v", method, err)
		}
		if _, err := h.a.Authorize(context.Background(), h.signed("type-1", typist, method, `{"upload":"at-1"}`, false)); err != nil {
			t.Errorf("%s with the type right: %v", method, err)
		}
	}
	// Chunks get no audit line each; files.put does (the upload's own
	// line comes when it ends).
	if a := h.audit(); strings.Contains(a, `"method":"files.chunk","ok":true`) || !strings.Contains(a, `"method":"files.put","ok":true`) {
		t.Fatalf("audit:\n%s", a)
	}
}
