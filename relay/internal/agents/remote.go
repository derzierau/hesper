package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"net"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Remote is how the local daemon reaches other Macs' daemons (part R). A
// remote agent looks like a local one; its id starts with its machine's
// short name. Without a Remote (nil), every call about another machine
// fails with "unavailable".
type Remote interface {
	// Machines are the other machines, for hello.
	Machines() []wire.Machine
	// Agents are the other machines' agents, for agents.list.
	Agents() []wire.Agent
	// Watch reports remote agents' changes (agent) and removals (removed,
	// with the host's reason when it gave one) until ctx ends.
	Watch(ctx context.Context, changed func(wire.Agent), removed func(id, reason string))
	// Call runs a method for a remote agent or machine (agents.input,
	// answer, stop, resume, remove, rename, spawn with machine).
	Call(ctx context.Context, machine, method string, params json.RawMessage) (json.RawMessage, error)
	// Attach serves an attach connection to a remote agent.
	Attach(conn net.Conn, r *bufio.Reader, req wire.AttachRequest)
	// Move hands an agent over to another machine (agents.move).
	Move(ctx context.Context, id, to string) (wire.Agent, error)
	// FilePut starts an attachment upload to machine (files.put with the
	// agent's local id or a draft); FileChunk sends one chunk of an upload
	// it started. Found reports whether the upload is the Remote's.
	FilePut(ctx context.Context, machine string, p FilePut) (FilePutResult, error)
	FileChunk(ctx context.Context, p FileChunkParams) (res FileChunkResult, found bool, err error)
}
