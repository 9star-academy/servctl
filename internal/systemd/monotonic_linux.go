package systemd

import (
	"syscall"
	"unsafe"
)

// ActiveEnterTimestampMonotonic uses Linux's CLOCK_MONOTONIC in microseconds.
func monotonicMicroseconds() uint64 {
	const clockMonotonic = 1
	var ts syscall.Timespec
	_, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&ts)), 0)
	if errno != 0 {
		return 0
	}
	return uint64(ts.Sec)*1_000_000 + uint64(ts.Nsec)/1_000
}
