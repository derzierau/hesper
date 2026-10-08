// Package attachtty bridges a terminal (stdin/stdout) to a hesperd attach
// connection: `hesperd attach` (what Hesper.app's terminal surfaces run)
// and `hesperctl attach`.
package attachtty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Options of a bridge.
type Options struct {
	Socket string
	ID     string
	RO     bool
	// Owner: this view sets the PTY's size (its own, and on every
	// SIGWINCH).
	Owner bool
	// View: a read-only window onto the agent's last rows (wire.View)
	// instead of its screen, ViewRows rows (0: the terminal's) of the
	// terminal's width; it follows SIGWINCH.
	View     bool
	ViewRows int
	// Fit (with View): ask that the PTY take this terminal's size while no
	// owner holds it (wire.AttachRequest.Fit); SIGWINCH updates it.
	Fit bool
	// DetachKey ends the bridge when typed (0: none; the app closes the
	// surface instead).
	DetachKey byte
	In        *os.File
	Out       *os.File
}

// Result is how the bridge ended.
type Result struct {
	Exit     *wire.Exit // the agent's exit (EXIT frame), nil otherwise
	Detached bool
}

// Run bridges until the agent exits, the detach key, the connection ends or
// ctx ends. The terminal is in raw mode meanwhile and restored after.
//
// Size: with Owner, the terminal's size goes as RESIZE at attach and on
// every SIGWINCH. Without, the bridge follows SIZE: it sets its own
// terminal's window size to the PTY's (TIOCSWINSZ), so the terminal it
// runs in reports the agent's grid; it never asks for a resize.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	tty := term.IsTerminal(int(o.In.Fd()))
	cols, rows := 0, 0
	if tty {
		if c, r, err := term.GetSize(int(o.In.Fd())); err == nil {
			cols, rows = c, r
		}
	}
	mode := wire.ModeRW
	if o.RO {
		mode = wire.ModeRO
	}
	req := wire.AttachRequest{Attach: o.ID, Mode: mode, Cols: cols, Rows: rows, Owner: o.Owner && !o.RO}
	if o.View {
		req.Mode, req.Owner = wire.ModeRO, false
		req.View = &wire.View{Rows: o.ViewRows, Cols: cols, Anchor: wire.AnchorBottom}
		if req.View.Rows <= 0 {
			req.View.Rows = rows
		}
		if req.View.Rows <= 0 {
			req.View.Rows = 24
		}
		if o.Fit && cols > 0 && rows > 0 {
			req.Fit = &wire.Size{Cols: cols, Rows: rows}
		}
	}
	conn, err := wire.Attach(ctx, o.Socket, req)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	follow := func(c, r int) {
		if tty && !o.Owner && !o.View && c > 0 && r > 0 {
			pty.Setsize(o.In, &pty.Winsize{Cols: uint16(c), Rows: uint16(r)})
		}
	}
	follow(conn.Reply.Cols, conn.Reply.Rows)
	if tty {
		old, err := term.MakeRaw(int(o.In.Fd()))
		if err != nil {
			return Result{}, err
		}
		defer term.Restore(int(o.In.Fd()), old)
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer signal.Stop(signals)

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 2)
	// The daemon to the terminal.
	go func() {
		for {
			typ, p, err := conn.ReadFrame()
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = errors.New("hesperd closed the connection")
				}
				done <- outcome{err: err}
				return
			}
			switch typ {
			case wire.FrameData:
				if _, err := o.Out.Write(p); err != nil {
					done <- outcome{err: err}
					return
				}
			case wire.FrameSize:
				if c, r, ok := wire.ParseSize(p); ok {
					follow(c, r)
				}
			case wire.FrameScroll:
				if o.View {
					if _, err := o.Out.Write(scrollTitle(p)); err != nil {
						done <- outcome{err: err}
						return
					}
				}
			case wire.FrameExit:
				var e wire.Exit
				json.Unmarshal(p, &e)
				done <- outcome{res: Result{Exit: &e}}
				return
			}
		}
	}()
	// The terminal to the daemon.
	go func() {
		buf := make([]byte, 32<<10)
		var scroll scrollParser
		for {
			n, err := o.In.Read(buf)
			if n > 0 {
				p := buf[:n]
				if o.View {
					for _, sc := range scroll.feed(p) {
						conn.Scroll(sc)
					}
				}
				if o.DetachKey != 0 {
					for i, b := range p {
						if b == o.DetachKey {
							if i > 0 && !o.RO && !o.View {
								conn.Input(p[:i])
							}
							done <- outcome{res: Result{Detached: true}}
							return
						}
					}
				}
				if !o.RO && !o.View {
					if err := conn.Input(p); err != nil {
						done <- outcome{err: err}
						return
					}
				}
			}
			if err != nil {
				done <- outcome{res: Result{Detached: true}}
				return
			}
		}
	}()
	for {
		select {
		case out := <-done:
			return out.res, out.err
		case sig := <-signals:
			if sig == syscall.SIGWINCH {
				if (o.Owner || o.View) && tty {
					if c, r, err := term.GetSize(int(o.In.Fd())); err == nil {
						conn.Resize(c, r)
					}
				}
				continue
			}
			return Result{Detached: true}, nil
		case <-ctx.Done():
			return Result{Detached: true}, nil
		}
	}
}

// ExitCode is a process exit code for an agent's exit.
func ExitCode(e *wire.Exit) int {
	if e == nil || e.Code == nil {
		return 0
	}
	return *e.Code
}

// Describe is a one-line note about how the bridge ended.
func Describe(id string, r Result) string {
	switch {
	case r.Exit != nil && r.Exit.Signal != "":
		return fmt.Sprintf("%s ended (%s)", id, r.Exit.Signal)
	case r.Exit != nil && r.Exit.Code != nil:
		return fmt.Sprintf("%s exited with status %d", id, *r.Exit.Code)
	case r.Detached:
		return "detached from " + id
	}
	return ""
}
