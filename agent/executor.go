package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

func executeCommand(agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	payload, _ := msg["payload"].(string)

	logMsg("INFO", "Executing command job=%s", jobID)

	cmd := exec.Command("powershell.exe",
		"-NonInteractive", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-Command", payload)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	out, err := cmd.CombinedOutput()
	status := "success"
	output := strings.TrimSpace(string(out))
	if err != nil {
		status = "error"
		if output != "" {
			output += "\n" + err.Error()
		} else {
			output = err.Error()
		}
	}

	sendExecResult(agentID, jobID, status, output)
}

func deployFile(agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	filename, _ := msg["filename"].(string)
	args, _ := msg["args"].(string)

	logMsg("INFO", "Deploying file job=%s filename=%s", jobID, filename)

	localPath, err := downloadFile(filename)
	if err != nil {
		sendExecResult(agentID, jobID, "error", "download failed: "+err.Error())
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

	cmd := exec.Command("powershell.exe",
		"-NonInteractive", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-Command", psCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	out, err := cmd.CombinedOutput()
	status := "success"
	output := strings.TrimSpace(string(out))
	if err != nil {
		status = "error"
		if output != "" {
			output += "\n" + err.Error()
		} else {
			output = err.Error()
		}
	}

	sendExecResult(agentID, jobID, status, output)
}

func sendExecResult(agentID, jobID, status, output string) {
	if len(output) > 4096 {
		output = output[:4096] + "...[truncated]"
	}
	msg := map[string]interface{}{
		"type":     "exec_result",
		"agent_id": agentID,
		"job_id":   jobID,
		"status":   status,
		"output":   output,
	}
	data, _ := json.Marshal(msg)
	wsSend(data)
}
