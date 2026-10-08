package attachtty

import (
	"bytes"
	"strconv"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Scrolling a view through the bridge. A view's terminal has no input
// (everything typed is dropped), so the terminal side may use it to steer
// the window: "S+N;" scrolls N lines back, "S-N;" forward, "S=N;" goes to
// offset N ("S=0;" is live). The daemon's ScrollState comes back as the
// terminal's title, "hesper-scroll " + JSON, which the embedding app reads
// (libghostty reports titles; a view never shows the agent's own title).

// ScrollTitlePrefix starts a title that carries a ScrollState.
const ScrollTitlePrefix = "hesper-scroll "

// scrollTitle is the OSC 2 that tells the terminal a ScrollState.
func scrollTitle(state []byte) []byte {
	b := append([]byte("\x1b]2;"+ScrollTitlePrefix), state...)
	return append(b, '\a')
}

// scrollParser reads scroll commands from a view's input.
type scrollParser struct{ buf []byte }

// feed returns the commands completed by p.
func (sp *scrollParser) feed(p []byte) []wire.Scroll {
	var out []wire.Scroll
	sp.buf = append(sp.buf, p...)
	for {
		i := bytes.IndexByte(sp.buf, ';')
		if i < 0 {
			if len(sp.buf) > 64 {
				sp.buf = sp.buf[:0] // not ours
			}
			return out
		}
		cmd := sp.buf[:i]
		sp.buf = sp.buf[i+1:]
		if j := bytes.LastIndexByte(cmd, 'S'); j >= 0 {
			cmd = cmd[j:]
		} else {
			continue
		}
		if len(cmd) < 3 {
			continue
		}
		n, err := strconv.Atoi(string(cmd[2:]))
		if err != nil {
			continue
		}
		switch cmd[1] {
		case '+':
			out = append(out, wire.Scroll{Delta: n})
		case '-':
			out = append(out, wire.Scroll{Delta: -n})
		case '=':
			off := n
			out = append(out, wire.Scroll{Offset: &off})
		}
	}
}
