package qr

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
)

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

func (c *Code) rows() string {
	var b strings.Builder
	for y := range c.Size {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := range c.Size {
			if c.Dark(x, y) {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
	}
	return b.String()
}

// Every version at every level, against symbols another encoder made
// (testdata/coreimage.txt): the same version for the largest payload it
// holds, and module for module the same symbol with the mask that encoder
// chose. That covers the block tables, interleaving, placement, format
// and version information.
func TestMatchesCoreImage(t *testing.T) {
	f, err := os.Open("testdata/coreimage.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	masks := map[int]int{}
	lines := 0
	for s := bufio.NewScanner(f); s.Scan(); {
		if strings.HasPrefix(s.Text(), "#") {
			continue
		}
		var level string
		var v, n int
		var want string
		if _, err := fmt.Sscan(s.Text(), &level, &v, &n, &want); err != nil {
			t.Fatal(err)
		}
		lines++
		l := Level(strings.Index("LMQH", level))
		if got := capacity(v, l); got != n {
			t.Errorf("%s %d: capacity %d, want %d", level, v, got, n)
		}
		c, err := Encode(payload(n), l)
		if err != nil || c.Version != v {
			t.Errorf("%s: %d bytes in version %v (%v), want %d", level, n, c, err, v)
			continue
		}
		found := -1
		for m := range 8 {
			sum := sha256.Sum256([]byte(encode(payload(n), l, v, m).rows()))
			if hex.EncodeToString(sum[:]) == want {
				found = m
			}
		}
		if found < 0 {
			t.Errorf("%s %d: no mask gives the reference symbol", level, v)
		}
		masks[found]++
	}
	if lines != 160 {
		t.Fatalf("%d reference symbols, want 160", lines)
	}
	t.Logf("masks of the reference symbols: %v", masks)
	if _, err := Encode(payload(2954), L); err != ErrTooLong {
		t.Fatalf("2954 bytes at L: %v", err)
	}
}

// The worked example of "HELLO WORLD" at 1-M (thonky.com's QR tutorial):
// its 16 data codewords and their 10 error correction codewords.
func TestReedSolomon(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	if got := remainder(data, generator(10)); !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// decode reads a symbol back: format, unmask, codewords, Reed–Solomon
// syndromes, byte-mode payload.
func decode(t *testing.T, c *Code) []byte {
	t.Helper()
	n := c.Size
	bits := 0
	for i := range 15 {
		x, y := formatPositions(n, i)
		if c.Dark(x[0], y[0]) != c.Dark(x[1], y[1]) {
			t.Fatalf("format bit %d differs between its copies", i)
		}
		if c.Dark(x[0], y[0]) {
			bits |= 1 << i
		}
	}
	bits ^= 0x5412
	level := Level([4]int{1, 0, 3, 2}[bits>>13])
	mask := bits >> 10 & 7
	if mask != c.Mask {
		t.Fatalf("format says mask %d, symbol has %d", mask, c.Mask)
	}
	function := make([]bool, n*n)
	scratch := &Code{Size: n, Version: (n - 17) / 4, modules: make([]bool, n*n)}
	scratch.drawFunction(function)
	var stream []byte
	k := 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range n {
			for j := range 2 {
				x, y := right-j, vert
				if (right+1)&2 == 0 {
					y = n - 1 - vert
				}
				if function[y*n+x] {
					continue
				}
				if k%8 == 0 {
					stream = append(stream, 0)
				}
				if c.Dark(x, y) != maskBit(mask, x, y) {
					stream[k/8] |= 0x80 >> (k % 8)
				}
				k++
			}
		}
	}
	v := scratch.Version
	count, ec := blocks[level][v], ecPerBlock[level][v]
	raw := rawModules(v) / 8
	stream = stream[:raw]
	short := count - raw%count
	shortLen := raw/count - ec
	blockData := make([][]byte, count)
	at := 0
	for i := range shortLen + 1 {
		for b := range count {
			if i < shortLen || b >= short {
				blockData[b] = append(blockData[b], stream[at])
				at++
			}
		}
	}
	for range ec {
		for b := range count {
			blockData[b] = append(blockData[b], stream[at])
			at++
		}
	}
	var data []byte
	for b, block := range blockData {
		for i := range ec {
			s := byte(0)
			for _, cw := range block {
				s = mul(s, expTable[i]) ^ cw
			}
			if s != 0 {
				t.Fatalf("block %d: syndrome %d is %d", b, i, s)
			}
		}
		data = append(data, block[:len(block)-ec]...)
	}
	if data[0]>>4 != 0b0100 {
		t.Fatalf("mode %04b, want byte mode", data[0]>>4)
	}
	var r bitBuffer
	r.bytes = data
	get := func(bits int) int {
		v := 0
		for range bits {
			v = v<<1 | int(r.bytes[r.n/8]>>(7-r.n%8)&1)
			r.n++
		}
		return v
	}
	get(4)
	length := get(countBits(v))
	out := make([]byte, length)
	for i := range out {
		out[i] = byte(get(8))
	}
	return out
}

func TestRoundTrip(t *testing.T) {
	const code = "HESPER1:eyJ2IjoxLCJyZWxheSI6Imh0dHBzOi8vcmVsYXkuZXhhbXBsZS5jb20iLCJtYWNoaW5lIjoiTUFDSElORV9JRCIsIm5hbWUiOiJzdHVkaW8iLCJob3N0S2V5IjoiQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQT0iLCJyaWdodHMiOlsib2JzZXJ2ZSIsImFuc3dlciIsInR5cGUiLCJ0cmFuc2ZlciJdLCJvZmZlciI6IkFBRUNBd1FGQmdjSUNRb0xEQTBPRHciLCJleHBpcmVzIjoxNzkzNjUwOTU2fQ"
	for _, level := range []Level{L, M, Q, H} {
		c, err := Encode([]byte(code), level)
		if err != nil {
			t.Fatal(err)
		}
		if got := decode(t, c); string(got) != code {
			t.Fatalf("level %d: decoded %q", level, got)
		}
		for m := range 8 {
			if got := decode(t, encode([]byte(code), level, c.Version, m)); string(got) != code {
				t.Fatalf("level %d mask %d: decoded %q", level, m, got)
			}
		}
	}
}

func TestHalfBlocks(t *testing.T) {
	c, _ := Encode([]byte("hesper"), L)
	out := c.HalfBlocks(2, false)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != (c.Size+4+1)/2 {
		t.Fatalf("%d lines for %d rows", len(lines), c.Size+4)
	}
	// The top-left finder: a quiet row, then its dark top edge.
	if !strings.HasPrefix(lines[1], "  █▀▀▀▀▀█ ") {
		t.Fatalf("line 1 %q", lines[1])
	}
	for y, line := range lines {
		if n := len([]rune(line)); n != c.Size+4 {
			t.Fatalf("line %d has %d columns", y, n)
		}
	}
	if colored := c.HalfBlocks(2, true); !strings.HasPrefix(colored, "\x1b[30;107m") || !strings.Contains(colored, "\x1b[0m\n") {
		t.Fatalf("colored %q", colored[:20])
	}
}
