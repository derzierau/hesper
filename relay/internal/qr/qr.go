// Package qr encodes bytes as a QR code (ISO/IEC 18004, model 2): byte
// mode only, versions 1 to 40, the four error correction levels, the mask
// with the lowest penalty. `hesperctl phone` draws its pairing code with
// it; Go's standard library has no encoder and one mode is all it needs.
package qr

import (
	"errors"
	"strings"
)

// Level is the error correction level.
type Level int

const (
	L Level = iota // recovers about 7% of the codewords
	M              // 15%
	Q              // 25%
	H              // 30%
)

// ErrTooLong means the data does not fit in version 40 at the level.
var ErrTooLong = errors.New("qr: data too long")

// Code is an encoded symbol, without its quiet zone.
type Code struct {
	Size    int // modules per side, 17 + 4 × version
	Version int
	Mask    int
	modules []bool
}

// Dark reports whether the module at column x, row y is dark. Outside the
// symbol (the quiet zone) is light.
func (c *Code) Dark(x, y int) bool {
	if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.modules[y*c.Size+x]
}

// Encode makes the smallest symbol at level that holds data.
func Encode(data []byte, level Level) (*Code, error) {
	for v := 1; v <= 40; v++ {
		if len(data) <= capacity(v, level) {
			return encode(data, level, v, -1), nil
		}
	}
	return nil, ErrTooLong
}

// ecPerBlock and blocks are ISO/IEC 18004 table 9: the error correction
// codewords of each block and the number of blocks, by level and version.
var ecPerBlock = [4][41]int{
	{0, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28},
	{0, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24, 28, 26, 24, 20, 30, 24, 28, 28, 26, 30, 28, 30, 30, 30, 30, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{0, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28, 24, 28, 22, 24, 24, 30, 28, 28, 26, 28, 30, 24, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
}

var blocks = [4][41]int{
	{0, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25},
	{0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49},
	{0, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29, 34, 34, 35, 38, 40, 43, 45, 48, 51, 53, 56, 59, 62, 65, 68},
	{0, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35, 37, 40, 42, 45, 48, 51, 54, 57, 60, 63, 66, 70, 74, 77, 81},
}

// rawModules is the number of modules of version v that carry codewords
// (and the remainder bits): everything but the function patterns.
func rawModules(v int) int {
	n := (16*v+128)*v + 64
	if v >= 2 {
		align := v/7 + 2
		n -= (25*align-10)*align - 55
		if v >= 7 {
			n -= 36 // the two version blocks
		}
	}
	return n
}

func dataCodewords(v int, level Level) int {
	return rawModules(v)/8 - ecPerBlock[level][v]*blocks[level][v]
}

// capacity is how many bytes version v holds in byte mode.
func capacity(v int, level Level) int {
	bits := dataCodewords(v, level)*8 - 4 - countBits(v)
	return bits / 8
}

func countBits(v int) int {
	if v <= 9 {
		return 8
	}
	return 16
}

// encode builds the symbol; mask -1 picks the one with the lowest penalty.
func encode(data []byte, level Level, v, mask int) *Code {
	codewords := interleave(dataBits(data, level, v), level, v)
	c := &Code{Size: 17 + 4*v, Version: v}
	c.modules = make([]bool, c.Size*c.Size)
	function := make([]bool, c.Size*c.Size)
	c.drawFunction(function)
	c.place(codewords, function)
	if mask < 0 {
		best := -1
		for m := range 8 {
			c.applyMask(m, function)
			c.drawFormat(level, m)
			if p := c.penalty(); best < 0 || p < best {
				best, mask = p, m
			}
			c.applyMask(m, function) // undo (XOR)
		}
	}
	c.applyMask(mask, function)
	c.drawFormat(level, mask)
	c.Mask = mask
	return c
}

// dataBits is the bit stream (mode, count, bytes, terminator, padding) as
// the version's data codewords.
func dataBits(data []byte, level Level, v int) []byte {
	var b bitBuffer
	b.put(0b0100, 4) // byte mode
	b.put(len(data), countBits(v))
	for _, d := range data {
		b.put(int(d), 8)
	}
	total := dataCodewords(v, level) * 8
	b.put(0, min(4, total-b.n))
	b.put(0, (8-b.n%8)%8)
	for pad := 0xEC; b.n < total; pad ^= 0xEC ^ 0x11 {
		b.put(pad, 8)
	}
	return b.bytes
}

type bitBuffer struct {
	bytes []byte
	n     int
}

func (b *bitBuffer) put(value, bits int) {
	for i := bits - 1; i >= 0; i-- {
		if b.n%8 == 0 {
			b.bytes = append(b.bytes, 0)
		}
		if value>>i&1 == 1 {
			b.bytes[b.n/8] |= 0x80 >> (b.n % 8)
		}
		b.n++
	}
}

// interleave splits the data into blocks, adds each block's Reed–Solomon
// codewords and interleaves them: the data codewords column by column,
// then the error correction codewords.
func interleave(data []byte, level Level, v int) []byte {
	count, ec := blocks[level][v], ecPerBlock[level][v]
	raw := rawModules(v) / 8
	short := count - raw%count // blocks with one data codeword less
	shortLen := raw/count - ec
	gen := generator(ec)
	var dataBlocks, ecBlocks [][]byte
	for i, at := 0, 0; i < count; i++ {
		n := shortLen
		if i >= short {
			n++
		}
		block := data[at : at+n]
		at += n
		dataBlocks = append(dataBlocks, block)
		ecBlocks = append(ecBlocks, remainder(block, gen))
	}
	out := make([]byte, 0, raw)
	for i := range shortLen + 1 {
		for _, block := range dataBlocks {
			if i < len(block) {
				out = append(out, block[i])
			}
		}
	}
	for i := range ec {
		for _, block := range ecBlocks {
			out = append(out, block[i])
		}
	}
	return out
}

// GF(256) with the polynomial x⁸ + x⁴ + x³ + x² + 1 (0x11D).
var expTable, logTable = func() ([512]byte, [256]byte) {
	var exp [512]byte
	var log [256]byte
	x := 1
	for i := range 255 {
		exp[i] = byte(x)
		log[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		exp[i] = exp[i-255]
	}
	return exp, log
}()

func mul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return expTable[int(logTable[a])+int(logTable[b])]
}

// generator is ∏ (x − αⁱ) for i < degree, highest coefficient first
// (that coefficient, 1, left out).
func generator(degree int) []byte {
	g := []byte{1}
	for i := range degree {
		next := make([]byte, len(g)+1)
		for j, c := range g {
			next[j] ^= c
			next[j+1] ^= mul(c, expTable[i])
		}
		g = next
	}
	return g[1:]
}

// remainder is data × x^degree mod the generator: the block's error
// correction codewords.
func remainder(data, gen []byte) []byte {
	r := make([]byte, len(gen))
	for _, d := range data {
		factor := d ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i, g := range gen {
			r[i] ^= mul(g, factor)
		}
	}
	return r
}

func (c *Code) set(function []bool, x, y int, dark bool) {
	c.modules[y*c.Size+x] = dark
	function[y*c.Size+x] = true
}

// drawFunction draws the finder, timing and alignment patterns, the dark
// module and the version information, and reserves the format areas.
func (c *Code) drawFunction(function []bool) {
	n := c.Size
	for i := range n {
		c.set(function, 6, i, i%2 == 0)
		c.set(function, i, 6, i%2 == 0)
	}
	for _, at := range [][2]int{{3, 3}, {n - 4, 3}, {3, n - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := at[0]+dx, at[1]+dy
				if x < 0 || y < 0 || x >= n || y >= n {
					continue
				}
				d := max(abs(dx), abs(dy))
				c.set(function, x, y, d != 2 && d != 4)
			}
		}
	}
	pos := alignment(c.Version)
	for i, ay := range pos {
		for j, ax := range pos {
			if (i == 0 && j == 0) || (i == 0 && j == len(pos)-1) || (i == len(pos)-1 && j == 0) {
				continue // the finders' corners
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					c.set(function, ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	c.drawFormat(L, 0) // reserves the areas; drawn again after masking
	c.set(function, 8, n-8, true)
	for i := range 15 {
		x, y := formatPositions(n, i)
		function[y[0]*n+x[0]], function[y[1]*n+x[1]] = true, true
	}
	if c.Version >= 7 {
		rem := c.Version
		for range 12 {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		bits := c.Version<<12 | rem
		for i := range 18 {
			dark := bits>>i&1 == 1
			a, b := n-11+i%3, i/3
			c.set(function, a, b, dark)
			c.set(function, b, a, dark)
		}
	}
}

// alignment is the centre coordinates of the alignment patterns.
func alignment(v int) []int {
	if v == 1 {
		return nil
	}
	count := v/7 + 2
	step := 26
	if v != 32 {
		step = (v*4 + count*2 + 1) / (count*2 - 2) * 2
	}
	pos := make([]int, count)
	pos[0] = 6
	for i, p := count-1, 17+4*v-7; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

// formatPositions are the two places of format bit i (0 the least
// significant): (x[0], y[0]) by the top-left finder, (x[1], y[1]) split
// between the other two.
func formatPositions(n, i int) (x, y [2]int) {
	switch {
	case i < 6:
		x[0], y[0] = 8, i
	case i < 8:
		x[0], y[0] = 8, i+1
	case i == 8:
		x[0], y[0] = 7, 8
	default:
		x[0], y[0] = 14-i, 8
	}
	if i < 8 {
		x[1], y[1] = n-1-i, 8
	} else {
		x[1], y[1] = 8, n-15+i
	}
	return x, y
}

func (c *Code) drawFormat(level Level, mask int) {
	data := [4]int{1, 0, 3, 2}[level]<<3 | mask
	rem := data
	for range 10 {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	bits := (data<<10 | rem) ^ 0x5412
	for i := range 15 {
		x, y := formatPositions(c.Size, i)
		dark := bits>>i&1 == 1
		c.modules[y[0]*c.Size+x[0]] = dark
		c.modules[y[1]*c.Size+x[1]] = dark
	}
}

// place writes the codewords in the zigzag order: two columns at a time
// from the right, up then down, skipping the vertical timing pattern.
func (c *Code) place(codewords []byte, function []bool) {
	n, i := c.Size, 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range n {
			for j := range 2 {
				x := right - j
				up := (right+1)&2 == 0
				y := vert
				if up {
					y = n - 1 - vert
				}
				if function[y*n+x] {
					continue
				}
				if i < len(codewords)*8 {
					c.modules[y*n+x] = codewords[i/8]>>(7-i%8)&1 == 1
				}
				i++ // remainder bits stay light
			}
		}
	}
}

func maskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	default:
		return ((x+y)%2+x*y%3)%2 == 0
	}
}

func (c *Code) applyMask(mask int, function []bool) {
	for y := range c.Size {
		for x := range c.Size {
			if !function[y*c.Size+x] && maskBit(mask, x, y) {
				c.modules[y*c.Size+x] = !c.modules[y*c.Size+x]
			}
		}
	}
}

// penalty scores the symbol by the four rules of ISO/IEC 18004 7.8.3:
// runs, 2×2 blocks, finder-like patterns and the dark proportion.
func (c *Code) penalty() int {
	n, p, dark := c.Size, 0, 0
	line := make([]bool, n)
	for _, rows := range []bool{true, false} {
		for a := range n {
			for b := range n {
				if rows {
					line[b] = c.modules[a*n+b]
				} else {
					line[b] = c.modules[b*n+a]
				}
			}
			run := 1
			for b := 1; b <= n; b++ {
				if b < n && line[b] == line[b-1] {
					run++
					continue
				}
				if run >= 5 {
					p += run - 2
				}
				run = 1
			}
			for b := 0; b+7 <= n; b++ {
				if line[b] && !line[b+1] && line[b+2] && line[b+3] && line[b+4] && !line[b+5] && line[b+6] &&
					(light(line, b-4, b) || light(line, b+7, b+11)) {
					p += 40
				}
			}
		}
	}
	for y := range n {
		for x := range n {
			d := c.modules[y*n+x]
			if d {
				dark++
			}
			if x+1 < n && y+1 < n && d == c.modules[y*n+x+1] && d == c.modules[(y+1)*n+x] && d == c.modules[(y+1)*n+x+1] {
				p += 3
			}
		}
	}
	return p + abs(dark*20-n*n*10)/(n*n)*10
}

// light reports whether line[from:to] is light; outside the symbol is.
func light(line []bool, from, to int) bool {
	for i := from; i < to; i++ {
		if i >= 0 && i < len(line) && line[i] {
			return false
		}
	}
	return true
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// HalfBlocks draws the code for a terminal, two rows of modules per line
// with a quiet zone of quiet modules: ▀ ▄ █ and space, where a drawn
// half is dark. With color the lines set black on bright white (SGR 30
// and 107) and reset at the end, so the code reads the same on dark and
// light terminal themes; without, the caller's background must be light.
func (c *Code) HalfBlocks(quiet int, color bool) string {
	var b strings.Builder
	for y := -quiet; y < c.Size+quiet; y += 2 {
		if color {
			b.WriteString("\x1b[30;107m")
		}
		for x := -quiet; x < c.Size+quiet; x++ {
			top, bottom := c.Dark(x, y), y+1 < c.Size+quiet && c.Dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		if color {
			b.WriteString("\x1b[0m")
		}
		b.WriteByte('\n')
	}
	return b.String()
}
