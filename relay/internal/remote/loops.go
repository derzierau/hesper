package remote

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
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
	reset := store.PeerSeen(h.Node, h.Epoch) // a new store there: it gets everything
	p := store.Peer(h.Node)                  // the cursors persist with the store (loops.peers.json)
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
	if l.loopsKick == nil {
		l.loopsKick = make(chan struct{}, 1)
		go f.loopsWorker(l, h.Node)
	}
	if reset || h.Epoch != p.Epoch || h.Seq > p.Pulled {
		kick(l.loopsKick)
	}
}

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
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
	f.mu.Unlock()
	if c == nil {
		return nil
	}
	p := store.Peer(node)
	self := store.Node()
	for {
		ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
		raw, err := f.request(ctx, c, m, "loops.pull", loops.PullParams{Since: p.Pulled, Epoch: p.Epoch, From: self})
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
		p.Epoch, p.Pulled = res.Epoch, res.Next
		store.PeerPulled(node, p.Epoch, p.Pulled)
		if !res.More {
			break
		}
	}
	p = store.Peer(node)
	for {
		mine := store.Hint().Epoch
		if p.Mine != mine {
			p.Pushed = 0
		}
		batch := store.Pull(loops.PullParams{Since: p.Pushed, Epoch: mine, From: node})
		if len(batch.Records) > 0 {
			ctx, cancel := context.WithTimeout(l.ctx, 60*time.Second)
			_, err := f.request(ctx, c, m, "loops.sync", loops.SyncParams{From: self, Records: batch.Records})
			cancel()
			if err != nil {
				return err
			}
		}
		if !store.PeerPushed(node, p.Gen, batch.Next, mine) {
			p = store.Peer(node) // the host was reset meanwhile: from its cursor again
			continue
		}
		p.Pushed, p.Mine = batch.Next, mine
		if !batch.More {
			return nil
		}
	}
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
	node := f.nodeLocked(m)
	if f.opt.Loops == nil || node == "" {
		return ""
	}
	return f.opt.Loops.LoopOf(node + "/" + local)
}

// nodeLocked is m's node: the one it names on its link; a host from
// before loops names none, and once this Mac keeps its loops it is known
// by a stand-in made from its relay device id (stable, and never a node a
// Mac names: nodes start "n-"). When it names its node later, its refs
// move over (renameStandInLocked).
func (f *Fleet) nodeLocked(m *machine) string {
	if m.node != "" {
		return m.node
	}
	if m.backfill && m.id != "" {
		return standInNode(m.id)
	}
	return ""
}

func standInNode(device string) string { return "dev-" + device }

// renameStandInLocked: m named its node (an upgraded host); the refs this
// Mac kept under its stand-in move to it.
func (f *Fleet) renameStandInLocked(m *machine, node string) {
	if f.opt.Loops == nil || m.id == "" || node == "" {
		return
	}
	f.opt.Loops.RenameNode(standInNode(m.id), node)
}

// loopsBackfillDelay is how long a host has, after its first inventory,
// to say it keeps loops itself (its loops hint comes when the link opens).
const loopsBackfillDelay = time.Minute

// armBackfillLocked follows an inventory of m: a host that has not said
// it keeps loops within loopsBackfillDelay of it is an older hesperd,
// whose agents this Mac puts in loops (startBackfillLocked). Keeping
// loops is the machine's, not the link's: a link replaced meanwhile (the
// direct path taking over from the relay) still counts; a machine with no
// link when the wait ends is left to its next inventory.
func (f *Fleet) armBackfillLocked(m *machine) {
	if f.opt.Loops == nil || m.loops {
		return
	}
	if m.backfill || m.backfillWait {
		f.backfillLoopsLocked(m)
		return
	}
	m.backfillWait = true
	time.AfterFunc(loopsBackfillDelay, func() { f.backfillDue(m) })
}

// backfillDue ends m's wait (armBackfillLocked's timer).
func (f *Fleet) backfillDue(m *machine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m.backfillWait = false
	if m.link != nil {
		f.startBackfillLocked(m)
	}
}

// startBackfillLocked: m keeps no loops (unless a hint said otherwise):
// from now on this Mac keeps the loops of its agents.
func (f *Fleet) startBackfillLocked(m *machine) {
	if m.loops {
		return
	}
	m.backfill = true
	f.backfillLoopsLocked(m)
}

// backfillLoopsLocked puts every agent of a host without loops that has no
// loop in one, parents first, so a child shares its parent's loop (the
// parent's ref when the parent is one of the host's agents). The store
// never calls back into the Fleet, so f.mu is held throughout.
func (f *Fleet) backfillLoopsLocked(m *machine) {
	store := f.opt.Loops
	if store == nil || !m.backfill || m.loops {
		return
	}
	node := f.nodeLocked(m)
	if node == "" {
		return
	}
	missing := make([]wire.Agent, 0, len(m.agents))
	for local, a := range m.agents {
		if a.Service == "" && store.LoopOf(node+"/"+local) == "" {
			missing = append(missing, a)
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].Depth != missing[j].Depth {
			return missing[i].Depth < missing[j].Depth
		}
		return missing[i].Created.Before(missing[j].Created)
	})
	for _, a := range missing {
		local := localID(a.ID)
		info := agents.AssignInfo{Ref: node + "/" + local, Agent: a}
		if short, parent, ok := strings.Cut(a.Parent, "/"); ok && short == m.short {
			if _, on := m.agents[parent]; on {
				info.ParentRef = node + "/" + parent
			}
		}
		a.Loop = store.Assign(info)
		m.agents[local] = a
		f.emitChangedLocked(a)
	}
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
