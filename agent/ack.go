package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// pendingResult is a terminal job result (exec_result, deepfreeze_result...)
// that has been produced locally but not yet confirmed received by the
// server. Persisted to disk so a command that severs the agent's own
// network connection (e.g. disabling the adapter carrying the WebSocket)
// can't cause the result to be silently lost — without this, the deploy
// queue's lease sweeper sees no ack, assumes the job never ran, and
// redispatches the identical command once the lease expires.
type pendingResult struct {
	JobID    string          `json:"job_id"`
	Message  json.RawMessage `json:"message"`
	QueuedAt time.Time       `json:"queued_at"`
}

var ackStoreMu sync.Mutex

func loadPendingResults() []pendingResult {
	data, err := os.ReadFile(pendingAcksFile)
	if err != nil {
		return nil
	}
	var list []pendingResult
	if err := json.Unmarshal(data, &list); err != nil {
		logMsg("WARN", "pending_acks store corrupt, discarding: %v", err)
		return nil
	}
	return list
}

func savePendingResults(list []pendingResult) error {
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp := pendingAcksFile + ".tmp"
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
	return os.Rename(tmp, pendingAcksFile)
}

// persistPendingResult records data (a marshaled exec_result/deepfreeze_result
// message) as unacknowledged, replacing any earlier entry for the same jobID.
func persistPendingResult(jobID string, data []byte) {
	ackStoreMu.Lock()
	defer ackStoreMu.Unlock()

	list := loadPendingResults()
	kept := list[:0]
	for _, e := range list {
		if e.JobID != jobID {
			kept = append(kept, e)
		}
	}
	kept = append(kept, pendingResult{JobID: jobID, Message: data, QueuedAt: time.Now()})
	if err := savePendingResults(kept); err != nil {
		logMsg("WARN", "failed to persist pending ack job=%s: %v", jobID, err)
	}
}

// clearPendingResult removes jobID from the unacknowledged store once the
// server confirms receipt via exec_result_ack.
func clearPendingResult(jobID string) {
	ackStoreMu.Lock()
	defer ackStoreMu.Unlock()

	list := loadPendingResults()
	kept := list[:0]
	changed := false
	for _, e := range list {
		if e.JobID == jobID {
			changed = true
			continue
		}
		kept = append(kept, e)
	}
	if changed {
		if err := savePendingResults(kept); err != nil {
			logMsg("WARN", "failed to clear pending ack job=%s: %v", jobID, err)
		}
	}
}

// replayPendingResults resends every unacknowledged result. Called at the
// start of every session (fresh connect, reconnect after a blip, or agent
// restart) so a result lost to a connection drop gets another chance the
// moment the agent is back online, instead of waiting for the server's
// lease timeout to redispatch — and re-run — the same command.
func replayPendingResults() {
	list := loadPendingResults()
	for _, e := range list {
		logMsg("INFO", "replaying unacked result job=%s", e.JobID)
		wsSend(e.Message)
	}
}

// sendDurableResult persists msg before attempting delivery, so the result
// survives a connection drop that happens during or immediately after send.
// The entry is only cleared once the server replies with exec_result_ack.
func sendDurableResult(jobID string, msg map[string]interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		logMsg("ERROR", "marshal result job=%s: %v", jobID, err)
		return
	}
	persistPendingResult(jobID, data)
	wsSend(data)
}
