// Command reliability helpers shared by the server (deploy queue / lease
// sweeper) and the agent (incoming-command dispatcher), so the two sides can
// never disagree about which commands are "destructive" — i.e. commands that
// power down or reboot the PC, where a lost result must NOT trigger a blind
// re-dispatch (a duplicate shutdown/restart). See server/deploy.go's
// sweepExpiredLeases and agent/main.go's command dispatcher for the call
// sites.
package shared

import "strings"

// IsDestructivePayload reports whether a deploy job would power off or reboot
// the target PC. Only `exec` jobs are considered — they carry an arbitrary
// PowerShell string; every other job type either has its own recovery path
// (file_deploy self-update checkpoint) or does not change power state.
//
// Matching is intentionally broad: a false positive only means a command
// whose result was lost won't be retried and gets marked "unconfirmed"
// (harmless), whereas a false negative could re-run an actual shutdown.
//
//   - Restart-Computer / Stop-Computer (the canned constants used by the
//     Command API and the MCP tools).
//   - shutdown(.exe) with a power-state flag: /s (shut down), /r (reboot),
//     /g (reboot + relaunch apps), /p (power off, no warning), /hybrid.
//     /l (log off) and /a (abort) do NOT change power state and are excluded.
func IsDestructivePayload(jobType, payload string) bool {
	if jobType != "exec" {
		return false
	}

	p := strings.ToLower(payload)
	if strings.Contains(p, "restart-computer") || strings.Contains(p, "stop-computer") {
		return true
	}

	p = strings.ReplaceAll(p, ".exe", "")
	p = strings.Join(strings.Fields(p), " ") // collapse runs of whitespace
	for _, flag := range []string{"/s", "-s", "/r", "-r", "/g", "-g", "/p", "-p", "/hybrid", "-hybrid"} {
		if strings.Contains(p, "shutdown "+flag) || strings.Contains(p, "shutdown"+flag) {
			return true
		}
	}
	return false
}
