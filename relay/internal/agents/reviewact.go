package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/internal/review"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// What a reviewer does with an agent's work (review.go): accept it
// (commit all or some hunks in the agent's folder), reject hunks (revert
// them there), or send it back with notes (typed into the agent; the
// reviewed point is kept for an interdiff later).

// reviewBusy: an agent in these states is still at work; its folder is
// not changed under it.
func reviewBusy(state string) bool {
	switch state {
	case wire.StateStarting, wire.StateWorking, wire.StateApproval, wire.StateQuestion:
		return true
	}
	return false
}

func busyError(a wire.Agent) error {
	return wire.Errorf(wire.CodeBusy, "%s is %s: wait until it settles", a.ID, a.State)
}

// reviewNow is an agent's folder, base, tree and diff for a change to
// it: refused while the agent works, or when the folder is no longer the
// tree the reviewer saw.
func (r *Registry) reviewNow(g review.Git, id string, contextLines *int, tree string) (wire.Agent, *review.Folder, string, wire.ReviewDiff, []review.Change, error) {
	fail := func(err error) (wire.Agent, *review.Folder, string, wire.ReviewDiff, []review.Change, error) {
		return wire.Agent{}, nil, "", wire.ReviewDiff{}, nil, err
	}
	a, err := r.reviewAgent(id)
	if err != nil {
		return fail(err)
	}
	if reviewBusy(a.State) {
		return fail(busyError(a))
	}
	f, base, err := r.reviewFolder(g, a)
	if err != nil {
		return fail(err)
	}
	now, err := g.Snapshot(f)
	if err != nil {
		return fail(wire.Errorf(wire.CodeInvalid, "%s: %v", a.ID, err))
	}
	if tree != "" && tree != now {
		return fail(wire.Errorf(wire.CodeInvalid, "%s's folder changed since that diff: fetch it again", a.ID))
	}
	lines := wire.DefaultReviewContext
	if contextLines != nil {
		lines = min(max(*contextLines, 0), wire.MaxReviewContext)
	}
	d, changes, err := g.Diff(f, base, now, lines)
	if err != nil {
		return fail(wire.Errorf(wire.CodeInvalid, "%s: %v", a.ID, err))
	}
	return a, f, base, d, changes, nil
}

// acceptMessage is the default commit message: the agent's summary,
// else its name.
func acceptMessage(a wire.Agent) string {
	subject := firstLine(a.Summary, 72)
	if subject == "" || subject == NotResumedNote {
		subject = firstLine(a.Name, 72)
	}
	return subject
}

// ReviewAccept is review.accept on this Mac: the accepted changes are
// committed on the folder's HEAD and become the agent's review base.
func (r *Registry) ReviewAccept(ctx context.Context, p wire.ReviewAcceptParams) (wire.ReviewAcceptResult, error) {
	r.reviewRun.Lock()
	defer r.reviewRun.Unlock()
	ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()
	g := r.reviewGit(ctx)
	a, f, base, d, changes, err := r.reviewNow(g, p.ID, p.Context, p.Tree)
	if err != nil {
		return wire.ReviewAcceptResult{}, err
	}
	if len(d.Files) == 0 {
		return wire.ReviewAcceptResult{}, wire.Errorf(wire.CodeInvalid, "%s has no changes to accept", a.ID)
	}
	var sel review.Selection
	if p.Hunks != nil {
		if len(p.Hunks) == 0 {
			return wire.ReviewAcceptResult{}, &badParams{errors.New("hunks is empty (omit it to accept everything)")}
		}
		if sel, err = review.ParseSelection(d, p.Hunks); err != nil {
			return wire.ReviewAcceptResult{}, wire.Errorf(wire.CodeInvalid, "%v", err)
		}
	}
	message := strings.TrimSpace(p.Message)
	if message == "" {
		message = acceptMessage(a)
	}
	commit, err := g.Accept(f, base, d, changes, sel, message)
	if err != nil {
		return wire.ReviewAcceptResult{}, wire.Errorf(wire.CodeInvalid, "%s: %v", a.ID, err)
	}
	r.setReviewBase(a, commit)
	r.NoteReview(a.ID)
	return wire.ReviewAcceptResult{Commit: commit}, nil
}

// setReviewBase moves an agent's review base (an accept).
func (r *Registry) setReviewBase(snap wire.Agent, commit string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, local := r.split(snap.ID)
	if a := r.agents[local]; a != nil && a.Created.Equal(snap.Created) && a.ReviewBase != commit {
		a.ReviewBase = commit
		r.changed(a)
	}
}

// ReviewReject is review.reject on this Mac: the hunks (or whole files)
// go back to the base in the working tree.
func (r *Registry) ReviewReject(ctx context.Context, p wire.ReviewRejectParams) error {
	if len(p.Hunks) == 0 {
		return &badParams{errors.New("hunks is required")}
	}
	r.reviewRun.Lock()
	defer r.reviewRun.Unlock()
	ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	defer cancel()
	g := r.reviewGit(ctx)
	a, f, _, d, changes, err := r.reviewNow(g, p.ID, p.Context, p.Tree)
	if err != nil {
		return err
	}
	sel, err := review.ParseSelection(d, p.Hunks)
	if err != nil {
		return wire.Errorf(wire.CodeInvalid, "%v", err)
	}
	if err := g.Reject(f, d, changes, sel); err != nil {
		return wire.Errorf(wire.CodeInvalid, "%s: %v", a.ID, err)
	}
	r.NoteReview(a.ID)
	return nil
}

// sendBackText is the one instruction review.sendBack types: the notes,
// one per line, then the message.
func sendBackText(notes []wire.ReviewNote, message string) string {
	var b strings.Builder
	if len(notes) > 0 {
		b.WriteString("Review notes on your changes; please address them:\n")
		for _, n := range notes {
			where := n.Path
			switch {
			case n.Line > 0 && n.Side == "old":
				where = fmt.Sprintf("%s (old line %d)", n.Path, n.Line)
			case n.Line > 0:
				where = fmt.Sprintf("%s:%d", n.Path, n.Line)
			}
			text := strings.Join(strings.Fields(n.Text), " ")
			fmt.Fprintf(&b, "- %s: %s\n", where, text)
		}
	}
	if message = strings.TrimSpace(message); message != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(message)
	}
	return strings.TrimSpace(b.String())
}

// ReviewSendBack is review.sendBack on this Mac: the reviewed point is
// kept (refs/hesper/checkpoints/<local id>-reviewed, pruned with the
// checkpoints), then the notes are typed into the agent as one
// instruction (agents.input: pasted, submitted).
func (r *Registry) ReviewSendBack(ctx context.Context, p wire.ReviewSendBackParams) error {
	for _, n := range p.Notes {
		if strings.TrimSpace(n.Text) == "" || n.Path == "" || n.Line < 0 || (n.Side != "" && n.Side != "old" && n.Side != "new") {
			return &badParams{errors.New("a note needs a path and text (line ≥ 0, side old or new)")}
		}
	}
	text := sendBackText(p.Notes, p.Message)
	if text == "" {
		return &badParams{errors.New("notes or a message is required")}
	}
	a, err := r.reviewAgent(p.ID)
	if err != nil {
		return err
	}
	if reviewBusy(a.State) {
		return busyError(a)
	}
	r.reviewRun.Lock()
	ctx, cancel := context.WithTimeout(ctx, reviewTimeout)
	g := r.reviewGit(ctx)
	if f, _, err := r.reviewFolder(g, a); err == nil {
		r.markReviewed(g, f, a)
	}
	cancel()
	r.reviewRun.Unlock()
	if err := r.Input(wire.InputParams{ID: a.ID, Text: text, Paste: true, Submit: true}); err != nil {
		return err
	}
	r.NoteReview(a.ID)
	return nil
}

// markReviewed keeps the folder as it is now as the agent's reviewed
// point.
func (r *Registry) markReviewed(g review.Git, f *review.Folder, a wire.Agent) {
	_, local := r.split(a.ID)
	tree, err := g.Snapshot(f)
	if err != nil {
		r.opt.Logf("hesperd: reviewed point of %s: %v", a.ID, err)
		return
	}
	ref := handoff.CheckpointPrefix + local + "-reviewed"
	commit, err := g.MarkReviewed(f, tree, ref)
	if err != nil {
		r.opt.Logf("hesperd: reviewed point of %s: %v", a.ID, err)
		return
	}
	if repo := r.g.mainRoot(f.Top); repo != "" {
		r.noteCheckpointRepo(repo) // pruned with the checkpoints
	}
	r.reviews().update(local, func(l *review.Log) {
		l.Reviewed = &review.Reviewed{Commit: commit, Tree: tree, Ref: ref, At: time.Now().UTC()}
	})
}
