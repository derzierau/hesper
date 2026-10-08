package ptyhost

import (
	"bufio"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// ViewCoalesce is the shortest time between two renders of a view: output
// that arrives sooner is drawn together in the next one.
var ViewCoalesce = 8 * time.Millisecond

// view is a read-only viewer that sees a window onto the screen (the last
// rows, following the cursor) rendered from the screen copy, instead of the
// program's output. Only rows that changed since the last render are sent.
type view struct {
	t    *Term
	conn net.Conn
	wake chan struct{}
	done chan struct{}

	mu         sync.Mutex // rows, cols, full, the scroll position
	rows, cols int
	full       bool
	// The window's place in the scrollback (scroll.go): lines above the
	// live bottom, the history total it was pinned at, the lines appended
	// since it left the bottom, what the viewer was told last.
	scroll   int
	pinned   bool
	pinTotal uint64
	newLines int
	told     wire.ScrollState
	tell     bool

	// fitCols, fitRows: the PTY size this view asks for (0: none); under
	// the Term's lock.
	fitCols, fitRows int

	// The render goroutine's own state.
	prev       []string
	prevCursor string
	started    bool
}

func (v *view) kick() {
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

func viewSize(req wire.AttachRequest) (cols, rows int) {
	cols, rows = req.View.Cols, req.View.Rows
	if rows <= 0 {
		rows = req.Rows
	}
	if cols <= 0 {
		cols = req.Cols
	}
	return max(cols, 0), max(rows, 1)
}

// attachView serves a read-only view attach (req.View set).
func (t *Term) attachView(conn net.Conn, r *bufio.Reader, req wire.AttachRequest) {
	defer conn.Close()
	if req.View.Anchor != "" && req.View.Anchor != wire.AnchorBottom {
		reply(conn, wire.AttachReply{Error: wire.Errorf(wire.CodeInvalid, "unknown view anchor %q", req.View.Anchor)})
		return
	}
	cols, rows := viewSize(req)
	v := &view{t: t, conn: conn, wake: make(chan struct{}, 1), done: make(chan struct{}), rows: rows, cols: cols}
	if req.Fit != nil && req.Fit.Cols > 0 && req.Fit.Rows > 0 {
		v.fitCols, v.fitRows = req.Fit.Cols, req.Fit.Rows
	}
	t.mu.Lock()
	line, _ := json.Marshal(wire.AttachReply{OK: true, Cols: t.cols, Rows: t.rows,
		View: &wire.View{Rows: rows, Cols: cols, Anchor: wire.AnchorBottom}})
	if t.views == nil {
		t.views = map[*view]struct{}{}
	}
	t.views[v] = struct{}{}
	if v.fitCols > 0 {
		t.scheduleFit()
	}
	t.mu.Unlock()
	conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
	if _, err := conn.Write(append(line, '\n')); err != nil {
		t.detachView(v)
		return
	}
	v.kick()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		v.run()
	}()
	v.read(r)
	t.detachView(v)
	conn.Close()
	<-writerDone
}

func (t *Term) detachView(v *view) {
	t.mu.Lock()
	if _, ok := t.views[v]; ok {
		delete(t.views, v)
		close(v.done)
		if v.fitCols > 0 {
			t.scheduleFit()
		}
	}
	t.mu.Unlock()
}

// read takes RESIZE frames (the window's new size); input is refused.
func (v *view) read(r *bufio.Reader) {
	var buf []byte
	for {
		typ, p, err := wire.ReadFrame(r, buf)
		if err != nil {
			return
		}
		if cap(p) > cap(buf) {
			buf = p[:0]
		}
		if typ == wire.FrameScroll {
			v.scrollTo(p)
			continue
		}
		if typ != wire.FrameResize {
			continue
		}
		cols, rows, ok := wire.ParseSize(p)
		if !ok || rows <= 0 {
			continue
		}
		v.mu.Lock()
		if cols != v.cols || rows != v.rows {
			v.cols, v.rows, v.full = cols, rows, true
		}
		v.mu.Unlock()
		v.t.mu.Lock()
		if v.fitCols > 0 && cols > 0 && (cols != v.fitCols || rows != v.fitRows) {
			// A view with a fit asks for its new grid.
			v.fitCols, v.fitRows = cols, rows
			v.t.scheduleFit()
		}
		v.t.mu.Unlock()
		v.kick()
	}
}

// run renders on every kick, at most once per ViewCoalesce, until the
// viewer leaves or the program's exit was sent.
func (v *view) run() {
	var last time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-v.wake:
		case <-v.done:
			return
		}
		if d := ViewCoalesce - time.Since(last); d > 0 && !last.IsZero() {
			timer.Reset(d)
			select {
			case <-timer.C:
			case <-v.done:
				return
			}
		}
		// Drain a kick that came while waiting: this render covers it.
		select {
		case <-v.wake:
		default:
		}
		last = time.Now()
		frame, exit, state := v.render()
		if len(frame) > 0 {
			v.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			if err := wire.WriteFrame(v.conn, wire.FrameData, frame); err != nil {
				v.conn.Close()
				return
			}
		}
		if state != nil {
			p, _ := json.Marshal(state)
			v.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			if err := wire.WriteFrame(v.conn, wire.FrameScroll, p); err != nil {
				v.conn.Close()
				return
			}
		}
		if exit != nil {
			v.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
			wire.WriteFrame(v.conn, wire.FrameExit, wire.ExitPayload(exit))
			if c, ok := v.conn.(interface{ CloseWrite() error }); ok {
				c.CloseWrite()
			}
			v.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			return
		}
	}
}

// render returns the output that brings the viewer's terminal from the last
// render to the current window (nil when nothing changed), and the exit
// once the program ended.
func (v *view) render() ([]byte, *wire.Exit, *wire.ScrollState) {
	v.mu.Lock()
	cols, rows, full := v.cols, v.rows, v.full
	v.full = false
	v.mu.Unlock()

	t := v.t
	t.mu.Lock()
	total := t.screen.HistoryTotal()
	v.mu.Lock()
	offset := v.place(total)
	v.mu.Unlock()
	lines, cursor, maxOffset := WindowAt(t.screen, cols, rows, offset)
	exit := t.exit
	t.mu.Unlock()
	state := v.state(offset, maxOffset)

	if !v.started || full || len(v.prev) != len(lines) {
		v.prev = make([]string, len(lines))
		for i := range v.prev {
			v.prev[i] = "\x00" // never equal to a row
		}
		v.prevCursor = ""
	}
	var b []byte
	if !v.started {
		// A fresh viewer: main screen, no scroll region, no autowrap (a row
		// never wraps or scrolls), default modes, cleared.
		b = append(b, "\x1b[?2026h\x1b[0m\x1b[?1049l\x1b[r\x1b[?6l\x1b[?7l\x1b[4l\x1b(B\x1b)B\x0f\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[H\x1b[2J"...)
		v.started = true
	} else if full {
		b = append(b, "\x1b[?2026h\x1b[0m\x1b[H\x1b[2J"...)
	}
	for i, line := range lines {
		if line == v.prev[i] {
			continue
		}
		if len(b) == 0 {
			b = append(b, "\x1b[?2026h"...)
		}
		b = append(b, "\x1b["...)
		b = strconv.AppendInt(b, int64(i+1), 10)
		// Erase first: with autowrap off, an erase after a row that fills
		// the width would take its last cell.
		b = append(b, ";1H\x1b[0m\x1b[2K"...)
		b = append(b, line...)
		b = append(b, "\x1b[0m"...)
		v.prev[i] = line
	}
	if len(b) > 0 || cursor != v.prevCursor {
		if len(b) == 0 {
			b = append(b, "\x1b[?2026h"...)
		}
		b = append(b, cursor...)
		b = append(b, "\x1b[?2026l"...)
		v.prevCursor = cursor
	}
	return b, exit, state
}

// Window renders the bottom-anchored window of rows×cols onto s: the rows
// that end at the lower of the last non-blank row and the cursor's row
// (each row as RowANSI, clipped to cols), and the cursor (position, shape
// and visibility, or hidden when outside the window).
func Window(s *vt.Screen, cols, rows int) ([]string, string) {
	w, h := s.Size()
	if cols <= 0 || cols > w {
		cols = w
	}
	rows = max(1, min(rows, h))
	cx, cy := s.Cursor()
	m := s.Modes()
	bottom := cy
	for y := h - 1; y > bottom; y-- {
		if !s.Blank(y) {
			bottom = y
			break
		}
	}
	top := min(max(bottom-rows+1, 0), h-rows)
	lines := make([]string, rows)
	for i := range rows {
		lines[i], _ = s.RowANSI(top+i, cols)
	}
	var c []byte
	if m.CursorVisible && cy >= top && cy < top+rows && cx < cols {
		c = append(c, "\x1b["...)
		c = strconv.AppendInt(c, int64(cy-top+1), 10)
		c = append(c, ';')
		c = strconv.AppendInt(c, int64(cx+1), 10)
		c = append(c, 'H')
		c = append(c, "\x1b["...)
		c = strconv.AppendInt(c, int64(m.CursorStyle), 10)
		c = append(c, " q\x1b[?25h"...)
	} else {
		c = append(c, "\x1b[?25l"...)
	}
	return lines, string(c)
}
