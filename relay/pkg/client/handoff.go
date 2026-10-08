package client

import (
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/session"
	"github.com/derzierau/hesper/relay/pkg/transfer"
)

// Probe asks a hesperd host whether it has the project at path (as on
// the source machine, whose home is home) and which of commits.
func (c *Controller) Probe(ctx context.Context, machineID, path, home string, commits []string) (session.Probe, error) {
	var result session.Probe
	params := map[string]any{"path": path, "commits": commits}
	if home != "" {
		params["home"] = home
	}
	raw, err := c.Request(ctx, machineID, "agents.probe", params)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}

// UploadFile is one bundle file: Name as the host stores it, Path locally.
type UploadFile struct{ Name, Path string }

// Upload encrypts files for hostKey (the host's transferKey) and sends them in
// 512 KiB chunks. A chunk that timed out is sent once more; the host accepts a
// repeated offset. progress, if set, receives the bytes sent so far.
func (c *Controller) Upload(ctx context.Context, machineID, hostKey, upload string, files []UploadFile, progress func(sent, total int64)) error {
	key, err := transfer.ParsePublicKey(hostKey)
	if err != nil {
		return err
	}
	sender, err := transfer.NewSender(key, upload)
	if err != nil {
		return err
	}
	var total, sent int64
	for _, f := range files {
		info, err := os.Stat(f.Path)
		if err != nil {
			return err
		}
		total += info.Size()
	}
	for _, f := range files {
		if err := c.uploadFile(ctx, machineID, sender, upload, f, func(n int64) {
			sent += n
			if progress != nil {
				progress(sent, total)
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) uploadFile(ctx context.Context, machineID string, sender *transfer.Sender, upload string, f UploadFile, sent func(int64)) error {
	in, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	hash := sha256.New()
	buf := make([]byte, transfer.ChunkSize)
	size := info.Size()
	for index, offset := uint64(0), int64(0); ; index, offset = index+1, offset+transfer.ChunkSize {
		n, err := io.ReadFull(in, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}
		last := offset+int64(n) >= size
		hash.Write(buf[:n])
		sealed, err := sender.Seal(f.Name, index, last, buf[:n])
		if err != nil {
			return err
		}
		params := map[string]any{"upload": upload, "name": f.Name, "offset": offset, "data": sealed, "last": last}
		if last {
			params["sha256"] = hex.EncodeToString(hash.Sum(nil))
			params["epk"] = sender.EphemeralKey()
		}
		if err := c.chunk(ctx, machineID, params); err != nil {
			return err
		}
		sent(int64(n))
		if last {
			return nil
		}
	}
}

func (c *Controller) chunk(ctx context.Context, machineID string, params map[string]any) error {
	for attempt := 0; ; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		_, err := c.Request(requestCtx, machineID, "transfer", params)
		cancel()
		var fault *protocol.Error
		if err == nil || attempt > 0 || ctx.Err() != nil || !errors.As(err, &fault) || fault.Code != "timeout" {
			return err
		}
	}
}

// Spawn has a hesperd host import a completed upload (agents.import:
// unpack it, start its agent). It returns the job to poll with Job.
func (c *Controller) Spawn(ctx context.Context, machineID, upload string) (string, error) {
	raw, err := c.Request(ctx, machineID, "agents.import", map[string]any{"upload": upload})
	if err != nil {
		return "", err
	}
	var result struct {
		Job string `json:"job"`
	}
	err = json.Unmarshal(raw, &result)
	return result.Job, err
}

// Job reports an import job; cancel stops a queued one.
func (c *Controller) Job(ctx context.Context, machineID, job string, cancel bool) (session.Job, error) {
	var result session.Job
	params := map[string]any{"job": job}
	if cancel {
		params["cancel"] = true
	}
	raw, err := c.Request(ctx, machineID, "job", params)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}

// Export is a staged export on a host.
type Export struct {
	Download string `json:"download"`
	State    string `json:"state"`
	ID       string `json:"id"` // the bundle ID (manifest id)
	Files    []struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// downloadNames are the files an export may hold; nothing else is written.
var downloadNames = map[string]bool{"manifest.json": true, "transcript.jsonl": true, "code.bundle": true, "folder.tar": true} // folder.tar: bring the folder

// Export has a hesperd host pack agent id for a move (incremental from
// have, commits the target has) for key (this side's ephemeral X25519
// key) and polls until it is staged.
func (c *Controller) Export(ctx context.Context, machineID, id string, have []string, key *ecdh.PrivateKey) (Export, error) {
	var result Export
	params := map[string]any{"key": transfer.EncodePublicKey(key.PublicKey()), "id": id}
	if len(have) > 0 {
		params["have"] = have
	}
	for {
		requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		raw, err := c.Request(requestCtx, machineID, "agents.export", params)
		cancel()
		if err != nil {
			return result, err
		}
		result = Export{}
		if err := json.Unmarshal(raw, &result); err != nil {
			return result, err
		}
		if result.State == "ready" {
			for _, f := range result.Files {
				if !downloadNames[f.Name] || f.Size < 0 {
					return result, protocol.Err("invalid_response", "The export names an unexpected file")
				}
			}
			return result, nil
		}
		if result.State != "packing" || result.Download == "" {
			return result, protocol.Err("invalid_response", "Unexpected export state "+result.State)
		}
		params = map[string]any{"download": result.Download}
	}
}

// Download fetches every file of a staged export into dir (created 0700),
// opening each chunk with key and the pinned hostKey and checking its size
// and sha256. progress, if set, receives the bytes received so far.
func (c *Controller) Download(ctx context.Context, machineID, hostKey string, e Export, key *ecdh.PrivateKey, dir string, progress func(got, total int64)) error {
	host, err := transfer.ParsePublicKey(hostKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	opener := transfer.NewRequesterDownload(key, host, e.Download)
	var total, got int64
	for _, f := range e.Files {
		total += f.Size
	}
	for _, f := range e.Files {
		if !downloadNames[f.Name] {
			return protocol.Err("invalid_response", "The export names an unexpected file")
		}
		if err := c.downloadFile(ctx, machineID, opener, e.Download, f.Name, f.Size, f.SHA256, dir, func(n int64) {
			got += n
			if progress != nil {
				progress(got, total)
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) downloadFile(ctx context.Context, machineID string, opener *transfer.Download, download, name string, size int64, sum, dir string, received func(int64)) error {
	final := filepath.Join(dir, name)
	out, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	hash := sha256.New()
	w := io.MultiWriter(out, hash)
	var written int64
	for index, offset := uint64(0), int64(0); ; index, offset = index+1, offset+transfer.ChunkSize {
		var chunk struct {
			Data []byte `json:"data"`
			Last bool   `json:"last"`
		}
		raw, err := c.retry(ctx, machineID, "download", map[string]any{"download": download, "name": name, "offset": offset})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return err
		}
		plain, err := opener.Open(name, index, chunk.Last, chunk.Data)
		if err != nil {
			return protocol.Err("integrity", name+" failed authentication; bring it back again")
		}
		if (!chunk.Last && len(plain) != transfer.ChunkSize) || written+int64(len(plain)) > size {
			return protocol.Err("integrity", name+" does not have the announced size")
		}
		if _, err := w.Write(plain); err != nil {
			return err
		}
		written += int64(len(plain))
		received(int64(len(plain)))
		if chunk.Last {
			break
		}
	}
	if written != size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), sum) {
		return protocol.Err("integrity", name+" does not match its sha256; bring it back again")
	}
	if err := out.Chmod(0600); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), final)
}

// retry sends a request once more after a timeout; the methods it is used
// for are safe to repeat.
func (c *Controller) retry(ctx context.Context, machineID, method string, params map[string]any) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		raw, err := c.Request(requestCtx, machineID, method, params)
		cancel()
		var fault *protocol.Error
		if err == nil || attempt > 0 || ctx.Err() != nil || !errors.As(err, &fault) || fault.Code != "timeout" {
			return raw, err
		}
	}
}
