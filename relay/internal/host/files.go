package host

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/transfer"
)

// Attachments from controllers (files.put, files.chunk; right "type"):
// files dropped on one of this machine's agents in another Mac's app.
// The controller seals each chunk for this host's transfer key
// (pkg/transfer, bound to the upload id and file name); the host opens
// them and stores the file like a local upload (agents.FileStore), then
// answers with its path here. One audit line per upload, when it ends:
// device, agent, size, ok — never the name or content.

type filePutParams struct {
	Agent  string `json:"agent"`
	Draft  string `json:"draft"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Upload string `json:"upload"`
	EPK    string `json:"epk"`
}

func (s *Service) files(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	if s.Handoff == nil || s.Handoff.Key == nil {
		return nil, protocol.Err("unavailable", "This machine takes no attachments (no transfer key)")
	}
	store := s.Agents.Files()
	switch m.Method {
	case "files.put":
		var p filePutParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		if p.Upload == "" || p.EPK == "" {
			return nil, protocol.Err("invalid_request", "upload and epk are required")
		}
		put := agents.FilePut{Name: p.Name, Size: p.Size, SHA256: p.SHA256, Upload: p.Upload, Draft: p.Draft}
		who := "draft"
		if p.Agent != "" {
			a, err := s.typable(ctx, m, p.Agent)
			if err != nil {
				return nil, err
			}
			put.Agent, who = a.ID, a.ID
		}
		// The sender sealed with the name it sent; storage sanitizes.
		opener, err := transfer.NewOpener(s.Handoff.Key, p.EPK, p.Upload, p.Name)
		if err != nil {
			return nil, protocol.Err("invalid_request", "bad epk")
		}
		caller, _ := CallerFrom(ctx)
		res, err := store.Begin(put, agents.UploadHooks{
			Open: opener.Open,
			End: func(ok bool, size int64) {
				if s.Audit != nil {
					s.Audit("files.put", map[string]any{"device": caller.Device, "name": caller.Name, "method": "files.put", "ok": ok,
						"detail": fmt.Sprintf("attachment for %s, %d bytes", who, size), "e2e": ViaE2E(ctx), "route": RouteOf(ctx)})
				}
			},
		})
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(res), nil
	default: // files.chunk
		var p agents.FileChunkParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		res, err := store.Chunk(p, true)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(res), nil
	}
}
