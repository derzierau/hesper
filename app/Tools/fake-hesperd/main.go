// Command fake-hesperd is a TEST-ONLY stand-in for part D's hesperd.
//
// It speaks the contract in docs/rebuild-contract.md (control JSON-RPC and
// attach frames) on a socket given by --socket, $HESPER_SOCKET or
// $HESPER_STATE_DIR, and runs fake TUIs instead of claude/codex. It never
// touches the live state dir, LaunchAgents or real agents.
//
//	fake-hesperd serve [--agents N] [--tui agent|flood] [--fps N] [--scenario] [--sizes 120x40,200x60]
//	fake-hesperd attach <id> [--ro] [--owner] [--view] [--view-rows R]
//	fake-hesperd tui [--mode agent|flood] [--name X] [--fps N]
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fake-hesperd serve|attach|tui ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "attach":
		os.Exit(runAttach(os.Args[2:]))
	case "tui":
		if err := runTUI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}
