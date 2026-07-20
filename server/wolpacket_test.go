package main

import "testing"

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
func withStubbedSendMagicPacket(t *testing.T, stub func(macAddr string) error, fn func()) {
	t.Helper()
	original := sendMagicPacket
	sendMagicPacket = stub
	t.Cleanup(func() { sendMagicPacket = original })
	fn()
}

func TestCreateWakeCommand_SingleTargetNoMAC(t *testing.T) {
	db := openTestDB(t)
	mustAgentFull(t, db, "agent-1", "agent-1", "", "") // no mac_address

	withStubbedSendMagicPacket(t, func(string) error { return nil }, func() {
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
	mustAgentFull(t, db, "agent-with-mac", "agent-with-mac", "", "")
	mustAgentFull(t, db, "agent-without-mac", "agent-without-mac", "", "")
	if _, err := db.Exec(`UPDATE agents SET mac_address = ? WHERE id = ?`, "AA:BB:CC:DD:EE:FF", "agent-with-mac"); err != nil {
		t.Fatalf("set mac_address: %v", err)
	}

	var sentTo string
	withStubbedSendMagicPacket(t, func(mac string) error { sentTo = mac; return nil }, func() {
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

