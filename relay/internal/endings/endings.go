// Package endings reads how an agent's turn ended from its final message:
// it asks the person something, names a step that is the person's, waits
// on someone else, offers more work, or is done (wire.Ending*). The rules
// are phrases, English and German, over the message's last paragraphs;
// no model, no regular expressions.
package endings

import (
	"strings"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Limits: Classify reads at most tailBytes from the message's end, its
// last maxParagraphs paragraphs (their last maxLines lines), and returns at most MaxSentences
// sentences of at most MaxSentence bytes.
const (
	MaxSentences  = 5
	MaxSentence   = 200
	tailBytes     = 2 << 10
	maxParagraphs = 3
	maxLines      = 12
)

// A rule is a phrase: fragments that follow each other in one sentence
// ("once", "is merged" matches "once the PR is merged"). question: the
// sentence must end with "?".
type rule struct {
	parts    []string
	question bool
}

func phrases(question bool, ps ...string) []rule {
	out := make([]rule, 0, len(ps))
	for _, p := range ps {
		out = append(out, rule{parts: strings.Split(p, "…"), question: question})
	}
	return out
}

// The rules, lower case, by ending in precedence order (asks > yourStep >
// waits > offer).
var (
	asksRules = phrases(true,
		"should i ", "do you want me to", "want me to", "shall i ", "would you like me to", "do you want to",
		"soll ich", "möchtest du", "willst du", "sollen wir")
	yourStepRules = phrases(false,
		"still yours", "you need to", "you have to", "you'll need to", "you will need to",
		"to release:", "your turn", "on your side", "run `…` in terminal", "run `…` in a terminal",
		"musst du", "du musst", "bitte führe … aus", "führe bitte … aus", "liegt bei dir")
	waitsRules = phrases(false,
		"waiting for", "waiting on", "once … is merged", "once … are merged", "once … merged", "after you've released",
		"after you have released", "sobald", "wartet auf", "warte auf", "warten auf")
	offerRules = phrases(false,
		"if you'd like, i can", "if you like, i can", "if you want, i can", "i can also", "happy to also",
		"let me know if you'd like", "let me know if you want", "wenn du möchtest", "wenn du willst", "ich kann auch")
)

// secondPerson marks a final question as one to the reader.
var secondPerson = []string{"you", "your", "du", "dir", "dich", "dein", "ihr", "euch"}

// Classify is the ending of a turn whose final message is message, and
// the sentences that carry the open item (none for done).
func Classify(message string) (ending string, sentences []string) {
	paras := lastParagraphs(message)
	if len(paras) == 0 {
		return wire.EndingDone, nil
	}
	var all []sentence
	for _, p := range paras {
		all = append(all, p...)
	}
	if hits := match(paras, asksRules); hits != nil {
		return wire.EndingAsks, hits
	}
	if last := all[len(all)-1]; strings.HasSuffix(last.lower, "?") && hasWord(last.lower, secondPerson) {
		return wire.EndingAsks, []string{clip(last.text)}
	}
	if hits := match(paras, yourStepRules); hits != nil {
		return wire.EndingYourStep, hits
	}
	if hits := match(paras, waitsRules); hits != nil {
		return wire.EndingWaits, hits
	}
	if hits := match(paras, offerRules); hits != nil {
		return wire.EndingOffer, hits
	}
	return wire.EndingDone, nil
}

type sentence struct {
	text  string // as written, markup trimmed
	lower string // lower case, straight apostrophes, no bold markers
}

// match is the sentences that match one of rules, in order, at most
// MaxSentences: a matched sentence that ends with ":" (a step list's
// head) brings the rest of its paragraph along.
func match(paras [][]sentence, rules []rule) []string {
	var out []string
	for _, p := range paras {
		for i := 0; i < len(p) && len(out) < MaxSentences; i++ {
			if !matchesAny(p[i].lower, rules) {
				continue
			}
			out = append(out, clip(p[i].text))
			if strings.HasSuffix(p[i].lower, ":") {
				for i++; i < len(p) && len(out) < MaxSentences; i++ {
					out = append(out, clip(p[i].text))
				}
			}
		}
	}
	return out
}

func matchesAny(s string, rules []rule) bool {
	for _, r := range rules {
		if r.question && !strings.HasSuffix(s, "?") {
			continue
		}
		if inOrder(s, r.parts) {
			return true
		}
	}
	return false
}

// inOrder reports whether parts occur in s one after the other.
func inOrder(s string, parts []string) bool {
	for _, p := range parts {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return true
}

// hasWord reports whether s has one of words as a whole word.
func hasWord(s string, words []string) bool {
	for _, w := range words {
		for rest := s; ; {
			i := strings.Index(rest, w)
			if i < 0 {
				break
			}
			before, after := rest[:i], rest[i+len(w):]
			if !letterEnd(before) && !letterStart(after) {
				return true
			}
			rest = after
		}
	}
	return false
}

func letterEnd(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return isLetter(r)
}

func letterStart(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return isLetter(r)
}

func isLetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 0x7f && r != utf8.RuneError && r != '’' && r != '…'
}

// lastParagraphs splits the end of message into paragraphs of sentences:
// code fences, headings and rules are left out.
func lastParagraphs(message string) [][]sentence {
	if len(message) > tailBytes {
		message = message[len(message)-tailBytes:]
		if i := strings.IndexByte(message, '\n'); i >= 0 {
			message = message[i+1:] // from a whole line
		}
		message = strings.ToValidUTF8(message, "")
	}
	// The paragraphs as lines first (cheap), sentences only for the last
	// few.
	var paras [][]string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			paras = append(paras, cur)
			cur = nil
		}
	}
	fenced := false
	for line := range strings.SplitSeq(message, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			flush()
			continue
		}
		if fenced {
			continue
		}
		if t == "" || t == "---" || t == "***" || strings.HasPrefix(t, "#") {
			flush()
			continue
		}
		cur = append(cur, t)
	}
	flush()
	if len(paras) > maxParagraphs {
		paras = paras[len(paras)-maxParagraphs:]
	}
	out := make([][]sentence, 0, len(paras))
	for _, p := range paras {
		if len(p) > maxLines {
			p = p[len(p)-maxLines:]
		}
		var ss []sentence
		for _, line := range p {
			ss = append(ss, sentences(listItem(line))...)
		}
		out = append(out, ss)
	}
	return out
}

// listItem drops a list marker ("- ", "* ", "1. ", "> ").
func listItem(t string) string {
	for _, m := range []string{"- ", "* ", "+ ", "> "} {
		if strings.HasPrefix(t, m) {
			return strings.TrimSpace(t[len(m):])
		}
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i > 0 && i < len(t)-1 && (t[i] == '.' || t[i] == ')') && t[i+1] == ' ' {
		return strings.TrimSpace(t[i+2:])
	}
	return t
}

// sentences splits one line at ". ", "! " and "? " (outside backticks).
func sentences(line string) []sentence {
	var out []sentence
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		lower := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "’", "'"), "**", ""))
		lower = strings.TrimRight(lower, " *_")
		out = append(out, sentence{text: strings.Trim(s, " *_"), lower: lower})
	}
	code := false
	start := 0
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '`':
			code = !code
		case !code && (c == '.' || c == '!' || c == '?') && i+1 < len(line) && line[i+1] == ' ':
			add(line[start : i+1])
			start = i + 1
		}
	}
	add(line[start:])
	return out
}

// clip bounds a sentence to MaxSentence bytes on a rune boundary.
func clip(s string) string {
	if len(s) <= MaxSentence {
		return s
	}
	cut := MaxSentence - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
