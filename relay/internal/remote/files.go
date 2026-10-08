package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/transfer"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Attachments for another machine's agents (files.put / files.chunk):
// this daemon picks the upload id, seals every chunk with pkg/transfer
// for the host's published transfer key (the relay carries ciphertext
// inside the end-to-end channel) and sends the host's files.put /
// files.chunk as signed requests (right "type"). The path that comes
// back is the host's.

// fileUploadIdle drops a forwarded upload nobody continues.
var fileUploadIdle = 10 * time.Minute

type fileUpload struct {
	short  string
	name   string
	sender *transfer.Sender
	last   time.Time
}

type fileUploads struct {
	mu sync.Mutex
	m  map[string]*fileUpload
}

func (f *Fleet) fileUploads() *fileUploads {
	u := &f.files
	u.mu.Lock()
	if u.m == nil {
		u.m = map[string]*fileUpload{}
	}
	u.mu.Unlock()
	return u
}

// FilePut starts an upload on machine short.
func (f *Fleet) FilePut(ctx context.Context, short string, p agents.FilePut) (agents.FilePutResult, error) {
	m, c, err := f.machineByShort(short)
	if err != nil {
		return agents.FilePutResult{}, err
	}
	f.mu.Lock()
	key := m.state.TransferKey
	f.mu.Unlock()
	if key == "" {
		return agents.FilePutResult{}, wire.Errorf(wire.CodeUnavailable, "%s does not take attachments (no transfer key)", short)
	}
	hostKey, err := transfer.ParsePublicKey(key)
	if err != nil {
		return agents.FilePutResult{}, wire.Errorf(wire.CodeUnavailable, "%s published a bad transfer key", short)
	}
	name := agents.SanitizeName(p.Name)
	var raw [8]byte
	rand.Read(raw[:])
	upload := "at-" + hex.EncodeToString(raw[:])
	sender, err := transfer.NewSender(hostKey, upload)
	if err != nil {
		return agents.FilePutResult{}, err
	}
	params := map[string]any{"name": name, "size": p.Size, "sha256": p.SHA256, "upload": upload, "epk": sender.EphemeralKey()}
	if p.Agent != "" {
		params["agent"] = localID(p.Agent)
	} else {
		params["draft"] = p.Draft
	}
	rawRes, err := f.request(ctx, c, m, "files.put", params)
	if err != nil {
		return agents.FilePutResult{}, err
	}
	var res agents.FilePutResult
	if err := json.Unmarshal(rawRes, &res); err != nil || res.Upload != upload {
		return agents.FilePutResult{}, wire.Errorf(wire.CodeRemote, "%s answered files.put with another upload", short)
	}
	u := f.fileUploads()
	u.mu.Lock()
	now := time.Now()
	for id, x := range u.m {
		if now.Sub(x.last) > fileUploadIdle {
			delete(u.m, id)
		}
	}
	u.m[upload] = &fileUpload{short: short, name: name, sender: sender, last: now}
	u.mu.Unlock()
	return res, nil
}

// FileChunk seals and sends one chunk of an upload this Fleet started.
func (f *Fleet) FileChunk(ctx context.Context, p agents.FileChunkParams) (agents.FileChunkResult, bool, error) {
	u := f.fileUploads()
	u.mu.Lock()
	up, ok := u.m[p.Upload]
	if ok {
		up.last = time.Now()
	}
	u.mu.Unlock()
	if !ok {
		return agents.FileChunkResult{}, false, nil
	}
	end := func() {
		u.mu.Lock()
		delete(u.m, p.Upload)
		u.mu.Unlock()
	}
	if p.Offset < 0 || p.Offset%transfer.ChunkSize != 0 {
		return agents.FileChunkResult{}, true, wire.Errorf(wire.CodeInvalid, "offset must be a multiple of %d", transfer.ChunkSize)
	}
	sealed, err := up.sender.Seal(up.name, uint64(p.Offset/transfer.ChunkSize), p.Last, p.Data)
	if err != nil {
		return agents.FileChunkResult{}, true, wire.Errorf(wire.CodeInvalid, "chunks hold %d bytes; only the last may be shorter", transfer.ChunkSize)
	}
	m, c, err := f.machineByShort(up.short)
	if err != nil {
		return agents.FileChunkResult{}, true, err
	}
	raw, err := c.Request(client.RequireE2E(ctx), m.id, "files.chunk", map[string]any{"upload": p.Upload, "offset": p.Offset, "data": sealed, "last": p.Last})
	if err != nil {
		end()
		return agents.FileChunkResult{}, true, wireError(err)
	}
	var res agents.FileChunkResult
	if err := json.Unmarshal(raw, &res); err != nil {
		end()
		return agents.FileChunkResult{}, true, wire.Errorf(wire.CodeRemote, "bad files.chunk answer")
	}
	if p.Last {
		end()
	}
	return res, true, nil
}
