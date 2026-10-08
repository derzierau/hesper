package host

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
	"github.com/derzierau/hesper/relay/pkg/transfer"
)

// Handoff limits (relay/docs/protocol.md, Moves).
const (
	MaxUploadBytes = 512 << 20
	MaxUploads     = 8
	MaxRunningJobs = 4
	Retention      = 24 * time.Hour
	MaxDefer       = 7 * 24 * time.Hour
	MaxDownloads   = 8
)

// downloadGrace keeps a fully downloaded export a little longer, so the
// requester can repeat a last chunk whose answer was lost (test seam).
var downloadGrace = time.Minute

// uploadFiles are the files of a move bundle (internal/handoff).
var uploadFiles = map[string]bool{"manifest.json": true, "transcript.jsonl": true, "code.bundle": true}
var uploadID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var jobID = regexp.MustCompile(`^job-[0-9a-f]{12}$`)
var commitID = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var downloadID = regexp.MustCompile(`^dl-[0-9a-f]{12}$`)

// Handoff moves agents in and out of this machine (agents.move, part R).
// In: encrypted uploads (transfer) staged in Dir/<upload>/ (0700) as
// sealed .part files until their last chunk arrives, then as verified
// plaintext; agents.import queues a job that unpacks one and starts its
// agent (Import). Job records live in Dir/.jobs so a queued job survives a
// restart; a job that was running when the host stopped is reported
// failed, never rerun. Out: agents.export packs an agent (Pack) into a
// staged download only the requester can open.
type Handoff struct {
	// Pack writes agent id's move bundle into dir (incremental from have,
	// commits the target has); Import unpacks a bundle directory and
	// starts its agent, returning it (JSON).
	Pack    func(ctx context.Context, id string, have []string, dir string) error
	Import  func(ctx context.Context, dir string) (json.RawMessage, error)
	Dir     string
	Key     *ecdh.PrivateKey
	Timeout time.Duration // per pack or import; default 10 minutes
	Logger  *slog.Logger
	Now     func() time.Time // test seam

	mu        sync.Mutex
	ctx       context.Context
	jobs      map[string]*job
	downloads map[string]*download
	running   int
	wake      chan struct{}
	wg        sync.WaitGroup
}

type job struct {
	session.Job
	Upload  string `json:"upload"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
	Cancel  bool   `json:"cancel,omitempty"`
}

func (h *Handoff) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Start loads persisted jobs and schedules them until ctx ends. Wait blocks
// until running jobs have finished after that.
func (h *Handoff) Start(ctx context.Context) error {
	if h.Timeout <= 0 {
		h.Timeout = 10 * time.Minute
	}
	if h.Logger == nil {
		h.Logger = slog.Default()
	}
	if err := os.MkdirAll(filepath.Join(h.Dir, ".jobs"), 0700); err != nil {
		return err
	}
	if err := os.Chmod(h.Dir, 0700); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ctx, h.jobs, h.downloads, h.wake = ctx, map[string]*job{}, map[string]*download{}, make(chan struct{}, 1)
	// Exports are sealed with keys held in memory only: what a previous run
	// staged can no longer be served.
	if err := os.RemoveAll(filepath.Join(h.Dir, ".downloads")); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(h.Dir, ".jobs"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(h.Dir, ".jobs", e.Name()))
		var j job
		if err != nil || json.Unmarshal(data, &j) != nil || !jobID.MatchString(j.ID) {
			continue
		}
		if j.State == "running" {
			j.State, j.Step = "failed", "failed"
			j.Error = &session.JobError{Code: "interrupted", Message: "The host restarted while the job ran"}
			h.saveLocked(&j)
		}
		h.jobs[j.ID] = &j
	}
	h.cleanupLocked()
	go h.schedule()
	return nil
}

func (h *Handoff) Wait() { h.wg.Wait() }

func (h *Handoff) started() error {
	if h == nil || h.jobs == nil {
		return protocol.Err("unsupported", "Host does not accept handoffs")
	}
	return nil
}

func (h *Handoff) poke() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *Handoff) schedule() {
	lastCleanup := h.now()
	for {
		h.mu.Lock()
		next := h.dispatchLocked()
		if h.now().Sub(lastCleanup) > time.Hour {
			h.cleanupLocked()
			lastCleanup = h.now()
		}
		h.mu.Unlock()
		timer := time.NewTimer(next)
		select {
		case <-h.ctx.Done():
			timer.Stop()
			return
		case <-h.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// dispatchLocked starts due jobs, oldest first, and returns how long to sleep
// until the next deferred one is due.
func (h *Handoff) dispatchLocked() time.Duration {
	next := time.Minute
	now := h.now()
	queued := []*job{}
	for _, j := range h.jobs {
		if j.State == "queued" {
			queued = append(queued, j)
		}
	}
	sort.Slice(queued, func(a, b int) bool {
		return queued[a].Created < queued[b].Created || (queued[a].Created == queued[b].Created && queued[a].ID < queued[b].ID)
	})
	for _, j := range queued {
		if due := time.Unix(j.StartAt, 0).Sub(now); j.StartAt != 0 && due > 0 {
			next = min(next, due)
			continue
		}
		if h.running >= MaxRunningJobs || h.ctx.Err() != nil {
			continue
		}
		h.running++
		j.State, j.Step = "running", "unpacking"
		h.saveLocked(j)
		h.wg.Add(1)
		go h.run(j.ID, j.Upload, j.StartAt)
	}
	return max(next, 10*time.Millisecond)
}

func (h *Handoff) run(id, upload string, startAt int64) {
	defer h.wg.Done()
	ctx, cancel := context.WithTimeout(h.ctx, h.Timeout)
	var out json.RawMessage
	var err error
	if delay := time.Until(time.Unix(startAt, 0)); startAt != 0 && delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			err = ctx.Err()
		}
		timer.Stop()
	}
	if err == nil {
		if h.Import == nil {
			err = protocol.Err("unsupported", "Host does not import agents")
		} else {
			out, err = h.Import(ctx, filepath.Join(h.Dir, upload))
		}
	}
	cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[id]
	h.running--
	j.Result = nil
	switch {
	case err != nil && h.ctx.Err() != nil:
		j.State, j.Step, j.Error = "failed", "failed", &session.JobError{Code: "interrupted", Message: "The host stopped while the job ran"}
	case err != nil:
		e := publicError(err)
		j.State, j.Step, j.Error = "failed", "failed", &session.JobError{Code: e.Code, Message: e.Message}
	default:
		j.State, j.Step, j.Result = "done", "started", out
		// The bundle served its purpose.
		os.RemoveAll(filepath.Join(h.Dir, upload))
	}
	h.saveLocked(j)
	h.poke()
}

func (h *Handoff) saveLocked(j *job) {
	j.Updated = h.now().Unix()
	path := filepath.Join(h.Dir, ".jobs", j.ID+".json")
	if err := writePrivate(path, protocol.JSON(j)); err != nil {
		h.Logger.Warn("cannot persist handoff job", "job", j.ID, "error", err)
	}
}

func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// cleanupLocked deletes finished jobs and uploads untouched for Retention.
// Uploads of queued or running jobs stay.
func (h *Handoff) cleanupLocked() {
	cutoff := h.now().Add(-Retention)
	active := map[string]bool{}
	for id, j := range h.jobs {
		if !j.Finished() {
			active[j.Upload] = true
		} else if time.Unix(j.Updated, 0).Before(cutoff) {
			os.Remove(filepath.Join(h.Dir, ".jobs", id+".json"))
			delete(h.jobs, id)
		}
	}
	for id, d := range h.downloads {
		if d.created.Before(cutoff) {
			h.dropLocked(id)
		}
	}
	entries, _ := os.ReadDir(h.Dir)
	for _, e := range entries {
		if !e.IsDir() || !uploadID.MatchString(e.Name()) || active[e.Name()] {
			continue
		}
		if newest(filepath.Join(h.Dir, e.Name())).Before(cutoff) {
			os.RemoveAll(filepath.Join(h.Dir, e.Name()))
		}
	}
}

func newest(dir string) time.Time {
	info, err := os.Stat(dir)
	if err != nil {
		return time.Time{}
	}
	t := info.ModTime()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().After(t) {
			t = info.ModTime()
		}
	}
	return t
}

func (h *Handoff) spawnedLocked(upload string) bool {
	for _, j := range h.jobs {
		if j.Upload == upload {
			return true
		}
	}
	return false
}

// pendingLocked counts staged uploads that no job has taken yet.
func (h *Handoff) pendingLocked() int {
	n := 0
	entries, _ := os.ReadDir(h.Dir)
	for _, e := range entries {
		if e.IsDir() && uploadID.MatchString(e.Name()) && !h.spawnedLocked(e.Name()) {
			n++
		}
	}
	return n
}

type transferParams struct {
	Upload string `json:"upload"`
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
	Last   bool   `json:"last"`
	SHA256 string `json:"sha256"`
	EPK    string `json:"epk"`
}

const sealedChunk = transfer.ChunkSize + transfer.Overhead

// Transfer stores one sealed chunk. Chunks arrive in order; repeating an
// earlier offset (a retry after an uncertain timeout, or 0 to restart) drops
// what followed it. The last chunk carries the plaintext hash and the
// sender's ephemeral key: the host then decrypts and verifies the file.
func (h *Handoff) Transfer(p transferParams) (json.RawMessage, error) {
	if err := h.started(); err != nil {
		return nil, err
	}
	switch {
	case !uploadID.MatchString(p.Upload):
		return nil, protocol.Err("invalid_request", "upload must be 1-64 letters, digits, - or _")
	case !uploadFiles[p.Name]:
		return nil, protocol.Err("invalid_request", "name must be manifest.json, transcript.jsonl or code.bundle")
	case p.Offset < 0 || p.Offset%transfer.ChunkSize != 0 || p.Offset >= MaxUploadBytes:
		return nil, protocol.Err("invalid_request", "offset must be a multiple of the 512 KiB chunk size")
	case len(p.Data) < transfer.Overhead || len(p.Data) > sealedChunk || (!p.Last && len(p.Data) != sealedChunk):
		return nil, protocol.Err("invalid_request", "Chunks hold 512 KiB of data; only the last may be shorter")
	case p.Last && (!sha256Hex.MatchString(p.SHA256) || p.EPK == ""):
		return nil, protocol.Err("invalid_request", "The last chunk needs sha256 and epk")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.spawnedLocked(p.Upload) {
		return nil, protocol.Err("invalid_request", "Upload was already spawned")
	}
	dir := filepath.Join(h.Dir, p.Upload)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if h.pendingLocked() >= MaxUploads {
			return nil, protocol.Err("busy", "Too many uploads in progress")
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			return nil, err
		}
	}
	part, final := filepath.Join(dir, p.Name+".part"), filepath.Join(dir, p.Name)
	index := p.Offset / transfer.ChunkSize
	var have int64
	if info, err := os.Stat(part); err == nil {
		have = info.Size() / sealedChunk
	}
	if _, err := os.Stat(final); err == nil {
		if index != 0 {
			return nil, protocol.Err("invalid_request", "File is already complete; restart it at offset 0")
		}
		os.Remove(final)
	}
	if index > have {
		return nil, protocol.Err("invalid_request", fmt.Sprintf("Chunk out of order; expected offset %d", have*transfer.ChunkSize))
	}
	if usage(dir, p.Name)+p.Offset+int64(len(p.Data)-transfer.Overhead) > MaxUploadBytes {
		os.Remove(part)
		return nil, protocol.Err("too_large", "Upload exceeds 512 MiB")
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Truncate(index * sealedChunk)
	if err == nil {
		_, err = f.WriteAt(p.Data, index*sealedChunk)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if !p.Last {
		return protocol.JSON(map[string]int64{"received": p.Offset + transfer.ChunkSize}), nil
	}
	size, err := h.finish(p, part, final, index)
	os.Remove(part)
	if err != nil {
		return nil, err
	}
	return protocol.JSON(map[string]int64{"received": size}), nil
}

// usage is the plaintext size of an upload's other files (sealed parts
// slightly overcount, which is on the safe side).
func usage(dir, except string) int64 {
	var total int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == except || e.Name() == except+".part" || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

// finish decrypts the sealed chunks 0..last into the final file and checks
// the plaintext hash; a failure leaves no file behind.
func (h *Handoff) finish(p transferParams, part, final string, last int64) (int64, error) {
	opener, err := transfer.NewOpener(h.Key, p.EPK, p.Upload, p.Name)
	if err != nil {
		return 0, protocol.Err("invalid_request", "Invalid epk")
	}
	in, err := os.Open(part)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(final), ".open-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	hash := sha256.New()
	w := io.MultiWriter(out, hash)
	r := bufio.NewReaderSize(in, sealedChunk)
	buf := make([]byte, sealedChunk)
	var size int64
	for i := int64(0); i <= last; i++ {
		n, err := io.ReadFull(r, buf)
		if err != nil && (i != last || !errors.Is(err, io.ErrUnexpectedEOF)) {
			return 0, protocol.Err("invalid_request", "Upload is incomplete; send the file again")
		}
		plain, err := opener.Open(uint64(i), i == last, buf[:n])
		if err != nil {
			return 0, protocol.Err("integrity", "File failed authentication; send it again")
		}
		if _, err := w.Write(plain); err != nil {
			return 0, err
		}
		size += int64(len(plain))
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), p.SHA256) {
		return 0, protocol.Err("integrity", "File does not match its sha256; send it again")
	}
	if err := out.Chmod(0600); err != nil {
		return 0, err
	}
	if err := out.Sync(); err != nil {
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	return size, os.Rename(out.Name(), final)
}

// Spawn queues the import (internal/handoff) of a completed upload and
// returns at once; callers poll Job.
func (h *Handoff) Spawn(upload string, startAt int64) (json.RawMessage, error) {
	if err := h.started(); err != nil {
		return nil, err
	}
	if !uploadID.MatchString(upload) {
		return nil, protocol.Err("invalid_request", "Invalid upload")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if startAt < 0 || time.Unix(startAt, 0).After(now.Add(MaxDefer)) {
		return nil, protocol.Err("invalid_request", "startAt must be within 7 days")
	}
	dir := filepath.Join(h.Dir, upload)
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		return nil, protocol.Err("invalid_request", "Upload has no complete manifest.json")
	}
	if parts, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(parts) > 0 {
		return nil, protocol.Err("invalid_request", "Upload has incomplete files")
	}
	if h.spawnedLocked(upload) {
		return nil, protocol.Err("invalid_request", "Upload was already spawned")
	}
	var id [6]byte
	rand.Read(id[:])
	j := &job{Job: session.Job{ID: "job-" + hex.EncodeToString(id[:]), State: "queued", Step: "queued", StartAt: startAt}, Upload: upload, Created: now.Unix()}
	if startAt > now.Unix() {
		j.Step = "waiting until " + time.Unix(startAt, 0).Format("2006-01-02 15:04")
	}
	h.jobs[j.ID] = j
	h.saveLocked(j)
	h.poke()
	return protocol.JSON(map[string]string{"job": j.ID}), nil
}

// Job reports a job; cancel stops a queued job from starting, or marks a
// running one so that whatever it starts is stopped again.
func (h *Handoff) Job(id string, cancel bool) (json.RawMessage, error) {
	if err := h.started(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[id]
	if j == nil {
		return nil, protocol.Err("not_found", "No such job")
	}
	if cancel && j.State == "queued" {
		j.State, j.Step, j.Error = "failed", "cancelled", &session.JobError{Code: "cancelled", Message: "The move was cancelled"}
		h.saveLocked(j)
	}
	return protocol.JSON(j.Job), nil
}

// Available reports whether agents can be imported here.
func (h *Handoff) Available() bool { return h != nil && h.jobs != nil && h.Import != nil }

// Exports: bring back over the relay. The requester asks for an agent's code
// (or a project's branch); the host packs it (internal/handoff), stages
// the bundle in Dir/.downloads/<download>/ and serves it in sealed chunks
// that only the requester can open. A fully downloaded export is deleted;
// others expire after Retention.

type exportParams struct {
	ID       string   `json:"id"`
	Have     []string `json:"have"`
	Key      string   `json:"key"`
	Download string   `json:"download"`
}

type downloadParams struct {
	Download string `json:"download"`
	Name     string `json:"name"`
	Offset   int64  `json:"offset"`
}

// DownloadFile is one staged file of an export.
type DownloadFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type download struct {
	id, dir, bundle string
	sealer          *transfer.Download
	ready, closing  bool
	err             error
	files           []DownloadFile
	served          map[string]bool
	created         time.Time
	done            chan struct{}
}

// Export packs an agent (its local id, already checked against the
// registry) for the requester's ephemeral key, and reports the staged
// files. Packing that outlasts the request answers "packing": the
// requester asks again with only the download ID.
func (h *Handoff) Export(ctx context.Context, p exportParams) (json.RawMessage, error) {
	if err := h.started(); err != nil {
		return nil, err
	}
	if p.Download != "" {
		if p.ID != "" || len(p.Have) > 0 || p.Key != "" {
			return nil, protocol.Err("invalid_request", "An export is polled with its download ID only")
		}
		return h.exportStatus(ctx, p.Download)
	}
	if p.ID == "" || h.Pack == nil {
		return nil, protocol.Err("invalid_request", "Export takes an agent id")
	}
	if len(p.Have) > 64 {
		return nil, protocol.Err("invalid_request", "At most 64 commits")
	}
	for _, c := range p.Have {
		if !commitID.MatchString(c) {
			return nil, protocol.Err("invalid_request", "have must be hexadecimal SHAs")
		}
	}
	var raw [6]byte
	rand.Read(raw[:])
	id := "dl-" + hex.EncodeToString(raw[:])
	sealer, err := transfer.NewHostDownload(h.Key, p.Key, id)
	if err != nil {
		return nil, protocol.Err("invalid_request", "key must be an X25519 public key (base64)")
	}
	h.mu.Lock()
	if len(h.downloads) >= MaxDownloads {
		h.mu.Unlock()
		return nil, protocol.Err("busy", "Too many exports in progress")
	}
	d := &download{id: id, dir: filepath.Join(h.Dir, ".downloads", id), sealer: sealer, served: map[string]bool{}, created: h.now(), done: make(chan struct{})}
	h.downloads[id] = d
	h.wg.Add(1)
	h.mu.Unlock()
	go h.pack(d, p.ID, p.Have)
	return h.exportStatus(ctx, id)
}

func (h *Handoff) pack(d *download, agent string, have []string) {
	defer h.wg.Done()
	defer close(d.done)
	ctx, cancel := context.WithTimeout(h.ctx, h.Timeout)
	err := os.MkdirAll(filepath.Dir(d.dir), 0700)
	if err == nil {
		err = h.Pack(ctx, agent, have, d.dir)
	}
	cancel()
	var bundle string
	var files []DownloadFile
	if err == nil {
		bundle, files, err = stage(d.dir)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		d.err = err
		os.RemoveAll(d.dir)
		return
	}
	d.ready, d.bundle, d.files = true, bundle, files
}

// stage describes a packed bundle's files (manifest first).
func stage(dir string) (string, []DownloadFile, error) {
	var files []DownloadFile
	for _, name := range []string{"manifest.json", "transcript.jsonl", "code.bundle"} {
		f, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		hash := sha256.New()
		size, err := io.Copy(hash, f)
		f.Close()
		if err != nil {
			return "", nil, err
		}
		files = append(files, DownloadFile{Name: name, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))})
	}
	if len(files) == 0 || files[0].Name != "manifest.json" {
		return "", nil, protocol.Err("handoff_failed", "The export has no manifest.json")
	}
	var m struct {
		ID string `json:"id"`
	}
	data, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	json.Unmarshal(data, &m)
	return m.ID, files, nil
}

// exportStatus waits for packing while the request allows, then reports the
// export: ready with its files, still packing, or the packing error (which
// also forgets the export).
func (h *Handoff) exportStatus(ctx context.Context, id string) (json.RawMessage, error) {
	h.mu.Lock()
	d := h.downloads[id]
	h.mu.Unlock()
	if d == nil {
		return nil, protocol.Err("not_found", "No such export")
	}
	wait := 15 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(deadline)-2*time.Second)
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-d.done:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case d.err != nil:
		delete(h.downloads, id)
		return nil, d.err
	case !d.ready:
		return protocol.JSON(map[string]string{"download": id, "state": "packing"}), nil
	}
	return protocol.JSON(map[string]any{"download": id, "state": "ready", "id": d.bundle, "files": d.files}), nil
}

// Download returns one sealed chunk of a staged export file. Chunks hold
// 512 KiB of plaintext; offset is the plaintext offset. Once every file's
// last chunk was served the export is deleted (after a short grace).
func (h *Handoff) Download(p downloadParams) (json.RawMessage, error) {
	if err := h.started(); err != nil {
		return nil, err
	}
	switch {
	case !downloadID.MatchString(p.Download):
		return nil, protocol.Err("invalid_request", "Invalid download")
	case !uploadFiles[p.Name]:
		return nil, protocol.Err("invalid_request", "name must be manifest.json, transcript.jsonl or code.bundle")
	case p.Offset < 0 || p.Offset%transfer.ChunkSize != 0:
		return nil, protocol.Err("invalid_request", "offset must be a multiple of the 512 KiB chunk size")
	}
	h.mu.Lock()
	d := h.downloads[p.Download]
	var file *DownloadFile
	ready := d != nil && d.ready
	if ready {
		for _, f := range d.files {
			if f.Name == p.Name {
				file = &f
			}
		}
	}
	h.mu.Unlock()
	switch {
	case d == nil:
		return nil, protocol.Err("not_found", "No such export")
	case !ready:
		return nil, protocol.Err("invalid_request", "The export is still packing")
	case file == nil:
		return nil, protocol.Err("not_found", "The export has no such file")
	case p.Offset > 0 && p.Offset >= file.Size:
		return nil, protocol.Err("invalid_request", "offset is beyond the end of the file")
	}
	f, err := os.Open(filepath.Join(d.dir, p.Name))
	if err != nil {
		return nil, protocol.Err("not_found", "The export is gone")
	}
	buf := make([]byte, min(transfer.ChunkSize, file.Size-p.Offset))
	_, err = f.ReadAt(buf, p.Offset)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	last := p.Offset+int64(len(buf)) >= file.Size
	sealed, err := d.sealer.Seal(p.Name, uint64(p.Offset/transfer.ChunkSize), last, buf)
	if err != nil {
		return nil, err
	}
	if last {
		h.mu.Lock()
		d.served[p.Name] = true
		if len(d.served) == len(d.files) && !d.closing {
			d.closing = true
			time.AfterFunc(downloadGrace, func() {
				h.mu.Lock()
				defer h.mu.Unlock()
				if h.downloads[d.id] == d {
					h.dropLocked(d.id)
				}
			})
		}
		h.mu.Unlock()
	}
	return protocol.JSON(map[string]any{"data": sealed, "last": last}), nil
}

// dropLocked forgets an export and deletes its staged files; one still
// packing is left to finish (its result is then dropped by cleanup).
func (h *Handoff) dropLocked(id string) {
	d := h.downloads[id]
	if d == nil || (!d.ready && d.err == nil) {
		return
	}
	os.RemoveAll(d.dir)
	delete(h.downloads, id)
}
