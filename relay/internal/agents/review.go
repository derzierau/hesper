package agents

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/internal/review"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Review (docs/rebuild-contract.md "As built — review"): a Claude or
// Codex agent that settled (done, idle, exited) with changes in its Git
// folder against its review base is ready for review. The base is HEAD
// when the agent started (Agent.ReviewBase; a moved agent keeps its own,
// an agent without one reviews from its branch point); the folder is its
// files now, untracked ones included (internal/review, through a copy of
// the index).

// reviewTimeout bounds one review call's Git work.
var reviewTimeout = 2 * time.Minute

// reviewParallel bounds the folders review.list looks at at once.
const reviewParallel = 4

// readyForReview: the states of an agent whose work can be reviewed.
func readyForReview(state string) bool {
	return state == wire.StateDone || state == wire.StateIdle || state == wire.StateExited
}

// reviewBaseAt is an agent's review base when it starts in dir: HEAD of
// its Git folder (Claude and Codex agents only; "" otherwise).
func (r *Registry) reviewBaseAt(kind, dir string) string {
	if !checkpointable(kind) {
		return ""
	}
	head, err := r.g.run(dir, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return ""
	}
	return head
}

// movedReviewBase is a moved agent's review base: the source's when the
// bundle brought that commit, else the commit it moved at.
func (r *Registry) movedReviewBase(workdir string, m *handoff.Manifest) string {
	if !checkpointable(m.Agent.Kind) {
		return ""
	}
	for _, sha := range []string{m.Agent.ReviewBase, m.Project.Base} {
		if sha != "" && r.g.ok(workdir, "rev-parse", "--verify", "-q", sha+"^{commit}") {
			return sha
		}
	}
	return ""
}

func (r *Registry) reviewGit(ctx context.Context) review.Git {
	return review.Git{Ctx: ctx, Env: r.env}
}

// reviewFolder opens an agent's folder and its review base.
func (r *Registry) reviewFolder(g review.Git, a wire.Agent) (*review.Folder, string, error) {
	if !checkpointable(a.Kind) {
		return nil, "", wire.Errorf(wire.CodeInvalid, "%s is a shell: only Claude and Codex agents are reviewed", a.ID)
	}
	f := g.Open(a.Dir())
	if f == nil {
		return nil, "", wire.Errorf(wire.CodeInvalid, "%s's folder %s is not a Git repository with a commit", a.ID, a.Dir())
	}
	base := a.ReviewBase
	if !g.HasCommit(f, base) {
		base = g.BranchPoint(f)
	}
	return f, base, nil
}

// settledForReview: a settles (the lock is held); once its folder shows
// changes, subscribers get review.changed.
func (r *Registry) settledForReview(a *agent) {
	if !checkpointable(a.Kind) || r.closing {
		return
	}
	snap := a.Agent
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), reviewTimeout)
		defer cancel()
		g := r.reviewGit(ctx)
		if f, base, err := r.reviewFolder(g, snap); err == nil && g.HasChanges(f, base) {
			r.NoteReview(snap.ID)
		}
	}()
}

// reviewAgent is one agent of this Mac for a review method.
func (r *Registry) reviewAgent(id string) (wire.Agent, error) {
	if id == "" {
		return wire.Agent{}, &badParams{errors.New("id is required")}
	}
	return r.Get(id)
}

// ReviewList is review.list on this Mac: the agents ready for review,
// the most recently settled first.
func (r *Registry) ReviewList(ctx context.Context) []wire.ReviewItem {
	var candidates []wire.Agent
	for _, a := range r.List() {
		if checkpointable(a.Kind) && readyForReview(a.State) {
			candidates = append(candidates, a)
		}
	}
	items := make([]*wire.ReviewItem, len(candidates))
	sem := make(chan struct{}, reviewParallel)
	var wg sync.WaitGroup
	for i, a := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			item, err := r.reviewItem(ctx, a)
			if err != nil {
				r.opt.Logf("hesperd: review of %s: %v", a.ID, err)
				return
			}
			items[i] = item
		}()
	}
	wg.Wait()
	list := []wire.ReviewItem{}
	for _, item := range items {
		if item != nil {
			list = append(list, *item)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].ReadyAt.After(list[j].ReadyAt) })
	return list
}

// reviewItem is a's entry of review.list; nil when its folder has no
// changes (or is no Git folder).
func (r *Registry) reviewItem(ctx context.Context, a wire.Agent) (*wire.ReviewItem, error) {
	ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()
	g := r.reviewGit(ctx)
	f, base, err := r.reviewFolder(g, a)
	if err != nil {
		return nil, nil
	}
	tree, err := g.Snapshot(f)
	if err != nil {
		return nil, err
	}
	changes, err := g.Changes(f, base, tree)
	if err != nil || len(changes) == 0 {
		return nil, err
	}
	item := &wire.ReviewItem{ID: a.ID, Machine: a.Machine, Name: a.Name, Kind: a.Kind, Project: a.Project, Branch: a.Branch,
		Worktree: a.Worktree, State: a.State, Files: len(changes), ReadyAt: a.StateSince, Base: base}
	for _, c := range changes {
		item.Added += c.Added
		item.Removed += c.Removed
	}
	item.Risk, item.RiskNotes = review.ChangeRisk(changes)
	item.Evidence = r.evidenceOf(a.ID, true).Freshness // reviewevidence.go
	_, local := r.split(a.ID)
	r.reviews().get(local, func(l *review.Log) {
		if l.Reviewed != nil {
			item.ReviewedAt = l.Reviewed.At
		}
	})
	return item, nil
}

// ReviewDiff is review.diff on this Mac.
func (r *Registry) ReviewDiff(ctx context.Context, p wire.ReviewDiffParams) (wire.ReviewDiff, error) {
	a, err := r.reviewAgent(p.ID)
	if err != nil {
		return wire.ReviewDiff{}, err
	}
	lines := wire.DefaultReviewContext
	if p.Context != nil {
		lines = min(max(*p.Context, 0), wire.MaxReviewContext)
	}
	ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()
	g := r.reviewGit(ctx)
	f, base, err := r.reviewFolder(g, a)
	if err != nil {
		return wire.ReviewDiff{}, err
	}
	tree, err := g.Snapshot(f)
	if err != nil {
		return wire.ReviewDiff{}, wire.Errorf(wire.CodeInvalid, "%s: %v", a.ID, err)
	}
	d, _, err := g.Diff(f, base, tree, lines)
	if err != nil {
		return wire.ReviewDiff{}, wire.Errorf(wire.CodeInvalid, "%s: %v", a.ID, err)
	}
	return d, nil
}

// reviewCall serves the review.* methods on the local socket: this Mac's
// agents here, another Mac's on its daemon (forward); review.list merges
// every Mac's.
func (s *Server) reviewCall(method string, params json.RawMessage, forward func(id string) (bool, any, error)) (any, error) {
	reg := s.reg
	if method == "review.list" {
		return s.reviewList()
	}
	var head wire.IDParams
	if err := decode(params, &head); err != nil {
		return nil, err
	}
	if head.ID == "" {
		return nil, &badParams{errors.New("id is required")}
	}
	if method == "review.diff" && reg.IsRemote(head.ID) {
		var p wire.ReviewDiffParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return s.remoteDiff(p)
	}
	if remote := reg.opt.Remote; remote != nil && treeMutating[method] && reg.IsRemote(head.ID) {
		// busy here: a host's "busy" would arrive as "unavailable".
		for _, a := range remote.Agents() {
			if a.ID == head.ID && reviewBusy(a.State) {
				return nil, busyError(a)
			}
		}
	}
	if ok, res, err := forward(head.ID); ok {
		if err == nil && treeMutating[method] {
			reg.NoteReview(head.ID) // the host's own note stays there
		}
		if raw, ok := res.(json.RawMessage); ok && err == nil && method == "review.provenance" {
			// The session as this daemon's shared history names its
			// machine.
			var p wire.ReviewProvenance
			if json.Unmarshal(raw, &p) == nil && p.SessionID != "" {
				machine, _ := reg.split(head.ID)
				if _, rest, ok := strings.Cut(p.SessionID, ":"); ok {
					p.SessionID = machine + ":" + rest
				}
				return p, nil
			}
		}
		return res, err
	}
	ctx := context.Background()
	switch method {
	case "review.diff":
		var p wire.ReviewDiffParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return reg.ReviewDiff(ctx, p)
	case "review.accept":
		var p wire.ReviewAcceptParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return reg.ReviewAccept(ctx, p) // reviewact.go
	case "review.reject":
		var p wire.ReviewRejectParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, reg.ReviewReject(ctx, p)
	case "review.sendBack":
		var p wire.ReviewSendBackParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, reg.ReviewSendBack(ctx, p)
	case "review.evidence":
		return reg.ReviewEvidence(ctx, head.ID) // reviewevidence.go
	case "review.provenance":
		var p wire.ReviewProvenanceParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return reg.ReviewProvenance(ctx, p)
	}
	return nil, &noMethod{method}
}

// reviewList is review.list across Macs: this Mac's, then every
// connected one's (a Mac whose daemon has no review.* lists none).
func (s *Server) reviewList() (any, error) {
	reg := s.reg
	ctx, cancel := context.WithTimeout(context.Background(), reviewTimeout)
	defer cancel()
	list := reg.ReviewList(ctx)
	remote := reg.opt.Remote
	if remote == nil {
		return list, nil
	}
	var machines []string
	for _, m := range remote.Machines() {
		if m.Online && m.Short != reg.machine {
			machines = append(machines, m.Short)
		}
	}
	lists := make([][]wire.ReviewItem, len(machines))
	var wg sync.WaitGroup
	for i, short := range machines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			raw, err := remote.Call(mctx, short, "review.list", json.RawMessage("{}"))
			if err != nil {
				return
			}
			var items []wire.ReviewItem
			if json.Unmarshal(raw, &items) != nil {
				return
			}
			for j := range items {
				_, local, _ := strings.Cut(items[j].ID, "/")
				if local == "" {
					local = items[j].ID
				}
				items[j].ID, items[j].Machine = short+"/"+local, short
			}
			lists[i] = items
		}()
	}
	wg.Wait()
	for _, items := range lists {
		list = append(list, items...)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].ReadyAt.After(list[j].ReadyAt) })
	return list, nil
}

// remoteDiff is review.diff of another Mac's agent: fetched in parts
// (internal/review/parts.go), as a diff can be larger than one relay
// message.
func (s *Server) remoteDiff(p wire.ReviewDiffParams) (any, error) {
	remote := s.reg.opt.Remote
	machine, _ := s.reg.split(p.ID)
	if remote == nil {
		return nil, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", machine)
	}
	ctx, cancel := context.WithTimeout(context.Background(), reviewTimeout)
	defer cancel()
	var data []string
	tree, parts := "", 1
	for n := 0; n < parts; n++ {
		params := map[string]any{"id": p.ID, "part": n}
		if p.Context != nil {
			params["context"] = *p.Context
		}
		if tree != "" {
			params["tree"] = tree
		}
		raw, _ := json.Marshal(params)
		res, err := remote.Call(ctx, machine, "review.diff", raw)
		if err != nil {
			return nil, err
		}
		var part review.Part
		if err := json.Unmarshal(res, &part); err != nil {
			return nil, wire.Errorf(wire.CodeRemote, "%s sent a bad diff part: %v", machine, err)
		}
		if n == 0 {
			tree, parts = part.Tree, part.Parts
			if parts < 1 || parts > review.MaxParts {
				return nil, wire.Errorf(wire.CodeRemote, "%s sent a diff in %d parts", machine, parts)
			}
		} else if part.Tree != tree || part.Parts != parts || part.Part != n {
			return nil, wire.Errorf(wire.CodeInvalid, "%s's folder changed while its diff was sent: fetch it again", p.ID)
		}
		data = append(data, part.Data)
	}
	var d wire.ReviewDiff
	if err := review.Decode(data, &d); err != nil {
		return nil, wire.Errorf(wire.CodeRemote, "%s sent a bad diff: %v", machine, err)
	}
	return d, nil
}
