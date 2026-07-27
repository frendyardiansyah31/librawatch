package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"library-monitor/shared"
)

// TestMain initializes agentLogger (normally set up once by initLogger() in
// main(), which tests never call) to a discard logger before any test runs —
// without it, the first logMsg call anywhere in the code under test panics
// on a nil *log.Logger. This is the first test file in agent/ (previously
// untested — see appmeta.go etc., which are Windows-syscall-bound); this
// setup is reused by any future agent test that exercises logMsg-calling code.
func TestMain(m *testing.M) {
	agentLogger = log.New(io.Discard, "", 0)
	os.Exit(m.Run())
}

// withTempPolicyCacheFile points policyCacheFile at a temp-dir path for the
// duration of one test, restoring the real (agentBaseDir-rooted) path
// afterward — savePolicyCacheFile/loadPolicyCacheFile must never touch the
// real C:\LibraryAgent during `go test`, since that directory is shared with
// whatever real LibraryAgent Windows service happens to be running on the
// machine running the tests.
func withTempPolicyCacheFile(t *testing.T) {
	t.Helper()
	original := policyCacheFile
	policyCacheFile = filepath.Join(t.TempDir(), "policy_cache.json")
	t.Cleanup(func() { policyCacheFile = original })
}

// resetPolicyCache clears the global atomic.Pointer before and after a test
// so tests can't observe each other's state.
func resetPolicyCache(t *testing.T) {
	t.Helper()
	policyCache.Store(nil)
	t.Cleanup(func() { policyCache.Store(nil) })
}

func TestCurrentPolicyCache_NeverNil(t *testing.T) {
	// Arrange
	resetPolicyCache(t)

	// Act
	cache := currentPolicyCache()

	// Assert
	if cache == nil {
		t.Fatal("currentPolicyCache() returned nil, must always return a usable empty cache")
	}
	if len(cache.Rules) != 0 {
		t.Errorf("fresh cache has %d rules, want 0", len(cache.Rules))
	}
}

func TestSetPolicyCache_UpdatesInMemoryAndPersistsToDisk(t *testing.T) {
	// Arrange
	resetPolicyCache(t)
	withTempPolicyCacheFile(t)

	pc := &PolicyCache{
		Version:     7,
		DeviceGroup: "Lantai 1",
		Rules:       []shared.PolicyRule{{ID: 1, Name: "block zoom", AppStatus: "blocked", Action: shared.PolicyActionKill, Enabled: true}},
		UpdatedAt:   time.Now(),
	}

	// Act
	setPolicyCache(pc)

	// Assert — in-memory read reflects the new cache immediately.
	got := currentPolicyCache()
	if got.Version != 7 {
		t.Errorf("in-memory Version = %d, want 7", got.Version)
	}

	// Assert — persisted file round-trips via loadPolicyCacheFile.
	loaded := loadPolicyCacheFile()
	if loaded == nil {
		t.Fatal("loadPolicyCacheFile() = nil after setPolicyCache, want persisted cache")
	}
	if loaded.Version != 7 || loaded.DeviceGroup != "Lantai 1" || len(loaded.Rules) != 1 {
		t.Errorf("loaded cache = %+v, want version=7 device_group=Lantai 1 with 1 rule", loaded)
	}
}

func TestLoadPolicyCacheFile_MissingFileReturnsNil(t *testing.T) {
	// Arrange
	withTempPolicyCacheFile(t)

	// Act / Assert — file doesn't exist yet (first run), must not error/panic.
	if got := loadPolicyCacheFile(); got != nil {
		t.Errorf("loadPolicyCacheFile() on missing file = %+v, want nil", got)
	}
}

func TestLoadPolicyCacheFile_CorruptFileReturnsNilNotPanic(t *testing.T) {
	// Arrange
	withTempPolicyCacheFile(t)
	if err := os.WriteFile(policyCacheFile, []byte("{not valid json"), 0644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	// Act / Assert
	if got := loadPolicyCacheFile(); got != nil {
		t.Errorf("loadPolicyCacheFile() on corrupt file = %+v, want nil (treated as no cache)", got)
	}
}

func TestHandlePolicyUpdate_DecodesRulesFromGenericMessage(t *testing.T) {
	// Arrange
	resetPolicyCache(t)
	withTempPolicyCacheFile(t)

	catID := float64(3) // JSON numbers decode as float64 in map[string]interface{}
	msg := map[string]interface{}{
		"type":           "policy_update",
		"policy_version": float64(12),
		"device_group":   "Lantai 2",
		"policy_rules": []interface{}{
			map[string]interface{}{
				"id":                 float64(1),
				"name":               "block zoom",
				"app_status":         "blocked",
				"category_id":        catID,
				"action":             "kill",
				"enabled":            true,
				"event_type":         "",
				"file_extension":     "",
				"execution_location": "",
				"device_group":       "",
			},
		},
	}

	// Act
	handlePolicyUpdate(msg)

	// Assert
	cache := currentPolicyCache()
	if cache.Version != 12 {
		t.Errorf("Version = %d, want 12", cache.Version)
	}
	if cache.DeviceGroup != "Lantai 2" {
		t.Errorf("DeviceGroup = %q, want %q", cache.DeviceGroup, "Lantai 2")
	}
	if len(cache.Rules) != 1 {
		t.Fatalf("len(Rules) = %d, want 1", len(cache.Rules))
	}
	r := cache.Rules[0]
	if r.Name != "block zoom" || r.AppStatus != "blocked" || r.Action != "kill" || !r.Enabled {
		t.Errorf("decoded rule = %+v, unexpected field values", r)
	}
	if r.CategoryID == nil || *r.CategoryID != 3 {
		t.Errorf("CategoryID = %v, want pointer to 3", r.CategoryID)
	}
}

func TestEvaluateProcessLocally_SkipsUnflaggedProcessInNormalLocation(t *testing.T) {
	// Arrange
	resetPolicyCache(t)
	withTempPolicyCacheFile(t)
	setPolicyCache(&PolicyCache{Version: 1, Rules: []shared.PolicyRule{
		{ID: 1, AppStatus: "blocked", Action: shared.PolicyActionKill, Enabled: true},
	}})

	// Act — chrome.exe isn't in RelevantApps, and Program Files isn't a
	// watched location, so this must be skipped entirely (matched=false),
	// same short-circuit as server/policy.go's EvaluateProcesses.
	_, _, matched := evaluateProcessLocally("chrome.exe", `C:\Program Files\Google\Chrome\chrome.exe`)

	// Assert
	if matched {
		t.Error("evaluateProcessLocally() matched an unflagged process outside any watched location, want skipped")
	}
}

func TestEvaluateProcessLocally_UsesDeviceGroupFromCacheAndFiltersDisabled(t *testing.T) {
	// Arrange
	resetPolicyCache(t)
	withTempPolicyCacheFile(t)

	catID := int64(3)
	setPolicyCache(&PolicyCache{
		Version:     1,
		DeviceGroup: "Lantai 3",
		Rules: []shared.PolicyRule{
			{ID: 1, AppStatus: "blocked", DeviceGroup: "Lantai 3", Action: shared.PolicyActionKill, Enabled: true},
			{ID: 2, AppStatus: "blocked", Action: shared.PolicyActionNotify, Enabled: false}, // disabled — must be ignored
		},
		RelevantApps: map[string]shared.PolicyRelevantApp{
			"zoom.exe": {Status: "blocked", CategoryID: &catID, Company: "Zoom Video Communications"}, // lowercased, as GetPolicyRelevantApps stores it
		},
	})

	// Act — Zoom.exe is flagged in RelevantApps (status=blocked), so this
	// must be evaluated even though its path isn't a watched location.
	decision, app, matched := evaluateProcessLocally("Zoom.exe", `C:\Users\libra\AppData\Roaming\Zoom\bin\Zoom.exe`)

	// Assert
	if !matched {
		t.Fatal("evaluateProcessLocally() matched = false, want true (exe is flagged in RelevantApps)")
	}
	if decision.Action != shared.PolicyActionKill {
		t.Errorf("Action = %q, want %q (disabled rule 2 must not win; DeviceGroup must come from cache)", decision.Action, shared.PolicyActionKill)
	}
	if app.Company != "Zoom Video Communications" {
		t.Errorf("app.Company = %q, want %q", app.Company, "Zoom Video Communications")
	}
}

// TestEvaluateProcessLocally_MatchesRegardlessOfNameCasing guards against the
// bug that made the realtime path never fire once in real testing: RelevantApps
// is keyed by whatever casing server/db.go's GetPolicyRelevantApps normalizes
// to (lowercased — see its doc comment), but the name passed in here comes
// from a DIFFERENT source (WMI's Win32_ProcessStartTrace.ProcessName), which
// isn't guaranteed to match the casing gopsutil's Name() originally reported
// when the applications row was created. A case-sensitive lookup silently
// missed every single time in practice.
func TestEvaluateProcessLocally_MatchesRegardlessOfNameCasing(t *testing.T) {
	// Arrange
	resetPolicyCache(t)
	withTempPolicyCacheFile(t)
	setPolicyCache(&PolicyCache{
		Version: 1,
		Rules:   []shared.PolicyRule{{ID: 1, AppStatus: "blocked", Action: shared.PolicyActionKill, Enabled: true}},
		RelevantApps: map[string]shared.PolicyRelevantApp{
			"zoom.exe": {Status: "blocked"}, // lowercased, as GetPolicyRelevantApps now stores it
		},
	})

	// Act — looked up with different casing than the stored key, simulating
	// WMI reporting "Zoom.exe" while the map was built from a lowercased key.
	decision, _, matched := evaluateProcessLocally("Zoom.exe", `C:\Users\libra\AppData\Roaming\Zoom\bin\Zoom.exe`)

	// Assert
	if !matched {
		t.Fatal("evaluateProcessLocally() matched = false, want true (lookup must be case-insensitive)")
	}
	if decision.Action != shared.PolicyActionKill {
		t.Errorf("Action = %q, want %q", decision.Action, shared.PolicyActionKill)
	}
}

// TestLoadPolicyCacheFile_NormalizesOldCasedKeys guards against a real
// regression found in production testing: a cache file persisted by an
// agent build from BEFORE the case-insensitivity fix still has
// original-cased RelevantApps keys ("Zoom.exe") on disk. Loading it must
// normalize those keys, or evaluateProcessLocally's lowercased lookups
// would keep silently missing forever, until the next policy_update
// happened to arrive and overwrite it.
func TestLoadPolicyCacheFile_NormalizesOldCasedKeys(t *testing.T) {
	// Arrange — hand-write a cache file exactly as an old agent build would
	// have (original-cased key), bypassing setPolicyCache/normalizeRelevantApps.
	withTempPolicyCacheFile(t)
	raw := `{"version":2,"relevant_apps":{"Zoom.exe":{"Status":"blocked","CategoryID":null,"Company":""}}}`
	if err := os.WriteFile(policyCacheFile, []byte(raw), 0644); err != nil {
		t.Fatalf("write raw cache file: %v", err)
	}

	// Act
	pc := loadPolicyCacheFile()

	// Assert
	if pc == nil {
		t.Fatal("loadPolicyCacheFile() = nil, want a loaded cache")
	}
	if _, ok := pc.RelevantApps["zoom.exe"]; !ok {
		t.Errorf(`RelevantApps["zoom.exe"] not found (keys = %v), want old-cased key normalized to lowercase`, pc.RelevantApps)
	}
	if _, ok := pc.RelevantApps["Zoom.exe"]; ok {
		t.Error(`RelevantApps["Zoom.exe"] still present — must be normalized away, not left alongside the lowercased key`)
	}
}

// TestInitPolicyCache_DiscardsCacheFromDifferentServer guards against the
// second real bug found in production testing: an agent tested against an
// isolated local server (its own independent policy_version counter) was
// later pointed at the real production server. The persisted cache's stale
// version number could be silently treated as "already current enough"
// against production's own, unrelated counter, permanently starving the
// agent of production's real policy data. initPolicyCache must detect the
// server changed and discard the mismatched cache instead of trusting it.
func TestInitPolicyCache_DiscardsCacheFromDifferentServer(t *testing.T) {
	// Arrange
	resetPolicyCache(t)
	withTempPolicyCacheFile(t)
	t.Setenv("LIBRARY_SERVER_URL", "ws://production.example/ws")

	pc := &PolicyCache{Version: 2, ServerURL: "ws://localhost:8080/ws", Rules: []shared.PolicyRule{{ID: 1, Name: "stale"}}}
	data, _ := json.Marshal(pc)
	if err := os.WriteFile(policyCacheFile, data, 0644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	// Act — current server (env var) differs from the cache's recorded ServerURL.
	initPolicyCache()

	// Assert
	got := currentPolicyCache()
	if len(got.Rules) != 0 {
		t.Errorf("cache from a different server was NOT discarded: got %+v, want an empty/discarded cache", got)
	}
}

// TestInitPolicyCache_KeepsCacheFromSameServer is the companion negative
// case — a cache genuinely written by the CURRENT server must still load
// normally (no regression from the new server-identity check).
func TestInitPolicyCache_KeepsCacheFromSameServer(t *testing.T) {
	// Arrange
	resetPolicyCache(t)
	withTempPolicyCacheFile(t)
	t.Setenv("LIBRARY_SERVER_URL", "ws://localhost:8080/ws")

	pc := &PolicyCache{Version: 2, ServerURL: "ws://localhost:8080/ws", Rules: []shared.PolicyRule{{ID: 1, Name: "kept"}}}
	data, _ := json.Marshal(pc)
	if err := os.WriteFile(policyCacheFile, data, 0644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	// Act
	initPolicyCache()

	// Assert
	got := currentPolicyCache()
	if len(got.Rules) != 1 || got.Rules[0].Name != "kept" {
		t.Errorf("cache from the same server was discarded/not loaded: got %+v", got)
	}
}
