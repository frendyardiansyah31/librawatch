package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// withTempCmdFiles redirects the durable-ack and completed-jobs stores to a
// temp dir so `go test` never touches the real C:\LibraryAgent (shared with a
// running LibraryAgent service). Same rationale as withTempPolicyCacheFile.
func withTempCmdFiles(t *testing.T) {
	t.Helper()
	origAcks, origCompleted := pendingAcksFile, completedJobsFile
	dir := t.TempDir()
	pendingAcksFile = filepath.Join(dir, "pending_acks.json")
	completedJobsFile = filepath.Join(dir, "completed_jobs.json")
	t.Cleanup(func() {
		pendingAcksFile = origAcks
		completedJobsFile = origCompleted
	})
}

func TestCompletedJobs_RecordAndLookup(t *testing.T) {
	withTempCmdFiles(t)

	if _, ok := completedJobResult("job-1"); ok {
		t.Fatal("lookup before record should miss")
	}

	recordCompletedResult("job-1", []byte(`{"status":"success"}`))

	got, ok := completedJobResult("job-1")
	if !ok {
		t.Fatal("lookup after record should hit")
	}
	if string(got) != `{"status":"success"}` {
		t.Errorf("stored result = %s", got)
	}

	recordCompletedResult("", []byte(`x`))
	if _, ok := completedJobResult(""); ok {
		t.Error("empty job_id must never be stored")
	}
}

func TestCompletedJobs_PersistsAcrossReload(t *testing.T) {
	withTempCmdFiles(t)
	recordCompletedResult("job-1", []byte(`{"a":1}`))

	list := loadCompletedJobs() // simulate a fresh process reading from disk
	if len(list) != 1 || list[0].JobID != "job-1" {
		t.Fatalf("reload = %+v", list)
	}
}

func TestCompletedJobs_BoundedToMax(t *testing.T) {
	withTempCmdFiles(t)

	total := completedJobsMax + 20
	for i := 0; i < total; i++ {
		recordCompletedResult(fmt.Sprintf("job-%04d", i), []byte(`{}`))
	}

	if got := len(loadCompletedJobs()); got != completedJobsMax {
		t.Fatalf("kept %d entries, want cap %d", got, completedJobsMax)
	}
	if _, ok := completedJobResult("job-0000"); ok {
		t.Error("oldest job should have been evicted")
	}
	if _, ok := completedJobResult(fmt.Sprintf("job-%04d", total-1)); !ok {
		t.Error("newest job should be retained")
	}
}

func TestCompletedJobs_TTLPrune(t *testing.T) {
	withTempCmdFiles(t)
	recordCompletedResult("old", []byte(`{}`))

	list := loadCompletedJobs()
	list[0].At = time.Now().Add(-completedJobsTTL - time.Hour)
	if err := saveCompletedJobs(list); err != nil {
		t.Fatal(err)
	}

	if _, ok := completedJobResult("old"); ok {
		t.Error("entry past TTL must not be returned")
	}
	if len(loadCompletedJobs()) != 0 {
		t.Error("stale entry should have been pruned from disk on lookup")
	}
}
