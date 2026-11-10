package main

import (
	"io"
	"os"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// pager pages seqs (ascending) backwards like agents.transcript, n a page.
func pager(seqs []int64, n int) func(before int64) (wire.TranscriptPage, error) {
	return func(before int64) (wire.TranscriptPage, error) {
		end := len(seqs)
		for before > 0 && end > 0 && seqs[end-1] >= before {
			end--
		}
		start := max(end-n, 0)
		var p wire.TranscriptPage
		for _, s := range seqs[start:end] {
			p.Items = append(p.Items, wire.TranscriptItem{Seq: s})
		}
		p.More = start > 0
		return p, nil
	}
}

// A clipped message preview is marked like a clipped result.
func TestPrintTranscriptMarksTruncated(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	printTranscript([]wire.TranscriptItem{
		{Seq: 1, Kind: wire.ItemUser, Text: "long ask", Truncated: true},
		{Seq: 2, Kind: wire.ItemAssistant, Text: "long reply", Truncated: true},
		{Seq: 3, Kind: wire.ItemAssistant, Text: "short"},
	}, false, -1)
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	if want := "› long ask…\nlong reply…\nshort\n"; string(out) != want {
		t.Fatalf("printed %q, want %q", out, want)
	}
}

func seqsOf(items []wire.TranscriptItem) []int64 {
	var out []int64
	for _, it := range items {
		out = append(out, it.Seq)
	}
	return out
}

func TestTranscriptSince(t *testing.T) {
	var seqs []int64
	for i := range 130 {
		seqs = append(seqs, int64(i*10))
	}
	// A burst of more than one page since the last printed: every item.
	items, restarted, gap, err := transcriptSince(pager(seqs, 50), 100)
	if err != nil || restarted || gap {
		t.Fatalf("burst: restarted %v gap %v err %v", restarted, gap, err)
	}
	if got := seqsOf(items); len(got) != 119 || got[0] != 110 || got[118] != 1290 {
		t.Fatalf("burst: %d items %v", len(got), got)
	}
	// Nothing new.
	if items, restarted, _, _ := transcriptSince(pager(seqs, 50), 1290); len(items) != 0 || restarted {
		t.Fatalf("nothing new: %v %v", seqsOf(items), restarted)
	}
	// Rewritten shorter: a restart and the newest page.
	items, restarted, _, _ = transcriptSince(pager([]int64{0, 10}, 50), 1290)
	if !restarted || len(items) != 2 {
		t.Fatalf("rewrite: %v %v", seqsOf(items), restarted)
	}
	// Emptied.
	if items, restarted, _, _ := transcriptSince(pager(nil, 50), 1290); !restarted || len(items) != 0 {
		t.Fatalf("emptied: %v %v", seqsOf(items), restarted)
	}
	// Too far behind: capped, and said so.
	many := make([]int64, wire.TranscriptMaxLimit+100)
	for i := range many {
		many[i] = int64(i + 1)
	}
	items, _, gap, _ = transcriptSince(pager(many, 50), 0)
	if !gap || len(items) > wire.TranscriptMaxLimit || items[len(items)-1].Seq != many[len(many)-1] {
		t.Fatalf("far behind: %d items, gap %v", len(items), gap)
	}
}
