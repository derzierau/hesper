package endings

import (
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Synthetic agent endings, written for these tests.
func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		message string
		ending  string
		first   string // the first sentence returned ("" none)
	}{
		{"empty", "", wire.EndingDone, ""},
		{"done", "Fixed the off-by-one in the parser and added a test.\n\nAll checks pass.", wire.EndingDone, ""},
		{"done summary list", "## Summary\n\n- Renamed the flag\n- Updated the docs\n\nThe build is green.", wire.EndingDone, ""},
		{"should I", "The branch is ready.\n\nShould I push it and open a PR?", wire.EndingAsks, "Should I push it and open a PR?"},
		{"do you want me to", "Tests pass. Do you want me to squash the commits?", wire.EndingAsks, "Do you want me to squash the commits?"},
		{"want me to", "I added the retry. Want me to also cap the backoff?", wire.EndingAsks, "Want me to also cap the backoff?"},
		{"shall I", "Shall I delete the old fixtures as well?", wire.EndingAsks, "Shall I delete the old fixtures as well?"},
		{"final question to the reader", "There are two ways to do this: a flag or a setting.\n\nWhich one do you prefer?", wire.EndingAsks, "Which one do you prefer?"},
		{"german soll ich", "Der Fix ist fertig. Soll ich ihn auch auf main übernehmen?", wire.EndingAsks, "Soll ich ihn auch auf main übernehmen?"},
		{"german möchtest du", "Alles grün.\n\nMöchtest du, dass ich die Doku anpasse?", wire.EndingAsks, "Möchtest du, dass ich die Doku anpasse?"},
		{"german final question", "Beide Varianten gehen. Welche nimmst du?", wire.EndingAsks, "Welche nimmst du?"},
		{"question before the end", "Should I keep the old API? I kept it for now.\n\nEverything is committed.", wire.EndingAsks, "Should I keep the old API?"},
		{"rhetorical question no reader", "Why did it fail? The cache was stale. Fixed now. Is it fast?", wire.EndingDone, ""},
		{"still yours", "I committed the change.\n\nStill yours to do: approve the deploy in the console.", wire.EndingYourStep, "Still yours to do: approve the deploy in the console."},
		{"you need to", "The migration is written. You need to run it against staging yourself.", wire.EndingYourStep, "You need to run it against staging yourself."},
		{"you'll need to curly", "Done. You’ll need to restart the daemon for it to load.", wire.EndingYourStep, "You’ll need to restart the daemon for it to load."},
		{"to release list", "Version bumped.\n\nTo release:\n1. Tag v1.2.3\n2. Push the tag", wire.EndingYourStep, "To release:"},
		{"run in terminal", "I can't sign it here: run `make sign` in Terminal.", wire.EndingYourStep, "I can't sign it here: run `make sign` in Terminal."},
		{"german musst du", "Fertig. Den Schlüssel musst du selbst eintragen.", wire.EndingYourStep, "Den Schlüssel musst du selbst eintragen."},
		{"german bitte führe aus", "Bitte führe `npm login` im Terminal aus.", wire.EndingYourStep, "Bitte führe `npm login` im Terminal aus."},
		{"waiting for", "The PR is open. Waiting for CI to finish.", wire.EndingWaits, "Waiting for CI to finish."},
		{"once merged", "I'll update the changelog once the PR is merged.", wire.EndingWaits, "I'll update the changelog once the PR is merged."},
		{"after you've released", "The notes go out after you've released 2.0.", wire.EndingWaits, "The notes go out after you've released 2.0."},
		{"german sobald", "Ich mache weiter, sobald der Build durch ist.", wire.EndingWaits, "Ich mache weiter, sobald der Build durch ist."},
		{"offer", "All done.\n\nIf you'd like, I can also add a benchmark.", wire.EndingOffer, "If you'd like, I can also add a benchmark."},
		{"i can also", "The parser is fixed. I can also port the fix to the CLI.", wire.EndingOffer, "I can also port the fix to the CLI."},
		{"german wenn du möchtest", "Erledigt. Wenn du möchtest, schreibe ich noch Tests.", wire.EndingOffer, "Wenn du möchtest, schreibe ich noch Tests."},
		{"asks over offer", "I can also add a test. Should I?\n\nShould I add it now?", wire.EndingAsks, "Should I add it now?"},
		{"yourStep over waits", "Waiting for review. You need to approve the release in the store.", wire.EndingYourStep, "You need to approve the release in the store."},
		{"waits over offer", "I can also tidy the imports. Waiting for the reviewer.", wire.EndingWaits, "Waiting for the reviewer."},
		{"asks over yourStep", "You need to pick a name. Do you want me to suggest some?", wire.EndingAsks, "Do you want me to suggest some?"},
		{"code fence ignored", "Done.\n\n```\n# Should I keep this?\nyou need to\n```\n\nAll good.", wire.EndingDone, ""},
		{"bold markers", "**Should I merge it?**", wire.EndingAsks, "Should I merge it?"},
		{"early paragraphs ignored", "You need to read this first.\n\nOne.\n\nTwo.\n\nThree. Done.", wire.EndingDone, ""},
		{"offer not a question", "If you want me to, I can push. Done otherwise.", wire.EndingDone, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ending, sentences := Classify(c.message)
			if ending != c.ending {
				t.Fatalf("ending %q, want %q (sentences %q)", ending, c.ending, sentences)
			}
			first := ""
			if len(sentences) > 0 {
				first = sentences[0]
			}
			if first != c.first {
				t.Fatalf("first sentence %q, want %q", first, c.first)
			}
		})
	}
}

func TestClassifyStepList(t *testing.T) {
	_, s := Classify("Ready.\n\nTo release:\n1. Tag it\n2. Push the tag\n3. Publish the notes\n4. Tell the team\n5. Close the milestone\n6. Celebrate")
	if len(s) != MaxSentences || s[1] != "Tag it" {
		t.Fatalf("sentences %q", s)
	}
}

func TestClassifyLimits(t *testing.T) {
	long := "You need to " + strings.Repeat("really ", 60) + "approve it."
	_, s := Classify(long)
	if len(s) != 1 || len(s[0]) > MaxSentence || !strings.HasSuffix(s[0], "…") {
		t.Fatalf("sentence %q (%d bytes)", s, len(s[0]))
	}
	// A huge message: only its tail is read.
	huge := strings.Repeat("Should I? ", 1) + strings.Repeat("filler line\n", 10000) + "\nDone."
	if e, _ := Classify(huge); e != wire.EndingDone {
		t.Fatalf("huge: %s", e)
	}
	// Invalid UTF-8 at the cut never breaks a sentence.
	if e, _ := Classify(strings.Repeat("ü", tailBytes) + "\n\nWant me to go on?"); e != wire.EndingAsks {
		t.Fatalf("utf-8 tail: %s", e)
	}
}

func BenchmarkClassify(b *testing.B) {
	msg := strings.Repeat("Some explanation of the change, with details.\n", 200) + "\n\nShould I push it?"
	for b.Loop() {
		Classify(msg)
	}
}
