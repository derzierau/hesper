package wire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Attach frame types.
const (
	FrameData   byte = 0 // both directions: terminal bytes
	FrameResize byte = 1 // client→daemon: cols u16, rows u16 (size owner only)
	FrameSize   byte = 2 // daemon→client: cols u16, rows u16
	FrameExit   byte = 3 // daemon→client: JSON Exit
	// FrameScroll (view attaches): client→daemon JSON Scroll (where the
	// window is in the scrollback), daemon→client JSON ScrollState.
	FrameScroll byte = 4
)

// Scroll moves a view's window into the scrollback: Offset (lines above
// the live bottom; 0: live) when set, else Delta lines (> 0: back in
// time). While scrolled, new output does not move the window: the daemon
// keeps it on the same lines (the offset grows by what was appended).
type Scroll struct {
	Offset *int `json:"offset,omitempty"`
	Delta  int  `json:"delta,omitempty"`
}

// ScrollState is where a view's window is: Offset lines above the live
// bottom (at most Max), and New lines appended below it since it left the
// bottom (0 when live). Sent when one of them changes.
type ScrollState struct {
	Offset int `json:"offset"`
	Max    int `json:"max"`
	New    int `json:"new"`
}

// MaxFrame bounds a frame's payload.
const MaxFrame = 1 << 24

// Attach modes.
const (
	ModeRW = "rw"
	ModeRO = "ro"
)

// AttachRequest is the first line of an attach connection.
type AttachRequest struct {
	Attach string `json:"attach"`
	Mode   string `json:"mode"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
	Owner  bool   `json:"owner"`
	// View (read-only attaches only) asks for a window onto the screen
	// instead of the screen: the daemon renders the last View.Rows rows
	// (following the cursor) clipped to View.Cols columns, and redraws the
	// rows that change. RESIZE from such a viewer sets its window size.
	View *View `json:"view,omitempty"`
	// Fit (view attaches only) is the grid the viewer would like the PTY
	// to have (its tile's cols x rows). While no rw owner holds the size,
	// the PTY follows the largest fit of all view attaches (debounced,
	// never below 80x24); a RESIZE from such a view updates its fit.
	Fit *Size `json:"fit,omitempty"`
	// History: false asks a read-write attach not to get the scrollback
	// before its redraw (a reattach that only resyncs); nil or true: it
	// does (at most HistoryPreambleLines / Bytes of it).
	History *bool `json:"history,omitempty"`
}

// View anchors.
const AnchorBottom = "bottom"

// View is a read-only viewer's window onto an agent's screen.
type View struct {
	Rows int `json:"rows"`
	// Cols clips each row (0: the PTY's width).
	Cols int `json:"cols,omitempty"`
	// Anchor: "bottom" (the only one, also the default): the window ends
	// at the screen's last non-blank row or the cursor's, whichever is
	// lower.
	Anchor string `json:"anchor,omitempty"`
}

// AttachReply is the daemon's answer line: ok with the PTY's size, or an
// error.
type AttachReply struct {
	OK   bool `json:"ok"`
	Cols int  `json:"cols,omitempty"`
	Rows int  `json:"rows,omitempty"`
	// View is the window a view attach got.
	View  *View  `json:"view,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// AppendFrame appends a frame header and payload to b.
func AppendFrame(b []byte, typ byte, payload []byte) []byte {
	b = append(b, typ, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(b[len(b)-4:], uint32(len(payload)))
	return append(b, payload...)
}

// FrameHeader is the 5 byte header of a frame of n payload bytes.
func FrameHeader(typ byte, n int) [5]byte {
	var h [5]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:], uint32(n))
	return h
}

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	_, err := w.Write(AppendFrame(make([]byte, 0, 5+len(payload)), typ, payload))
	return err
}

// ReadFrame reads one frame, reusing buf for the payload when it is large
// enough.
func ReadFrame(r io.Reader, buf []byte) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > MaxFrame {
		return 0, nil, fmt.Errorf("frame of %d bytes", n)
	}
	if uint32(cap(buf)) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return h[0], buf, nil
}

// SizePayload is a RESIZE or SIZE frame's payload.
func SizePayload(cols, rows int) []byte {
	var p [4]byte
	binary.BigEndian.PutUint16(p[:2], uint16(min(max(cols, 0), 0xffff)))
	binary.BigEndian.PutUint16(p[2:], uint16(min(max(rows, 0), 0xffff)))
	return p[:]
}

// ParseSize reads a RESIZE or SIZE payload.
func ParseSize(p []byte) (cols, rows int, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(p[:2])), int(binary.BigEndian.Uint16(p[2:])), true
}

// ExitPayload is an EXIT frame's payload.
func ExitPayload(e *Exit) []byte {
	if e == nil {
		e = &Exit{}
	}
	p, _ := json.Marshal(e)
	return p
}
