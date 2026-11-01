package remote

import (
	"context"
	"encoding/json"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/loops"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Loops (internal/loops) are replicated with every machine that keeps
// them. A host's hint arrives on its link (link.read → loopsEvent): the
// first one on a link says the host has the "loops" capability (inside
// the end-to-end channel, never in the relay-visible snapshot) and starts
// the link's loops worker, which pulls what this Mac lacks (loops.pull)
// and pushes this Mac's changes (loops.sync), one exchange at a time.
// Remote agents carry the loop their ref belongs to (Agent.Loop). A host
// without loops gets spawns without the loop fields (it decodes them
// strictly) and this Mac records the new agent's loop itself.

// loopsSyncDebounce collects bursts of local changes into one push;
// loopsRetry is how soon a failed exchange is tried again.
var (
	loopsSyncDebounce = 150 * time.Millisecond
	loopsRetry        = 5 * time.Second
)

// loopPeer is what this Mac knows of one host node's loops.
type loopPeer struct {
	epoch  string // the host's epoch as last pulled
	pulled int64  // the host's sequence number pulled up to
	pushed int64  // this Mac's sequence number pushed up to
	mine   string // this Mac's epoch when pushed
	seen   string // the host's epoch in its last hint
}

// loopsEvent takes a host's loops hint.
func (f *Fleet) loopsEvent(l *link, raw json.RawMessage) {
	store := f.opt.Loops
	if store == nil {
		return
	}
	var h loops.Hint
	if json.Unmarshal(raw, &h) != nil || h.Node == "" || h.Node == store.Node() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m := l.m
	if m.link != l {
		return
	}
	m.loops = true
	if m.node == "" {
		m.node = h.Node
	}
	p := f.loopPeerLocked(h.Node)
	if p.seen != h.Epoch {
		p.seen, p.pushed = h.Epoch, 0 // a new store there: it gets everything
	}
	if l.loopsKick == nil {
		l.loopsKick = make(chan struct{}, 1)
		go f.loopsWorker(l, h.Node)
	}
	if h.Epoch != p.epoch || h.Seq > p.pulled {
		kick(l.loopsKick)
	}
}

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (f *Fleet) loopPeerLocked(node string) *loopPeer {
	if f.loopPeers == nil {
		f.loopPeers = map[string]*loopPeer{}
	}
	p := f.loopPeers[node]
	if p == nil {
		p = &loopPeer{}
		f.loopPeers[node] = p
	}
	return p
}

// loopsWorker runs one link's exchanges until the link ends.
func (f *Fleet) loopsWorker(l *link, node string) {
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-l.loopsKick:
		}
		if err := f.exchangeLoops(l, node); err != nil && l.ctx.Err() == nil {
			f.opt.Logf("remote: loops with %s: %v", f.shortOf(l.m), err)
			select {
			case <-l.ctx.Done():
				return
			case <-time.After(loopsRetry):
				kick(l.loopsKick)
			}
		}
	}
}

// exchangeLoops pulls what the host has that this Mac lacks, then pushes
// this Mac's changes, in batches.
func (f *Fleet) exchangeLoops(l *link, node string) error {
	store := f.opt.Loops
	f.mu.Lock()
	c, m := f.c, l.m
	p := *f.loopPeerLocked(node)
	f.mu.Unlock()
	if c == nil {
		return nil
	}
	self := store.Node()
	for {
		ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
		raw, err := f.request(ctx, c, m, "loops.pull", loops.PullParams{Since: p.pulled, Epoch: p.epoch, From: self})
		cancel()
		if err != nil {
			return err
		}
		var res loops.PullResult
		if err := json.Unmarshal(raw, &res); err != nil || res.Node != node {
			return wire.Errorf(wire.CodeRemote, "bad loops.pull answer from %s", f.shortOf(m))
		}
		if store.Merge(res.Records) {
			f.refreshLoops()
		}
		p.epoch, p.pulled = res.Epoch, res.Next
		f.savePeer(node, func(q *loopPeer) { q.epoch, q.pulled = p.epoch, p.pulled })
		if !res.More {
			break
		}
	}
	for {
		mine := store.Hint().Epoch
		if p.mine != mine {
			p.pushed = 0
		}
		batch := store.Pull(loops.PullParams{Since: p.pushed, Epoch: mine, From: node})
		if len(batch.Records) > 0 {
			ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
			_, err := f.request(ctx, c, m, "loops.sync", loops.SyncParams{From: self, Records: batch.Records})
			cancel()
			if err != nil {
				return err
			}
		}
		p.pushed, p.mine = batch.Next, mine
		f.savePeer(node, func(q *loopPeer) { q.pushed, q.mine = p.pushed, p.mine })
		if !batch.More {
			return nil
		}
	}
}

func (f *Fleet) savePeer(node string, fn func(*loopPeer)) {
	f.mu.Lock()
	fn(f.loopPeerLocked(node))
	f.mu.Unlock()
}

// syncLoopsLoop pushes every change of this Mac's loops to the linked
// machines that keep loops, and refreshes the remote agents' loops,
// until ctx ends (one relay connection).
func (f *Fleet) syncLoopsLoop(ctx context.Context, c *client.Controller) {
	store := f.opt.Loops
	if store == nil {
		return
	}
	for {
		changed := store.Changes()
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(loopsSyncDebounce):
		}
		f.refreshLoops()
		f.mu.Lock()
		if f.c == c {
			for _, m := range f.machines {
				if m.link != nil && m.link.loopsKick != nil {
					kick(m.link.loopsKick)
				}
			}
		}
		f.mu.Unlock()
	}
}

// loopOfLocked is the loop of one of m's agents ("" none or unknown).
func (f *Fleet) loopOfLocked(m *machine, local string) string {
	if f.opt.Loops == nil || m.node == "" {
		return ""
	}
	return f.opt.Loops.LoopOf(m.node + "/" + local)
}

// refreshLoops updates the remote agents' Agent.Loop after the loops
// changed.
func (f *Fleet) refreshLoops() {
	if f.opt.Loops == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		for local, a := range m.agents {
			if loop := f.loopOfLocked(m, local); loop != a.Loop {
				a.Loop = loop
				m.agents[local] = a
				f.emitChangedLocked(a)
			}
		}
	}
}

// spawnFields: before an agents.spawn goes to m, the loop fields leave
// the params of a host without loops (it decodes params strictly); the
// returned function records the new agent's loop on this Mac instead. A
// host with loops gets its parent's loop as loop when the spawn names
// none (and no milestone): only this Mac knows the parent's ref, the
// host resolves only parents of its own.
func (f *Fleet) spawnFields(m *machine, fields map[string]json.RawMessage) func(wire.Agent) {
	store := f.opt.Loops
	if store == nil {
		return nil
	}
	f.mu.Lock()
	capable := m.loops
	f.mu.Unlock()
	var want struct {
		Loop      string `json:"loop"`
		Milestone string `json:"milestone"`
		Parent    string `json:"parent"`
	}
	raw, _ := json.Marshal(fields)
	json.Unmarshal(raw, &want)
	if capable {
		if want.Loop == "" && want.Milestone == "" && want.Parent != "" {
			if loop := store.LoopOf(store.RefOf(want.Parent)); loop != "" {
				fields["loop"], _ = json.Marshal(loop)
			}
		}
		return nil
	}
	delete(fields, "loop")
	delete(fields, "milestone")
	delete(fields, "reportToParent")
	return func(a wire.Agent) {
		f.mu.Lock()
		node := m.node
		f.mu.Unlock()
		if node == "" {
			return // a host too old to name its node: no ref to record
		}
		info := agents.AssignInfo{Ref: node + "/" + localID(a.ID), Agent: a, Loop: want.Loop, Milestone: want.Milestone}
		if want.Parent != "" {
			info.ParentRef = store.RefOf(want.Parent)
		}
		store.Assign(info)
	}
}

// shortOf is m's short name (the inventory renames machines under f.mu).
func (f *Fleet) shortOf(m *machine) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m.short
}

// NodeOf is the node of a machine as this Mac calls it, from its link
// ("" unknown).
func (f *Fleet) NodeOf(short string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		if m.short == short {
			return m.node
		}
	}
	return ""
}

// MachineOf is the name this Mac gives the machine of a node ("" none
// linked).
func (f *Fleet) MachineOf(node string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		if node != "" && m.node == node {
			return m.short
		}
	}
	return ""
}

func (f *Fleet) namedNoLock(m *machine, a wire.Agent) wire.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m.named(a)
}
