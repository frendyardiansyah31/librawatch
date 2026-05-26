package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
)

// handleInstallSSH installs OpenSSH Server on Windows 11/10 and restricts
// inbound SSH to the admin IP sent in the message.
// Steps streamed back as log lines, final status sent as ssh_result.
func handleInstallSSH(conn *websocket.Conn, agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	adminIP, _ := msg["args"].(string) // admin IP passed via "args" field (optional)

	logMsg("INFO", "InstallSSH: start job_id=%s admin_ip=%s", jobID, adminIP)

	sendLine := func(line string) {
		logMsg("INFO", "InstallSSH: %s", line)
		resp, _ := json.Marshal(map[string]interface{}{
			"type":     "ssh_log",
			"agent_id": agentID,
			"job_id":   jobID,
			"output":   line,
		})
		_ = conn.WriteMessage(websocket.TextMessage, resp)
	}

	sendResult := func(status, output string) {
		resp, _ := json.Marshal(map[string]interface{}{
			"type":     "exec_result",
			"agent_id": agentID,
			"job_id":   jobID,
			"status":   status,
			"output":   output,
		})
		if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
			logMsg("ERROR", "InstallSSH send result: %v", err)
		}
	}

	steps := []struct {
		desc string
		ps   string
	}{
		{
			"Memeriksa status OpenSSH...",
			`Get-WindowsCapability -Online -Name OpenSSH.Server* | Select-Object Name, State`,
		},
		{
			"Menginstall OpenSSH Server (bisa 1-2 menit)...",
			`$cap = Get-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0; if ($cap.State -ne 'Installed') { Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0 | Out-Null; "Installed" } else { "Already installed" }`,
		},
		{
			"Mengaktifkan service sshd (Automatic)...",
			`Set-Service -Name sshd -StartupType Automatic; Start-Service sshd; "sshd started"`,
		},
		{
			"Membuka port 22 di firewall...",
			`if (-not (Get-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -ErrorAction SilentlyContinue)) { New-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -DisplayName "OpenSSH Server (sshd)" -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 | Out-Null }; "Firewall rule OK"`,
		},
	}

	// Optional: restrict SSH to admin IP only
	if adminIP != "" {
		steps = append(steps, struct {
			desc string
			ps   string
		}{
			fmt.Sprintf("Membatasi SSH hanya dari %s...", adminIP),
			fmt.Sprintf(`$rule = Get-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -ErrorAction SilentlyContinue; if ($rule) { Set-NetFirewallRule -Name "OpenSSH-Server-In-TCP" -RemoteAddress "%s" }; "IP restricted to %s"`, adminIP, adminIP),
		})
	}

	steps = append(steps, struct {
		desc string
		ps   string
	}{
		"Verifikasi service sshd...",
		`$s = Get-Service sshd; "Status: $($s.Status) | StartType: $($s.StartType)"`,
	})

	var allOutput strings.Builder
	for _, step := range steps {
		sendLine("→ " + step.desc)

		cmd := exec.Command("powershell.exe",
			"-NonInteractive", "-NoProfile", "-ExecutionPolicy", "Bypass",
			"-Command", step.ps)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

		out, err := cmd.CombinedOutput()
		output := strings.TrimSpace(string(out))

		if err != nil {
			errMsg := fmt.Sprintf("GAGAL: %s\n(exit: %v)", output, err)
			sendLine(errMsg)
			allOutput.WriteString(errMsg + "\n")
			logMsg("ERROR", "InstallSSH step %q failed: %v output: %s", step.desc, err, output)
			sendResult("error", allOutput.String())
			return
		}

		sendLine("✓ " + output)
		allOutput.WriteString(output + "\n")
	}

	logMsg("INFO", "InstallSSH: completed successfully job_id=%s", jobID)
	sendResult("success", "OpenSSH Server berhasil diinstall dan dikonfigurasi.\n"+allOutput.String())
}
