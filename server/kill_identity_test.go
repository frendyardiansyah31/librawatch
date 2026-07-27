package main

import (
	"encoding/json"
	"testing"
	"time"
)

// Positive: KillProcessByIdentity must put the triggering process's PID on
// the wire (kill_by_identity message) — Priority 3's whole point is that the
// agent kills the exact instance that triggered the policy decision,
// verified live by PID, instead of only ever matching by name.
func TestHub_KillProcessByIdentity_SendsPID(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	hub := NewHub(db)
	agentID := "agent-kill-identity-test"

	client := &Client{agentID: agentID, send: make(chan []byte, 1)}
	hub.addClient(client)

	const wantPID = 4321

	// Act — run in a goroutine since KillProcessByIdentity blocks on a reply.
	resultCh := make(chan struct {
		output string
		err    error
	}, 1)
	go func() {
		output, err := hub.KillProcessByIdentity(agentID, "Zoom.exe", "Zoom Video Communications", wantPID)
		resultCh <- struct {
			output string
			err    error
		}{output, err}
	}()

	// Assert — inspect the actual bytes sent to the agent.
	select {
	case data := <-client.send:
		var msg OutgoingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal sent message: %v", err)
		}
		if msg.Type != "kill_by_identity" {
			t.Errorf("Type = %q, want %q", msg.Type, "kill_by_identity")
		}
		if msg.ProcName != "Zoom.exe" {
			t.Errorf("ProcName = %q, want %q", msg.ProcName, "Zoom.exe")
		}
		if msg.Company != "Zoom Video Communications" {
			t.Errorf("Company = %q, want %q", msg.Company, "Zoom Video Communications")
		}
		if msg.PID != wantPID {
			t.Errorf("PID = %d, want %d", msg.PID, wantPID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for kill_by_identity message to be sent")
	}

	// Unblock the waiting goroutine so the test doesn't hang for the full
	// 10s timeout — simulate the agent's kill_result reply.
	if ch, ok := hub.killWaiters.Load(agentID); ok {
		ch.(chan string) <- "kill_by_identity: name=Zoom.exe company=\"Zoom Video Communications\" pid=4321 killed=true (targeted)"
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Errorf("KillProcessByIdentity returned error: %v", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for KillProcessByIdentity to return")
	}
}

// Negative: pid=0 (unknown) must be omitted from the wire message entirely
// (OutgoingMessage.PID has `omitempty`), so the agent's floatVal(msg["pid"])
// reads it as 0 and falls back to the name-based sweep — not a literal "0".
func TestHub_KillProcessByIdentity_OmitsZeroPID(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	hub := NewHub(db)
	agentID := "agent-kill-identity-test-zero-pid"

	client := &Client{agentID: agentID, send: make(chan []byte, 1)}
	hub.addClient(client)

	go func() { _, _ = hub.KillProcessByIdentity(agentID, "notepad.exe", "", 0) }()

	// Act / Assert
	select {
	case data := <-client.send:
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal sent message: %v", err)
		}
		if _, present := raw["pid"]; present {
			t.Errorf(`"pid" key present in JSON for pid=0, want omitted: %s`, data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for kill_by_identity message to be sent")
	}

	if ch, ok := hub.killWaiters.Load(agentID); ok {
		ch.(chan string) <- "kill_by_identity: name=notepad.exe company=\"\" killed=0 skipped=0 (sweep)"
	}
}
