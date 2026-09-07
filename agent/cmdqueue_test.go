package main

import (
	"encoding/json"
	"testing"
	"time"
)

// withDrainlessQueue swaps in a buffered channel with no worker draining it,
// so tests can inspect what submitCommand/processCommand enqueue without any
// real handler (PowerShell) running.
func withDrainlessQueue(t *testing.T) {
	t.Helper()
	origCh, origActive := cmdCh, cmdActive
	cmdCh = make(chan incomingCommand, 32)
	cmdActive = map[string]struct{}{}
	t.Cleanup(func() {
		cmdCh = origCh
		cmdActive = origActive
	})
}

func TestSerialCommandTypes_Classification(t *testing.T) {
	for _, tp := range []string{
		"exec", "winget", "file_deploy", "deepfreeze", "network_mode",
		"msiexec_uninstall", "quiet_uninstall", "install_ssh",
	} {
		if !serialCommandTypes[tp] {
			t.Errorf("%q must be a serial (worker) command type", tp)
		}
	}
	for _, tp := range []string{
		"kill_process", "kill_by_identity", "get_logs", "delete_file",
		"policy_update", "exec_result_ack", "",
	} {
		if serialCommandTypes[tp] {
			t.Errorf("%q must NOT be a serial command type", tp)
		}
	}
}

func TestSubmitCommand_NonSerialReturnsFalse(t *testing.T) {
	withDrainlessQueue(t)
	if submitCommand("a1", "kill_process", map[string]interface{}{"job_id": "j"}) {
		t.Error("non-serial type should return false so the caller runs it directly")
	}
}

func TestSubmitCommand_EnqueuesSerialTypeOnce(t *testing.T) {
	withTempCmdFiles(t)
	withDrainlessQueue(t)

	msg := map[string]interface{}{"job_id": "job-1", "payload": "Write-Output hi"}
	if !submitCommand("a1", "exec", msg) {
		t.Fatal("serial type should be accepted")
	}
	if len(cmdCh) != 1 {
		t.Fatalf("expected 1 queued command, got %d", len(cmdCh))
	}
	if _, ok := cmdActive["job-1"]; !ok {
		t.Error("job should be marked in-flight")
	}
}

// The mandatory duplicate-delivery guard (prompt-03 §24), at submit time.
func TestSubmitCommand_DuplicateWhileQueuedIsDropped(t *testing.T) {
	withTempCmdFiles(t)
	withDrainlessQueue(t)
	msg := map[string]interface{}{"job_id": "job-1"}

	submitCommand("a1", "exec", msg)
	submitCommand("a1", "exec", msg) // same command delivered twice

	if len(cmdCh) != 1 {
		t.Fatalf("a duplicate must not be enqueued again, got %d queued", len(cmdCh))
	}
}

func TestSubmitCommand_DuplicateOfCompletedJobResendsResult(t *testing.T) {
	withTempCmdFiles(t)
	withDrainlessQueue(t)
	recordCompletedResult("job-1", []byte(`{"type":"exec_result","status":"success"}`))

	if !submitCommand("a1", "exec", map[string]interface{}{"job_id": "job-1"}) {
		t.Fatal("should return true (handled as a duplicate)")
	}
	if len(cmdCh) != 0 {
		t.Fatalf("a completed job must not be re-enqueued, got %d queued", len(cmdCh))
	}
	if _, active := cmdActive["job-1"]; active {
		t.Error("a completed job must not be marked in-flight")
	}
}

func TestParseExpireAt(t *testing.T) {
	if parseExpireAt(map[string]interface{}{}) != nil {
		t.Error("missing expire_at → nil")
	}
	if parseExpireAt(map[string]interface{}{"expire_at": ""}) != nil {
		t.Error("empty expire_at → nil")
	}
	if parseExpireAt(map[string]interface{}{"expire_at": "not-a-time"}) != nil {
		t.Error("unparseable expire_at → nil")
	}
	if parseExpireAt(map[string]interface{}{"expire_at": "2026-09-07T13:20:41+07:00"}) == nil {
		t.Error("valid RFC3339 → non-nil")
	}
}

func TestProcessCommand_ExpiredIsRejectedNotRun(t *testing.T) {
	withTempCmdFiles(t)
	withDrainlessQueue(t)
	cmdActive["job-1"] = struct{}{} // as submitCommand would have marked it

	past := time.Now().Add(-time.Minute)
	processCommand("a1", incomingCommand{
		Type:     "exec",
		JobID:    "job-1",
		ExpireAt: &past,
		Raw:      map[string]interface{}{"job_id": "job-1", "attempt": float64(0)},
	})

	if _, active := cmdActive["job-1"]; active {
		t.Error("processCommand must release the in-flight claim")
	}
	data, ok := completedJobResult("job-1")
	if !ok {
		t.Fatal("an expired command should still record a terminal result")
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("recorded result not JSON: %v", err)
	}
	if m["status"] != "expired" {
		t.Errorf("recorded status = %v, want %q", m["status"], "expired")
	}
}

func TestProcessCommand_DuplicateAtExecTimeResendsNotRun(t *testing.T) {
	withTempCmdFiles(t)
	withDrainlessQueue(t)
	recordCompletedResult("job-1", []byte(`{"status":"success"}`))
	cmdActive["job-1"] = struct{}{}

	// If the exec-time dedup check failed this would call executeCommand and
	// spawn a real PowerShell; reaching the end without that is the assertion.
	processCommand("a1", incomingCommand{
		Type:  "exec",
		JobID: "job-1",
		Raw:   map[string]interface{}{"job_id": "job-1"},
	})

	if _, active := cmdActive["job-1"]; active {
		t.Error("the in-flight claim should be released")
	}
}
