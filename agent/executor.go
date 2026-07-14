package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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

	logMsg("INFO", "Executing command job=%s", jobID)

	sendExecResult(agentID, jobID, runPSCommand(payload))
}

func deployFile(agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	filename, _ := msg["filename"].(string)
	args, _ := msg["args"].(string)

	logMsg("INFO", "Deploying file job=%s filename=%s", jobID, filename)

	localPath, err := downloadFile(filename)
	if err != nil {
		sendExecResult(agentID, jobID, psResult{
			Status: "error", Output: "download failed: " + err.Error(), ExitCode: -1,
		})
		return
	}

	// Escape single quotes so they cannot break out of the PS string literal.
	safePath := strings.ReplaceAll(localPath, "'", "''")
	var psCmd string
	if args != "" {
		safeArgs := strings.ReplaceAll(args, "'", "''")
		psCmd = fmt.Sprintf(
			"$p = Start-Process -FilePath '%s' -ArgumentList '%s' -Wait -PassThru; $p.ExitCode",
			safePath, safeArgs)
	} else {
		psCmd = fmt.Sprintf(
			"$p = Start-Process -FilePath '%s' -Wait -PassThru; $p.ExitCode",
			safePath)
	}

	sendExecResult(agentID, jobID, runPSCommand(psCmd))
}

func sendExecResult(agentID, jobID string, r psResult) {
	output := r.Output
	if len(output) > 4096 {
		output = output[:4096] + "...[truncated]"
	}
	msg := map[string]interface{}{
		"type":        "exec_result",
		"agent_id":    agentID,
		"job_id":      jobID,
		"status":      r.Status,
		"output":      output,
		"exit_code":   r.ExitCode,
		"duration_ms": r.DurationMS,
	}
	data, _ := json.Marshal(msg)
	wsSend(data)
}
