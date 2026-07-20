package main

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

// wolPort is the standard Wake-on-LAN UDP port.
const wolPort = 9

// buildMagicPacket builds the standard Wake-on-LAN payload: 6 bytes of
// 0xFF followed by the target MAC address repeated 16 times (102 bytes).
func buildMagicPacket(macAddr string) ([]byte, error) {
	mac, err := net.ParseMAC(macAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid mac address %q: %w", macAddr, err)
	}
	if len(mac) != 6 {
		return nil, fmt.Errorf("invalid mac address %q: expected 6 bytes, got %d", macAddr, len(mac))
	}

	packet := make([]byte, 0, 6+16*6)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, mac...)
	}
	return packet, nil
}

// sendMagicPacket is a var (not a plain func) so tests can stub it out and
// avoid sending real network traffic. It broadcasts the WOL magic packet
// for macAddr to the local network — fire-and-forget, no delivery or
// wake confirmation is possible (the target PC is, by definition, off and
// can't ack anything).
var sendMagicPacket = func(macAddr string) error {
	packet, err := buildMagicPacket(macAddr)
	if err != nil {
		return err
	}

	// Go's net package does not set SO_BROADCAST by default — without it,
	// writing to a broadcast destination address fails with a permission
	// error on Linux. setSocketBroadcast (wol_unix.go/wol_windows.go)
	// enables it on the raw socket fd before we send.
	lc := net.ListenConfig{Control: func(_, _ string, rc syscall.RawConn) error {
		return setSocketBroadcast(rc)
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		return fmt.Errorf("open UDP socket: %w", err)
	}
	defer pc.Close()

	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", wolPort))
	if err != nil {
		return err
	}
	if _, err := pc.WriteTo(packet, addr); err != nil {
		return fmt.Errorf("send magic packet: %w", err)
	}
	return nil
}
