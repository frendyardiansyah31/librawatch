package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// execTimeout bounds how long any single PowerShell invocation may run
// before it's killed and reported as a timeout error.
const execTimeout = 10 * time.Minute

// psResult is what runPSCommand extracts from a completed (or timed-out)
// PowerShell invocation — combined output stays a single string, same as
// before; exit_code/duration_ms are the only new facts captured.
type psResult struct {
	Status     string
	Output     string
	ExitCode   int
	DurationMS int64
}

// runPSCommand runs psCmd via powershell.exe with a timeout, and reports the
// numeric exit code and duration alongside the existing combined-output
// status shape. Shared by executeCommand and deployFile so both commands
// gain the same timeout/exit-code/duration handling from one place.
func runPSCommand(psCmd string) psResult {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NonInteractive", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-Command", psCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	out, err := cmd.CombinedOutput()
	durationMS := time.Since(start).Milliseconds()
	status := "success"
	output := strings.TrimSpace(string(out))
	exitCode := 0

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		status = "error"
		exitCode = -1
		if output != "" {
			output += "\n"
		}
		output += "[timeout: command exceeded execution time limit]"
	case err != nil:
		status = "error"
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		if output != "" {
			output += "\n" + err.Error()
		} else {
			output = err.Error()
		}
	}

	return psResult{Status: status, Output: output, ExitCode: exitCode, DurationMS: durationMS}
}

func executeCommand(agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	payload, _ := msg["payload"].(string)
	attempt := msg["attempt"]
	msgType, _ := msg["type"].(string)

	logMsg("INFO", "Executing command job=%s", jobID)

	r := runPSCommand(payload)
	logPSResult("Command", jobID, r)
	sendExecResult(agentID, jobID, attempt, r)

	// Tier C (winget fallback) uninstall path for the Software Inventory
	// feature: refresh inventory after a winget *uninstall* so the server's
	// reconciliation — not this job's exit code alone — confirms the
	// software is actually gone. Plain winget installs from the Deploy tab
	// don't pay this extra scan cost.
	if msgType == "winget" && strings.Contains(payload, "uninstall") {
		triggerImmediateSnapshot(agentID)
	}
}

func deployFile(agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	filename, _ := msg["filename"].(string)
	checksum, _ := msg["checksum"].(string)
	args, _ := msg["args"].(string)
	attempt := msg["attempt"]

	logMsg("INFO", "Deploying file job=%s filename=%s", jobID, filename)

	localPath, err := downloadFile(filename, checksum)
	if err != nil {
		logMsg("ERROR", "File deploy job=%s filename=%s: download failed: %v", jobID, filename, err)
		sendExecResult(agentID, jobID, attempt, psResult{
			Status: "error", Output: "download failed: " + err.Error(), ExitCode: -1,
		})
		return
	}
	defer func() {
		if err := os.Remove(localPath); err != nil {
			logMsg("WARN", "File deploy job=%s: cleanup temp file failed: %s: %v", jobID, localPath, err)
		} else {
			logMsg("DEBUG", "File deploy job=%s: cleaned up %s", jobID, localPath)
		}
	}()

	// Escape single quotes so they cannot break out of the PS string literal.
	safePath := strings.ReplaceAll(localPath, "'", "''")
	var psCmd string
	if args != "" {
		safeArgs := strings.ReplaceAll(args, "'", "''")
		logMsg("INFO", "File deploy job=%s: executing %s (args: %s)", jobID, localPath, args)
		// Call operator (&), not Start-Process: Start-Process launches the
		// target as a separate detached process whose stdout/stderr is lost
		// (never reaches this host's CombinedOutput) and, for .ps1 files,
		// goes through Windows' shell file association — which defaults
		// .ps1 to Notepad, not PowerShell, so it would never actually run.
		// `&` invokes directly, inheriting this process's output and running
		// .ps1 as an actual script.
		psCmd = fmt.Sprintf("& '%s' %s; exit $LASTEXITCODE", safePath, safeArgs)
	} else {
		logMsg("INFO", "File deploy job=%s: executing %s", jobID, localPath)
		psCmd = fmt.Sprintf("& '%s'; exit $LASTEXITCODE", safePath)
	}

	// Written right before the risky part: if the deployed file kills this
	// process (a self-update script stopping/killing agent.exe as part of
	// replacing it — see selfupdate.go), the next startup's
	// checkSelfUpdateCheckpoint infers the outcome instead of the job
	// sitting stuck at "dispatched" forever. Cleared below once a real
	// result exists, since the fallback is then unnecessary.
	writeSelfUpdateCheckpoint(agentID, jobID, filename, attempt)

	r := runPSCommand(psCmd)
	clearSelfUpdateCheckpoint()
	logPSResult(fmt.Sprintf("File deploy (%s)", filepath.Base(localPath)), jobID, r)
	sendExecResult(agentID, jobID, attempt, r)
}

// logPSResult writes a single agent.log line summarizing a finished
// runPSCommand invocation — success/failure, exit code, duration, and (on
// failure) the actual output/error text — so agent.log alone is enough to
// diagnose what happened without cross-referencing the dashboard.
func logPSResult(label, jobID string, r psResult) {
	if r.Status == "success" {
		logMsg("INFO", "%s job=%s succeeded: exit_code=%d duration_ms=%d", label, jobID, r.ExitCode, r.DurationMS)
		return
	}
	logMsg("ERROR", "%s job=%s failed: exit_code=%d duration_ms=%d error=%s", label, jobID, r.ExitCode, r.DurationMS, r.Output)
}

func sendExecResult(agentID, jobID string, attempt interface{}, r psResult) {
	output := r.Output
	if len(output) > 4096 {
		output = output[:4096] + "...[truncated]"
	}
	msg := map[string]interface{}{
		"type":        "exec_result",
		"agent_id":    agentID,
		"job_id":      jobID,
		"attempt":     attempt,
		"status":      r.Status,
		"output":      output,
		"exit_code":   r.ExitCode,
		"duration_ms": r.DurationMS,
	}
	sendDurableResult(jobID, msg)
}
