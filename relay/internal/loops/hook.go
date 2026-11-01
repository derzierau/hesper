package loops

import (
	"strings"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The registry's hooks (internal/agents/loophook.go): called with the
// registry's lock held, so they only touch the store (the names and the
// classifier are read under the store's lock and called without it).

// maxSentences bounds the items one settled turn adds.
const maxSentences = 5

// SetMachine sets this Mac's short name (views).
func (s *Store) SetMachine(short string) {
	s.mu.Lock()
	s.opt.Machine, s.allDirty = short, true
	s.mu.Unlock()
	s.kickViews()
}

// sessionRef is the ref of one of this Mac's sessions ("" unknown).
func (s *Store) sessionRef(kind, sid string) string {
	s.mu.Lock()
	fn := s.sessNode
	s.mu.Unlock()
	if fn == nil || sid == "" || kind == "" || kind == wire.KindShell {
		return ""
	}
	node := fn()
	if node == "" {
		return ""
	}
	return node + ":" + kind + ":" + sid
}

// titleOf is a new loop's title for an agent: its name, else its task's
// first line.
func titleOf(a wire.Agent) string {
	t := strings.TrimSpace(a.Name)
	if t == "" {
		t, _, _ = strings.Cut(strings.TrimSpace(a.Task), "\n")
	}
	if t == "" {
		t = "Untitled loop"
	}
	return clip(t, maxShortText)
}

// Assign puts a newly inserted agent in a loop and returns its id: a.Loop
// when it names one; else a new loop under a.Milestone; else the parent's
// loop; else the source session's; else a new loop titled after the
// agent. Its member link says how it came (started from the parent,
// continued from the session); a touches link records its project. An
// agent that already has a loop (re-inserted) keeps it.
func (s *Store) Assign(a agents.AssignInfo) string {
	if a.Ref == "" || a.Agent.Service != "" {
		return ""
	}
	sess := s.sessionRef(a.Agent.Kind, a.Agent.SessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	var l *LoopRecord
	if cur := s.loopOfLocked(a.Ref); cur != "" && (a.Loop == "" || s.resolveLocked(a.Loop) == cur) {
		l = s.loops[cur]
	}
	if l == nil && a.Loop != "" {
		if id := s.resolveLocked(a.Loop); id != "" {
			l = s.loops[id]
		} else {
			s.opt.Logf("hesperd: loops: agent %s asked for unknown loop %s", a.Ref, a.Loop)
		}
	}
	if l == nil && a.Milestone != "" {
		if m := s.milestones[a.Milestone]; m != nil && !m.Deleted.V {
			l = s.newLoopLocked(titleOf(a.Agent), a.Milestone)
		}
	}
	for _, ref := range []string{a.ParentRef, a.SourceSession} {
		if l == nil && ref != "" {
			if id := s.loopOfLocked(ref); id != "" {
				l = s.loops[id]
			}
		}
	}
	if l == nil {
		l = s.newLoopLocked(titleOf(a.Agent), "")
	}
	link := wire.LoopLink{Kind: wire.LinkAgent, Ref: a.Ref, Rel: wire.RelMember, Label: a.Agent.Name}
	switch {
	case a.ParentRef != "":
		link.Rel, link.From = wire.RelStarted, a.ParentRef
	case a.SourceSession != "":
		link.Rel, link.From = wire.RelContinued, a.SourceSession
	}
	if memberLink(l, a.Ref) == nil {
		s.addLinkLocked(l, link)
	}
	if sess != "" && sess != a.SourceSession {
		s.addLinkLocked(l, wire.LoopLink{Kind: wire.LinkSession, Ref: sess, Rel: wire.RelMember})
	}
	if p := a.Agent.ProjectID; p != "" && !strings.HasPrefix(p, wire.ScratchPrefix) {
		s.addLinkLocked(l, wire.LoopLink{Kind: wire.LinkProject, Ref: p, Rel: wire.RelTouches})
	}
	s.commitLocked("l:" + l.ID)
	return l.ID
}

// summaryOf is a settled turn's one-line summary: the agent's own, else
// the message's first sentence.
func summaryOf(a wire.Agent, message string, sentences []string) string {
	if a.Summary != "" {
		return clip(a.Summary, maxShortText)
	}
	if len(sentences) > 0 {
		return clip(sentences[0], maxShortText)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return clip(line, maxShortText)
}

// itemFor is the owner and kind of the items a turn's ending brings
// ("" none).
func itemFor(ending string) (owner, kind string) {
	switch ending {
	case wire.EndingAsks:
		return wire.OwnerYou, wire.ItemQuestion
	case wire.EndingYourStep:
		return wire.OwnerYou, wire.ItemStep
	case wire.EndingWaits:
		return wire.OwnerOthers, wire.ItemLooseEnd
	case wire.EndingOffer:
		return wire.OwnerAgent, wire.ItemOffer
	}
	return "", ""
}

func norm(text string) string { return strings.ToLower(strings.Join(strings.Fields(text), " ")) }

// AgentSettled records how one of this Mac's agents' turn ended on its
// link (ending, summary) and adds the sentences that ask the person,
// name their step, wait on others or offer more as items (once each).
func (s *Store) AgentSettled(e agents.AgentEvent) {
	if e.Ref == "" || e.Agent.Service != "" {
		return
	}
	s.mu.Lock()
	classify := s.classify
	s.mu.Unlock()
	ending, sentences := classify(e.Message)
	if e.Ending != "" {
		ending = e.Ending
	}
	sess := s.sessionRef(e.Agent.Kind, e.Agent.SessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.loopOfLocked(e.Ref)
	if id == "" {
		return
	}
	l := s.loops[id]
	k := memberLink(l, e.Ref)
	if k == nil {
		k, _ = s.addLinkLocked(l, wire.LoopLink{Kind: wire.LinkAgent, Ref: e.Ref, Rel: wire.RelMember, Label: e.Agent.Name})
	}
	k.Ending.Set(ending, s.clock.Tick())
	k.Summary.Set(summaryOf(e.Agent, e.Message, sentences), s.clock.Tick())
	if e.Agent.Name != "" {
		k.Label.Set(clip(e.Agent.Name, maxShortText), s.clock.Tick())
	}
	if sess != "" {
		s.addLinkLocked(l, wire.LoopLink{Kind: wire.LinkSession, Ref: sess, Rel: wire.RelMember})
	}
	if owner, kind := itemFor(ending); owner != "" {
		seen := map[string]bool{}
		for _, it := range l.Items {
			if !it.Deleted.V && it.State.V != wire.ItemDropped {
				seen[norm(it.Text.V)] = true
			}
		}
		for i, text := range sentences {
			if i == maxSentences {
				break
			}
			text = clip(strings.TrimSpace(text), maxShortText)
			if text == "" || seen[norm(text)] {
				continue
			}
			seen[norm(text)] = true
			s.itemLocked(l.Items, wire.LoopItem{Text: text, Owner: owner, Kind: kind, Source: e.Ref}, kind)
		}
	}
	s.commitLocked("l:" + id)
}

// AgentClosed records that one of this Mac's agents left the registry:
// why, when and the start of its final message. A move (reason "moved",
// To the agent it became) rewrites the link's ref instead: the work goes
// on in the same loop.
func (s *Store) AgentClosed(e agents.AgentEvent) {
	if e.Ref == "" || e.Agent.Service != "" {
		return
	}
	to := ""
	if e.Reason == wire.ReasonMoved {
		to = e.ToRef
		if to == "" && e.To != "" {
			to = s.refOf(e.To) // a controller from before toRef
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.loopOfLocked(e.Ref)
	if id == "" {
		return
	}
	l := s.loops[id]
	k := memberLink(l, e.Ref)
	if k == nil {
		return
	}
	if to != "" {
		// The agent it became may have been put in the loop already
		// (imported first): that link goes, this one keeps the history.
		for _, o := range l.Links {
			if o != k && member(o) && o.Ref.V == to && o.Kind.V == wire.LinkAgent {
				o.Deleted.Set(true, s.clock.Tick())
			}
		}
		k.Ref.Set(to, s.clock.Tick())
		s.commitLocked("l:" + id)
		return
	}
	if e.Ending != "" {
		k.Ending.Set(e.Ending, s.clock.Tick())
	}
	k.ClosedReason.Set(clip(e.Reason, 64), s.clock.Tick())
	k.Closed.Set(s.opt.Now().UTC(), s.clock.Tick())
	k.Result.Set(clip(e.Message, resultMax), s.clock.Tick())
	s.commitLocked("l:" + id)
}

// StewardConfig is the Steward's shared configuration (Level defaults to
// suggest; Machine is Node's name here).
func (s *Store) StewardConfig() wire.StewardConfig {
	s.mu.Lock()
	c := wire.StewardConfig{Enabled: s.steward.Enabled.V, Level: s.steward.Level.V, Node: s.steward.Node.V}
	node, short, machineOf := s.node, s.opt.Machine, s.machineOf
	s.mu.Unlock()
	if c.Level == "" {
		c.Level = wire.StewardSuggest
	}
	switch {
	case c.Node == "":
	case c.Node == node:
		c.Machine = short
	case machineOf != nil:
		c.Machine = machineOf(c.Node)
	}
	return c
}

// SetStewardConfig changes the Steward's configuration (steward.set; the
// Steward's methods call it): Machine names the Mac that runs it ("" or
// this Mac's name: this one). Turning it on without a Mac picks this one.
func (s *Store) SetStewardConfig(p wire.StewardSetParams) (wire.StewardConfig, error) {
	if p.Level != nil && !oneOf(*p.Level, wire.StewardWatch, wire.StewardSuggest, wire.StewardAct) {
		return wire.StewardConfig{}, wire.Errorf(wire.CodeInvalid, "unknown Steward level %q", *p.Level)
	}
	s.mu.Lock()
	own, short, nodeOf := s.node, s.opt.Machine, s.nodeOf
	s.mu.Unlock()
	node := ""
	if p.Machine != nil {
		switch m := *p.Machine; {
		case m == "" || m == short:
			node = own
		case nodeOf != nil:
			node = nodeOf(m)
		}
		if node == "" {
			return wire.StewardConfig{}, wire.Errorf(wire.CodeNotFound, "no machine %s", *p.Machine)
		}
	}
	s.mu.Lock()
	st := &s.steward
	changed := false
	if p.Enabled != nil {
		changed = st.Enabled.Set(*p.Enabled, s.clock.Tick()) || changed
		if *p.Enabled && st.Node.V == "" && node == "" {
			node = own
		}
	}
	if p.Level != nil {
		changed = st.Level.Set(*p.Level, s.clock.Tick()) || changed
	}
	if node != "" {
		changed = st.Node.Set(node, s.clock.Tick()) || changed
	}
	if changed {
		s.commitLocked(keySteward)
	}
	s.mu.Unlock()
	return s.StewardConfig(), nil
}
