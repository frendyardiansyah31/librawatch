package main

import (
	"sync"
	"time"
)

// Serialized, at-most-once command execution on the agent.
//
// The WebSocket reader must not execute OS-mutating commands directly: a
// duplicate delivery, or a lease-timeout re-dispatch racing the still-running
// original, would otherwise run `Restart-Computer` twice on the same PC.
// Instead, serial command types are funnelled through one worker goroutine:
//
//	handleServerMessage → submitCommand → cmdCh → cmdWorker → handler (sync)
//
// Fast / read-only / idempotent types (kill_process, kill_by_identity,
// get_logs, delete_file, policy_update, exec_result_ack) keep running as
// their own goroutines — they don't change power state and can't cause a
// duplicate reboot, so serializing them would only add latency.

// serialCommandTypes are OS-mutating and must never run concurrently or twice
// on the same PC.
var serialCommandTypes = map[string]bool{
	"exec":              true,
	"winget":            true,
	"file_deploy":       true,
	"deepfreeze":        true,
	"network_mode":      true,
	"msiexec_uninstall": true,
	"quiet_uninstall":   true,
	"install_ssh":       true,
}

type incomingCommand struct {
	Type     string
	JobID    string
	ExpireAt *time.Time
	Raw      map[string]interface{}
}

var (
	cmdCh        chan incomingCommand
	cmdStartOnce sync.Once

	cmdTrackMu sync.Mutex
	cmdActive  = map[string]struct{}{} // job_ids currently queued or in-flight
)

// startCommandWorker launches the single command worker. Idempotent (safe to
// call on every reconnect); the worker lives for the process lifetime so the
// queue and in-flight set survive reconnects.
func startCommandWorker(agentID string) {
	cmdStartOnce.Do(func() {
		cmdCh = make(chan incomingCommand, 16)
		go func() {
			for cmd := range cmdCh {
				processCommand(agentID, cmd)
			}
		}()
	})
}

// submitCommand routes an OS-mutating command message onto the worker queue,
// deduplicating against completed and in-flight jobs. Returns false if the
// type is not a serial type (the caller then runs it as a plain goroutine).
func submitCommand(agentID, msgType string, msg map[string]interface{}) bool {
	if !serialCommandTypes[msgType] {
		return false
	}
	if cmdCh == nil {
		logMsg("WARN", "command worker not started, dropping type=%s (server will re-dispatch)", msgType)
		return true
	}

	jobID, _ := msg["job_id"].(string)

	if jobID != "" {
		if data, ok := completedJobResult(jobID); ok {
			logMsg("INFO", "duplicate command job=%s: already completed, re-sending stored result", jobID)
			wsSend(data)
			return true
		}
		cmdTrackMu.Lock()
		if _, busy := cmdActive[jobID]; busy {
			cmdTrackMu.Unlock()
			logMsg("INFO", "duplicate command job=%s: already queued/running, ignoring", jobID)
			return true
		}
		cmdActive[jobID] = struct{}{}
		cmdTrackMu.Unlock()
	}

	cmd := incomingCommand{
		Type:     msgType,
		JobID:    jobID,
		ExpireAt: parseExpireAt(msg),
		Raw:      msg,
	}

	select {
	case cmdCh <- cmd:
	default:
		// Queue full — extremely unlikely (serial commands are rare). Release
		// the claim; the server re-dispatches on lease timeout.
		trackCommandDone(jobID)
		logMsg("WARN", "command queue full, dropping job=%s (server will re-dispatch)", jobID)
	}
	return true
}

func trackCommandDone(jobID string) {
	if jobID == "" {
		return
	}
	cmdTrackMu.Lock()
	delete(cmdActive, jobID)
	cmdTrackMu.Unlock()
}

func processCommand(agentID string, cmd incomingCommand) {
	defer trackCommandDone(cmd.JobID)

	// Re-check completion: the original may have finished between submit and
	// now, or this is a second copy that was already sitting in the channel.
	if cmd.JobID != "" {
		if data, ok := completedJobResult(cmd.JobID); ok {
			logMsg("INFO", "duplicate command job=%s at exec time, re-sending stored result", cmd.JobID)
			wsSend(data)
			return
		}
	}

	if cmd.ExpireAt != nil && time.Now().After(*cmd.ExpireAt) {
		logMsg("WARN", "command job=%s expired before execution (expire_at=%s), rejecting",
			cmd.JobID, cmd.ExpireAt.Format(time.RFC3339))
		sendExpiredResult(agentID, cmd)
		return
	}

	runCommandHandler(agentID, cmd)
}

// runCommandHandler calls the same handlers the WebSocket reader used to
// invoke with `go`, but SYNCHRONOUSLY — the worker owns serialization.
func runCommandHandler(agentID string, cmd incomingCommand) {
	switch cmd.Type {
	case "exec", "winget":
		executeCommand(agentID, cmd.Raw)
	case "msiexec_uninstall":
		executeMsiexecUninstall(agentID, cmd.Raw)
	case "quiet_uninstall":
		executeQuietUninstall(agentID, cmd.Raw)
	case "file_deploy":
		deployFile(agentID, cmd.Raw)
	case "deepfreeze":
		handleDeepFreeze(agentID, cmd.Raw)
	case "install_ssh":
		handleInstallSSH(agentID, cmd.Raw)
	case "network_mode":
		mode, _ := cmd.Raw["network_mode"].(string)
		reconcileNetworkMode(agentID, mode)
	}
}

func parseExpireAt(msg map[string]interface{}) *time.Time {
	s, _ := msg["expire_at"].(string)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// sendExpiredResult reports a command the agent refused to run because it was
// already past its expiry. Uses the normal durable-result path so the server
// records a terminal state and a re-delivery gets the same answer.
func sendExpiredResult(agentID string, cmd incomingCommand) {
	sendDurableResult(cmd.JobID, map[string]interface{}{
		"type":        "exec_result",
		"agent_id":    agentID,
		"job_id":      cmd.JobID,
		"attempt":     cmd.Raw["attempt"],
		"status":      "expired",
		"output":      "command expired before execution on agent",
		"exit_code":   -1,
		"duration_ms": 0,
	})
}
