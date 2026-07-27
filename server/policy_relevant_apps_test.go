package main

import "testing"

// Positive: GetPolicyRelevantApps must key its map by lowercased exe_name —
// this is the fix for the bug where the agent's realtime local-enforcement
// path never fired: it looks up a process name reported by WMI
// (Win32_ProcessStartTrace.ProcessName), which isn't guaranteed to match the
// casing gopsutil's Name() originally used when the applications row was
// created, and a case-sensitive map silently missed every time.
func TestGetPolicyRelevantApps_KeysAreLowercased(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	now := "2026-01-01 00:00:00"
	if _, err := db.Exec(
		`INSERT INTO applications (exe_name, company, status, created_at, updated_at)
		 VALUES ('Zoom.exe', '', 'blocked', ?, ?)`,
		now, now,
	); err != nil {
		t.Fatalf("insert application: %v", err)
	}

	// Act
	apps, err := db.GetPolicyRelevantApps()
	if err != nil {
		t.Fatalf("GetPolicyRelevantApps: %v", err)
	}

	// Assert — looked up with a DIFFERENT casing than the stored exe_name,
	// simulating a WMI-reported name that disagrees with the DB's casing.
	app, ok := apps["zoom.exe"]
	if !ok {
		t.Fatalf(`apps["zoom.exe"] not found (map keys = %v), want lowercased key present`, keysOf(apps))
	}
	if app.Status != "blocked" {
		t.Errorf("Status = %q, want %q", app.Status, "blocked")
	}

	// The original (differently-cased) exact string must NOT be a separate key.
	if _, ok := apps["Zoom.exe"]; ok {
		t.Error(`apps["Zoom.exe"] present — keys must be normalized, not left as originally-cased`)
	}
}

func keysOf(m map[string]PolicyRelevantApp) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
