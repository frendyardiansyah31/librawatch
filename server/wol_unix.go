//go:build !windows

package main

import "syscall"

// setSocketBroadcast enables SO_BROADCAST on the UDP socket so the OS
// permits sending to a broadcast destination address — required on Linux
// (EACCES otherwise); see sendMagicPacket in wolpacket.go.
func setSocketBroadcast(rc syscall.RawConn) error {
	var sockErr error
	if err := rc.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
