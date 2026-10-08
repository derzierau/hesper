package host

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crypto/rand"
	"sync"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
	"github.com/derzierau/hesper/relay/pkg/transfer"
)

// startHandoff runs a Handoff whose Pack writes a fake bundle (manifest
// and a 1.2 MB code.bundle; packGate holds it until closed, packErr fails it) and whose
// Import reads the upload's manifest (importErr fails it).
type fakeMoves struct {
	mu        sync.Mutex
	packGate  chan struct{}
	packErr   error
	importErr error
	imported  []string
}

func startHandoff(t *testing.T, dir string, now func() time.Time) (*Handoff, *fakeMoves, context.CancelFunc) {
	t.Helper()
	key, err := transfer.LoadOrCreateKey(filepath.Join(dir, "host.transfer.key"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeMoves{}
	h := &Handoff{Dir: filepath.Join(dir, "uploads"), Key: key, Now: now,
		Pack: func(ctx context.Context, id string, have []string, out string) error {
			f.mu.Lock()
			gate, perr := f.packGate, f.packErr
			f.mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if perr != nil {
				return perr
			}
			os.MkdirAll(out, 0o700)
			os.WriteFile(filepath.Join(out, "manifest.json"), []byte(`{"version":2,"id":"ho-0123456789ab","agent":"`+id+`"}`), 0o600)
			return os.WriteFile(filepath.Join(out, "code.bundle"), bytes.Repeat([]byte("0123456789"), 120000), 0o600)
		},
		Import: func(ctx context.Context, dir string) (json.RawMessage, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.importErr != nil {
				return nil, f.importErr
			}
			data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				return nil, protocol.Err("invalid", "no manifest")
			}
			f.imported = append(f.imported, string(data))
			return json.RawMessage(`{"id":"mini/abc123","kind":"claude"}`), nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); h.Wait() })
	return h, f, cancel
}

// upload seals and sends one file the way hesperctl does.
func upload(t *testing.T, h *Handoff, upload, name string, data []byte) error {
	t.Helper()
	s, err := transfer.NewSender(h.Key.PublicKey(), upload)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range chunks(t, s, upload, name, data) {
		if _, err := h.Transfer(p); err != nil {
			return err
		}
	}
	return nil
}

func chunks(t *testing.T, s *transfer.Sender, upload, name string, data []byte) []transferParams {
	t.Helper()
	sum := sha256.Sum256(data)
	n := max(1, (len(data)+transfer.ChunkSize-1)/transfer.ChunkSize)
	var out []transferParams
	for i := range n {
		offset := i * transfer.ChunkSize
		last := i == n-1
		sealed, err := s.Seal(name, uint64(i), last, data[offset:min(offset+transfer.ChunkSize, len(data))])
		if err != nil {
			t.Fatal(err)
		}
		p := transferParams{Upload: upload, Name: name, Offset: int64(offset), Data: sealed, Last: last}
		if last {
			p.SHA256, p.EPK = hex.EncodeToString(sum[:]), s.EphemeralKey()
		}
		out = append(out, p)
	}
	return out
}

func code(err error) string {
	var p *protocol.Error
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestTransferStagesDecryptsAndVerifies(t *testing.T) {
	h, _, _ := startHandoff(t, t.TempDir(), nil)
	data := bytes.Repeat([]byte("0123456789abcdef"), (2*transfer.ChunkSize+100)/16)
	s, _ := transfer.NewSender(h.Key.PublicKey(), "ho-1")
	parts := chunks(t, s, "ho-1", "code.bundle", data)
	if len(parts) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(parts))
	}
	if _, err := h.Transfer(parts[1]); code(err) != "invalid_request" {
		t.Fatal("out-of-order chunk accepted", err)
	}
	for _, p := range parts[:2] {
		if _, err := h.Transfer(p); err != nil {
			t.Fatal(err)
		}
	}
	// A retry of the previous chunk after an uncertain timeout is accepted.
	if _, err := h.Transfer(parts[1]); err != nil {
		t.Fatal("retry rejected", err)
	}
	if bytes.Contains(parts[2].Data, []byte("0123456789")) {
		t.Fatal("data travels in plaintext")
	}
	result, err := h.Transfer(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"received":`+itoa(len(data))+`}` {
		t.Fatalf("result %s", result)
	}
	got, err := os.ReadFile(filepath.Join(h.Dir, "ho-1", "code.bundle"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("file did not round trip", err)
	}
	for path, mode := range map[string]os.FileMode{h.Dir: 0700, filepath.Join(h.Dir, "ho-1"): 0700, filepath.Join(h.Dir, "ho-1", "code.bundle"): 0600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s must be %o", path, mode)
		}
	}
	if _, err := os.Stat(filepath.Join(h.Dir, "ho-1", "code.bundle.part")); !os.IsNotExist(err) {
		t.Fatal("sealed part left behind")
	}

	// Wrong hash and tampered data leave no file behind.
	bad := chunks(t, s, "ho-1", "transcript.jsonl", []byte("conversation"))
	bad[0].SHA256 = strings.Repeat("0", 64)
	if _, err := h.Transfer(bad[0]); code(err) != "integrity" {
		t.Fatal("wrong hash accepted", err)
	}
	tampered := chunks(t, s, "ho-1", "transcript.jsonl", []byte("conversation"))
	tampered[0].Data[3] ^= 1
	if _, err := h.Transfer(tampered[0]); code(err) != "integrity" {
		t.Fatal("tampered chunk accepted", err)
	}
	if _, err := os.Stat(filepath.Join(h.Dir, "ho-1", "transcript.jsonl")); !os.IsNotExist(err) {
		t.Fatal("unverified file kept")
	}
	if err := upload(t, h, "ho-1", "transcript.jsonl", nil); err != nil {
		t.Fatal("empty file rejected", err)
	}
	for _, p := range []transferParams{
		{Upload: "../x", Name: "transcript.jsonl", Data: make([]byte, 16), Last: true, SHA256: strings.Repeat("0", 64), EPK: "x"},
		{Upload: "ho-1", Name: "evil.sh", Data: make([]byte, 16), Last: true, SHA256: strings.Repeat("0", 64), EPK: "x"},
		{Upload: "ho-1", Name: "transcript.jsonl", Data: make([]byte, 16)},
		{Upload: "ho-1", Name: "transcript.jsonl", Offset: 7, Data: make([]byte, 16), Last: true, SHA256: strings.Repeat("0", 64), EPK: "x"},
		{Upload: "ho-1", Name: "transcript.jsonl", Data: make([]byte, 16), Last: true},
	} {
		if _, err := h.Transfer(p); code(err) != "invalid_request" {
			t.Fatalf("accepted %+v: %v", p.Upload+"/"+p.Name, err)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestTransferLimits(t *testing.T) {
	h, _, _ := startHandoff(t, t.TempDir(), nil)
	for i := range MaxUploads {
		if err := upload(t, h, "up-"+itoa(i), "transcript.jsonl", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := upload(t, h, "up-extra", "transcript.jsonl", []byte("x")); code(err) != "busy" {
		t.Fatal("ninth concurrent upload accepted", err)
	}
	// Existing uploads may continue; an upload over the cap (MaxUploadBytes) is refused.
	big := filepath.Join(h.Dir, "up-0", "transcript.jsonl")
	if err := os.WriteFile(big, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, MaxUploadBytes-10); err != nil {
		t.Fatal(err)
	}
	if err := upload(t, h, "up-0", "code.bundle", bytes.Repeat([]byte("y"), 100)); code(err) != "too_large" {
		t.Fatal("upload over the limit accepted", err)
	}
	if err := upload(t, h, "up-0", "manifest.json", []byte("{}")); err != nil {
		t.Fatal("small file under the limit refused", err)
	}
}

func call(s *Service, method string, params any) (json.RawMessage, error) {
	return s.Execute(context.Background(), protocol.Message{Method: method, Params: protocol.JSON(params), Deadline: time.Now().Add(20 * time.Second).UnixMilli()})
}

func waitJob(t *testing.T, s *Service, id string) session.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := call(s, "job", map[string]any{"job": id})
		if err != nil {
			t.Fatal(err)
		}
		var j session.Job
		json.Unmarshal(raw, &j)
		if j.Finished() {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s: %+v", id, j)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A move into this machine: the upload, then agents.import as a job.
func TestImportJob(t *testing.T) {
	h, f, _ := startHandoff(t, t.TempDir(), nil)
	s := &Service{Handoff: h}
	if err := upload(t, h, "mv-1", "manifest.json", []byte(`{"version":2}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := call(s, "agents.import", map[string]any{"upload": "mv-1"})
	if err != nil {
		t.Fatal(err)
	}
	var started struct{ Job string }
	json.Unmarshal(raw, &started)
	j := waitJob(t, s, started.Job)
	if j.State != "done" || !strings.Contains(string(j.Result), "mini/abc123") || len(f.imported) != 1 {
		t.Fatalf("job %+v, imported %v", j, f.imported)
	}
	if _, err := os.Stat(filepath.Join(h.Dir, "mv-1")); !os.IsNotExist(err) {
		t.Fatal("an imported upload stays")
	}
	if _, err := call(s, "agents.import", map[string]any{"upload": "mv-1"}); code(err) != "invalid_request" {
		t.Fatalf("import of nothing: %v", err)
	}
	// A failing import keeps its code.
	f.importErr = protocol.Err("branch_diverged", "feature/x here has commits the source does not have")
	upload(t, h, "mv-2", "manifest.json", []byte(`{}`))
	raw, _ = call(s, "agents.import", map[string]any{"upload": "mv-2"})
	json.Unmarshal(raw, &started)
	if j := waitJob(t, s, started.Job); j.State != "failed" || j.Error == nil || j.Error.Code != "branch_diverged" {
		t.Fatalf("failed import: %+v", j)
	}
}

// A move out of this machine: agents.export packs (polled while it
// packs) and download serves sealed chunks only the requester opens.
func TestExportStagesAndDownloadsSealedChunks(t *testing.T) {
	saved := downloadGrace
	downloadGrace = 0
	defer func() { downloadGrace = saved }()
	h, f, _ := startHandoff(t, t.TempDir(), nil)
	s := &Service{Handoff: h}
	requester, _ := ecdh.X25519().GenerateKey(rand.Reader)
	key := transfer.EncodePublicKey(requester.PublicKey())
	raw, err := h.Export(context.Background(), exportParams{ID: "mini/abc123", Key: key})
	if err != nil {
		t.Fatal(err)
	}
	got := fetchExport(t, s, h, requester, raw)
	if !strings.Contains(string(got["manifest.json"]), "mini/abc123") || len(got["code.bundle"]) != 1200000 {
		t.Fatalf("export content %d bytes", len(got["code.bundle"]))
	}
	// Deleted after the (zero) grace, on a timer of its own.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if entries, _ := os.ReadDir(filepath.Join(h.Dir, ".downloads")); len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a downloaded export stays")
		}
	}
	// Packing that outlasts the request: "packing", then the download id.
	gate := make(chan struct{})
	f.mu.Lock()
	f.packGate = gate
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2100*time.Millisecond)
	raw, err = h.Export(ctx, exportParams{ID: "mini/abc123", Key: key})
	cancel()
	var e struct{ Download, State string }
	if err != nil || json.Unmarshal(raw, &e) != nil || e.State != "packing" {
		t.Fatalf("slow export %s %v", raw, err)
	}
	// Packing ends; asking again with the download id waits for it.
	close(gate)
	raw, err = call(s, "agents.export", map[string]any{"download": e.Download})
	if err != nil {
		t.Fatal(err)
	}
	fetchExport(t, s, h, requester, raw)
	// A packing error keeps its code and forgets the export.
	f.mu.Lock()
	f.packGate, f.packErr = nil, protocol.Err("conflicts", "the checkout has unresolved merge conflicts")
	f.mu.Unlock()
	if _, err := h.Export(context.Background(), exportParams{ID: "mini/abc123", Key: key}); code(err) != "conflicts" {
		t.Fatalf("pack error: %v", err)
	}
	for _, bad := range []exportParams{{Key: key}, {ID: "x", Key: "nope"}, {ID: "x", Key: key, Have: []string{"zz"}}, {Download: "dl-0123456789ab", ID: "x"}} {
		if _, err := h.Export(context.Background(), bad); code(err) != "invalid_request" {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

func fetchExport(t *testing.T, s *Service, h *Handoff, requester *ecdh.PrivateKey, raw json.RawMessage) map[string][]byte {
	t.Helper()
	var e struct {
		Download, State, ID string
		Files               []DownloadFile
	}
	if json.Unmarshal(raw, &e) != nil || e.State != "ready" || e.ID != "ho-0123456789ab" || len(e.Files) != 2 {
		t.Fatalf("export %s", raw)
	}
	opener := transfer.NewRequesterDownload(requester, h.Key.PublicKey(), e.Download)
	got := map[string][]byte{}
	for _, f := range e.Files {
		var data []byte
		for index, offset := uint64(0), int64(0); ; index, offset = index+1, offset+transfer.ChunkSize {
			raw, err := call(s, "download", map[string]any{"download": e.Download, "name": f.Name, "offset": offset})
			if err != nil {
				t.Fatal(err)
			}
			var chunk struct {
				Data []byte
				Last bool
			}
			json.Unmarshal(raw, &chunk)
			if bytes.Contains(chunk.Data, []byte("0123456789")) {
				t.Fatal("chunk is not sealed")
			}
			plain, err := opener.Open(f.Name, index, chunk.Last, chunk.Data)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, plain...)
			if chunk.Last {
				break
			}
		}
		if sum := sha256.Sum256(data); int64(len(data)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("%s: %d bytes, want %d with its sha256", f.Name, len(data), f.Size)
		}
		got[f.Name] = data
	}
	return got
}
