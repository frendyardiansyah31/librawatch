package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shirou/gopsutil/v3/process"
)

// handleKillByIdentity implements the "kill_by_identity" message from
// hub.KillProcessByIdentity (server/hub.go) — PolicyEngine's app_status=
// blocked kill rule.
//
// The message carries the PID of the specific process instance that
// triggered this decision (EvaluateProcesses calls actOnExecution once per
// matched process, so each instance already gets its own targeted
// decision). When that PID is present, it's used directly: re-verified live
// (name, and company if known) to guard against the PID having been
// recycled by an unrelated process in the gap between the decision and this
// message arriving, then killed precisely — no enumeration needed, no risk
// of hitting the wrong process sharing that exe_name.
//
// PID is only ever a live value carried on the current decision (from the
// WMI proc-start event or the current metrics tick) — never persisted or
// reused across decisions.
//
// Fallback (pid absent, or the live re-check finds it no longer matches):
// enumerate every running process with that name and, for each one, verify
// its own file's CompanyName (via readVersionInfo, the same PE version-info
// read agent/appmeta.go already does for the app catalog) against the
// company recorded for the blocked application. If the blocked application
// has no recorded company (many legitimate tools — portable utilities,
// PyInstaller/script-compiled exes — don't embed a VERSIONINFO resource at
// all), this matches by exe_name alone: the app was already identified and
// marked blocked via the dashboard/policy_rules, so exe_name is treated as
// sufficient authorization in that case, same as before company-verification
// existed.
func handleKillByIdentity(agentID string, msg map[string]interface{}) {
	procName, _ := msg["proc_name"].(string)
	expectedCompany, _ := msg["company"].(string)
	pid := int(floatVal(msg["pid"]))

	output, _ := killByIdentity(procName, expectedCompany, pid)
	logMsg("INFO", output)
	reportKillResult(agentID, output)
}

// killByIdentity is the core PID-first/name-sweep-fallback kill logic,
// shared by two callers: handleKillByIdentity above (a server-initiated
// "kill_by_identity" message) and the realtime local-enforcement path
// (agent/procwatch.go's handleProcessStartEvent, Priority 5) — the agent
// evaluating its own policy cache and killing inline needs the exact same
// logic without round-tripping through a fake WebSocket message to reach it.
// See handleKillByIdentity's doc comment above for the full PID-first/
// name-sweep-fallback rationale. Every actual termination now goes through
// verifiedKillByPID (Priority 6: terminate → verify → retry → graceful
// close → final terminate → verify, see killverify.go) instead of a bare
// one-shot taskkill — result is returned alongside the descriptive string
// so callers that want structured Result/Reason/Duration (the realtime
// audit report) can use it without re-parsing the string.
func killByIdentity(procName, expectedCompany string, pid int) (output string, result KillResult) {
	if pid > 0 {
		if res, matched := killSpecificPIDIfMatches(pid, procName, expectedCompany); matched {
			return fmt.Sprintf("kill_by_identity: name=%s company=%q pid=%d result=%s reason=%s duration=%s (targeted)",
				procName, expectedCompany, pid, successLabel(res.Success), res.Reason, res.Duration), res
		}
		logMsg("WARNING", "kill_by_identity: pid %d no longer matches name=%s company=%q (process exited or PID recycled) — falling back to name sweep", pid, procName, expectedCompany)
	}

	procs, err := process.Processes()
	if err != nil {
		return fmt.Sprintf("kill_by_identity failed: could not list processes: %v", err), KillResult{Success: false, Reason: KillReasonUnknown}
	}

	var killed, skipped int
	var last KillResult
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

		last = verifiedKillByPID(int(p.Pid))
		if last.Success {
			killed++
		}
	}

	return fmt.Sprintf("kill_by_identity: name=%s company=%q killed=%d skipped=%d (sweep)", procName, expectedCompany, killed, skipped), last
}

// successLabel renders a KillResult.Success bool for the human-readable
// summary string (kept separate from the JSON-facing metadata, which uses
// the bool/Reason fields directly).
func successLabel(success bool) string {
	if success {
		return "Success"
	}
	return "Failed"
}

// killSpecificPIDIfMatches re-verifies pid's live name (and company, if
// expectedCompany is known) before killing it. matched=false tells the
// caller the PID no longer identifies the expected process (already exited,
// or recycled by an unrelated process) and it should fall back to the
// name-based sweep instead.
func killSpecificPIDIfMatches(pid int, procName, expectedCompany string) (result KillResult, matched bool) {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return KillResult{}, false
	}
	name, err := p.Name()
	if err != nil || !strings.EqualFold(name, procName) {
		return KillResult{}, false
	}
	if expectedCompany != "" {
		path, err := p.Exe()
		if err != nil {
			return KillResult{}, false
		}
		info, _ := readVersionInfo(path)
		if !strings.EqualFold(info["CompanyName"], expectedCompany) {
			return KillResult{}, false
		}
	}
	return verifiedKillByPID(pid), true
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
