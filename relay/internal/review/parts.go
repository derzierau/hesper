package review

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// A review diff can be larger than one relay message (1 MiB): a host
// sends it to a controller's hesperd in parts, the JSON gzipped, base64,
// cut into PartSize pieces (review.diff with "part").

// PartSize is the most base64 text one part carries.
const PartSize = 384 << 10

// MaxParts bounds a diff in parts (PartSize × MaxParts, compressed).
const MaxParts = 256

// Part is one part of a value: Data is the Part-th piece of Parts.
type Part struct {
	Tree  string `json:"tree"`
	Parts int    `json:"parts"`
	Part  int    `json:"part"`
	Data  string `json:"data"`
}

// Encode is v as JSON, gzipped, base64.
func Encode(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	z.Write(raw)
	if err := z.Close(); err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes()), nil
}

// Cut is part n of encoded; ok false past the last.
func Cut(encoded string, n int) (data string, parts int, ok bool) {
	parts = max(1, (len(encoded)+PartSize-1)/PartSize)
	if n < 0 || n >= parts {
		return "", parts, false
	}
	end := min(len(encoded), (n+1)*PartSize)
	return encoded[n*PartSize : end], parts, true
}

// Decode reads the parts' data back into v.
func Decode(data []string, v any) error {
	var all bytes.Buffer
	for _, d := range data {
		all.WriteString(d)
	}
	raw, err := base64.StdEncoding.DecodeString(all.String())
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(z, 1<<30))
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return json.Unmarshal(body, v)
}
