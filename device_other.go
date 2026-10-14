//go:build !unix

package client

// statDevice knows no device ids here: the sweep falls back to the parent
// directory checks alone.
func statDevice(string) (uint64, bool) { return 0, false }
