package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Exit codes: scripts and agents branch on them, so they never change
// meaning. 6 is what waiting commands return when their time runs out.
const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitNotFound    = 3
	exitUnavailable = 4
	exitForbidden   = 5
	exitTimeout     = 6
	exitExists      = 7
)

// exitCodes documents them (help, reference), in order.
var exitCodes = []struct {
	Code    int    `json:"code"`
	Name    string `json:"name"`
	Meaning string `json:"meaning"`
}{
	{exitOK, "ok", "Success"},
	{exitError, "error", "Any other error (also daemon code invalid)"},
	{exitUsage, "usage", "Bad command line: unknown command or flag, missing argument, ambiguous name"},
	{exitNotFound, "not_found", "No such agent, machine, project or session"},
	{exitUnavailable, "unavailable", "hesperd is not running, or a machine is offline or unreachable (codes unavailable, offline, remote)"},
	{exitForbidden, "forbidden", "Not allowed (codes forbidden, unauthorized)"},
	{exitTimeout, "timeout", "Waited too long"},
	{exitExists, "exists", "Already exists or in use (codes exists, live, busy, processes)"},
}

// Error codes the CLI adds to the daemon's (wire.Code*).
const (
	codeUsage     = "usage"
	codeError     = "error"
	codeTimeout   = "timeout"
	codeAmbiguous = "ambiguous"
)

// codeExits maps an error code (the daemon's data.code, the relay's or
// the CLI's own) to its exit code; others exit 1.
var codeExits = map[string]int{
	codeUsage: exitUsage, codeAmbiguous: exitUsage,
	wire.CodeNotFound:    exitNotFound,
	wire.CodeUnavailable: exitUnavailable, wire.CodeOffline: exitUnavailable, wire.CodeRemote: exitUnavailable,
	"connection_lost": exitUnavailable, "disconnected": exitUnavailable,
	wire.CodeForbidden: exitForbidden, "unauthorized": exitForbidden,
	codeTimeout:     exitTimeout,
	wire.CodeExists: exitExists, wire.CodeLive: exitExists, wire.CodeBusy: exitExists, wire.CodeProcesses: exitExists,
}

// cliError is an error with a code (printed with --json, mapped to the
// exit code).
type cliError struct {
	Code    string
	Message string
}

func (e *cliError) Error() string { return e.Message }

// failf makes an error with a code: failf(codeTimeout, "…") exits 6.
func failf(code, format string, args ...any) error {
	return &cliError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// usagef is a usage error (exit 2).
func usagef(format string, args ...any) error { return failf(codeUsage, format, args...) }

// errorCode is err's code: the CLI's, the daemon's or the relay's.
func errorCode(err error) string {
	var ce *cliError
	var we *wire.Error
	var pe *protocol.Error
	switch {
	case errors.As(err, &ce):
		return ce.Code
	case errors.As(err, &we):
		return we.Code
	case errors.Is(err, errNoDaemon):
		return wire.CodeUnavailable
	case errors.As(err, &pe):
		return pe.Code
	case errors.Is(err, context.DeadlineExceeded):
		return codeTimeout
	}
	return codeError
}

// exitCode is the process exit code for err.
func exitCode(err error) int {
	if err == nil {
		return exitOK
	}
	if code, ok := codeExits[errorCode(err)]; ok {
		return code
	}
	return exitError
}

// printError writes err to w: its message, or with asJSON
// {"error":{"code","message"[,"agentId"]}}.
func printError(w io.Writer, err error, asJSON bool) {
	if !asJSON {
		fmt.Fprintln(w, err)
		return
	}
	body := map[string]string{"code": errorCode(err), "message": err.Error()}
	var we *wire.Error
	if errors.As(err, &we) && we.AgentID != "" {
		body["agentId"] = we.AgentID
	}
	data, _ := json.Marshal(map[string]any{"error": body})
	fmt.Fprintln(w, string(data))
}
