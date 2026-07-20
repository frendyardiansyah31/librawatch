package main

import (
	"testing"
	"time"
)

// openTestDB is defined in batcher_test.go (same package).

func mustAgent(t *testing.T, db *DB, id string) {
	t.Helper()
	if err := db.UpsertAgent(&Agent{ID: id, Hostname: id, IP: "127.0.0.1", Status: "online", CreatedAt: nowWIB(), LastSeen: nowWIB()}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
}

func mustJob(t *testing.T, db *DB, priority int, createdAt time.Time) *DeployJob {
	t.Helper()
	job := &DeployJob{
		ID: generateJobID(), Type: "exec", Payload: "Write-Output hi",
		Targets: "[]", Status: "pending", Priority: priority, CreatedBy: "system",
		CreatedAt: createdAt,
	}
	if err := db.InsertDeployJob(job); err != nil {
		t.Fatalf("InsertDeployJob: %v", err)
	}
	return job
}

func mustResult(t *testing.T, db *DB, jobID, agentID string, maxRetry int) {
	t.Helper()
	if err := db.InsertDeployResult(&DeployResult{JobID: jobID, AgentID: agentID, Status: "pending", MaxRetry: maxRetry}); err != nil {
		t.Fatalf("InsertDeployResult: %v", err)
	}
}

// ── isPendingLikeStatus ──────────────────────────────────────────────────────

func TestIsPendingLikeStatus(t *testing.T) {
	cases := map[string]bool{
		"pending": true, "running": true,
		"success": false, "error": false, "ok": false,
		"cancelled": false, "expired": false, "failed": false,
	}
	for status, want := range cases {
		if got := isPendingLikeStatus(status); got != want {
			t.Errorf("isPendingLikeStatus(%q) = %v, want %v", status, got, want)
		}
	}
}

// ── AcquireNextJob ───────────────────────────────────────────────────────────

func TestAcquireNextJob_HigherPriorityFirst(t *testing.T) {
	db := openTestDB(t)
	mustAgent(t, db, "agent-1")

	low := mustJob(t, db, 0, nowWIB())
	mustResult(t, db, low.ID, "agent-1", 3)
	high := mustJob(t, db, 5, nowWIB().Add(time.Second)) // created later, but higher priority
	mustResult(t, db, high.ID, "agent-1", 3)

	claimed, err := db.AcquireNextJob("agent-1", nowWIB().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("AcquireNextJob: %v", err)
	}
	if claimed == nil || claimed.ID != high.ID {
		t.Fatalf("expected higher-priority job %q claimed first, got %+v", high.ID, claimed)
	}
}

func TestAcquireNextJob_FIFOWithinSamePriority(t *testing.T) {
	db := openTestDB(t)
	mustAgent(t, db, "agent-1")

	first := mustJob(t, db, 0, nowWIB())
	mustResult(t, db, first.ID, "agent-1", 3)
	second := mustJob(t, db, 0, nowWIB().Add(time.Second))
	mustResult(t, db, second.ID, "agent-1", 3)

	claimed, err := db.AcquireNextJob("agent-1", nowWIB().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("AcquireNextJob: %v", err)
	}
	if claimed == nil || claimed.ID != first.ID {
		t.Fatalf("expected oldest job %q claimed first, got %+v", first.ID, claimed)
	}
}

func TestAcquireNextJob_NothingClaimableWhileOneRunning(t *testing.T) {
	db := openTestDB(t)
	mustAgent(t, db, "agent-1")

	job1 := mustJob(t, db, 0, nowWIB())
	mustResult(t, db, job1.ID, "agent-1", 3)
	job2 := mustJob(t, db, 0, nowWIB().Add(time.Second))
	mustResult(t, db, job2.ID, "agent-1", 3)

	claimed, err := db.AcquireNextJob("agent-1", nowWIB().Add(10*time.Minute))
	if err != nil || claimed == nil {
		t.Fatalf("expected first claim to succeed, got %+v, err %v", claimed, err)
	}

	claimedAgain, err := db.AcquireNextJob("agent-1", nowWIB().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("AcquireNextJob: %v", err)
	}
	if claimedAgain != nil {
		t.Fatalf("expected no job claimable while one is running, got %+v", claimedAgain)
	}
}

func TestAcquireNextJob_NothingPending_ReturnsNil(t *testing.T) {
	db := openTestDB(t)
	mustAgent(t, db, "agent-1")

	claimed, err := db.AcquireNextJob("agent-1", nowWIB().Add(10*time.Minute))
	if err != nil {
		t.Fatalf("AcquireNextJob: %v", err)
	}
	if claimed != nil {
		t.Fatalf("expected nil with nothing queued, got %+v", claimed)
	}
}

// ── UpdateDeployResult retry boundary ───────────────────────────────────────

func TestUpdateDeployResult_RetryBoundary(t *testing.T) {
	db := openTestDB(t)
	mustAgent(t, db, "agent-1")
	job := mustJob(t, db, 0, nowWIB())
	mustResult(t, db, job.ID, "agent-1", 2) // max_retry = 2

	claimed, err := db.AcquireNextJob("agent-1", nowWIB().Add(-time.Minute)) // already-expired lease
	if err != nil || claimed == nil {
		t.Fatalf("acquire: %+v, %v", claimed, err)
	}

	// Simulate the sweep's retry-until-exhausted loop.
	results, _ := db.GetExpiredLeaseResults(nowWIB())
	if len(results) != 1 {
		t.Fatalf("expected 1 expired lease result, got %d", len(results))
	}
	r := results[0]

	for i := 0; i < 2; i++ {
		newRetryCount := r.RetryCount + 1
		if err := db.UpdateDeployResult(r.JobID, r.AgentID, "pending", "Lease timeout: retrying", nil, nil, &newRetryCount); err != nil {
			t.Fatalf("requeue: %v", err)
		}
		// Re-claim + re-expire to simulate another lease timeout.
		if _, err := db.AcquireNextJob(r.AgentID, nowWIB().Add(-time.Minute)); err != nil {
			t.Fatalf("reacquire: %v", err)
		}
		results, _ = db.GetExpiredLeaseResults(nowWIB())
		if len(results) != 1 {
			t.Fatalf("iteration %d: expected 1 expired lease result, got %d", i, len(results))
		}
		r = results[0]
	}

	// retry_count is now 2 == max_retry, so the next timeout must be terminal.
	if r.RetryCount+1 <= r.MaxRetry {
		t.Fatalf("expected retry budget exhausted, retry_count=%d max_retry=%d", r.RetryCount, r.MaxRetry)
	}
	if err := db.UpdateDeployResult(r.JobID, r.AgentID, "failed", "Lease timeout: retry limit exceeded", nil, nil, nil); err != nil {
		t.Fatalf("fail: %v", err)
	}

	final, err := db.GetDeployResultsByJobID(job.ID)
	if err != nil || len(final) != 1 {
		t.Fatalf("get final result: %+v, %v", final, err)
	}
	if final[0].Status != "failed" {
		t.Errorf("expected final status 'failed', got %q", final[0].Status)
	}
	if final[0].ExecutedAt == nil {
		t.Errorf("expected executed_at to be set on terminal status")
	}
}

func TestUpdateDeployResult_CancelledCannotBeOverwritten(t *testing.T) {
	db := openTestDB(t)
	mustAgent(t, db, "agent-1")
	job := mustJob(t, db, 0, nowWIB())
	mustResult(t, db, job.ID, "agent-1", 3)

	if _, err := db.AcquireNextJob("agent-1", nowWIB().Add(10*time.Minute)); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := db.CancelDeployJob(job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// A late real agent reply must not overwrite the cancellation.
	if err := db.UpdateDeployResult(job.ID, "agent-1", "success", "too late", nil, nil, nil); err != nil {
		t.Fatalf("update after cancel: %v", err)
	}

	results, err := db.GetDeployResultsByJobID(job.ID)
	if err != nil || len(results) != 1 {
		t.Fatalf("get results: %+v, %v", results, err)
	}
	if results[0].Status != "cancelled" {
		t.Errorf("expected status to remain 'cancelled', got %q", results[0].Status)
	}
}
