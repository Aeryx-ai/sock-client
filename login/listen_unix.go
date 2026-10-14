//go:build !windows

package login

import (
	"context"
	"net"
)

// listen binds addr. Go sets no SO_REUSEPORT on Unix, so no other process
// can bind the same address and port while this listener holds it.
func listen(ctx context.Context, addr string) (net.Listener, error) {
	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp4", addr)
}
