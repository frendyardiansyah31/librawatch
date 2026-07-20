package main

import "testing"

// ── resolveCommandTargets ───────────────────────────────────────────────────

// mustAgentFull inserts an agent and sets device_group/floor — UpsertAgent
// itself doesn't persist those two columns (see server/db.go), only
// SetAgentDeviceGroup/SetAgentFloor do.
func mustAgentFull(t *testing.T, db *DB, id, hostname, deviceGroup, floor string) {
	t.Helper()
	if err := db.UpsertAgent(&Agent{
		ID: id, Hostname: hostname, IP: "127.0.0.1", Status: "online",
		CreatedAt: nowWIB(), LastSeen: nowWIB(),
	}); err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if deviceGroup != "" {
		if err := db.SetAgentDeviceGroup(id, deviceGroup); err != nil {
			t.Fatalf("SetAgentDeviceGroup: %v", err)
		}
	}
	if floor != "" {
		if err := db.SetAgentFloor(id, floor); err != nil {
			t.Fatalf("SetAgentFloor: %v", err)
		}
	}
}

func TestResolveCommandTargets(t *testing.T) {
	db := openTestDB(t)
	mustAgentFull(t, db, "pc-05", "PC-05", "lab-a", "floor-4")
	mustAgentFull(t, db, "pc-06", "PC-06", "lab-a", "floor-4")
	mustAgentFull(t, db, "pc-07", "PC-07", "lab-b", "floor-5")

	cases := []struct {
		name   string
		target string
		want   []string
	}{
		{"exact agent ID", "pc-05", []string{"pc-05"}},
		{"hostname case-insensitive", "pc-06", []string{"pc-06"}},
		{"device_group fan-out", "lab-a", []string{"pc-05", "pc-06"}},
		{"floor fan-out", "floor-5", []string{"pc-07"}},
		{"all wildcard", "all", []string{"pc-05", "pc-06", "pc-07"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveCommandTargets(db, tc.target)
			if err != nil {
				t.Fatalf("resolveCommandTargets(%q): %v", tc.target, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("resolveCommandTargets(%q) = %v, want %v", tc.target, got, tc.want)
			}
			wantSet := make(map[string]bool, len(tc.want))
			for _, id := range tc.want {
				wantSet[id] = true
			}
			for _, id := range got {
				if !wantSet[id] {
					t.Errorf("resolveCommandTargets(%q) = %v, want %v", tc.target, got, tc.want)
				}
			}
		})
	}
}

func TestResolveCommandTargets_HostnameCaseInsensitive(t *testing.T) {
	db := openTestDB(t)
	mustAgentFull(t, db, "pc-05", "PC-Library-05", "", "")

	got, err := resolveCommandTargets(db, "pc-library-05")
	if err != nil {
		t.Fatalf("resolveCommandTargets: %v", err)
	}
	if len(got) != 1 || got[0] != "pc-05" {
		t.Fatalf("resolveCommandTargets(hostname, mixed case) = %v, want [pc-05]", got)
	}
}

func TestResolveCommandTargets_NoMatch(t *testing.T) {
	db := openTestDB(t)
	mustAgentFull(t, db, "pc-05", "PC-05", "lab-a", "floor-4")

	_, err := resolveCommandTargets(db, "does-not-exist")
	if err != errNoCommandTarget {
		t.Fatalf("resolveCommandTargets(no match) error = %v, want errNoCommandTarget", err)
	}
}

// ── computeNetworkModeTransition ────────────────────────────────────────────

func TestComputeNetworkModeTransition(t *testing.T) {
	cases := []struct {
		action, current, wantMode string
		wantOK                    bool
	}{
		{"enable_wifi", "ethernet", "both", true},
		{"enable_wifi", "both", "both", true},
		{"enable_wifi", "wifi", "wifi", true},
		{"disable_wifi", "both", "ethernet", true},
		{"disable_wifi", "wifi", "", false},
		{"disable_wifi", "ethernet", "ethernet", true},
		{"enable_lan", "wifi", "both", true},
		{"enable_lan", "both", "both", true},
		{"disable_lan", "both", "wifi", true},
		{"disable_lan", "ethernet", "", false},
		{"disable_lan", "wifi", "wifi", true},
	}
	for _, tc := range cases {
		gotMode, gotOK := computeNetworkModeTransition(tc.action, tc.current)
		if gotOK != tc.wantOK || (gotOK && gotMode != tc.wantMode) {
			t.Errorf("computeNetworkModeTransition(%q, %q) = (%q, %v), want (%q, %v)",
				tc.action, tc.current, gotMode, gotOK, tc.wantMode, tc.wantOK)
		}
	}
}

// ── sanitizeExecMessage ──────────────────────────────────────────────────────

func TestSanitizeExecMessage(t *testing.T) {
	got, err := sanitizeExecMessage("Library will close in 10 minutes.")
	if err != nil {
		t.Fatalf("sanitizeExecMessage: %v", err)
	}
	if got != "Library will close in 10 minutes." {
		t.Errorf("sanitizeExecMessage = %q", got)
	}

	got, err = sanitizeExecMessage("it's closing")
	if err != nil {
		t.Fatalf("sanitizeExecMessage: %v", err)
	}
	if got != "it''s closing" {
		t.Errorf("sanitizeExecMessage single-quote escape = %q, want %q", got, "it''s closing")
	}

	if _, err := sanitizeExecMessage(""); err == nil {
		t.Error("sanitizeExecMessage(empty) expected error, got nil")
	}
	if _, err := sanitizeExecMessage("line one\nline two"); err == nil {
		t.Error("sanitizeExecMessage(control char) expected error, got nil")
	}

	long := ""
	for i := 0; i < 501; i++ {
		long += "a"
	}
	if _, err := sanitizeExecMessage(long); err == nil {
		t.Error("sanitizeExecMessage(over 500 chars) expected error, got nil")
	}
}

// ── kill_process process-name validation (via actionToJob) ─────────────────

func TestActionToJob_KillProcessValidation(t *testing.T) {
	if _, _, _, err := actionToJob("kill_process", map[string]interface{}{"process": "chrome.exe"}); err != nil {
		t.Errorf("actionToJob(kill_process, chrome.exe): unexpected error: %v", err)
	}
	badNames := []string{"", "chrome", "chrome.exe; Remove-Item C:\\", "../evil.exe", "chrome.exe\n"}
	for _, name := range badNames {
		if _, _, _, err := actionToJob("kill_process", map[string]interface{}{"process": name}); err == nil {
			t.Errorf("actionToJob(kill_process, %q): expected error, got nil", name)
		}
	}
}

func TestActionToJob_CannedActions(t *testing.T) {
	cases := map[string]string{
		"restart":  psRestart,
		"shutdown": psShutdown,
		"lock":     psLock,
		"logout":   psLogout,
		"sleep":    psSleep,
	}
	for action, wantPayload := range cases {
		jobType, payload, _, err := actionToJob(action, nil)
		if err != nil {
			t.Errorf("actionToJob(%q): unexpected error: %v", action, err)
		}
		if jobType != "exec" || payload != wantPayload {
			t.Errorf("actionToJob(%q) = (%q, %q), want (exec, %q)", action, jobType, payload, wantPayload)
		}
	}
}
