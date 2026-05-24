package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
)

func executeCommand(conn *websocket.Conn, agentID string, msg map[string]interface{}) {
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

	sendExecResult(conn, agentID, jobID, status, output)
}

func deployFile(conn *websocket.Conn, agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	filename, _ := msg["filename"].(string)
	args, _ := msg["args"].(string)

	logMsg("INFO", "Deploying file job=%s filename=%s", jobID, filename)

	localPath, err := downloadFile(filename)
	if err != nil {
		sendExecResult(conn, agentID, jobID, "error", "download failed: "+err.Error())
		return
	}

	var psCmd string
	if args != "" {
		psCmd = fmt.Sprintf(
			"$p = Start-Process -FilePath '%s' -ArgumentList '%s' -Wait -PassThru; $p.ExitCode",
			localPath, args)
	} else {
		psCmd = fmt.Sprintf(
			"$p = Start-Process -FilePath '%s' -Wait -PassThru; $p.ExitCode",
			localPath)
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

	sendExecResult(conn, agentID, jobID, status, output)
}

func sendExecResult(conn *websocket.Conn, agentID, jobID, status, output string) {
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
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		logMsg("ERROR", "Send exec result: %v", err)
	}
}
