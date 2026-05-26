package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
)

// Common DFCmd.exe paths for Deep Freeze 8 (32-bit and 64-bit installations)
var dfcmdCandidates = []string{
	`C:\Program Files\Faronics\Deep Freeze 8\DFCmd.exe`,
	`C:\Program Files (x86)\Faronics\Deep Freeze 8\DFCmd.exe`,
	`C:\Program Files\Faronics\Deep Freeze\DFCmd.exe`,
	`C:\Program Files (x86)\Faronics\Deep Freeze\DFCmd.exe`,
}

func handleDeepFreeze(conn *websocket.Conn, agentID string, msg map[string]interface{}) {
	action, _ := msg["action"].(string)
	password, _ := msg["password"].(string)
	jobID, _ := msg["job_id"].(string)

	logMsg("INFO", "DeepFreeze: action=%s job_id=%s", action, jobID)

	send := func(status, output string) {
		resp, _ := json.Marshal(map[string]interface{}{
			"type":     "deepfreeze_result",
			"agent_id": agentID,
			"job_id":   jobID,
			"status":   status,
			"output":   output,
		})
		if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
			logMsg("ERROR", "DeepFreeze send result: %v", err)
		}
	}

	dfcmd := findDFCmd()
	if dfcmd == "" {
		logMsg("WARN", "DeepFreeze: DFCmd.exe not found in any of %v", dfcmdCandidates)
		send("error", "DFCmd.exe tidak ditemukan. Pastikan Deep Freeze 8 terinstall.")
		return
	}
	logMsg("INFO", "DeepFreeze: using %s", dfcmd)

	var arg string
	switch action {
	case "thaw":
		if password != "" {
			arg = fmt.Sprintf("/BOOTTHAWED:%s", password)
		} else {
			arg = "/BOOTTHAWED"
		}
	case "freeze":
		if password != "" {
			arg = fmt.Sprintf("/BOOTFROZEN:%s", password)
		} else {
			arg = "/BOOTFROZEN"
		}
	case "query_df":
		if password != "" {
			arg = fmt.Sprintf("/QUERY:%s", password)
		} else {
			arg = "/QUERY"
		}
	default:
		logMsg("WARN", "DeepFreeze: unknown action %q", action)
		send("error", fmt.Sprintf("action tidak dikenal: %s", action))
		return
	}

	logMsg("INFO", "DeepFreeze: exec DFCmd.exe %s", arg)

	cmd := exec.Command(dfcmd, arg)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if err != nil {
		// thaw/freeze trigger a reboot — DFCmd.exe exits non-zero but the command still works.
		// Only query_df should treat a non-zero exit as a real error.
		logMsg("WARN", "DeepFreeze: DFCmd.exe exit=%v output=%q", err, output)
		if action == "thaw" || action == "freeze" {
			logMsg("INFO", "DeepFreeze: %s delivered, PC will reboot shortly", action)
			send("ok", fmt.Sprintf("%s\n(PC akan restart segera)", output))
		} else {
			send("error", fmt.Sprintf("%s\n(exit: %v)", output, err))
		}
		return
	}

	logMsg("INFO", "DeepFreeze: done action=%s output=%q", action, output)
	send("ok", output)
}

func findDFCmd() string {
	for _, p := range dfcmdCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
