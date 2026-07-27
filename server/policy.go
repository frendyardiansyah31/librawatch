package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"library-monitor/shared"
)

// PolicyContext and PolicyDecision live in shared/policy.go now — aliased
// here so every existing reference in this file (and elsewhere in the
// server package) keeps compiling unchanged. See PolicyRule's alias in
// db.go for why: the agent (agent/policycache.go) needs to run the exact
// same matching logic against its local policy cache, so it moved to the
// shared module both packages import instead of staying server-only.
type PolicyContext = shared.PolicyContext
type PolicyDecision = shared.PolicyDecision

// PolicyEngine is the data-driven rule matcher Module 8 asks for — nothing
// here is hardcoded, every dimension comes from the policy_rules table
// (server/db.go). It reuses Hub for the two actions that need to reach an
// agent (kill, and Alerter's existing Telegram/email plumbing via
// hub.alerter), the same way Alerter already reuses Hub for auto-kill.
type PolicyEngine struct {
	db  *DB
	hub *Hub

	relevantAppsMu    sync.RWMutex
	relevantAppsCache map[string]PolicyRelevantApp
	relevantAppsAt    time.Time
}

func NewPolicyEngine(db *DB, hub *Hub) *PolicyEngine {
	return &PolicyEngine{db: db, hub: hub}
}

// relevantAppsCacheTTL bounds how stale the app_status/category_id lookup
// can be. EvaluateProcesses is now called both from the 30s metrics tick and
// from the WMI process-start fast path (one call per process launch,
// machine-wide, across every connected agent) — without this cache, every
// process start would trigger a SQLite query. A few seconds of staleness on
// an application's blocked/allowed status is an acceptable tradeoff for
// keeping the hot path in-memory only.
const relevantAppsCacheTTL = 5 * time.Second

// relevantApps returns the same data as db.GetPolicyRelevantApps, served
// from a short-TTL in-memory cache shared by every EvaluateProcesses caller.
func (p *PolicyEngine) relevantApps() (map[string]PolicyRelevantApp, error) {
	p.relevantAppsMu.RLock()
	if p.relevantAppsCache != nil && time.Since(p.relevantAppsAt) < relevantAppsCacheTTL {
		cached := p.relevantAppsCache
		p.relevantAppsMu.RUnlock()
		return cached, nil
	}
	p.relevantAppsMu.RUnlock()

	fresh, err := p.db.GetPolicyRelevantApps()
	if err != nil {
		return nil, err
	}

	p.relevantAppsMu.Lock()
	p.relevantAppsCache = fresh
	p.relevantAppsAt = time.Now()
	p.relevantAppsMu.Unlock()

	return fresh, nil
}

// Evaluate matches ctx against every enabled policy_rules row, via the same
// shared.Evaluate/shared.MatchScore matcher the agent's local policy cache
// runs (agent/policycache.go) — most-specific-wins scoring, ties broken by
// lowest ID (oldest rule first), documented on shared.MatchScore. No match
// (or no rules at all) defaults to "log" — Phase 2 never silently drops an
// event, it just doesn't escalate it.
func (p *PolicyEngine) Evaluate(ctx PolicyContext) PolicyDecision {
	rules, err := p.db.GetEnabledPolicyRules()
	if err != nil {
		slog.Error("policy: load rules failed", "error", err)
		return PolicyDecision{Action: EventActionLog}
	}
	return shared.Evaluate(rules, ctx)
}

// ─── Module 6: File Execution Policy ───────────────────────────────────────
//
// Two independent reasons a running process gets evaluated against
// policy_rules: (1) it's running from a watched location (downloads/
// desktop/temp/usb — classifyExecutionLocation), or (2) its exe_name is
// flagged in the applications table (a non-default status or a category),
// regardless of where it's running. (2) exists so an app_status=blocked
// rule fires wherever the app runs, not only if it also happens to be
// executed from a watched folder. Everything else — the common case of an
// ordinary, unflagged process running from Program Files/Windows — is
// skipped via cheap in-memory map lookups (GetPolicyRelevantApps, loaded
// once per EvaluateProcesses call), no per-process DB query.

// describeExecutionLocation renders loc for Indonesian-language
// notification text. "" now legitimately means a process was matched by
// app_status/category rather than location, i.e. a normal install path —
// not "unknown".
func describeExecutionLocation(loc string) string {
	if loc == "" {
		return "lokasi instalasi standar"
	}
	return loc
}

// EvaluateProcesses implements Module 6: evaluate every process that's
// either running from a watched location or whose exe_name is flagged in
// applications (see the section comment above), and act on the decision.
// Cheap for the common case — GetPolicyRelevantApps loads the flagged-app
// set once per call, so an ordinary unflagged process outside a watched
// location is skipped via two map lookups, no DB round trip.
func (p *PolicyEngine) EvaluateProcesses(agentID, hostname string, procs []Process) {
	relevantApps, err := p.relevantApps()
	if err != nil {
		slog.Error("policy: load policy-relevant apps failed", "error", err)
		relevantApps = nil // fall back to location-only matching, don't abort the tick
	}

	var deviceGroup string
	var groupLoaded bool

	for _, proc := range procs {
		loc := shared.ClassifyExecutionLocation(proc.Path)
		app, flagged := relevantApps[strings.ToLower(proc.Name)]
		if loc == "" && !flagged {
			continue
		}
		if !groupLoaded {
			deviceGroup, _ = p.db.GetAgentDeviceGroup(agentID)
			groupLoaded = true
		}

		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(proc.Path)), ".")

		decision := p.Evaluate(PolicyContext{
			AgentID:           agentID,
			DeviceGroup:       deviceGroup,
			CategoryID:        app.CategoryID,
			AppStatus:         app.Status,
			FileExtension:     ext,
			ExecutionLocation: loc,
		})

		if decision.Action == EventActionLog {
			continue // don't spam the events table for the (very common) default
		}

		// Must run async: actOnExecution can block on hub.KillProcess
		// waiting for the agent's kill_result reply, but this function runs
		// synchronously on that same agent connection's readPump goroutine
		// (handleMetrics → EvaluateProcesses) — blocking here would prevent
		// that very reply from ever being read, deadlocking until the 10s
		// timeout. Same reason Alerter.autoKill already fires via `go`.
		go p.actOnExecution(agentID, hostname, proc, loc, app.Company, decision)
	}
}

// actOnExecution carries out decision for one matched process. company is
// the applications.company of the matched exe_name (empty if there's no
// applications row / no company recorded) — passed through to a kill
// decision so the agent verifies the running process's own file metadata
// against it before terminating (see hub.KillProcessByIdentity), rather than
// killing anything that merely shares the exe_name the way a bare
// "taskkill /IM" would.
func (p *PolicyEngine) actOnExecution(agentID, hostname string, proc Process, loc, company string, decision PolicyDecision) {
	finalAction := decision.Action
	message := fmt.Sprintf("Kebijakan eksekusi: %s dijalankan dari %s di %s", proc.Name, describeExecutionLocation(loc), hostname)

	start := time.Now()
	var killOutput string
	var killErr error

	switch decision.Action {
	case PolicyActionKill:
		killOutput, killErr = p.hub.KillProcessByIdentity(agentID, proc.Name, company, proc.PID)
		if killErr != nil {
			slog.Warn("policy: kill failed", "agent_id", agentID, "process", proc.Name, "error", killErr)
			finalAction = EventActionLog
		} else {
			finalAction = EventActionKilled
			message = fmt.Sprintf("🚫 Proses %s dari %s di %s dihentikan (kebijakan eksekusi) — %s",
				proc.Name, describeExecutionLocation(loc), hostname, killOutput)
		}
		p.notify(message)
	case PolicyActionNotify:
		finalAction = EventActionNotify
		p.notify(message)
	case PolicyActionBlock, PolicyActionDelete:
		// Recorded only — Module 6 doesn't prevent a process from starting
		// (that would need a launch-time hook the agent doesn't have) or
		// delete a running executable's file out from under it. "kill" is
		// the enforcement action for already-running processes; block/delete
		// on an exec-location rule are logged as a decision for visibility,
		// matching the brief's "prepare policy support" framing.
		finalAction = EventActionBlocked
	}

	ruleName := ""
	if decision.Rule != nil {
		ruleName = decision.Rule.Name
	}
	policyVersion, _ := p.db.GetPolicyVersion()
	slog.Info("server-tick policy enforcement",
		"policy_version", policyVersion,
		"evaluation_source", "server_tick",
		"pid", proc.PID,
		"exe", proc.Name,
		"company", company,
		"rule", ruleName,
		"action", decision.Action,
		"result", finalAction,
		"duration", time.Since(start),
	)

	metadata, _ := json.Marshal(map[string]interface{}{
		"process":            proc.Name,
		"pid":                proc.PID,
		"path":               proc.Path,
		"execution_location": loc,
		"policy_action":      decision.Action,
		"policy_version":     policyVersion,
		"evaluation_source":  "server_tick",
		"rule":               ruleName,
		"kill_output":        killOutput,
	})
	if _, err := p.db.InsertEvent(agentID, "exec_policy", string(metadata), finalAction); err != nil {
		slog.Error("policy: insert exec_policy event failed", "error", err)
	}
}

// notify reuses the existing Alerter's Telegram/email plumbing (Module 9 —
// no second notification system) via the Hub it already holds a reference
// to, exactly the way Alerter.autoKill already reuses hub.KillProcess.
func (p *PolicyEngine) notify(message string) {
	if p.hub == nil || p.hub.alerter == nil {
		return
	}
	p.hub.alerter.NotifyEvent(message)
}
