package host

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/review"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The review methods (docs/rebuild-contract.md "As built — review"):
// computed here, on the agent's Mac, for a controller's hesperd. Params
// and results are the local socket's; ids may be full or local.
// review.diff with "part" answers in parts (internal/review/parts.go):
// a diff can be larger than one relay message.

func (s *Service) review(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	reg := s.Agents
	switch m.Method {
	case "review.list":
		var p struct{}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		return protocol.JSON(reg.ReviewList(ctx)), nil
	case "review.diff":
		var p struct {
			wire.ReviewDiffParams
			Part *int   `json:"part,omitempty"`
			Tree string `json:"tree,omitempty"`
		}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		a, err := s.agent(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		p.ID = a.ID
		if p.Part == nil {
			d, err := reg.ReviewDiff(ctx, p.ReviewDiffParams)
			if err != nil {
				return nil, publicError(err)
			}
			return protocol.JSON(d), nil
		}
		part, err := s.reviewParts.part(ctx, reg.ReviewDiff, p.ReviewDiffParams, *p.Part, p.Tree)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(part), nil
	}
	return nil, protocol.Err("unsupported", "Unsupported operation: "+m.Method)
}

// partCache keeps the last diffs sent in parts for a while, so the next
// parts need no new diff.
type partCache struct {
	mu      sync.Mutex
	entries []partEntry
}

type partEntry struct {
	key, tree, encoded string
	at                 time.Time
}

const (
	partCacheSize = 4
	partCacheTTL  = 2 * time.Minute
)

// part is part n of the diff p asks for; tree (parts after the first)
// is the diff's tree: a folder that changed since is refused.
func (c *partCache) part(ctx context.Context, diff func(context.Context, wire.ReviewDiffParams) (wire.ReviewDiff, error),
	p wire.ReviewDiffParams, n int, tree string) (review.Part, error) {
	lines := wire.DefaultReviewContext
	if p.Context != nil {
		lines = *p.Context
	}
	key := p.ID + "\x00" + strconv.Itoa(lines)
	c.mu.Lock()
	var hit *partEntry
	for i := range c.entries {
		e := &c.entries[i]
		if e.key == key && time.Since(e.at) < partCacheTTL && (tree == "" || e.tree == tree) && n > 0 {
			hit = e
		}
	}
	var entry partEntry
	if hit != nil {
		entry = *hit
	}
	c.mu.Unlock()
	if hit == nil {
		d, err := diff(ctx, p)
		if err != nil {
			return review.Part{}, err
		}
		if tree != "" && d.Tree != tree {
			return review.Part{}, wire.Errorf(wire.CodeInvalid, "%s's folder changed since the first part: fetch the diff again", p.ID)
		}
		encoded, err := review.Encode(d)
		if err != nil {
			return review.Part{}, err
		}
		entry = partEntry{key: key, tree: d.Tree, encoded: encoded, at: time.Now()}
		c.mu.Lock()
		c.entries = append(c.entries, entry)
		if len(c.entries) > partCacheSize {
			c.entries = c.entries[1:]
		}
		c.mu.Unlock()
	}
	data, parts, ok := review.Cut(entry.encoded, n)
	if !ok {
		return review.Part{}, wire.Errorf(wire.CodeInvalid, "the diff has %d parts", parts)
	}
	if parts > review.MaxParts {
		return review.Part{}, wire.Errorf(wire.CodeInvalid, "%s's diff is too large to send (%d parts)", p.ID, parts)
	}
	return review.Part{Tree: entry.tree, Parts: parts, Part: n, Data: data}, nil
}
