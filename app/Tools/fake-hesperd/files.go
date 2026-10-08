package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// files.put / files.chunk (contract "As built — drop to attach"), the
// fake's version: no sealing, no relay; an agent on the fake mini ("M/…")
// gets its files under remote-M/ in the temp state dir, so the app sees a
// path that is not the one it sent.

type fakeUpload struct {
	dir, name, sha string
	size, got      int64
	data           []byte
}

var (
	uploadsMu sync.Mutex
	uploads   = map[string]*fakeUpload{}
)

const fakeChunk = 512 * 1024

func (d *daemon) filesCall(method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "files.put":
		var p struct {
			Agent, Machine, Draft, Name, SHA256 string
			Size                                int64
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, errCode("invalid", "bad params")
		}
		name := strings.TrimLeft(filepath.Base(strings.ReplaceAll(p.Name, "\n", "")), ".")
		if name == "" || len(name) > 120 {
			return nil, errCode("invalid", "bad name")
		}
		if p.Size < 0 || p.Size > 50<<20 {
			return nil, errCode("invalid", name+" is larger than 50 MB")
		}
		base := os.Getenv("HESPER_STATE_DIR")
		if base == "" {
			base = os.TempDir()
		}
		var dir string
		switch {
		case p.Agent != "":
			d.mu.Lock()
			_, ok := d.agents[p.Agent]
			d.mu.Unlock()
			if !ok {
				return nil, errCode("not_found", "no agent "+p.Agent)
			}
			m, local, _ := strings.Cut(p.Agent, "/")
			if m != d.self {
				base = filepath.Join(base, "remote-"+m)
			}
			dir = filepath.Join(base, "attachments", local)
		case p.Machine != "" && p.Draft != "":
			if p.Machine != d.self {
				base = filepath.Join(base, "remote-"+p.Machine)
			}
			dir = filepath.Join(base, "attachments", p.Draft)
		default:
			return nil, errCode("invalid", "agent or machine+draft is required")
		}
		var b [8]byte
		rand.Read(b[:])
		id := "at-" + hex.EncodeToString(b[:])
		uploadsMu.Lock()
		uploads[id] = &fakeUpload{dir: dir, name: name, sha: p.SHA256, size: p.Size}
		uploadsMu.Unlock()
		return map[string]any{"upload": id, "chunk": fakeChunk}, nil
	case "files.chunk":
		var p struct {
			Upload string
			Offset int64
			Data   []byte
			Last   bool
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, errCode("invalid", "bad params")
		}
		uploadsMu.Lock()
		defer uploadsMu.Unlock()
		u := uploads[p.Upload]
		if u == nil {
			return nil, errCode("not_found", "no upload "+p.Upload)
		}
		if p.Offset != u.got {
			return nil, errCode("invalid", fmt.Sprintf("expected offset %d", u.got))
		}
		u.data = append(u.data, p.Data...)
		u.got += int64(len(p.Data))
		if !p.Last {
			return map[string]any{"received": u.got}, nil
		}
		delete(uploads, p.Upload)
		sum := sha256.Sum256(u.data)
		if u.got != u.size || hex.EncodeToString(sum[:]) != u.sha {
			return nil, errCode("invalid", "checksum mismatch")
		}
		os.MkdirAll(u.dir, 0o700)
		path := filepath.Join(u.dir, u.name)
		if err := os.WriteFile(path, u.data, 0o600); err != nil {
			return nil, errCode("invalid", err.Error())
		}
		return map[string]any{"path": path, "size": u.got}, nil
	}
	return nil, errCode("not_found", method)
}
