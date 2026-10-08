package client

import (
	"context"
	"encoding/json"

	"github.com/derzierau/hesper/relay/pkg/session"
)

// Snapshot requests a host's current relay-visible snapshot, rather than
// returning cached metadata.
func (c *Controller) Snapshot(ctx context.Context, machineID string) (session.Snapshot, error) {
	var result session.Snapshot
	raw, err := c.Request(ctx, machineID, "snapshot", struct{}{})
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}
