//go:build !linux

package systemd

// The CLI only queries systemd on Linux.
func monotonicMicroseconds() uint64 {
	return 0
}
