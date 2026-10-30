package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Port forwards: this Mac listens on a loopback port and carries each
// connection to a port on another machine's loopback, as a byte-stream
// channel of the link to it (ports.open, internal/host/forward.go;
// agentlink.Pipe). A connection does not survive its link: when the link
// breaks or moves to the direct path its connections are reset, and the
// next ones go on the new link. Forwards persist in forwards.json.

const forwardsFile = "forwards.json"

type forwards struct {
	once    sync.Once
	mu      sync.Mutex
	byLocal map[int]*forward
	stopped bool
	// flush bounds writing what the host sent before its X (zero:
	// forwardFlush; tests change it before any link).
	flush time.Duration
}

// forwardFlush bounds a closed connection's last writes to the local
// client, as the host bounds its own.
const forwardFlush = 30 * time.Second

func (f *Fleet) forwardFlush() time.Duration {
	if f.fwd.flush > 0 {
		return f.fwd.flush
	}
	return forwardFlush
}

type forward struct {
	wire.Forward
	lns   []net.Listener
	conns map[*fwdConn]struct{}
}

// fwdConn is one forwarded connection on a link channel.
type fwdConn struct {
	ch     uint32
	nc     net.Conn
	pipe   *agentlink.Pipe
	ctx    context.Context
	cancel context.CancelFunc
	// removed (under forwards.mu): its forward went, so the host hears X.
	removed bool
}

// reset ends a connection whose link went: the local client gets a reset,
// not a clean end, so it does not take a cut-off reply for a whole one.
func (c *fwdConn) reset() {
	if tc, ok := c.nc.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	c.cancel()
}

// startForwards listens for the saved forwards (once).
func (f *Fleet) startForwards() {
	f.fwd.once.Do(func() {
		f.fwd.mu.Lock()
		defer f.fwd.mu.Unlock()
		f.fwd.byLocal = map[int]*forward{}
		var saved struct {
			Forwards []wire.Forward `json:"forwards"`
		}
		data, err := os.ReadFile(filepath.Join(f.opt.StateDir, forwardsFile))
		if err == nil {
			err = json.Unmarshal(data, &saved)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			f.opt.Logf("remote: %s: %v", forwardsFile, err)
		}
		for _, w := range saved.Forwards {
			if w.Machine == "" || w.Port < 1 || w.Port > 65535 || w.Local < 1 || w.Local > 65535 || f.fwd.byLocal[w.Local] != nil {
				continue
			}
			fw := &forward{Forward: wire.Forward{ID: strconv.Itoa(w.Local), Machine: w.Machine, Port: w.Port, Local: w.Local}, conns: map[*fwdConn]struct{}{}}
			if err := f.listenForward(fw); err != nil {
				fw.Error = err.Error()
				f.opt.Logf("remote: forward %s:%d: %v", w.Machine, w.Port, err)
			}
			f.fwd.byLocal[w.Local] = fw
		}
	})
}

// stopForwards closes every listener and connection (Run's end).
func (f *Fleet) stopForwards() {
	f.startForwards()
	f.fwd.mu.Lock()
	defer f.fwd.mu.Unlock()
	f.fwd.stopped = true
	for _, fw := range f.fwd.byLocal {
		fw.close()
	}
}

func (fw *forward) close() {
	for _, ln := range fw.lns {
		ln.Close()
	}
	fw.lns = nil
	for c := range fw.conns {
		c.removed = true
		c.reset()
	}
}

// listenForward listens on fw.Local (0: a free port) on 127.0.0.1 and,
// where there is IPv6, the same port on ::1. A port taken on either is
// taken: a browser's localhost may try ::1 first, and a dev server may
// listen there only.
func (f *Fleet) listenForward(fw *forward) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(fw.Local)))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return wire.Errorf(wire.CodeExists, "port %d is in use on this Mac", fw.Local)
		}
		return wire.Errorf(wire.CodeUnavailable, "cannot listen on port %d: %v", fw.Local, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	lns := []net.Listener{ln}
	ln6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	switch {
	case err == nil:
		lns = append(lns, ln6)
	case errors.Is(err, syscall.EADDRINUSE):
		ln.Close()
		return wire.Errorf(wire.CodeExists, "port %d is in use on this Mac (on ::1)", port)
	}
	// Any other error: no IPv6 loopback here.
	fw.Local, fw.ID, fw.lns = port, strconv.Itoa(port), lns
	for _, ln := range fw.lns {
		go f.acceptForward(fw, ln)
	}
	return nil
}

// acceptForward accepts until ln closes. Temporary errors (out of file
// descriptors) are retried with a growing pause; another error ends the
// listener. Both show in the forward's error.
func (f *Fleet) acceptForward(fw *forward, ln net.Listener) {
	pause := 5 * time.Millisecond
	for {
		nc, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			f.fwd.mu.Lock()
			fw.Error = fmt.Sprintf("accepting on port %d: %v", fw.Local, err)
			f.fwd.mu.Unlock()
			if !temporaryAccept(err) {
				f.opt.Logf("remote: forward %s:%d: %v", fw.Machine, fw.Port, err)
				return
			}
			time.Sleep(pause)
			pause = min(2*pause, time.Second)
			continue
		}
		pause = 5 * time.Millisecond
		go f.forwardConn(fw, nc)
	}
}

func temporaryAccept(err error) bool {
	var ne net.Error
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOBUFS) ||
		errors.Is(err, syscall.ENOMEM) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EINTR) ||
		errors.As(err, &ne) && ne.Timeout()
}

// forwardConn carries one local connection to the forward's machine.
func (f *Fleet) forwardConn(fw *forward, nc net.Conn) {
	defer nc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), attachSetup)
	defer cancel()
	var l *link
	var c *fwdConn
	for attempt := 0; ; attempt++ {
		var m *machine
		var err error
		m, l, err = f.waitLink(ctx, fw.Machine, 10*time.Second)
		if err == nil {
			err = f.forwardable(m)
		}
		if err == nil {
			c, err = f.openForward(ctx, l, fw.Port, nc)
		}
		if err == nil {
			break
		}
		// The link closed or moved while the connection was on its way:
		// once more on the current one.
		if l != nil && l.gone() && attempt < 3 && ctx.Err() == nil {
			continue
		}
		f.fwd.mu.Lock()
		fw.Error = err.Error()
		f.fwd.mu.Unlock()
		return
	}
	f.fwd.mu.Lock()
	if f.fwd.stopped || f.fwd.byLocal[fw.Local] != fw {
		f.fwd.mu.Unlock()
		c.cancel()
		l.removeForward(c, true)
		return
	}
	fw.Error = ""
	fw.conns[c] = struct{}{}
	f.fwd.mu.Unlock()
	local := c.pipe.Run(c.ctx)
	c.cancel()
	f.fwd.mu.Lock()
	removed := c.removed
	delete(fw.conns, c)
	f.fwd.mu.Unlock()
	// Ended here, or its forward was removed: the host closes its side.
	l.removeForward(c, local || removed)
}

// forwardable: m serves port forwards. A host without the capability
// is never sent ports.open or W frames (an older one ends the link on an
// unknown frame kind).
func (f *Fleet) forwardable(m *machine) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !m.state.Capabilities.Forward {
		return wire.Errorf(wire.CodeUnavailable, "%s does not forward ports (hesperd serve --allow-forward PORTS on %s, and a device approved there with the shell right)", m.short, m.short)
	}
	return nil
}

// openForward opens a byte-stream channel of l to port on its machine
// (ports.open, signed) and bridges nc to it.
func (f *Fleet) openForward(ctx context.Context, l *link, port int, nc net.Conn) (*fwdConn, error) {
	c, ok := l.addForward(nc)
	if !ok {
		return nil, wire.Errorf(wire.CodeUnavailable, "the link to %s just closed", l.m.short)
	}
	f.mu.Lock()
	ctl := f.c
	f.mu.Unlock()
	var err error
	if ctl == nil {
		err = wire.Errorf(wire.CodeUnavailable, "not connected to the relay")
	} else {
		_, err = f.request(ctx, ctl, l.m, "ports.open", map[string]any{"link": l.id, "ch": c.ch, "port": port})
	}
	if err != nil {
		// The host may have opened it after all (a timeout): X ends it.
		l.removeForward(c, true)
		c.cancel()
		return nil, err
	}
	return c, nil
}

// --- agents.Remote ---

// Forwards are this Mac's port forwards, by local port.
func (f *Fleet) Forwards() []wire.Forward {
	f.startForwards()
	f.fwd.mu.Lock()
	defer f.fwd.mu.Unlock()
	out := []wire.Forward{}
	for _, fw := range f.fwd.byLocal {
		w := fw.Forward
		w.Conns = len(fw.conns)
		out = append(out, w)
	}
	slices.SortFunc(out, func(a, b wire.Forward) int { return a.Local - b.Local })
	return out
}

// AddForward listens on p.Local for p.Machine's p.Port. Without a Local
// it listens on p.Port, or a free port when that one is taken.
func (f *Fleet) AddForward(p wire.ForwardAddParams) (wire.Forward, error) {
	f.startForwards()
	if p.Port < 1 || p.Port > 65535 || p.Local < 0 || p.Local > 65535 {
		return wire.Forward{}, wire.Errorf(wire.CodeInvalid, "ports are 1-65535")
	}
	m, _, err := f.machineByShort(p.Machine)
	if err != nil {
		return wire.Forward{}, err
	}
	if err := f.forwardable(m); err != nil {
		return wire.Forward{}, err
	}
	auto := p.Local == 0
	if auto {
		p.Local = p.Port
	}
	f.fwd.mu.Lock()
	defer f.fwd.mu.Unlock()
	if f.fwd.stopped {
		return wire.Forward{}, wire.Errorf(wire.CodeUnavailable, "the controller role stopped")
	}
	if old := f.fwd.byLocal[p.Local]; old != nil && !auto {
		return wire.Forward{}, wire.Errorf(wire.CodeExists, "port %d already forwards to %s:%d", p.Local, old.Machine, old.Port)
	}
	fw := &forward{Forward: wire.Forward{Machine: p.Machine, Port: p.Port, Local: p.Local}, conns: map[*fwdConn]struct{}{}}
	if f.fwd.byLocal[p.Local] != nil {
		fw.Local = 0
	}
	err = f.listenForward(fw)
	var we *wire.Error
	// Taken, without a local port asked for: a free one (which may be
	// taken on ::1 alone, rarely; then another).
	for try := 0; auto && try < 3 && errors.As(err, &we) && we.Code == wire.CodeExists; try++ {
		fw.Local = 0
		err = f.listenForward(fw)
	}
	if err != nil {
		return wire.Forward{}, err
	}
	f.fwd.byLocal[fw.Local] = fw
	if err := f.saveForwardsLocked(); err != nil {
		fw.close()
		delete(f.fwd.byLocal, fw.Local)
		return wire.Forward{}, err
	}
	return fw.Forward, nil
}

// RemoveForward ends a forward: by id (its local port), or machine:port
// when only one forward goes there.
func (f *Fleet) RemoveForward(id string) error {
	f.startForwards()
	f.fwd.mu.Lock()
	defer f.fwd.mu.Unlock()
	var found []*forward
	for _, fw := range f.fwd.byLocal {
		if fw.ID == id || fmt.Sprintf("%s:%d", fw.Machine, fw.Port) == strings.TrimSpace(id) {
			found = append(found, fw)
		}
	}
	switch {
	case len(found) == 0:
		return wire.Errorf(wire.CodeNotFound, "no forward %s", id)
	case len(found) > 1:
		return wire.Errorf(wire.CodeInvalid, "%d forwards go to %s: name one by its local port", len(found), id)
	}
	fw := found[0]
	fw.close()
	delete(f.fwd.byLocal, fw.Local)
	return f.saveForwardsLocked()
}

func (f *Fleet) saveForwardsLocked() error {
	var saved struct {
		Forwards []wire.Forward `json:"forwards"`
	}
	for _, fw := range f.fwd.byLocal {
		saved.Forwards = append(saved.Forwards, wire.Forward{ID: fw.ID, Machine: fw.Machine, Port: fw.Port, Local: fw.Local})
	}
	slices.SortFunc(saved.Forwards, func(a, b wire.Forward) int { return a.Local - b.Local })
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", forwardsFile, err)
	}
	if err := writeAtomic(filepath.Join(f.opt.StateDir, forwardsFile), data); err != nil {
		return fmt.Errorf("save %s: %w", forwardsFile, err)
	}
	return nil
}

// writeAtomic replaces path with data (mode 0600): a temp file in the
// same directory, synced, renamed over it.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
