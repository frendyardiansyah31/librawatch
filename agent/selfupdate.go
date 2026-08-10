package main

import (
	"encoding/json"
	"os"
	"time"
)

// selfUpdateCheckpoint records that a file_deploy job is about to run,
// before it's known whether the deployed file will kill this very process —
// e.g. a self-update script (ambil_file2.ps1/.bat) that stops the
// LibraryAgent service and taskkills agent.exe as part of replacing it.
// Confirmed on real hardware (2026-08-10, see SESSION_MEMORY.md): when that
// happens, agent.exe is killed mid-runPSCommand and never reaches
// sendExecResult, so the job sits stuck at "dispatched" forever on the
// server even though the deploy itself succeeded (the script survives the
// kill and finishes on its own, as an orphaned child process).
type selfUpdateCheckpoint struct {
	JobID      string      `json:"job_id"`
	AgentID    string      `json:"agent_id"`
	Attempt    interface{} `json:"attempt"`
	Filename   string      `json:"filename"`
	ExeModTime time.Time   `json:"exe_mod_time"`
	StartedAt  time.Time   `json:"started_at"`
}

// writeSelfUpdateCheckpoint is called by deployFile right before running the
// downloaded file, so a checkpoint exists on disk even if this process never
// gets to run another line of Go code afterward. Best-effort: any failure
// here just means the fallback-inference-on-restart won't be available for
// this particular job — deployFile's normal flow is unaffected either way.
func writeSelfUpdateCheckpoint(agentID, jobID, filename string, attempt interface{}) {
	exePath, err := os.Executable()
	if err != nil {
		logMsg("WARN", "selfupdate checkpoint: os.Executable failed: %v", err)
		return
	}
	info, err := os.Stat(exePath)
	if err != nil {
		logMsg("WARN", "selfupdate checkpoint: stat %s failed: %v", exePath, err)
		return
	}

	data, err := json.Marshal(selfUpdateCheckpoint{
		JobID:      jobID,
		AgentID:    agentID,
		Attempt:    attempt,
		Filename:   filename,
		ExeModTime: info.ModTime(),
		StartedAt:  time.Now(),
	})
	if err != nil {
		logMsg("WARN", "selfupdate checkpoint: marshal failed: %v", err)
		return
	}
	if err := os.WriteFile(selfUpdateCheckpointFile, data, 0644); err != nil {
		logMsg("WARN", "selfupdate checkpoint: write failed: %v", err)
	}
}

// clearSelfUpdateCheckpoint removes the checkpoint once deployFile gets a
// real result to report — this process survived runPSCommand, so the
// fallback inference on next startup is not needed for this job.
func clearSelfUpdateCheckpoint() {
	if err := os.Remove(selfUpdateCheckpointFile); err != nil && !os.IsNotExist(err) {
		logMsg("WARN", "selfupdate checkpoint: cleanup failed: %v", err)
	}
}

// checkSelfUpdateCheckpoint runs once at startup, before the agent connects
// to the server. A leftover checkpoint means the previous run never reached
// clearSelfUpdateCheckpoint — the most likely explanation is exactly the
// self-update-kills-itself scenario this exists for. The outcome is inferred
// from whether agent.exe itself actually changed on disk since the
// checkpoint was written: if it did, we're now running a different binary
// than the one that started the deploy, which is strong evidence the
// replace step succeeded (a coincidental unrelated restart would leave
// agent.exe untouched). If it's unchanged, something went wrong before the
// binary was actually replaced — reported as an error rather than assumed
// success, so the dashboard doesn't silently show "success" for a deploy
// that may have failed; the deploy script's own persistent log (e.g.
// C:\LibraryAgentDeploy\ambil_file2_log.txt) has the granular detail this
// inference can't provide.
func checkSelfUpdateCheckpoint() {
	data, err := os.ReadFile(selfUpdateCheckpointFile)
	if err != nil {
		return // nothing pending — the common case, every normal startup
	}

	var cp selfUpdateCheckpoint
	if unmarshalErr := json.Unmarshal(data, &cp); unmarshalErr != nil {
		logMsg("WARN", "selfupdate checkpoint: corrupt, discarding: %v", unmarshalErr)
		clearSelfUpdateCheckpoint()
		return
	}

	logMsg("INFO", "selfupdate checkpoint: found leftover checkpoint job=%s filename=%s (started %s) — previous run likely got killed mid-deploy, inferring outcome",
		cp.JobID, cp.Filename, cp.StartedAt.Format(time.RFC3339))

	result := psResult{
		Status: "error",
		Output: "agent restarted after this file deploy, but agent.exe was not modified — " +
			"deploy likely failed before replacing the binary; check the deploy script's own log on this PC",
		ExitCode: -1,
	}
	if exePath, err := os.Executable(); err == nil {
		if info, statErr := os.Stat(exePath); statErr == nil && info.ModTime().After(cp.ExeModTime) {
			result = psResult{
				Status: "success",
				Output: "agent restarted with a modified agent.exe after this file deploy — inferred success " +
					"(the process running it was killed before it could report, most likely by a self-update " +
					"script stopping its own service)",
				ExitCode: 0,
			}
		}
	}

	logPSResult("File deploy (inferred, "+cp.Filename+")", cp.JobID, result)
	sendExecResult(cp.AgentID, cp.JobID, cp.Attempt, result)
	clearSelfUpdateCheckpoint()
}
