package agents

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/review"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Evidence and provenance (review.go, phase 2): every Claude and Codex
// agent's prompts, commands and edits from its hook events go into its
// review log (reviewlog.go; the last 2,000 events, kept across daemon
// restarts, removed with the agent).

// recordEvidence adds one hook event of the agent local to its review
// log (without the registry's lock).
func (r *Registry) recordEvidence(local, event string, data map[string]any) {
	if !review.RecordsEvent(event) {
		return
	}
	at := time.Now().UTC()
	r.mu.Lock()
	gone := r.agents[local] == nil
	r.mu.Unlock()
	if gone {
		return
	}
	r.reviews().record(local, func(l *review.Log) bool { return l.Record(event, data, at) })
}

// reviewChanges are an agent's folder and its changes against its base.
func (r *Registry) reviewChanges(g review.Git, a wire.Agent) (*review.Folder, []review.Change, error) {
	f, base, err := r.reviewFolder(g, a)
	if err != nil {
		return nil, nil, err
	}
	tree, err := g.Snapshot(f)
	if err != nil {
		return nil, nil, wire.Errorf(wire.CodeInvalid, "%s: %v", a.ID, err)
	}
	changes, err := g.Changes(f, base, tree)
	if err != nil {
		return nil, nil, wire.Errorf(wire.CodeInvalid, "%s: %v", a.ID, err)
	}
	return f, changes, nil
}

// evidenceOf is an agent's evidence from its log.
func (r *Registry) evidenceOf(id string, changed bool) wire.ReviewEvidence {
	_, local := r.split(id)
	var ev wire.ReviewEvidence
	r.reviews().get(local, func(l *review.Log) { ev = l.Evidence(changed) })
	return ev
}

// ReviewEvidence is review.evidence on this Mac.
func (r *Registry) ReviewEvidence(ctx context.Context, id string) (wire.ReviewEvidence, error) {
	a, err := r.reviewAgent(id)
	if err != nil {
		return wire.ReviewEvidence{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()
	f, changes, err := r.reviewChanges(r.reviewGit(ctx), a)
	if err != nil {
		return wire.ReviewEvidence{}, err
	}
	ev := r.evidenceOf(a.ID, len(changes) > 0)
	ev.Attachments = review.Attachments(f.Top, changes)
	return ev, nil
}

// ReviewProvenance is review.provenance on this Mac: path is the diff's
// (relative to the repository's top).
func (r *Registry) ReviewProvenance(ctx context.Context, p wire.ReviewProvenanceParams) (wire.ReviewProvenance, error) {
	if p.Path == "" || filepath.IsAbs(p.Path) || strings.HasPrefix(filepath.Clean(p.Path), "..") {
		return wire.ReviewProvenance{}, &badParams{errors.New("path is the diff's path, relative to the repository")}
	}
	a, err := r.reviewAgent(p.ID)
	if err != nil {
		return wire.ReviewProvenance{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()
	f, _, err := r.reviewFolder(r.reviewGit(ctx), a)
	if err != nil {
		return wire.ReviewProvenance{}, err
	}
	_, local := r.split(a.ID)
	var res wire.ReviewProvenance
	r.reviews().get(local, func(l *review.Log) {
		e, ok := l.Provenance(filepath.Join(f.Top, filepath.FromSlash(p.Path)), p.Line)
		if !ok {
			return
		}
		res = wire.ReviewProvenance{Turn: e.Turn, Tool: e.Tool, At: e.At, Prompt: l.PromptOf(e.Session, e.Turn)}
		if e.Session != "" {
			res.SessionID = r.machine + ":" + a.Kind + ":" + e.Session
		}
	})
	return res, nil
}
