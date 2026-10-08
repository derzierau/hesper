package agents

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fs.stat: the app checks a draft's folder on the machine the agent will
// run on before it starts it (and while the draft is edited), so a folder
// that only exists on another Mac is said in the composer, not by the
// spawn's "no directory". Local here; another machine's through the
// gateway (internal/host, right "observe"). Only exists/isDir are told.

// CleanAbs reports whether path is a clean absolute path fs.stat answers
// for.
func CleanAbs(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && len(path) <= 4096 && !strings.ContainsRune(path, 0)
}

// Stat answers fs.stat for this machine (symlinks followed).
func Stat(path string) (wire.FSStat, error) {
	if !CleanAbs(path) {
		return wire.FSStat{}, wire.Errorf(wire.CodeInvalid, "path must be a clean absolute path")
	}
	st, err := os.Stat(path)
	if err != nil {
		return wire.FSStat{}, nil
	}
	return wire.FSStat{Exists: true, IsDir: st.IsDir()}, nil
}
