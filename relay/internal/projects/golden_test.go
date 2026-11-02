package projects

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// goldenState fills every field of State, so the golden file pins the
// wire shape other Macs' daemons decode (strictly, on older hosts).
func goldenState() *State {
	st := Stamp{T: 1767225600000, C: 2, N: "n-0011223344556677"}
	return &State{
		Node:  "n-0011223344556677",
		Short: "mini",
		Nodes: map[string]string{"n-8899aabbccddeeff": "studio"},
		Projects: []*Record{{
			ID:       ProjectID(wire.ProjectIdentity{Remote: "github.com/example/tool"}),
			Identity: wire.ProjectIdentity{Remote: "github.com/example/tool"},
			ParentID: "p-0000000000000000",
			Name:     Field[string]{V: "tool", S: st},
			Color:    Field[string]{V: "#336699", S: st},
			Kind:     Field[string]{V: wire.ProjectRepo, S: st},
			Defaults: Field[wire.ProjectDefaults]{V: wire.ProjectDefaults{Profile: "work", Machine: "studio"}, S: st},
			Deleted:  Field[bool]{V: true, S: st},
			Paths:    map[string]Field[string]{"n-0011223344556677": {V: "/src/tool", S: st}},
			LastUsed: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			Created:  time.Date(2025, 12, 31, 8, 0, 0, 0, time.UTC),
			Scratch: &ScratchRecord{Home: Field[string]{V: "n-0011223344556677", S: st}, Keep: Field[bool]{V: true, S: st},
				ArchivedAt: Field[int64]{V: 1767225600000, S: st}, Git: Field[bool]{V: true, S: st}, AdoptedAt: Field[int64]{V: 1767225500000, S: st}},
		}},
		Groups: []*GroupRecord{{
			ID:         "g-tools",
			Name:       Field[string]{V: "Tools", S: st},
			ProjectIDs: Field[[]string]{V: []string{"p-0000000000000000"}, S: st},
			Order:      Field[int]{V: 3, S: st},
			Color:      Field[string]{V: "#112233", S: st},
			Deleted:    Field[bool]{V: false, S: Stamp{T: 1767225600001, N: "n-8899aabbccddeeff"}},
		}},
	}
}

// TestStateGolden: projects.State's JSON is what testdata/state.golden.json
// says, both ways.
func TestStateGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "state.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(goldenState(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if !bytes.Equal(got, want) {
		t.Fatalf("State's JSON changed:\n%s\nwant:\n%s", got, want)
	}
	var back State
	dec := json.NewDecoder(bytes.NewReader(want))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	again, _ := json.MarshalIndent(&back, "", "  ")
	if !bytes.Equal(append(again, '\n'), want) {
		t.Fatalf("round trip changed it:\n%s", again)
	}
}

func TestNodeOfMachineOf(t *testing.T) {
	a := Open(Options{Machine: "mini", Logf: t.Logf})
	defer a.Close()
	b := Open(Options{Machine: "studio", Logf: t.Logf})
	defer b.Close()
	a.Merge(b.Export(), "big-mac") // this Mac calls studio "big-mac"
	if a.NodeOf("mini") != a.Node() || a.NodeOf("big-mac") != b.Node() || a.NodeOf("studio") != "" || a.NodeOf("") != "" {
		t.Fatalf("NodeOf: %q %q %q", a.NodeOf("mini"), a.NodeOf("big-mac"), a.NodeOf("studio"))
	}
	if a.MachineOf(b.Node()) != "big-mac" || a.MachineOf("n-unknown") != "" {
		t.Fatalf("MachineOf %q", a.MachineOf(b.Node()))
	}
}
