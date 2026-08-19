package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"syscall"
)

// wolPort is the standard Wake-on-LAN UDP port, used as a last-resort
// fallback in getWoLConfig if the settings table somehow has an empty value
// (normally unreachable — InitDefaultSettings always seeds it).
const wolPort = 9

// WoLNetwork is one admin-configured network profile — just a name and a
// CIDR subnet. The broadcast address is never stored: it's always derived
// from the subnet at match time (parseWoLSubnet/cidrBroadcast), so a typo
// like 172.16.5.255 for a 172.16.4.0/22 network can't happen. Used both for
// config.yaml (yaml tags) and for JSON persistence in the settings table /
// dashboard payloads (json tags) — one struct, no duplication.
type WoLNetwork struct {
	Name   string `yaml:"name" json:"name"`
	Subnet string `yaml:"subnet" json:"subnet"`
}

// WoLConfig is the global (not per-PC) Wake-on-LAN configuration — see
// settings keys wol_enabled/wol_port/wol_networks.
type WoLConfig struct {
	Enabled  bool
	Port     int
	Networks []WoLNetwork
}

func validateWoLPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid Wake-on-LAN port %d: must be between 1 and 65535", port)
	}
	return nil
}

// parseWoLSubnet parses subnet as a CIDR and rejects it unless the address
// portion is already the network address for that prefix length — e.g.
// 172.16.5.0/22 is rejected (block size 4 in the third octet means the real
// network boundary is 172.16.4.0/22), 172.16.4.0/22 is accepted.
func parseWoLSubnet(subnet string) (*net.IPNet, error) {
	ip, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", subnet, err)
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("invalid CIDR %q: must be IPv4", subnet)
	}
	if !ip.Equal(ipnet.IP) {
		return nil, fmt.Errorf("invalid network address %q: not aligned to its subnet boundary, expected %s", subnet, ipnet.String())
	}
	return ipnet, nil
}

// cidrBroadcast computes the IPv4 broadcast address for a network: the
// network address with every host bit set to 1.
func cidrBroadcast(n *net.IPNet) net.IP {
	ip4 := n.IP.To4()
	mask := n.Mask
	bcast := make(net.IP, len(ip4))
	for i := range ip4 {
		bcast[i] = ip4[i] | ^mask[i]
	}
	return bcast
}

// validateWoLNetworks checks every network profile's name/CIDR, then
// rejects duplicate or overlapping subnets. Shared by config loading and
// POST /api/settings so both paths enforce the same rules.
func validateWoLNetworks(networks []WoLNetwork) error {
	nets := make([]*net.IPNet, 0, len(networks))
	for _, n := range networks {
		if n.Name == "" {
			return fmt.Errorf("Wake-on-LAN network profile is missing a name")
		}
		ipnet, err := parseWoLSubnet(n.Subnet)
		if err != nil {
			return err
		}
		nets = append(nets, ipnet)
	}
	for i := 0; i < len(nets); i++ {
		for j := i + 1; j < len(nets); j++ {
			if nets[i].String() == nets[j].String() {
				return fmt.Errorf("duplicate Wake-on-LAN network %s", nets[i].String())
			}
			if nets[i].Contains(nets[j].IP) || nets[j].Contains(nets[i].IP) {
				return fmt.Errorf("overlapping Wake-on-LAN networks %s and %s", nets[i].String(), nets[j].String())
			}
		}
	}
	return nil
}

// findWoLNetwork returns the configured network whose subnet contains ip,
// using real CIDR containment (net.IPNet.Contains) — never string-prefix
// matching, which would get /22-style boundaries wrong.
func findWoLNetwork(ipStr string, networks []WoLNetwork) (*net.IPNet, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, fmt.Errorf("Cannot determine Wake-on-LAN network: PC has no valid IP address.")
	}
	for _, n := range networks {
		ipnet, err := parseWoLSubnet(n.Subnet)
		if err != nil {
			continue // already validated at save time; skip defensively rather than fail the whole match
		}
		if ipnet.Contains(ip) {
			return ipnet, nil
		}
	}
	return nil, fmt.Errorf("No Wake-on-LAN network configuration matches PC IP %s", ipStr)
}

// resolveWoLBroadcast finds the configured network containing ip and
// returns its calculated broadcast address — the one thing createWakeCommand
// needs per target agent.
func resolveWoLBroadcast(networks []WoLNetwork, ip string) (string, error) {
	ipnet, err := findWoLNetwork(ip, networks)
	if err != nil {
		return "", err
	}
	return cidrBroadcast(ipnet).String(), nil
}

// getWoLConfig reads the global WoL configuration from the settings table
// (seeded from config.yaml on first run by InitDefaultSettings, editable
// afterwards via the dashboard's Settings > Wake-on-LAN form / POST
// /api/settings — settings table values always take precedence over
// config.yaml once seeded).
func getWoLConfig(db *DB) (WoLConfig, error) {
	settings, err := db.GetAllSettings()
	if err != nil {
		return WoLConfig{}, err
	}

	cfg := WoLConfig{
		Enabled: settings["wol_enabled"] == "true",
		Port:    wolPort,
	}
	if p, err := strconv.Atoi(settings["wol_port"]); err == nil && p != 0 {
		cfg.Port = p
	}
	if raw := settings["wol_networks"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Networks); err != nil {
			return WoLConfig{}, fmt.Errorf("invalid wol_networks setting: %w", err)
		}
	}

	if err := validateWoLPort(cfg.Port); err != nil {
		return WoLConfig{}, err
	}
	if err := validateWoLNetworks(cfg.Networks); err != nil {
		return WoLConfig{}, err
	}
	return cfg, nil
}

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
// avoid sending real network traffic. It sends the WOL magic packet for
// macAddr to broadcastAddr:port — fire-and-forget, no delivery or wake
// confirmation is possible (the target PC is, by definition, off and can't
// ack anything). broadcastAddr/port come from getWoLConfig — never
// hardcoded, since 255.255.255.255 doesn't reach targets on networks where
// the router/switch requires the actual subnet broadcast address (e.g. a
// /25 network's broadcast is the subnet's last address, not
// 255.255.255.255).
var sendMagicPacket = func(macAddr, broadcastAddr string, port int) error {
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

	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", broadcastAddr, port))
	if err != nil {
		return err
	}
	if _, err := pc.WriteTo(packet, addr); err != nil {
		return fmt.Errorf("send magic packet: %w", err)
	}
	return nil
}
