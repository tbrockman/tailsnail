package netplay

import (
	"context"

	"github.com/tbrockman/tailsnail/internal/gossip"
	"github.com/tbrockman/tailsnail/internal/proto"
)

// gossipInitiate is a thin alias so the integration test can drive the dialing
// half of an exchange without importing the gossip package's naming into every
// assertion.
func gossipInitiate(ctx context.Context, c *proto.Conn, r gossip.Recorder) (gossip.Result, error) {
	return gossip.Initiate(ctx, c, r)
}
