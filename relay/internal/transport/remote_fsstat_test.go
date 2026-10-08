package transport_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fs.stat {machine} goes through the gateway: L asks whether a folder is
// on M (a draft's folder on the machine it will start on). An observer
// may ask; an unapproved device may not; the relay never sees the path.
func TestRemoteFSStat(t *testing.T) {
	t.Run("observer", func(t *testing.T) {
		w := newWorld(t, worldOptions{rights: []string{"observe"}})
		w.L.waitLinked(t, "M")
		var got wire.FSStat
		if err := w.L.call(t, "fs.stat", wire.FSStatParams{Machine: "M", Path: w.M.project}, &got); err != nil || !got.Exists || !got.IsDir {
			t.Fatalf("M's project from L: %+v %v", got, err)
		}
		// A folder only L has: not on M (the composer's "This folder is on
		// L, not on M").
		only := filepath.Join(w.L.home, "projects", "SECRET-ONLY-ON-L")
		if err := w.L.call(t, "fs.stat", wire.FSStatParams{Path: w.L.project}, &got); err != nil || !got.IsDir {
			t.Fatalf("L's own project: %+v %v", got, err)
		}
		if err := w.L.call(t, "fs.stat", wire.FSStatParams{Machine: "M", Path: only}, &got); err != nil || got.Exists {
			t.Fatalf("missing on M: %+v %v", got, err)
		}
		err := w.L.call(t, "fs.stat", wire.FSStatParams{Machine: "M", Path: "relative"}, nil)
		if err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Fatalf("relative path on M: %v", err)
		}
		if leaked := w.tap.sawAny("SECRET-ONLY-ON-L"); leaked != "" {
			t.Fatalf("the relay saw %q", leaked)
		}
		if w.tap.count(`"method":"fs.`) != 0 {
			t.Fatal("fs.stat went in plaintext")
		}
	})
	t.Run("unapproved", func(t *testing.T) {
		w := newWorld(t, worldOptions{stranger: true})
		err := w.L.call(t, "fs.stat", wire.FSStatParams{Machine: "M", Path: w.M.project}, nil)
		if code := wireCode(err); code != wire.CodeForbidden && code != wire.CodeUnavailable {
			t.Fatalf("fs.stat from an unapproved device: %v", err)
		}
	})
}
