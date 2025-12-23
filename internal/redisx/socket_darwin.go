//go:build darwin

package redisx

import (
	"syscall"
)

// setReceiveBuffer sets the SO_RCVBUF socket option on macOS/Darwin
func setReceiveBuffer(fd int, size int) error {
	return syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, size)
}
