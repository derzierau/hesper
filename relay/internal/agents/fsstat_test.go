package agents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fs.stat on the local socket: a draft's folder here (exists, isDir);
// unclean paths are refused; another machine without the gateway is
// unavailable.
func TestFSStatLocal(t *testing.T) {
	h := newHarness(t)
	file := filepath.Join(h.project, "notes.txt")
	os.WriteFile(file, []byte("x"), 0o600)
	for _, c := range []struct {
		path string
		want wire.FSStat
	}{
		{h.project, wire.FSStat{Exists: true, IsDir: true}},
		{file, wire.FSStat{Exists: true}},
		{filepath.Join(h.project, "missing"), wire.FSStat{}},
	} {
		var got wire.FSStat
		if err := h.call("fs.stat", wire.FSStatParams{Path: c.path}, &got); err != nil || got != c.want {
			t.Fatalf("fs.stat %s: %+v %v", c.path, got, err)
		}
	}
	// This machine named explicitly: still here.
	var got wire.FSStat
	if err := h.call("fs.stat", wire.FSStatParams{Machine: h.reg.Machine(), Path: h.project}, &got); err != nil || !got.IsDir {
		t.Fatalf("own machine: %+v %v", got, err)
	}
	for _, bad := range []string{"", "relative/x", h.project + "/../x", h.project + "/"} {
		err := h.call("fs.stat", wire.FSStatParams{Path: bad}, nil)
		if we := asWire(err); we == nil || we.Code != wire.CodeInvalid {
			t.Fatalf("fs.stat %q: %v", bad, err)
		}
	}
	err := h.call("fs.stat", wire.FSStatParams{Machine: "M", Path: h.project}, nil)
	if we := asWire(err); we == nil || we.Code != wire.CodeUnavailable {
		t.Fatalf("remote without gateway: %v", err)
	}
}

// machineExplicit is kept by drafts.save (and older drafts without it
// read as false).
func TestDraftMachineExplicitKept(t *testing.T) {
	h := newHarness(t)
	var got wire.Draft
	if err := h.call("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "d-explicit", Machine: "mini", MachineExplicit: true, Project: "/p"}}, &got); err != nil || !got.MachineExplicit {
		t.Fatalf("save: %+v %v", got, err)
	}
	got = wire.Draft{}
	if err := h.call("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "d-old", Machine: "mini", Project: "/p"}}, &got); err != nil || got.MachineExplicit {
		t.Fatalf("save old: %+v %v", got, err)
	}
	var list []wire.Draft
	h.call("drafts.list", nil, &list)
	if len(list) != 2 || !list[0].MachineExplicit || list[1].MachineExplicit {
		t.Fatalf("list %+v", list)
	}
}
