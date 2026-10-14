//go:build windows

package login

import (
	"context"
	"net"
	"syscall"
)

// soExclusiveAddrUse is SO_EXCLUSIVEADDRUSE, ^SO_REUSEADDR in winsock.
const soExclusiveAddrUse = ^4

// listen binds addr with SO_EXCLUSIVEADDRUSE so no other process can bind
// the same address and port while this listener holds it.
func listen(ctx context.Context, addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, soExclusiveAddrUse, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(ctx, "tcp4", addr)
}
