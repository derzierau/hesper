package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Attach frame types (docs/rebuild-contract.md).
const (
	frameData   = 0
	frameResize = 1
	frameSize   = 2
	frameExit   = 3
)

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	hdr := make([]byte, 5, 5+len(payload))
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	_, err := w.Write(append(hdr, payload...))
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > 16<<20 {
		return 0, nil, errors.New("frame too large")
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return hdr[0], p, nil
}

func sizePayload(cols, rows int) []byte {
	p := make([]byte, 4)
	binary.BigEndian.PutUint16(p[0:], uint16(cols))
	binary.BigEndian.PutUint16(p[2:], uint16(rows))
	return p
}

func parseSize(p []byte) (int, int, bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint16(p[0:])), int(binary.BigEndian.Uint16(p[2:])), true
}

// socketPath resolves the socket the way the app and the real hesperd do:
// --socket, then $HESPER_SOCKET, then $HESPER_STATE_DIR/hesperd.sock. The
// fake refuses to fall back to the live default state dir.
func socketPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if s := os.Getenv("HESPER_SOCKET"); s != "" {
		return s, nil
	}
	if d := os.Getenv("HESPER_STATE_DIR"); d != "" {
		return filepath.Join(d, "hesperd.sock"), nil
	}
	return "", errors.New("fake-hesperd: set --socket, HESPER_SOCKET or HESPER_STATE_DIR (test-only daemon never uses the live state dir)")
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

func errCode(code, msg string) *rpcError {
	return &rpcError{Code: -32000, Message: msg, Data: map[string]any{"code": code}}
}
