package agents

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Attachments (files.put / files.chunk): files dropped on an agent's
// terminal in the app, stored on the agent's machine so a path to them can
// be pasted into the agent. The app sends them in 512 KiB chunks; the
// local daemon stores them for its own agents and forwards them, sealed,
// to another machine's (internal/remote, internal/host). Nothing here
// reads a file's content beyond hashing it.

const (
	// FileChunk is the size of every chunk but the last (pkg/transfer's
	// ChunkSize, so sealed chunks stay under the relay's message limit).
	FileChunk = 512 * 1024
	// MaxAttachment caps one file.
	MaxAttachment = 50 << 20
	// maxUploads in progress per daemon; uploadIdle drops a quiet one.
	maxUploads = 8
	uploadIdle = 10 * time.Minute
	maxNameLen = 120
)

// Attachments setting values (settings.json "attachments").
const (
	AttachmentsState   = "state"
	AttachmentsProject = "project"
)

// FilePut starts an upload: for agent, or for a draft (machine + draft).
// Upload and EPK are set by a controller's daemon on a host (sealed
// chunks); the app leaves them empty.
type FilePut struct {
	Agent   string `json:"agent,omitempty"`
	Machine string `json:"machine,omitempty"`
	Draft   string `json:"draft,omitempty"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Upload  string `json:"upload,omitempty"`
	EPK     string `json:"epk,omitempty"`
}

// FilePutResult names the upload and its chunk size.
type FilePutResult struct {
	Upload string `json:"upload"`
	Chunk  int    `json:"chunk"`
}

// FileChunkParams is one chunk, at offset (the bytes sent so far).
type FileChunkParams struct {
	Upload string `json:"upload"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
	Last   bool   `json:"last"`
}

// FileChunkResult: received so far, or (last chunk) the stored file.
type FileChunkResult struct {
	Received int64  `json:"received,omitempty"`
	Path     string `json:"path,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// UploadHooks: Open decrypts a sealed chunk (hosts); End reports how an
// upload ended (audit).
type UploadHooks struct {
	Open func(index uint64, last bool, data []byte) ([]byte, error)
	End  func(ok bool, size int64)
}

var (
	draftIDRe  = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	uploadIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	sha256Re   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SanitizeName makes a file name safe to store: its base name without
// control characters, no leading dots, trimmed; "" when nothing is left.
func SanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.TrimSpace(name)
	if name == "." || name == "/" {
		return ""
	}
	if trimmed := strings.TrimLeft(name, "."); trimmed != name {
		if trimmed == "" {
			return ""
		}
		name = "_" + trimmed
	}
	return name
}

type upload struct {
	id       string
	dir      string
	name     string
	size     int64
	sha      string
	tmp      *os.File
	received int64
	hash     hash.Hash
	last     time.Time
	hooks    UploadHooks
}

// FileStore keeps the uploads in progress.
type FileStore struct {
	reg     *Registry
	now     func() time.Time
	mu      sync.Mutex
	uploads map[string]*upload
}

// Files is the registry's attachment store (one per daemon).
func (r *Registry) Files() *FileStore {
	r.filesOnce.Do(func() { r.files = &FileStore{reg: r, now: time.Now, uploads: map[string]*upload{}} })
	return r.files
}

// AttachmentDir is where an agent's (or a draft's) attachments go on this
// machine.
func (r *Registry) AttachmentDir(agentID, draft string) (string, error) {
	if agentID == "" {
		return filepath.Join(r.opt.StateDir, "attachments", draft), nil
	}
	a, err := r.Get(agentID)
	if err != nil {
		return "", err
	}
	if r.settings.Attachments == AttachmentsProject {
		cwd := a.Worktree
		if cwd == "" {
			cwd = a.Project
		}
		if cwd != "" {
			return filepath.Join(cwd, ".hesper", "attachments"), nil
		}
	}
	_, local := r.split(a.ID)
	return filepath.Join(r.opt.StateDir, "attachments", local), nil
}

// Begin starts an upload. sealed: chunks come sealed (hooks.Open set).
func (s *FileStore) Begin(p FilePut, hooks UploadHooks) (FilePutResult, error) {
	name := SanitizeName(p.Name)
	switch {
	case name == "" || len(name) > maxNameLen:
		return FilePutResult{}, wire.Errorf(wire.CodeInvalid, "a file name of 1 to %d bytes is required", maxNameLen)
	case p.Size < 0:
		return FilePutResult{}, wire.Errorf(wire.CodeInvalid, "size must not be negative")
	case p.Size > MaxAttachment:
		return FilePutResult{}, wire.Errorf(wire.CodeInvalid, "%s is larger than 50 MB", name)
	case !sha256Re.MatchString(p.SHA256):
		return FilePutResult{}, wire.Errorf(wire.CodeInvalid, "sha256 must be 64 lowercase hex digits")
	case (p.Agent == "") == (p.Draft == ""):
		return FilePutResult{}, wire.Errorf(wire.CodeInvalid, "give either agent, or machine and draft")
	case p.Agent == "" && !draftIDRe.MatchString(p.Draft):
		return FilePutResult{}, wire.Errorf(wire.CodeInvalid, "draft must be 1-40 of a-z, 0-9 and -")
	case p.Upload != "" && !uploadIDRe.MatchString(p.Upload):
		return FilePutResult{}, wire.Errorf(wire.CodeInvalid, "upload must be 1-64 letters, digits, - or _")
	}
	dir, err := s.reg.AttachmentDir(p.Agent, p.Draft)
	if err != nil {
		return FilePutResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	id := p.Upload
	if id == "" {
		var b [8]byte
		rand.Read(b[:])
		id = "at-" + hex.EncodeToString(b[:])
	}
	if _, ok := s.uploads[id]; ok {
		return FilePutResult{}, wire.Errorf(wire.CodeExists, "upload %s is in progress", id)
	}
	if len(s.uploads) >= maxUploads {
		return FilePutResult{}, wire.Errorf(wire.CodeUnavailable, "too many uploads")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return FilePutResult{}, err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return FilePutResult{}, err
	}
	s.uploads[id] = &upload{id: id, dir: dir, name: name, size: p.Size, sha: p.SHA256, tmp: tmp, hash: sha256.New(), last: s.now(), hooks: hooks}
	return FilePutResult{Upload: id, Chunk: FileChunk}, nil
}

// Chunk stores one chunk; the last one checks size and checksum and puts
// the file in place. sealed must match how the upload began.
func (s *FileStore) Chunk(p FileChunkParams, sealed bool) (FileChunkResult, error) {
	s.mu.Lock()
	s.expireLocked()
	u, ok := s.uploads[p.Upload]
	if !ok || (u.hooks.Open != nil) != sealed {
		s.mu.Unlock()
		return FileChunkResult{}, wire.Errorf(wire.CodeNotFound, "no upload %s", p.Upload)
	}
	u.last = s.now()
	s.mu.Unlock()
	// One upload's chunks come one at a time (the app, the fleet).
	if p.Offset != u.received {
		return FileChunkResult{}, wire.Errorf(wire.CodeInvalid, "expected offset %d", u.received)
	}
	data := p.Data
	if u.hooks.Open != nil {
		plain, err := u.hooks.Open(uint64(p.Offset/FileChunk), p.Last, data)
		if err != nil {
			s.discard(u)
			return FileChunkResult{}, wire.Errorf(wire.CodeInvalid, "chunk does not open: %v", err)
		}
		data = plain
	}
	if len(data) > FileChunk || (!p.Last && len(data) != FileChunk) {
		return FileChunkResult{}, wire.Errorf(wire.CodeInvalid, "chunks hold %d bytes; only the last may be shorter", FileChunk)
	}
	if u.received+int64(len(data)) > u.size {
		s.discard(u)
		return FileChunkResult{}, wire.Errorf(wire.CodeInvalid, "more data than the declared size %d", u.size)
	}
	if _, err := u.tmp.Write(data); err != nil {
		s.discard(u)
		return FileChunkResult{}, err
	}
	u.hash.Write(data)
	u.received += int64(len(data))
	if !p.Last {
		return FileChunkResult{Received: u.received}, nil
	}
	if u.received != u.size || hex.EncodeToString(u.hash.Sum(nil)) != u.sha {
		s.discard(u)
		return FileChunkResult{}, wire.Errorf(wire.CodeInvalid, "checksum mismatch")
	}
	err := u.tmp.Sync()
	if cerr := u.tmp.Close(); err == nil {
		err = cerr
	}
	path := ""
	if err == nil {
		path, err = placeUnique(u.tmp.Name(), u.dir, u.name)
	}
	if err != nil {
		s.discard(u)
		return FileChunkResult{}, err
	}
	s.mu.Lock()
	delete(s.uploads, u.id)
	s.mu.Unlock()
	if u.hooks.End != nil {
		u.hooks.End(true, u.size)
	}
	return FileChunkResult{Path: path, Size: u.size}, nil
}

// placeUnique links tmp to dir/name (name-2.ext, -3 … when taken) and
// removes tmp.
func placeUnique(tmp, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem, ext = name, ""
	}
	for n := 1; n < 10000; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, n, ext)
		}
		path := filepath.Join(dir, candidate)
		err := os.Link(tmp, path)
		if err == nil {
			os.Remove(tmp)
			os.Chmod(path, 0o600)
			abs, _ := filepath.Abs(path)
			return abs, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", wire.Errorf(wire.CodeExists, "no free name for %s", name)
}

func (s *FileStore) discard(u *upload) {
	s.mu.Lock()
	_, present := s.uploads[u.id]
	delete(s.uploads, u.id)
	s.mu.Unlock()
	if !present {
		return
	}
	u.tmp.Close()
	os.Remove(u.tmp.Name())
	if u.hooks.End != nil {
		u.hooks.End(false, u.size)
	}
}

func (s *FileStore) expireLocked() {
	now := s.now()
	for id, u := range s.uploads {
		if now.Sub(u.last) > uploadIdle {
			delete(s.uploads, id)
			u.tmp.Close()
			os.Remove(u.tmp.Name())
			if u.hooks.End != nil {
				go u.hooks.End(false, u.size)
			}
		}
	}
}

// Pending is the number of uploads in progress (tests).
func (s *FileStore) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}
