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

const dfcPath = `C:\Windows\SysWOW64\DFC.exe`

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
			logMsg("ERROR", "DeepFreeze: send result failed: %v", err)
		}
	}

	if _, err := os.Stat(dfcPath); err != nil {
		logMsg("ERROR", "DeepFreeze: DFC.exe tidak ditemukan di %s: %v", dfcPath, err)
		send("error", fmt.Sprintf("DFC.exe tidak ditemukan di %s", dfcPath))
		return
	}
	logMsg("INFO", "DeepFreeze: DFC.exe ditemukan di %s", dfcPath)

	switch action {
	case "thaw":
		if password == "" {
			logMsg("WARN", "DeepFreeze: password kosong untuk action thaw")
			send("error", "Password Deep Freeze harus diisi untuk thaw")
			return
		}
		runDFCmd(password, "/BOOTTHAWED", action, send)

	case "freeze":
		if password == "" {
			logMsg("WARN", "DeepFreeze: password kosong untuk action freeze")
			send("error", "Password Deep Freeze harus diisi untuk freeze")
			return
		}
		runDFCmd(password, "/BOOTFROZEN", action, send)

	case "query_df":
		frozen, output, err := queryIsFrozen()
		if err != nil {
			logMsg("ERROR", "DeepFreeze: query_df gagal: %v | output=%q", err, output)
			send("error", output)
			return
		}
		status := "THAWED"
		if frozen {
			status = "FROZEN"
		}
		logMsg("INFO", "DeepFreeze: query_df result=%s (output=%q)", status, output)
		send("ok", status)

	default:
		logMsg("WARN", "DeepFreeze: action tidak dikenal: %q", action)
		send("error", fmt.Sprintf("action tidak dikenal: %s", action))
	}
}

// runDFCmd menjalankan DFC.exe <password> <flag>, lalu verifikasi status via ISFROZEN.
func runDFCmd(password, flag, action string, send func(string, string)) {
	// DFC.exe requires the password as the first positional argument:
	// DFC "Library2025!" /BOOTTHAWED   →  thaw on next reboot
	// DFC "Library2025!" /BOOTFROZEN   →  freeze on next reboot
	logMsg("INFO", "DeepFreeze: exec DFC.exe [password] %s", flag)

	cmd := exec.Command(dfcPath, password, flag)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	cmdOutput := strings.TrimSpace(string(out))

	logMsg("INFO", "DeepFreeze: DFC.exe %s exit=%v output=%q", flag, err, cmdOutput)

	// DFC.exe often exits non-zero for thaw/freeze because it schedules a reboot.
	// We treat any non-zero exit as a warning, not a hard failure — then verify.
	if err != nil {
		logMsg("WARN", "DeepFreeze: DFC.exe exited non-zero (%v) — mungkin normal, verifikasi status", err)
	}

	// Verify the new state via ISFROZEN so the operator can confirm the change.
	verifyFrozen, verifyOut, verifyErr := queryIsFrozen()
	verifyStatus := "THAWED"
	if verifyFrozen {
		verifyStatus = "FROZEN"
	}
	if verifyErr != nil {
		logMsg("ERROR", "DeepFreeze: verifikasi ISFROZEN gagal setelah %s: %v | output=%q", action, verifyErr, verifyOut)
	} else {
		logMsg("INFO", "DeepFreeze: verifikasi setelah %s — status=%s", action, verifyStatus)
	}

	var sb strings.Builder
	if cmdOutput != "" {
		sb.WriteString(cmdOutput)
		sb.WriteString("\n")
	}
	if verifyErr != nil {
		fmt.Fprintf(&sb, "Verifikasi ISFROZEN gagal: %s", verifyOut)
	} else {
		fmt.Fprintf(&sb, "Verifikasi status: %s", verifyStatus)
	}
	sb.WriteString("\n(PC akan restart segera untuk menerapkan perubahan)")

	send("ok", sb.String())
}

// queryIsFrozen runs DFC.exe get /ISFROZEN. Deep Freeze's DFC.exe reports
// state via exit code, not stdout: exit 1 = FROZEN, exit 0 = THAWED. Any
// other exit code (or a launch failure) is a genuine error.
// Returns (frozen, combined output, error).
func queryIsFrozen() (bool, string, error) {
	logMsg("INFO", "DeepFreeze: exec DFC.exe get /ISFROZEN")

	cmd := exec.Command(dfcPath, "get", "/ISFROZEN")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))

	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode := exitErr.ExitCode()
		logMsg("INFO", "DeepFreeze: ISFROZEN exit=%d output=%q", exitCode, output)
		if exitCode == 1 {
			return true, output, nil
		}
		msg := output
		if msg == "" {
			msg = fmt.Sprintf("DFC.exe exited with unexpected code %d", exitCode)
		} else {
			msg = fmt.Sprintf("%s (exit: %d)", msg, exitCode)
		}
		return false, msg, exitErr
	}
	if err != nil {
		logMsg("ERROR", "DeepFreeze: ISFROZEN exec failed: %v", err)
		return false, err.Error(), err
	}

	logMsg("INFO", "DeepFreeze: ISFROZEN exit=0 output=%q", output)
	return false, output, nil
}
