package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Resuming or forking a session on another Mac carries its transcript
// and its code there, which takes as long as the link needs: an 80 MB
// conversation over a slow upload is minutes, while one request over the
// relay lasts at most 20 s. So the Mac that runs the session (the
// target) does it as an operation of its own and answers at once:
//
//   - the caller sends sessions.resume / sessions.fork with poll; the
//     target starts the operation (its own context, at most
//     TransferLimit; a transfer fails when no bytes moved for
//     client.TransferIdle, never for its length) and answers the result
//     when it is done within opWait, else {pending: op, steps, progress};
//   - the caller asks again with {op, seen} until the result comes, telling
//     its subscribers the progress (agents.moving, Session set: ID the
//     session's id, steps checkpoint, transfer with percent, bytes and
//     total, worktree, resume, done or failed), so the app and hesperctl
//     show it and never report a failure while the transfer moves;
//   - a caller without poll (an older hesperd) gets the answer of the
//     same operation as before: when it ends within the request.
//
// An operation that ended is kept for opKeep, so a lost answer can be
// asked again.

var (
	// TransferLimit bounds one resume or fork on another Mac as a whole.
	TransferLimit = 2 * time.Hour
	// opWait is how long a target waits for its operation before it
	// answers pending (and a poll waits for the next answer).
	opWait = 2 * time.Second
	// opKeep is how long an ended operation's result stays to be asked.
	opKeep = 10 * time.Minute
	// pollIdle: polls that fail on the way for this long end the wait.
	pollIdle = 60 * time.Second
)

type resumeOp struct {
	id    string
	done  chan struct{}
	mu    sync.Mutex
	log   []wire.Moving // each step once; a transfer's latest in place
	res   ResumeResult
	err   error
	ended time.Time
}

func (op *resumeOp) note(m wire.Moving) {
	op.mu.Lock()
	defer op.mu.Unlock()
	if n := len(op.log); n > 0 && op.log[n-1].Step == m.Step && m.Step == wire.MoveTransfer {
		op.log[n-1] = m
		return
	}
	op.log = append(op.log, m)
}

// since is the steps after the first seen ones, and the latest.
func (op *resumeOp) since(seen int) ([]wire.Moving, *wire.Moving) {
	op.mu.Lock()
	defer op.mu.Unlock()
	if len(op.log) == 0 {
		return nil, nil
	}
	last := op.log[len(op.log)-1]
	if seen < 0 || seen > len(op.log) {
		seen = len(op.log)
	}
	return append([]wire.Moving(nil), op.log[seen:]...), &last
}

// startOp runs resumeHere for rec as an operation.
func (s *Service) startOp(rec Record, fork bool, tree Tree) *resumeOp {
	op := &resumeOp{id: "rs-" + randHex(6), done: make(chan struct{})}
	s.opsMu.Lock()
	if s.ops == nil {
		s.ops = map[string]*resumeOp{}
	}
	for id, o := range s.ops {
		if !o.ended.IsZero() && time.Since(o.ended) > opKeep {
			delete(s.ops, id)
		}
	}
	s.ops[op.id] = op
	s.opsMu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.ctx, TransferLimit)
		defer cancel()
		res, err := s.resumeHere(ctx, &rec, fork, tree, op.note)
		if err != nil {
			s.opt.Logf("history: %s of %s here: %v", map[bool]string{true: "fork", false: "resume"}[fork], rec.SID, err)
		}
		s.opsMu.Lock()
		op.res, op.err, op.ended = res, err, time.Now()
		s.opsMu.Unlock()
		close(op.done)
	}()
	return op
}

// awaitOp answers a request for op: its result once it ended, else
// (poll) pending with its progress; without poll it waits as long as
// the request may. A poller gets the steps after the seen first ones
// (Steps) and the latest (Progress).
func (s *Service) awaitOp(ctx context.Context, op *resumeOp, poll bool, seen int) (any, error) {
	wait := opWait
	if !poll {
		wait = TransferLimit
	}
	if deadline, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(deadline)-time.Second)
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-op.done:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
	}
	select {
	case <-op.done:
		if op.err != nil || !poll {
			return op.res, op.err
		}
		res := op.res
		res.Steps, _ = op.since(seen)
		return res, nil
	default:
	}
	if !poll {
		return nil, wire.Errorf(wire.CodeUnavailable, "the session is still on its way here (it continues; update hesperd on the asking Mac to follow it)")
	}
	steps, last := op.since(seen)
	return ResumeResult{Pending: op.id, Steps: steps, Progress: last}, nil
}

func (s *Service) pollOp(ctx context.Context, id string, seen int) (any, error) {
	s.opsMu.Lock()
	op := s.ops[id]
	s.opsMu.Unlock()
	if op == nil {
		return nil, wire.Errorf(wire.CodeNotFound, "no session transfer %s here (its Mac restarted?)", id)
	}
	return s.awaitOp(ctx, op, true, seen)
}

// resumeOn runs a resume or fork on another Mac (target) and follows
// it to its result, telling this Mac's subscribers its progress.
func (s *Service) resumeOn(target string, peers Peers, rec *Record, fork bool, tree Tree) (ResumeResult, error) {
	method := "sessions.resume"
	if fork {
		method = "sessions.fork"
	}
	reg := s.registry()
	sid := s.nameOf(rec.Node, rec.Home) + ":" + rec.Kind + ":" + rec.SID
	bringing := rec.Node != s.nodeOf(target)
	emit := func(m wire.Moving) {
		if reg == nil || !bringing {
			return
		}
		m.ID, m.To, m.Fork, m.Session = sid, target, fork, true
		reg.NoteMoving(m)
	}
	ctx, cancel := context.WithTimeout(s.ctx, TransferLimit+time.Minute)
	defer cancel()
	res, err := s.followOp(ctx, peers, target, method, hostParams{Key: rec.Key(), treeParams: tree.params(), Poll: true}, emit)
	if err != nil {
		var we *wire.Error
		if !errors.As(err, &we) {
			we = &wire.Error{Code: wire.CodeRemote, Message: err.Error()}
		}
		emit(wire.Moving{Step: wire.MoveFailed, Error: we})
		return ResumeResult{}, err
	}
	res.Steps, res.Progress = nil, nil
	if _, local, ok := strings.Cut(res.ID, "/"); ok {
		res.ID, res.Machine = target+"/"+local, target
	}
	emit(wire.Moving{Step: wire.MoveDone, Agent: res.ID})
	return res, nil
}

func (s *Service) followOp(ctx context.Context, peers Peers, target, method string, p hostParams, emit func(wire.Moving)) (ResumeResult, error) {
	call := func(p hostParams) (ResumeResult, error) {
		params, _ := json.Marshal(p)
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		raw, err := peers.Call(rctx, target, method, params)
		if err != nil {
			return ResumeResult{}, err
		}
		var res ResumeResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return ResumeResult{}, wire.Errorf(wire.CodeRemote, "bad answer from %s", target)
		}
		return res, nil
	}
	var last wire.Moving
	seen := 0
	show := func(res ResumeResult) {
		for _, m := range res.Steps {
			if m != last {
				last = m
				emit(m)
			}
		}
		seen += len(res.Steps)
		if pr := res.Progress; pr != nil && *pr != last {
			last = *pr
			emit(last)
		}
	}
	// The first request starts it: never sent twice (a second agent).
	res, err := call(p)
	lastOK := time.Now()
	for err == nil {
		show(res)
		if res.Pending == "" {
			break
		}
		op := res.Pending
		for {
			res, err = call(hostParams{Op: op, Poll: true, Seen: seen})
			if err == nil {
				lastOK = time.Now()
				break
			}
			var we *wire.Error
			if ctx.Err() != nil || !errors.As(err, &we) || we.Code != wire.CodeUnavailable || time.Since(lastOK) > pollIdle {
				return ResumeResult{}, err
			}
			select {
			case <-ctx.Done():
				return ResumeResult{}, err
			case <-time.After(time.Second):
			}
		}
	}
	return res, err
}

// trimPartialLine cuts a file after its last newline: a copy that ends
// inside a line (the mirror is fetched in chunks) ends with a whole one.
func trimPartialLine(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return err
	}
	buf := make([]byte, 64<<10)
	for end := st.Size(); end > 0; {
		start := max(0, end-int64(len(buf)))
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && n < int(end-start) {
			return err
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				if cut := start + int64(i) + 1; cut != st.Size() {
					return f.Truncate(cut)
				}
				return nil
			}
		}
		end = start
	}
	return f.Truncate(0)
}

// dirBytes is the size of a bundle directory's files.
func dirBytes(dir string) int64 {
	var n int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
	}
	return n
}
