// Package ptyhost runs programs in PTYs for hesperd: one Term per agent,
// its screen copy (internal/vt), and the attach connections that view it.
//
// Output fans out without per-viewer work: each read becomes one immutable
// chunk that every viewer's queue references. A viewer whose queue grows
// past its bound (it reads too slowly) loses the queue and gets a fresh
// redraw of the screen instead, so a slow viewer never holds the agent up.
package ptyhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/derzierau/hesper/relay/internal/perf"
	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Stages recorded in perf.Host.
const (
	StageInput  = "hesperd.input"  // attach DATA frame to the PTY
	StageOutput = "hesperd.output" // PTY read to the viewers' queues
	StageRedraw = "hesperd.redraw" // attach redraw
)

// Config starts a Term.
type Config struct {
	Argv       []string
	Env        []string
	Dir        string
	Cols, Rows int
	// OnOutput sees the output after the screen took it (with the Term's
	// lock released). It runs on the reading goroutine: keep it short.
	OnOutput func(p []byte)
	// OnResize is told the PTY's new size.
	OnResize func(cols, rows int)
	// OnExit is told how the process ended, once all its output is in.
	OnExit func(*wire.Exit)
	// FitDebounce: how long view fits must settle before they size the
	// PTY (0: FitDebounce).
	FitDebounce time.Duration
}

// Term is one program in a PTY.
type Term struct {
	cfg Config

	mu      sync.Mutex // the screen, viewers, size, owner, exit
	screen  *vt.Screen
	cols    int
	rows    int
	viewers map[*viewer]struct{}
	views   map[*view]struct{} // read-only window viewers (view.go)
	owner   *viewer
	// fitTimer debounces sizing the PTY to the views' fits (fit.go).
	fitTimer *time.Timer
	exit     *wire.Exit
	filter   filter

	ptmx    *os.File
	cmd     *exec.Cmd
	pid     int
	inMu    sync.Mutex
	replies chan []byte
	done    chan struct{}
	stopped bool
}

// Default size of a new PTY.
const DefaultCols, DefaultRows = 120, 40

// Start runs cfg.Argv in a new PTY (its own session and process group).
func Start(cfg Config) (*Term, error) {
	if len(cfg.Argv) == 0 {
		return nil, errors.New("no command")
	}
	if cfg.Cols <= 0 || cfg.Rows <= 0 {
		cfg.Cols, cfg.Rows = DefaultCols, DefaultRows
	}
	// The command is found on the agent's PATH, never the daemon's own
	// (exec.Command would look it up there).
	path, err := LookPath(cfg.Argv[0], cfg.Env)
	if err != nil {
		return nil, err
	}
	cmd := &exec.Cmd{Path: path, Args: cfg.Argv}
	cmd.Env, cmd.Dir = cfg.Env, cfg.Dir
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cfg.Cols), Rows: uint16(cfg.Rows)})
	if err != nil {
		return nil, err
	}
	t := &Term{
		cfg: cfg, screen: vt.New(cfg.Cols, cfg.Rows), cols: cfg.Cols, rows: cfg.Rows,
		viewers: map[*viewer]struct{}{}, ptmx: ptmx, cmd: cmd, pid: cmd.Process.Pid,
		replies: make(chan []byte, 64), done: make(chan struct{}),
	}
	readerDone := make(chan struct{})
	go t.read(readerDone)
	go t.answer()
	go t.wait(readerDone)
	return t, nil
}

// NotFoundError: a command that is not on the agent's PATH.
type NotFoundError struct {
	Name, Path string
}

func (e *NotFoundError) Error() string {
	path := e.Path
	if len(path) > 600 {
		path = path[:600] + "…"
	}
	return fmt.Sprintf("%q not found on the agent's PATH (%s)", e.Name, path)
}

// Unwrap: errors.Is(err, exec.ErrNotFound).
func (e *NotFoundError) Unwrap() error { return exec.ErrNotFound }

// LookPath finds a command on the PATH of env (the agent's), not the
// daemon's own; env without a PATH (nil: the daemon's environment) uses
// the daemon's. Only ':' separates entries: directories with spaces or
// other characters are taken as they are; empty entries (the current
// directory) are skipped. A name with a slash is used as it is.
func LookPath(name string, env []string) (string, error) {
	if name == "" {
		return "", errors.New("no command")
	}
	if strings.Contains(name, "/") {
		return name, nil
	}
	path, ok := "", false
	for _, kv := range env {
		if v, found := strings.CutPrefix(kv, "PATH="); found {
			path, ok = v, true
		}
	}
	if !ok {
		path = os.Getenv("PATH")
	}
	for _, dir := range strings.Split(path, ":") {
		if dir == "" {
			continue
		}
		p := dir + "/" + name
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &NotFoundError{Name: name, Path: path}
}

// Exited is a Term for an agent whose process is gone (after a daemon
// restart): a blank screen, and EXIT for whoever attaches.
func Exited(cols, rows int, exit *wire.Exit) *Term {
	if cols <= 0 || rows <= 0 {
		cols, rows = DefaultCols, DefaultRows
	}
	if exit == nil {
		exit = &wire.Exit{}
	}
	t := &Term{screen: vt.New(cols, rows), cols: cols, rows: rows, viewers: map[*viewer]struct{}{}, exit: exit, done: make(chan struct{})}
	close(t.done)
	return t
}

// PID is the program's process ID (0 for an Exited Term).
func (t *Term) PID() int { return t.pid }

// Done is closed when the program ended and its output is in.
func (t *Term) Done() <-chan struct{} { return t.done }

// Exit is how the program ended, nil while it runs.
func (t *Term) Exit() *wire.Exit {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exit
}

// Size is the PTY's size.
func (t *Term) Size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows
}

// Viewers is the number of attached viewers.
func (t *Term) Viewers() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.viewers)
}

// WithScreen calls f with the screen under the Term's lock (no output is
// taken meanwhile): keep f short.
func (t *Term) WithScreen(f func(*vt.Screen)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f(t.screen)
}

// Redraw is the output that draws the current screen.
func (t *Term) Redraw() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.screen.Redraw(nil)
}

var errExited = errors.New("the program has exited")

// Input writes terminal input to the program.
func (t *Term) Input(p []byte) error {
	if t.ptmx == nil {
		return errExited
	}
	select {
	case <-t.done:
		return errExited
	default:
	}
	t.inMu.Lock()
	defer t.inMu.Unlock()
	_, err := t.ptmx.Write(p)
	return err
}

// Resize sets the PTY's size; viewers get SIZE.
func (t *Term) Resize(cols, rows int) error {
	t.mu.Lock()
	changed, err := t.resizeLocked(cols, rows)
	t.mu.Unlock()
	if changed && t.cfg.OnResize != nil {
		t.cfg.OnResize(cols, rows)
	}
	return err
}

func (t *Term) resizeLocked(cols, rows int) (bool, error) {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return false, errors.New("bad size")
	}
	if cols == t.cols && rows == t.rows {
		return false, nil
	}
	if t.exit != nil || t.ptmx == nil {
		return false, errExited
	}
	if err := pty.Setsize(t.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return false, err
	}
	t.cols, t.rows = cols, rows
	t.screen.Resize(cols, rows)
	size := item{typ: wire.FrameSize, data: wire.SizePayload(cols, rows)}
	for v := range t.viewers {
		v.push(size)
	}
	for v := range t.views {
		v.kick()
	}
	return true, nil
}

// Signal sends sig to the program's process group.
func (t *Term) Signal(sig syscall.Signal) error {
	if t.pid <= 0 {
		return errExited
	}
	err := unix.Kill(-t.pid, sig)
	if err == unix.ESRCH {
		err = unix.Kill(t.pid, sig)
	}
	return err
}

// Stop hangs up the program (SIGHUP to its group) and kills it (SIGKILL)
// when it has not ended after grace. It returns at once.
func (t *Term) Stop(grace time.Duration) {
	t.mu.Lock()
	if t.stopped || t.exit != nil || t.pid <= 0 {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	t.mu.Unlock()
	t.Signal(syscall.SIGHUP)
	go func() {
		select {
		case <-t.done:
		case <-time.After(grace):
			t.Signal(syscall.SIGKILL)
		}
	}()
}

// Kill terminates the program now (SIGTERM to its group) and kills it
// (SIGKILL) when it has not ended after grace, also while a Stop is under
// way. It returns at once.
func (t *Term) Kill(grace time.Duration) {
	t.mu.Lock()
	if t.exit != nil || t.pid <= 0 {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	t.mu.Unlock()
	t.Signal(syscall.SIGTERM)
	go func() {
		select {
		case <-t.done:
		case <-time.After(grace):
			t.Signal(syscall.SIGKILL)
		}
	}()
}

// Stopped reports whether Stop was called.
func (t *Term) Stopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

func (t *Term) read(done chan struct{}) {
	defer close(done)
	buf := make([]byte, 32<<10)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			start := time.Now()
			out := make([]byte, 0, n)
			t.mu.Lock()
			t.filter.feed(buf[:n], func(p []byte) {
				t.screen.Write(p)
				out = append(out, p...)
			}, func(q []byte) {
				if reply := answer(q, t.screen); len(reply) > 0 {
					select {
					case t.replies <- reply:
					default:
					}
				}
			})
			if len(out) > 0 {
				chunk := item{typ: wire.FrameData, data: out}
				for v := range t.viewers {
					v.push(chunk)
				}
				for v := range t.views {
					v.kick()
				}
			}
			t.mu.Unlock()
			perf.Host.Since(StageOutput, start)
			if len(out) > 0 && t.cfg.OnOutput != nil {
				t.cfg.OnOutput(out)
			}
		}
		if err != nil {
			return
		}
	}
}

// answer writes query replies to the program, off the reading goroutine.
func (t *Term) answer() {
	for {
		select {
		case r := <-t.replies:
			t.Input(r)
		case <-t.done:
			return
		}
	}
}

func (t *Term) wait(readerDone chan struct{}) {
	err := t.cmd.Wait()
	// The output still in the PTY first; a background process that keeps
	// the terminal open must not keep the agent alive.
	select {
	case <-readerDone:
	case <-time.After(300 * time.Millisecond):
	}
	t.ptmx.Close()
	<-readerDone
	exit := exitOf(t.cmd.ProcessState, err)
	t.mu.Lock()
	t.exit = exit
	e := item{typ: wire.FrameExit, data: wire.ExitPayload(exit)}
	for v := range t.viewers {
		v.push(e)
	}
	for v := range t.views {
		v.kick()
	}
	t.mu.Unlock()
	close(t.done)
	if t.cfg.OnExit != nil {
		t.cfg.OnExit(exit)
	}
}

func exitOf(ps *os.ProcessState, err error) *wire.Exit {
	if ps == nil {
		code := -1
		return &wire.Exit{Code: &code, Signal: ""}
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return &wire.Exit{Signal: unix.SignalName(ws.Signal())}
	}
	code := ps.ExitCode()
	return &wire.Exit{Code: &code}
}
