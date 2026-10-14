//go:build unix

package client

import (
	"os"
	"syscall"
)

// statDevice is the id of the device path is on.
func statDevice(path string) (uint64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true // Dev is int32 on darwin, uint64 on linux
}
