package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"github.com/shirou/gopsutil/v3/process"
)

// handleKillByIdentity implements the "kill_by_identity" message from
// hub.KillProcessByIdentity (server/hub.go) — PolicyEngine's app_status=
// blocked kill rule. Unlike handleKillProcess's "taskkill /F /IM name"
// fallback, which blindly terminates any process sharing that exe_name,
// this enumerates every running process with that name and, for each one,
// verifies its own file's CompanyName (via readVersionInfo, the same PE
// version-info read agent/appmeta.go already does for the app catalog)
// against the company recorded for the blocked application — so a
// multi-process app (e.g. a browser: several same-named processes for the
// main window, GPU, renderer-per-tab, etc.) gets every instance killed,
// while a different, unrelated app that happens to share the exe_name is
// left alone.
//
// If the blocked application has no recorded company (many legitimate
// tools — portable utilities, PyInstaller/script-compiled exes — don't
// embed a VERSIONINFO resource at all, so there's nothing to verify against),
// this matches by exe_name alone: the app was already identified and marked
// blocked via the dashboard/policy_rules, so exe_name is treated as
// sufficient authorization in that case, same as before company-verification
// existed.
func handleKillByIdentity(agentID string, msg map[string]interface{}) {
	procName, _ := msg["proc_name"].(string)
	expectedCompany, _ := msg["company"].(string)

	procs, err := process.Processes()
	if err != nil {
		reportKillResult(agentID, fmt.Sprintf("kill_by_identity failed: could not list processes: %v", err))
		return
	}

	var killed, skipped int
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || !strings.EqualFold(name, procName) {
			continue
		}

		if expectedCompany != "" {
			path, err := p.Exe()
			if err != nil {
				skipped++
				continue
			}
			info, _ := readVersionInfo(path)
			if !strings.EqualFold(info["CompanyName"], expectedCompany) {
				skipped++
				continue
			}
		}

		if killProcessByPID(int(p.Pid)) {
			killed++
		}
	}

	output := fmt.Sprintf("kill_by_identity: name=%s company=%q killed=%d skipped=%d", procName, expectedCompany, killed, skipped)
	logMsg("INFO", output)
	reportKillResult(agentID, output)
}

// killProcessByPID mirrors the /PID branch of handleKillProcess.
func killProcessByPID(pid int) bool {
	cmd := exec.Command("taskkill", "/F", "/PID", fmt.Sprintf("%d", pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run() == nil
}

// reportKillResult sends the same "kill_result" reply type handleKillProcess
// uses — hub.KillProcessByIdentity waits on the same killWaiters channel as
// hub.KillProcess, keyed only by agentID, not by which kill message triggered it.
func reportKillResult(agentID, output string) {
	resp := map[string]interface{}{
		"type":     "kill_result",
		"agent_id": agentID,
		"output":   output,
	}
	data, _ := json.Marshal(resp)
	wsSend(data)
}
