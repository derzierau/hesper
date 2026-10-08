package agents

import (
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/ptyhost"
	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Shell agents (docs/rebuild-contract.md, "As built — shell agents"): a
// shell has no hooks, so hesperd watches its terminal instead.
//
//   - Prompt ready: the shell wrote something (its prompt) and then
//     nothing for ShellQuiet while no job runs, or ShellReadyWait passed.
//     Input (agents.input) to a shell whose prompt is not ready yet waits
//     for it, so text sent right after the start comes after the prompt.
//   - TASK: a shell spawned with a task gets it typed (pasted when the
//     shell has bracketed paste on) and Enter once its prompt is ready.
//     Only the spawn types it: a respawn or a resume starts the shell
//     without it (a command must never run twice).
//   - State, without Track (the default): idle until it exits, as
//     always. With Track (agents.spawn track; hesperctl new --track): a
//     shell with a task is starting until it is typed, then working
//     while a job runs (the terminal's foreground process
//     group is not the shell's; activity: the job's command name), and
//     from a command hesperd typed (the task, agents.input with submit)
//     until the shell is back at its prompt and quiet for ShellQuiet;
//     else idle. A builtin (cd, export) or a command too quick to see
//     counts as done once the shell is quiet again.

// Shell watch timing (variables for tests).
var (
	shellPoll      = 150 * time.Millisecond
	shellQuiet     = 400 * time.Millisecond
	shellReadyWait = 10 * time.Second
)

// shellWatch follows one shell process's terminal.
type shellWatch struct {
	mu        sync.Mutex
	output    time.Time // last output
	input     time.Time // last command hesperd typed
	sawOutput bool
	ready     chan struct{}
	readyOnce sync.Once
}

func newShellWatch() *shellWatch { return &shellWatch{ready: make(chan struct{})} }

// saw is the terminal's OnOutput.
func (w *shellWatch) saw([]byte) {
	w.mu.Lock()
	w.output, w.sawOutput = time.Now(), true
	w.mu.Unlock()
}

func (w *shellWatch) typed() {
	w.mu.Lock()
	w.input = time.Now()
	w.mu.Unlock()
}

// quiet: no output and no typed command for ShellQuiet.
func (w *shellWatch) quiet(now time.Time) (quiet, sawOutput bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return now.Sub(w.output) >= shellQuiet && now.Sub(w.input) >= shellQuiet, w.sawOutput
}

func (w *shellWatch) markReady() { w.readyOnce.Do(func() { close(w.ready) }) }

func (w *shellWatch) isReady() bool {
	select {
	case <-w.ready:
		return true
	default:
		return false
	}
}

// waitReady waits (at most ShellReadyWait) for the shell's prompt.
func (w *shellWatch) waitReady() {
	if w == nil {
		return
	}
	select {
	case <-w.ready:
	case <-time.After(shellReadyWait):
	}
}

// watchShell follows a shell agent's process (generation gen) until it
// ends: prompt ready, its task, working and idle.
func (r *Registry) watchShell(local string, gen int, w *shellWatch) {
	defer w.markReady()
	started := time.Now()
	tick := time.NewTicker(shellPoll)
	defer tick.Stop()
	for range tick.C {
		r.mu.Lock()
		a := r.agents[local]
		if a == nil || a.gen != gen || a.term == nil || a.Exit != nil {
			r.mu.Unlock()
			return
		}
		term := a.term
		r.mu.Unlock()
		fg, err := term.Foreground()
		if err != nil {
			continue // ending: the next tick sees its exit
		}
		busy := fg > 0 && fg != term.PID()
		job := ""
		if busy {
			job = processName(fg)
		}
		now := time.Now()
		quiet, sawOutput := w.quiet(now)
		if !w.isReady() && !(sawOutput && quiet && !busy) && now.Sub(started) < shellReadyWait {
			continue
		}
		r.mu.Lock()
		if a.gen != gen || a.Exit != nil {
			r.mu.Unlock()
			return
		}
		track := a.Track
		if task := a.shellTask; task != "" {
			a.shellTask = ""
			w.typed()
			if track {
				a.Activity = firstLine(task, 80)
				r.setState(a, wire.StateWorking, nil)
			}
			r.mu.Unlock()
			typeCommand(term, task)
			w.typed()
			w.markReady() // input waiting for the prompt comes after the task
			if !track {
				return // an untracked shell: idle, nothing more to follow
			}
			continue
		}
		w.markReady()
		if !track {
			r.mu.Unlock()
			return
		}
		switch {
		case busy:
			newJob := job != "" && a.Activity != job
			if newJob {
				a.Activity = job
			}
			if a.State != wire.StateWorking {
				r.setState(a, wire.StateWorking, nil)
			} else if newJob {
				r.changed(a)
			}
		case a.State != wire.StateIdle && quiet:
			a.Activity = ""
			r.setState(a, wire.StateIdle, nil)
		}
		r.mu.Unlock()
	}
}

// typeCommand types a command and Enter, done when both are written:
// pasted when the shell has bracketed paste on (a multi-line task stays
// one command line), then Enter once the shell took the paste.
func typeCommand(term *ptyhost.Term, text string) {
	paste := false
	term.WithScreen(func(s *vt.Screen) { paste = s.Modes().BracketedPaste })
	if paste {
		term.Input([]byte("\x1b[200~" + text + "\x1b[201~"))
		time.Sleep(100 * time.Millisecond)
	} else {
		term.Input([]byte(text))
	}
	term.Input([]byte("\r"))
}

// shellSubmitted: a command was typed into shell a with Enter
// (agents.input): it works on it (the lock is held).
func (r *Registry) shellSubmitted(a *agent, text string) {
	if a.shell == nil || !a.Track || a.closeReason != "" {
		return
	}
	a.shell.typed()
	if line := strings.TrimSpace(text); line != "" {
		a.Activity = firstLine(line, 80)
	}
	r.setState(a, wire.StateWorking, nil)
}
