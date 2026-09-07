package shared

import "testing"

func TestIsDestructivePayload(t *testing.T) {
	cases := []struct {
		name    string
		jobType string
		payload string
		want    bool
	}{
		// Canned constants used by the Command API (actionToJob) and MCP tools.
		{"restart canned", "exec", "Restart-Computer -Force", true},
		{"shutdown canned", "exec", "Stop-Computer -Force", true},
		{"restart lowercase", "exec", "restart-computer", true},
		{"shutdown uppercase", "exec", "STOP-COMPUTER -FORCE", true},

		// shutdown.exe / shutdown with a power-state flag.
		{"shutdown /s", "exec", "shutdown.exe /s /t 0", true},
		{"shutdown /r", "exec", "shutdown /r /t 0", true},
		{"shutdown -s dashes", "exec", "shutdown.exe -s -t 00", true},
		{"shutdown full path", "exec", `C:\Windows\System32\shutdown.exe /r`, true},
		{"shutdown /g", "exec", "& shutdown /g", true},
		{"shutdown /p", "exec", "shutdown /p", true},
		{"shutdown /hybrid", "exec", "shutdown /hybrid /t 0", true},
		{"shutdown no space", "exec", "shutdown/s", true},

		// Non-power-state shutdown flags are NOT destructive.
		{"logoff", "exec", "shutdown.exe /l", false},
		{"abort", "exec", "shutdown /a", false},

		// Unrelated exec payloads.
		{"write-output", "exec", "Write-Output hi", false},
		{"get-process", "exec", "Get-Process | Out-String", false},
		{"taskkill", "exec", "taskkill /F /IM chrome.exe", false},
		{"empty payload", "exec", "", false},

		// Only exec jobs are considered.
		{"winget", "winget", "winget uninstall --id Foo.Bar", false},
		{"deepfreeze", "deepfreeze", "freeze", false},
		{"file_deploy", "file_deploy", "agent-update.ps1", false},
		{"exec text in non-exec type", "winget", "Restart-Computer -Force", false},
		{"empty type", "", "Restart-Computer -Force", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsDestructivePayload(c.jobType, c.payload); got != c.want {
				t.Errorf("IsDestructivePayload(%q, %q) = %v, want %v",
					c.jobType, c.payload, got, c.want)
			}
		})
	}
}
