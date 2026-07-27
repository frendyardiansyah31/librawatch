package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"library-monitor/shared"
)

// PolicyCache is the agent's local, offline-capable copy of the server's
// policy_rules — Priority 4's whole point: the agent enforces policy
// locally (Priority 5) using this cache instead of round-tripping to the
// server for every decision, and keeps enforcing the last-known rules even
// while disconnected.
type PolicyCache struct {
	Version int64 `json:"version"`
	// ServerURL is the server this cache was synced from (getServerURL() at
	// the time it was written). A persisted cache is only ever trusted if
	// this matches the CURRENT session's server — otherwise a version
	// number from one server (e.g. an isolated local test instance) could be
	// misread as "already current enough" against a completely different
	// server's own, unrelated version counter, silently skipping the fresh
	// sync that server actually needs to hand over (see initPolicyCache).
	ServerURL string `json:"server_url"`
	// DeviceGroup is this agent's own current device_group, pushed alongside
	// the rules (server/hub.go's sendPolicyUpdateTo) since the agent has no
	// other way to know its own assigned group — needed to evaluate
	// DeviceGroup-scoped rules locally.
	DeviceGroup string              `json:"device_group"`
	Rules       []shared.PolicyRule `json:"rules"`
	// RelevantApps mirrors server/db.go's GetPolicyRelevantApps, keyed by
	// strings.ToLower(exe_name) — without this, the agent would know the
	// policy_rules themselves but not which apps are currently
	// blocked/categorized, so it could never evaluate an app_status=blocked
	// rule locally (exactly the dimension behind the original Zoom bug this
	// whole refactor started from).
	RelevantApps map[string]shared.PolicyRelevantApp `json:"relevant_apps"`
	UpdatedAt    time.Time                           `json:"updated_at"`
}

// normalizeRelevantApps returns a copy of apps with every key lowercased.
// Defensive, applied wherever a PolicyCache is constructed or loaded: the
// server already sends lowercased keys (GetPolicyRelevantApps), but a cache
// persisted to disk by an older agent build (before that normalization
// existed) could still have original-cased keys, and would otherwise never
// match evaluateProcessLocally's lowercased lookups again until the next
// policy_update happened to arrive.
func normalizeRelevantApps(apps map[string]shared.PolicyRelevantApp) map[string]shared.PolicyRelevantApp {
	if apps == nil {
		return nil
	}
	out := make(map[string]shared.PolicyRelevantApp, len(apps))
	for k, v := range apps {
		out[strings.ToLower(k)] = v
	}
	return out
}

// policyCache is the current cache, read by every process-start decision
// (agent/procwatch.go) and replaced wholesale — never mutated in place — on
// every policy_update. atomic.Pointer gives lock-free concurrent reads: the
// realtime process watcher must never block on a policy edit arriving on a
// different goroutine.
var policyCache atomic.Pointer[PolicyCache]

var policyCacheFile = filepath.Join(agentBaseDir, "policy_cache.json")

// currentPolicyCache returns the active cache, never nil — callers can
// evaluate against it immediately after agent startup even if no
// policy_update has arrived yet (e.g. first run, or the server was
// unreachable at boot): an empty rule set just means shared.Evaluate always
// falls back to its default "log" decision, never a panic.
func currentPolicyCache() *PolicyCache {
	if pc := policyCache.Load(); pc != nil {
		return pc
	}
	return &PolicyCache{}
}

// setPolicyCache atomically replaces the in-memory cache and persists it to
// disk. Called on every policy_update (handlePolicyUpdate) — never called
// with a partial/incremental update, always the full rule set the server
// just sent, so a swap can never leave a reader looking at a half-updated
// cache.
func setPolicyCache(pc *PolicyCache) {
	policyCache.Store(pc)
	if err := savePolicyCacheFile(pc); err != nil {
		logMsg("WARNING", "policy cache: failed to persist to disk: %v", err)
	}
}

// loadPolicyCacheFile reads policy_cache.json at startup, so the agent can
// keep enforcing the last-known policy immediately, before it's even
// connected to the server — offline-capable from boot, per spec. Returns
// nil (not an error) if the file doesn't exist yet (first run) or is
// corrupt (a bad cache is treated as no cache, not a fatal condition — the
// agent will just re-sync on its next successful connection).
func loadPolicyCacheFile() *PolicyCache {
	data, err := os.ReadFile(policyCacheFile)
	if err != nil {
		return nil
	}
	var pc PolicyCache
	if err := json.Unmarshal(data, &pc); err != nil {
		logMsg("WARNING", "policy cache: %s is corrupt, ignoring: %v", policyCacheFile, err)
		return nil
	}
	pc.RelevantApps = normalizeRelevantApps(pc.RelevantApps)
	return &pc
}

// savePolicyCacheFile writes pc via a temp-file-then-rename so a crash or
// power loss mid-write can never leave policy_cache.json half-written —
// os.Rename on the same volume is atomic, unlike a direct WriteFile.
func savePolicyCacheFile(pc *PolicyCache) error {
	data, err := json.Marshal(pc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentBaseDir, 0755); err != nil {
		return err
	}
	tmp := policyCacheFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, policyCacheFile)
}

// initPolicyCache loads the persisted cache (if any) at agent startup,
// before the first connection attempt — see loadPolicyCacheFile. A cache
// written by a different server (identified by ServerURL — e.g. this agent
// was previously pointed at an isolated local test server and is now
// pointed back at the real one, or vice versa) is discarded rather than
// trusted: its Version number belongs to a different server's own,
// unrelated counter, and sendPolicyVersionCheck comparing it against the
// CURRENT server's counter could wrongly conclude "already current" and
// skip the fresh sync this server actually needs to hand over — silently
// leaving the agent enforcing stale, wrong-server policy data indefinitely.
func initPolicyCache() {
	pc := loadPolicyCacheFile()
	if pc == nil {
		return
	}
	currentServer := getServerURL()
	if pc.ServerURL != "" && pc.ServerURL != currentServer {
		logMsg("WARNING", "policy cache: cached copy was synced from a different server (%q, now connecting to %q) — discarding stale cache, will full-sync on connect", pc.ServerURL, currentServer)
		return
	}
	policyCache.Store(pc)
	logMsg("INFO", "policy cache: loaded %d rule(s) from disk, version=%d", len(pc.Rules), pc.Version)
}

// handlePolicyUpdate processes a "policy_update" message (pushed on every
// server-side policy change, or sent as the direct reply to a stale
// policy_version_check) — replaces the cache wholesale.
func handlePolicyUpdate(msg map[string]interface{}) {
	version := int64(floatVal(msg["policy_version"]))
	deviceGroup, _ := msg["device_group"].(string)

	// Round-trip through JSON rather than hand-walking the []interface{}/
	// map[string]interface{} msg was itself decoded into — re-marshaling and
	// unmarshaling into the typed shapes is the simplest correct way to get
	// shared.PolicyRule/PolicyRelevantApp values (nested fields like
	// CategoryID *int64 are awkward to hand-extract from interface{}).
	var rules []shared.PolicyRule
	if rawRules, ok := msg["policy_rules"].([]interface{}); ok {
		if data, err := json.Marshal(rawRules); err == nil {
			if err := json.Unmarshal(data, &rules); err != nil {
				logMsg("ERROR", "policy_update: decode rules failed: %v", err)
				return
			}
		}
	}

	var apps map[string]shared.PolicyRelevantApp
	if rawApps, ok := msg["policy_apps"].(map[string]interface{}); ok {
		if data, err := json.Marshal(rawApps); err == nil {
			if err := json.Unmarshal(data, &apps); err != nil {
				logMsg("ERROR", "policy_update: decode relevant apps failed: %v", err)
				return
			}
		}
	}

	setPolicyCache(&PolicyCache{
		Version:      version,
		ServerURL:    getServerURL(),
		DeviceGroup:  deviceGroup,
		Rules:        rules,
		RelevantApps: normalizeRelevantApps(apps),
		UpdatedAt:    time.Now(),
	})
	logMsg("INFO", "policy cache: updated to version=%d (%d rule(s), %d relevant app(s))", version, len(rules), len(apps))
}

// sendPolicyVersionCheck reports the agent's currently-cached version to the
// server — sent once right after connecting (runSession), and periodically
// by the fallback ticker (startPolicyFallbackTicker) as a safety net. The
// server only replies with a full policy_update if this version is stale
// (server/hub.go's handlePolicyVersionCheck) — an already-current agent
// gets no reply, so this is cheap to call often.
func sendPolicyVersionCheck(agentID string) {
	msg := map[string]interface{}{
		"type":           "policy_version_check",
		"agent_id":       agentID,
		"policy_version": currentPolicyCache().Version,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	wsSend(data)
}

// policyFallbackCheckInterval is deliberately a safety net, not the primary
// sync path — real-time sync is the server pushing policy_update on every
// change (BroadcastPolicyUpdate) and the version-check sent once per
// connect. This only guards against a missed broadcast (e.g. the agent
// briefly disconnected exactly when an edit was pushed) going unnoticed for
// an entire session.
const policyFallbackCheckInterval = 45 * time.Minute

// startPolicyFallbackTicker runs sendPolicyVersionCheck on a slow interval
// for the lifetime of ctx. Safe to call once per agent session; stops when
// ctx is cancelled (agent shutdown).
func startPolicyFallbackTicker(ctx context.Context, agentID string) {
	go func() {
		ticker := time.NewTicker(policyFallbackCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sendPolicyVersionCheck(agentID)
			}
		}
	}()
}

// evaluateProcessLocally is Priority 5's realtime enforcement entry point,
// called by agent/procwatch.go's handleProcessStartEvent for every freshly
// launched process. Mirrors server/policy.go's EvaluateProcesses
// short-circuit exactly: a process is only evaluated if it's running from a
// watched location (downloads/desktop/temp/usb) OR its exe_name is flagged
// in RelevantApps — the common case (an ordinary, unflagged process running
// from Program Files/Windows) is skipped via two cheap lookups, matched=false,
// no decision made at all. Never touches the network or blocks: it's a pure
// in-memory lookup against whatever was last persisted by setPolicyCache, so
// it stays usable even fully offline.
func evaluateProcessLocally(name, path string) (decision shared.PolicyDecision, app shared.PolicyRelevantApp, matched bool) {
	cache := currentPolicyCache()
	loc := shared.ClassifyExecutionLocation(path)
	// Case-insensitive: RelevantApps keys are lowercased server-side
	// (GetPolicyRelevantApps) — see its doc comment for why (WMI's
	// ProcessName here vs gopsutil's Name() used to populate the map don't
	// reliably agree on casing).
	relevantApp, flagged := cache.RelevantApps[strings.ToLower(name)]
	if loc == "" && !flagged {
		return shared.PolicyDecision{}, shared.PolicyRelevantApp{}, false
	}

	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	ctx := shared.PolicyContext{
		DeviceGroup:       cache.DeviceGroup,
		CategoryID:        relevantApp.CategoryID,
		AppStatus:         relevantApp.Status,
		FileExtension:     ext,
		ExecutionLocation: loc,
	}
	return shared.Evaluate(enabledOnly(cache.Rules), ctx), relevantApp, true
}

// enabledOnly filters to Enabled rules — shared.Evaluate itself doesn't
// check Enabled (see shared/policy.go), matching
// db.GetEnabledPolicyRules()'s contract on the server side, so the agent
// must do the same filtering the server does.
func enabledOnly(rules []shared.PolicyRule) []shared.PolicyRule {
	out := make([]shared.PolicyRule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out
}
