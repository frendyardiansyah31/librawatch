package main

import (
	"bytes"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildMagicPacket(t *testing.T) {
	cases := []string{"AA:BB:CC:DD:EE:FF", "aa-bb-cc-dd-ee-ff"}
	wantMAC := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}

	for _, mac := range cases {
		packet, err := buildMagicPacket(mac)
		if err != nil {
			t.Fatalf("buildMagicPacket(%q): %v", mac, err)
		}
		if len(packet) != 102 {
			t.Fatalf("buildMagicPacket(%q): len = %d, want 102", mac, len(packet))
		}
		for i := 0; i < 6; i++ {
			if packet[i] != 0xFF {
				t.Errorf("buildMagicPacket(%q): byte %d = %#x, want 0xFF", mac, i, packet[i])
			}
		}
		for rep := 0; rep < 16; rep++ {
			off := 6 + rep*6
			for j := 0; j < 6; j++ {
				if packet[off+j] != wantMAC[j] {
					t.Fatalf("buildMagicPacket(%q): repetition %d byte %d = %#x, want %#x",
						mac, rep, j, packet[off+j], wantMAC[j])
				}
			}
		}
	}
}

func TestBuildMagicPacket_InvalidMAC(t *testing.T) {
	badMACs := []string{"", "not-a-mac", "AA:BB:CC:DD:EE", "AA:BB:CC:DD:EE:FF:00"}
	for _, mac := range badMACs {
		if _, err := buildMagicPacket(mac); err == nil {
			t.Errorf("buildMagicPacket(%q): expected error, got nil", mac)
		}
	}
}

// ── createWakeCommand ────────────────────────────────────────────────────────

// withStubbedSendMagicPacket swaps sendMagicPacket for the duration of fn,
// so tests never send real UDP traffic.
func withStubbedSendMagicPacket(t *testing.T, stub func(macAddr, broadcastAddr string, port int) error, fn func()) {
	t.Helper()
	original := sendMagicPacket
	sendMagicPacket = stub
	t.Cleanup(func() { sendMagicPacket = original })
	fn()
}

// setWoLSettings seeds the settings table directly (bypassing
// InitDefaultSettings/config.yaml) so tests can set up whatever WoL config
// state they need in isolation.
func setWoLSettings(t *testing.T, db *DB, enabled bool, port int, networks []WoLNetwork) {
	t.Helper()
	if err := db.SetSetting("wol_enabled", strconv.FormatBool(enabled)); err != nil {
		t.Fatalf("set wol_enabled: %v", err)
	}
	if err := db.SetSetting("wol_port", strconv.Itoa(port)); err != nil {
		t.Fatalf("set wol_port: %v", err)
	}
	networksJSON, err := json.Marshal(networks)
	if err != nil {
		t.Fatalf("marshal networks: %v", err)
	}
	if err := db.SetSetting("wol_networks", string(networksJSON)); err != nil {
		t.Fatalf("set wol_networks: %v", err)
	}
}

// setAgentIP sets an agent's stored IP address directly, same pattern the
// existing tests already use for mac_address.
func setAgentIP(t *testing.T, db *DB, id, ip string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE agents SET ip = ? WHERE id = ?`, ip, id); err != nil {
		t.Fatalf("set ip: %v", err)
	}
}

var libraryNetwork = []WoLNetwork{{Name: "library", Subnet: "10.5.39.0/25"}}
var multiSubnetNetworks = []WoLNetwork{
	{Name: "library", Subnet: "10.5.39.0/25"},
	{Name: "office", Subnet: "172.16.4.0/22"},
}

func TestCreateWakeCommand_SingleTargetNoMAC(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 9, libraryNetwork)
	mustAgentFull(t, db, "agent-1", "agent-1", "", "") // no mac_address

	withStubbedSendMagicPacket(t, func(string, string, int) error { return nil }, func() {
		_, err := createWakeCommand(db, []string{"agent-1"}, "test")
		if err == nil {
			t.Fatal("expected commandClientError for agent with no MAC address, got nil")
		}
		clientErr, ok := err.(*commandClientError)
		if !ok || clientErr.Status != 400 {
			t.Fatalf("expected 400 commandClientError, got %v (%T)", err, err)
		}
	})
}

func TestCreateWakeCommand_MultiTargetPartialFailure(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 9, libraryNetwork)
	mustAgentFull(t, db, "agent-with-mac", "agent-with-mac", "", "")
	mustAgentFull(t, db, "agent-without-mac", "agent-without-mac", "", "")
	if _, err := db.Exec(`UPDATE agents SET mac_address = ? WHERE id = ?`, "AA:BB:CC:DD:EE:FF", "agent-with-mac"); err != nil {
		t.Fatalf("set mac_address: %v", err)
	}
	setAgentIP(t, db, "agent-with-mac", "10.5.39.84")
	setAgentIP(t, db, "agent-without-mac", "10.5.39.85")

	var sentTo string
	withStubbedSendMagicPacket(t, func(mac, _ string, _ int) error { sentTo = mac; return nil }, func() {
		job, err := createWakeCommand(db, []string{"agent-with-mac", "agent-without-mac"}, "test")
		if err != nil {
			t.Fatalf("createWakeCommand: %v", err)
		}
		if job.Status != "done" {
			t.Errorf("job.Status = %q, want done", job.Status)
		}
		if sentTo != "AA:BB:CC:DD:EE:FF" {
			t.Errorf("sendMagicPacket called with %q, want AA:BB:CC:DD:EE:FF", sentTo)
		}

		results, err := db.GetDeployResultsByJobID(job.ID)
		if err != nil {
			t.Fatalf("GetDeployResultsByJobID: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(results))
		}
		byAgent := map[string]DeployResult{}
		for _, r := range results {
			byAgent[r.AgentID] = r
		}
		if byAgent["agent-with-mac"].Status != "success" {
			t.Errorf("agent-with-mac status = %q, want success", byAgent["agent-with-mac"].Status)
		}
		if byAgent["agent-without-mac"].Status != "failed" {
			t.Errorf("agent-without-mac status = %q, want failed", byAgent["agent-without-mac"].Status)
		}
	})
}

// ── getWoLConfig ─────────────────────────────────────────────────────────────

func TestGetWoLConfig_Defaults(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 9, libraryNetwork)

	cfg, err := getWoLConfig(db)
	if err != nil {
		t.Fatalf("getWoLConfig: %v", err)
	}
	if !cfg.Enabled || cfg.Port != 9 || len(cfg.Networks) != 1 || cfg.Networks[0].Subnet != "10.5.39.0/25" {
		t.Errorf("getWoLConfig = %+v, want {true 9 [{library 10.5.39.0/25}]}", cfg)
	}
}

func TestGetWoLConfig_InvalidNetworks(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 9, []WoLNetwork{{Name: "bad", Subnet: "not-a-cidr"}})

	if _, err := getWoLConfig(db); err == nil {
		t.Fatal("getWoLConfig: expected error for invalid network CIDR, got nil")
	}
}

func TestGetWoLConfig_InvalidPort(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 70000, libraryNetwork)

	if _, err := getWoLConfig(db); err == nil {
		t.Fatal("getWoLConfig: expected error for out-of-range port, got nil")
	}
}

// ── CIDR broadcast calculation ───────────────────────────────────────────────

func TestCIDRBroadcast_Slash25(t *testing.T) {
	n, err := parseWoLSubnet("10.5.39.0/25")
	if err != nil {
		t.Fatalf("parseWoLSubnet: %v", err)
	}
	if got := cidrBroadcast(n).String(); got != "10.5.39.127" {
		t.Errorf("cidrBroadcast(10.5.39.0/25) = %s, want 10.5.39.127", got)
	}
}

func TestCIDRBroadcast_Slash22(t *testing.T) {
	// Mandatory case: /22 has a block size of 4 in the third octet, so this
	// must not be treated like a /24 (which would wrongly give .4.255).
	n, err := parseWoLSubnet("172.16.4.0/22")
	if err != nil {
		t.Fatalf("parseWoLSubnet: %v", err)
	}
	if got := cidrBroadcast(n).String(); got != "172.16.7.255" {
		t.Errorf("cidrBroadcast(172.16.4.0/22) = %s, want 172.16.7.255", got)
	}
}

func TestParseWoLSubnet_MisalignedBoundary(t *testing.T) {
	_, err := parseWoLSubnet("172.16.5.0/22")
	if err == nil {
		t.Fatal("parseWoLSubnet(172.16.5.0/22): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "172.16.4.0/22") {
		t.Errorf("parseWoLSubnet(172.16.5.0/22) error = %q, want it to name 172.16.4.0/22", err.Error())
	}
}

// ── findWoLNetwork / resolveWoLBroadcast ─────────────────────────────────────

func TestFindWoLNetwork_MatchesSlash22(t *testing.T) {
	n, err := findWoLNetwork("172.16.5.20", multiSubnetNetworks)
	if err != nil {
		t.Fatalf("findWoLNetwork: %v", err)
	}
	if got := n.String(); got != "172.16.4.0/22" {
		t.Errorf("findWoLNetwork(172.16.5.20) matched %s, want 172.16.4.0/22", got)
	}
	broadcast, err := resolveWoLBroadcast(multiSubnetNetworks, "172.16.5.20")
	if err != nil {
		t.Fatalf("resolveWoLBroadcast: %v", err)
	}
	if broadcast != "172.16.7.255" {
		t.Errorf("resolveWoLBroadcast(172.16.5.20) = %s, want 172.16.7.255", broadcast)
	}
}

func TestFindWoLNetwork_BoundaryInsideSlash22(t *testing.T) {
	broadcast, err := resolveWoLBroadcast(multiSubnetNetworks, "172.16.7.100")
	if err != nil {
		t.Fatalf("resolveWoLBroadcast: %v", err)
	}
	if broadcast != "172.16.7.255" {
		t.Errorf("resolveWoLBroadcast(172.16.7.100) = %s, want 172.16.7.255", broadcast)
	}
}

func TestFindWoLNetwork_OutsideSlash22(t *testing.T) {
	if _, err := findWoLNetwork("172.16.8.10", multiSubnetNetworks); err == nil {
		t.Fatal("findWoLNetwork(172.16.8.10): expected no match against 172.16.4.0/22, got a match")
	}
}

func TestFindWoLNetwork_NoMatch(t *testing.T) {
	_, err := findWoLNetwork("192.168.1.20", multiSubnetNetworks)
	if err == nil {
		t.Fatal("findWoLNetwork(192.168.1.20): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "192.168.1.20") {
		t.Errorf("findWoLNetwork error = %q, want it to mention the IP", err.Error())
	}
}

func TestFindWoLNetwork_InvalidIP(t *testing.T) {
	cases := []string{"", "not-an-ip", "999.999.999.999"}
	for _, ip := range cases {
		if _, err := findWoLNetwork(ip, multiSubnetNetworks); err == nil {
			t.Errorf("findWoLNetwork(%q): expected error, got nil", ip)
		}
	}
}

// ── validateWoLNetworks ──────────────────────────────────────────────────────

func TestValidateWoLNetworks_InvalidCIDR(t *testing.T) {
	cases := []string{"172.16.5.0/33", "10.5.39.0/999", "invalid"}
	for _, subnet := range cases {
		err := validateWoLNetworks([]WoLNetwork{{Name: "n", Subnet: subnet}})
		if err == nil {
			t.Errorf("validateWoLNetworks(%q): expected error, got nil", subnet)
		}
	}
}

func TestValidateWoLNetworks_MisalignedBoundary(t *testing.T) {
	err := validateWoLNetworks([]WoLNetwork{{Name: "office", Subnet: "172.16.5.0/22"}})
	if err == nil {
		t.Fatal("validateWoLNetworks(172.16.5.0/22): expected error, got nil")
	}
}

func TestValidateWoLNetworks_Duplicate(t *testing.T) {
	err := validateWoLNetworks([]WoLNetwork{
		{Name: "a", Subnet: "10.5.39.0/25"},
		{Name: "b", Subnet: "10.5.39.0/25"},
	})
	if err == nil {
		t.Fatal("validateWoLNetworks: expected error for duplicate subnet, got nil")
	}
}

func TestValidateWoLNetworks_Overlap(t *testing.T) {
	err := validateWoLNetworks([]WoLNetwork{
		{Name: "a", Subnet: "10.5.39.0/25"},
		{Name: "b", Subnet: "10.5.39.0/24"},
	})
	if err == nil {
		t.Fatal("validateWoLNetworks: expected error for overlapping subnets, got nil")
	}
}

// ── sendMagicPacket (real, unstubbed) ───────────────────────────────────────

// TestSendMagicPacket_WoLUsesConfiguredDestination proves sendMagicPacket
// actually sends to the broadcastAddr/port it's given, not a hardcoded
// 255.255.255.255:9 — sends to a real loopback listener bound on the
// configured address/port and asserts the exact magic-packet bytes arrive
// there. If this ever regresses to a hardcoded destination, the listener
// below (bound on 127.0.0.1) receives nothing and the test times out.
func TestSendMagicPacket_WoLUsesConfiguredDestination(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port

	const mac = "AA:BB:CC:DD:EE:FF"
	sendErrCh := make(chan error, 1)
	go func() { sendErrCh <- sendMagicPacket(mac, "127.0.0.1", port) }()

	buf := make([]byte, 256)
	pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("no packet received at configured destination 127.0.0.1:%d: %v", port, err)
	}
	if err := <-sendErrCh; err != nil {
		t.Fatalf("sendMagicPacket: %v", err)
	}

	want, err := buildMagicPacket(mac)
	if err != nil {
		t.Fatalf("buildMagicPacket: %v", err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Errorf("received packet does not match expected magic packet")
	}
}

// ── createWakeCommand + WoL settings ────────────────────────────────────────

func TestCreateWakeCommand_WoLDisabled(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, false, 9, libraryNetwork)
	mustAgentFull(t, db, "agent-1", "agent-1", "", "")
	if _, err := db.Exec(`UPDATE agents SET mac_address = ? WHERE id = ?`, "AA:BB:CC:DD:EE:FF", "agent-1"); err != nil {
		t.Fatalf("set mac_address: %v", err)
	}

	called := false
	withStubbedSendMagicPacket(t, func(string, string, int) error { called = true; return nil }, func() {
		_, err := createWakeCommand(db, []string{"agent-1"}, "test")
		if err == nil {
			t.Fatal("expected commandClientError when WoL is disabled, got nil")
		}
		clientErr, ok := err.(*commandClientError)
		if !ok || clientErr.Status != 400 {
			t.Fatalf("expected 400 commandClientError, got %v (%T)", err, err)
		}
	})
	if called {
		t.Error("sendMagicPacket was called despite WoL being disabled")
	}
}

func TestCreateWakeCommand_WoLUsesConfiguredDestination(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 9, libraryNetwork)
	mustAgentFull(t, db, "agent-1", "agent-1", "", "")
	if _, err := db.Exec(`UPDATE agents SET mac_address = ? WHERE id = ?`, "AA:BB:CC:DD:EE:FF", "agent-1"); err != nil {
		t.Fatalf("set mac_address: %v", err)
	}
	setAgentIP(t, db, "agent-1", "10.5.39.84")

	var gotBroadcast string
	var gotPort int
	withStubbedSendMagicPacket(t, func(_, broadcastAddr string, port int) error {
		gotBroadcast, gotPort = broadcastAddr, port
		return nil
	}, func() {
		if _, err := createWakeCommand(db, []string{"agent-1"}, "test"); err != nil {
			t.Fatalf("createWakeCommand: %v", err)
		}
	})
	if gotBroadcast != "10.5.39.127" || gotPort != 9 {
		t.Errorf("sendMagicPacket destination = %s:%d, want 10.5.39.127:9", gotBroadcast, gotPort)
	}
}

func TestCreateWakeCommand_WoLNoMatchingNetwork(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 9, libraryNetwork)
	mustAgentFull(t, db, "agent-1", "agent-1", "", "")
	if _, err := db.Exec(`UPDATE agents SET mac_address = ? WHERE id = ?`, "AA:BB:CC:DD:EE:FF", "agent-1"); err != nil {
		t.Fatalf("set mac_address: %v", err)
	}
	setAgentIP(t, db, "agent-1", "192.168.1.20") // outside the configured "library" network

	withStubbedSendMagicPacket(t, func(string, string, int) error { return nil }, func() {
		_, err := createWakeCommand(db, []string{"agent-1"}, "test")
		if err == nil {
			t.Fatal("expected commandClientError for IP with no matching network, got nil")
		}
		clientErr, ok := err.(*commandClientError)
		if !ok || clientErr.Status != 400 {
			t.Fatalf("expected 400 commandClientError, got %v (%T)", err, err)
		}
		if !strings.Contains(clientErr.Message, "No Wake-on-LAN network configuration matches PC IP") {
			t.Errorf("error message = %q, want it to mention no matching network", clientErr.Message)
		}
	})
}

func TestCreateWakeCommand_WoLMissingIP(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 9, libraryNetwork)
	mustAgentFull(t, db, "agent-1", "agent-1", "", "")
	if _, err := db.Exec(`UPDATE agents SET mac_address = ? WHERE id = ?`, "AA:BB:CC:DD:EE:FF", "agent-1"); err != nil {
		t.Fatalf("set mac_address: %v", err)
	}
	setAgentIP(t, db, "agent-1", "")

	withStubbedSendMagicPacket(t, func(string, string, int) error { return nil }, func() {
		_, err := createWakeCommand(db, []string{"agent-1"}, "test")
		if err == nil {
			t.Fatal("expected commandClientError for missing IP, got nil")
		}
		clientErr, ok := err.(*commandClientError)
		if !ok || clientErr.Status != 400 {
			t.Fatalf("expected 400 commandClientError, got %v (%T)", err, err)
		}
		if !strings.Contains(clientErr.Message, "Cannot determine Wake-on-LAN network") {
			t.Errorf("error message = %q, want it to mention no valid IP", clientErr.Message)
		}
	})
}

// TestCreateWakeCommand_WoLMultiSubnetTargets is the key regression guard
// for per-agent (not job-global) broadcast resolution: two agents on two
// different configured subnets in the same wake request must each get their
// own subnet's broadcast address.
func TestCreateWakeCommand_WoLMultiSubnetTargets(t *testing.T) {
	db := openTestDB(t)
	setWoLSettings(t, db, true, 9, multiSubnetNetworks)
	mustAgentFull(t, db, "agent-library", "agent-library", "", "")
	mustAgentFull(t, db, "agent-office", "agent-office", "", "")
	if _, err := db.Exec(`UPDATE agents SET mac_address = ? WHERE id = ?`, "AA:BB:CC:DD:EE:01", "agent-library"); err != nil {
		t.Fatalf("set mac_address: %v", err)
	}
	if _, err := db.Exec(`UPDATE agents SET mac_address = ? WHERE id = ?`, "AA:BB:CC:DD:EE:02", "agent-office"); err != nil {
		t.Fatalf("set mac_address: %v", err)
	}
	setAgentIP(t, db, "agent-library", "10.5.39.84")
	setAgentIP(t, db, "agent-office", "172.16.5.20")

	gotBroadcast := map[string]string{}
	withStubbedSendMagicPacket(t, func(mac, broadcastAddr string, _ int) error {
		gotBroadcast[mac] = broadcastAddr
		return nil
	}, func() {
		if _, err := createWakeCommand(db, []string{"agent-library", "agent-office"}, "test"); err != nil {
			t.Fatalf("createWakeCommand: %v", err)
		}
	})
	if gotBroadcast["AA:BB:CC:DD:EE:01"] != "10.5.39.127" {
		t.Errorf("library agent broadcast = %q, want 10.5.39.127", gotBroadcast["AA:BB:CC:DD:EE:01"])
	}
	if gotBroadcast["AA:BB:CC:DD:EE:02"] != "172.16.7.255" {
		t.Errorf("office agent broadcast = %q, want 172.16.7.255", gotBroadcast["AA:BB:CC:DD:EE:02"])
	}
}

// ── InitDefaultSettings precedence ──────────────────────────────────────────

// TestInitDefaultSettings_WoLNetworksSeedFromConfig proves the documented
// precedence: config.yaml only seeds wol_networks when the DB key is
// absent, and never overwrites a value that already exists.
func TestInitDefaultSettings_WoLNetworksSeedFromConfig(t *testing.T) {
	db := openTestDB(t)

	cfg1 := &Config{}
	cfg1.WoL.Enabled = true
	cfg1.WoL.Port = 9
	cfg1.WoL.Networks = libraryNetwork
	if err := db.InitDefaultSettings(cfg1); err != nil {
		t.Fatalf("InitDefaultSettings (first): %v", err)
	}

	got, _ := db.GetSetting("wol_networks")
	var gotNetworks []WoLNetwork
	if err := json.Unmarshal([]byte(got), &gotNetworks); err != nil || len(gotNetworks) != 1 || gotNetworks[0].Subnet != "10.5.39.0/25" {
		t.Fatalf("wol_networks after first seed = %q, want it to decode to %v", got, libraryNetwork)
	}

	cfg2 := &Config{}
	cfg2.WoL.Enabled = false
	cfg2.WoL.Port = 7
	cfg2.WoL.Networks = []WoLNetwork{{Name: "different", Subnet: "192.168.1.0/24"}}
	if err := db.InitDefaultSettings(cfg2); err != nil {
		t.Fatalf("InitDefaultSettings (second): %v", err)
	}

	got, _ = db.GetSetting("wol_networks")
	gotNetworks = nil
	if err := json.Unmarshal([]byte(got), &gotNetworks); err != nil || len(gotNetworks) != 1 || gotNetworks[0].Subnet != "10.5.39.0/25" {
		t.Errorf("wol_networks after second seed = %q, want unchanged (config.yaml must not override an existing DB setting)", got)
	}
	gotEnabled, _ := db.GetSetting("wol_enabled")
	if gotEnabled != "true" {
		t.Errorf("wol_enabled after second seed = %q, want unchanged true", gotEnabled)
	}
	gotPort, _ := db.GetSetting("wol_port")
	if gotPort != "9" {
		t.Errorf("wol_port after second seed = %q, want unchanged 9", gotPort)
	}
}
