package review

import (
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Word ranges: inside a hunk, each run of removed lines is paired line by
// line with the run of added lines after it; a pair that shares enough
// words gets the ranges that differ (UTF-8 byte offsets into the
// line's text). Bounded by time and size: a review diff never waits on them.

var (
	// wordTime bounds the word ranges of one diff.
	wordTime = 300 * time.Millisecond
	// maxWordTokens bounds one line's words; maxWordPairs the pairs of
	// one diff.
	maxWordTokens = 400
	maxWordPairs  = 20000
)

type wordBudget struct {
	deadline time.Time
	pairs    int
}

func newWordBudget(deadline time.Time) *wordBudget { return &wordBudget{deadline: deadline} }

func (b *wordBudget) take() bool {
	if b.pairs >= maxWordPairs || (b.pairs%64 == 0 && time.Now().After(b.deadline)) {
		b.pairs = maxWordPairs
		return false
	}
	b.pairs++
	return true
}

// addWords sets the word ranges of h's changed lines.
func addWords(h *wire.ReviewHunk, budget *wordBudget) {
	lines := h.Lines
	for i := 0; i < len(lines); {
		if lines[i].Kind != "-" {
			i++
			continue
		}
		start := i
		for i < len(lines) && lines[i].Kind == "-" {
			i++
		}
		removed := lines[start:i]
		begin := i
		for i < len(lines) && lines[i].Kind == "+" {
			i++
		}
		added := lines[begin:i]
		for k := 0; k < len(removed) && k < len(added); k++ {
			if !budget.take() {
				return
			}
			old, nw := wordRanges(removed[k].Text, added[k].Text)
			removed[k].Words, added[k].Words = old, nw
		}
	}
}

type token struct {
	text       string
	start, end int // byte offsets
}

// tokens splits a line into words (letters, digits, _), runs of spaces
// and single other characters.
func tokens(s string) []token {
	var list []token
	class := func(r rune) int {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			return 1
		case unicode.IsSpace(r):
			return 2
		}
		return 0
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		c := class(r)
		j := i + size
		if c != 0 {
			for j < len(s) {
				r2, size2 := utf8.DecodeRuneInString(s[j:])
				if class(r2) != c {
					break
				}
				j += size2
			}
		}
		list = append(list, token{text: s[i:j], start: i, end: j})
		i = j
	}
	return list
}

// wordRanges are the changed ranges of old and new, nil for both when
// the lines are too long or share too little to be read as one changed
// line.
func wordRanges(old, nw string) ([][2]int, [][2]int) {
	a, b := tokens(old), tokens(nw)
	if len(a) == 0 || len(b) == 0 || len(a) > maxWordTokens || len(b) > maxWordTokens {
		return nil, nil
	}
	// Longest common subsequence of the tokens.
	n, m := len(a), len(b)
	table := make([]int32, (n+1)*(m+1))
	at := func(i, j int) *int32 { return &table[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i].text == b[j].text {
				*at(i, j) = *at(i+1, j+1) + 1
			} else {
				*at(i, j) = max(*at(i+1, j), *at(i, j+1))
			}
		}
	}
	common := 0
	keepA, keepB := make([]bool, n), make([]bool, m)
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i].text == b[j].text:
			keepA[i], keepB[j] = true, true
			common += len(a[i].text)
			i++
			j++
		case *at(i+1, j) >= *at(i, j+1):
			i++
		default:
			j++
		}
	}
	// Too little in common: the whole line changed.
	if 2*common < (len(old)+len(nw))/3 {
		return nil, nil
	}
	return spans(a, keepA), spans(b, keepB)
}

// spans merges the tokens not kept into ranges.
func spans(list []token, keep []bool) [][2]int {
	var out [][2]int
	for i, t := range list {
		if keep[i] {
			continue
		}
		if n := len(out); n > 0 && out[n-1][1] == t.start {
			out[n-1][1] = t.end
			continue
		}
		out = append(out, [2]int{t.start, t.end})
	}
	return out
}
