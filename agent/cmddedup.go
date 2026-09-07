package main

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// completed_jobs.json is the agent's incoming-command deduplication memory.
// pending_acks.json (ack.go) tracks OUTGOING results not yet acknowledged;
// this tracks jobs the agent has FINISHED, so a duplicate delivery of the
// same job_id — a lease-timeout re-dispatch, or the same command replayed on
// reconnect — re-sends the stored result instead of executing the command a
// second time (a duplicate Restart-Computer / Stop-Computer).
//
// Bounded: at most completedJobsMax entries, each kept for completedJobsTTL,
// persisted atomically (tmp + rename) so the protection survives an agent
// restart.
// completedJobsFile is declared in config.go (var, so tests can redirect it).
const (
	completedJobsMax = 256
	completedJobsTTL = 24 * time.Hour
)

type completedJob struct {
	JobID  string          `json:"job_id"`
	Result json.RawMessage `json:"result"`
	At     time.Time       `json:"at"`
}

var completedJobsMu sync.Mutex

func loadCompletedJobs() []completedJob {
	data, err := os.ReadFile(completedJobsFile)
	if err != nil {
		return nil
	}
	var list []completedJob
	if err := json.Unmarshal(data, &list); err != nil {
		logMsg("WARN", "completed_jobs store corrupt, discarding: %v", err)
		return nil
	}
	return list
}

func saveCompletedJobs(list []completedJob) error {
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := completedJobsFile + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, completedJobsFile)
}

// pruneCompletedJobs drops entries past their TTL and, if still over the cap,
// the oldest ones. Returns a new slice; input is not reused.
func pruneCompletedJobs(list []completedJob, now time.Time) []completedJob {
	kept := make([]completedJob, 0, len(list))
	for _, e := range list {
		if now.Sub(e.At) < completedJobsTTL {
			kept = append(kept, e)
		}
	}
	if len(kept) > completedJobsMax {
		sort.Slice(kept, func(i, j int) bool { return kept[i].At.Before(kept[j].At) })
		kept = kept[len(kept)-completedJobsMax:]
	}
	return kept
}

// recordCompletedResult stores result as the terminal outcome of jobID.
func recordCompletedResult(jobID string, result []byte) {
	if jobID == "" {
		return
	}
	completedJobsMu.Lock()
	defer completedJobsMu.Unlock()

	now := time.Now()
	list := pruneCompletedJobs(loadCompletedJobs(), now)

	out := list[:0]
	for _, e := range list {
		if e.JobID != jobID {
			out = append(out, e)
		}
	}
	stored := make(json.RawMessage, len(result))
	copy(stored, result)
	out = append(out, completedJob{JobID: jobID, Result: stored, At: now})
	out = pruneCompletedJobs(out, now)

	if err := saveCompletedJobs(out); err != nil {
		logMsg("WARN", "failed to persist completed job=%s: %v", jobID, err)
	}
}

// completedJobResult returns the stored terminal result for jobID if the job
// finished within the retention window.
func completedJobResult(jobID string) ([]byte, bool) {
	if jobID == "" {
		return nil, false
	}
	completedJobsMu.Lock()
	defer completedJobsMu.Unlock()

	loaded := loadCompletedJobs()
	list := pruneCompletedJobs(loaded, time.Now())
	if len(list) != len(loaded) {
		_ = saveCompletedJobs(list) // keep the store self-cleaning between records
	}
	for _, e := range list {
		if e.JobID == jobID {
			return e.Result, true
		}
	}
	return nil, false
}
