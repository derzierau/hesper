package steward

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The brief: a new Steward session's first message (its first start, and
// each day's rotation). Built from the records only, never from an
// earlier session's transcript: the open and parked loops with who acts
// next, and the milestones with their progress.

// Brief limits.
const (
	briefLoops      = 30
	briefMilestones = 20
	briefLine       = 120
)

// Brief is the first message of a new Steward session.
func (s *Steward) Brief() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hesper started your session for %s. The records as they are now:\n\n", s.dayOf(s.opt.Now()))
	loops := s.opt.Store.List(wire.LoopsListParams{}) // open and parked
	b.WriteString("Open loops:\n")
	if len(loops) == 0 {
		b.WriteString("- none\n")
	}
	for i, l := range loops {
		if i == briefLoops {
			fmt.Fprintf(&b, "- … and %d more (hesperctl loops ls)\n", len(loops)-i)
			break
		}
		line := l.ID + " " + l.Title + " [" + l.Status + ", next: " + l.Next + "]"
		if l.NextText != "" {
			line += " " + l.NextText
		}
		b.WriteString("- " + clip(line) + "\n")
	}
	ms := s.opt.Store.Goals("").Milestones
	b.WriteString("\nMilestones:\n")
	n := 0
	for _, m := range ms {
		if m.Status != wire.LoopOpen && m.Status != wire.MilestoneProposed {
			continue
		}
		if n == briefMilestones {
			b.WriteString("- … (hesperctl goals ls)\n")
			break
		}
		n++
		line := fmt.Sprintf("%s %s [%s, %d/%d done, %d loops", m.ID, m.Title, m.Status, m.Done, m.Total, len(m.Loops))
		if m.Stalled {
			line += ", stalled"
		}
		b.WriteString("- " + clip(line+"]") + "\n")
	}
	if n == 0 {
		b.WriteString("- none\n")
	}
	b.WriteString("\nNothing to do now: wait until Hesper tells you to run hesperctl steward inbox.")
	return b.String()
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= briefLine {
		return s
	}
	cut := briefLine - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// rotate ends the Steward's session of an earlier day and starts a new
// one with a fresh brief (the first wake after local midnight); the
// waiting events stay for the new session's first wake.
func (s *Steward) rotate(old string) {
	s.mu.Lock()
	reg := s.reg
	s.agent, s.state, s.session, s.day = "", "", "", ""
	s.mu.Unlock()
	if _, err := reg.CloseAgent(old); err != nil {
		s.opt.Logf("steward: closing yesterday's session %s: %v", old, err)
	}
	a, err := s.spawn()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.opt.Logf("steward: starting today's session: %v", err)
		s.notifyLocked()
		return // ensure starts one, within the restart budget
	}
	s.agent, s.state, s.session, s.day = a.ID, a.State, a.SessionID, s.dayOf(s.opt.Now())
	s.notifyLocked()
}
