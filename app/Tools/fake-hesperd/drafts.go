package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Drafts (contract: drafts.list/save/remove, drafts.changed/removed on
// agents.subscribe), kept in drafts.json next to the socket so a restarted
// fake daemon (or app) finds them again.

type Draft map[string]any

func (d *daemon) draftsPath() string { return filepath.Join(filepath.Dir(d.sock), "drafts.json") }

func (d *daemon) loadDrafts() {
	d.drafts = map[string]Draft{}
	data, err := os.ReadFile(d.draftsPath())
	if err != nil {
		return
	}
	var f struct {
		Drafts []Draft `json:"drafts"`
	}
	if json.Unmarshal(data, &f) == nil {
		for _, x := range f.Drafts {
			if id, _ := x["id"].(string); id != "" {
				d.drafts[id] = x
			}
		}
	}
}

// draftList is every draft, oldest first (the lock is held).
func (d *daemon) draftList() []Draft {
	list := make([]Draft, 0, len(d.drafts))
	for _, x := range d.drafts {
		list = append(list, x)
	}
	sort.Slice(list, func(i, j int) bool {
		ci, _ := list[i]["created"].(string)
		cj, _ := list[j]["created"].(string)
		if ci != cj {
			return ci < cj
		}
		return list[i]["id"].(string) < list[j]["id"].(string)
	})
	return list
}

func (d *daemon) saveDraftsLocked() {
	data, _ := json.MarshalIndent(map[string]any{"version": 1, "drafts": d.draftList()}, "", "  ")
	tmp := d.draftsPath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, d.draftsPath())
	}
}

func (d *daemon) broadcastLocked(method string, params any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	for s := range d.subs {
		select {
		case s <- b:
		default:
		}
	}
}

func (d *daemon) draftCall(method string, raw json.RawMessage) (any, *rpcError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch method {
	case "drafts.list":
		return d.draftList(), nil
	case "drafts.save":
		var p struct {
			Draft Draft `json:"draft"`
		}
		if json.Unmarshal(raw, &p) != nil || p.Draft == nil {
			return nil, errCode("invalid", "draft is required")
		}
		id, _ := p.Draft["id"].(string)
		if id == "" {
			id = "d-" + newID()
			p.Draft["id"] = id
		}
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		if old, ok := d.drafts[id]; ok && old["created"] != nil {
			p.Draft["created"] = old["created"]
		} else if c, _ := p.Draft["created"].(string); c == "" {
			p.Draft["created"] = ts
		}
		p.Draft["updated"] = ts
		d.drafts[id] = p.Draft
		d.saveDraftsLocked()
		d.broadcastLocked("drafts.changed", map[string]any{"draft": p.Draft})
		return p.Draft, nil
	case "drafts.remove":
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &p)
		if _, ok := d.drafts[p.ID]; !ok {
			return nil, errCode("not_found", "no draft "+p.ID)
		}
		delete(d.drafts, p.ID)
		d.saveDraftsLocked()
		d.broadcastLocked("drafts.removed", map[string]any{"id": p.ID})
		return map[string]any{}, nil
	}
	return nil, errCode("not_found", "method not found: "+method)
}
