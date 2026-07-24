package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// PolicyContext is what an incoming event or a running process is evaluated
// against. Empty string / nil fields mean "not applicable" for that
// dimension, and a PolicyRule with the same field left empty means "any" —
// see matchScore.
type PolicyContext struct {
	AgentID           string
	DeviceGroup       string
	EventType         string // '' when evaluating a running process, not an event
	CategoryID        *int64
	AppStatus         string // applications.status ('' when unset or not applicable, e.g. plain events)
	FileExtension     string
	ExecutionLocation string // downloads|desktop|temp|usb|'' (unknown/other)
}

type PolicyDecision struct {
	Action string // log|notify|block|delete|kill (EventAction*/PolicyAction* constants)
	Rule   *PolicyRule
}

// PolicyEngine is the data-driven rule matcher Module 8 asks for — nothing
// here is hardcoded, every dimension comes from the policy_rules table
// (server/db.go). It reuses Hub for the two actions that need to reach an
// agent (kill, and Alerter's existing Telegram/email plumbing via
// hub.alerter), the same way Alerter already reuses Hub for auto-kill.
type PolicyEngine struct {
	db  *DB
	hub *Hub
}

func NewPolicyEngine(db *DB, hub *Hub) *PolicyEngine {
	return &PolicyEngine{db: db, hub: hub}
}

// Evaluate matches ctx against every enabled policy_rules row. A rule
// matches only if every one of its non-empty fields equals the
// corresponding ctx field; among matching rules, the one with the most
// non-empty fields set wins (most-specific-wins), ties broken by lowest ID
// (oldest rule first). No match (or no rules at all) defaults to "log" —
// Phase 2 never silently drops an event, it just doesn't escalate it.
func (p *PolicyEngine) Evaluate(ctx PolicyContext) PolicyDecision {
	rules, err := p.db.GetEnabledPolicyRules()
	if err != nil {
		slog.Error("policy: load rules failed", "error", err)
		return PolicyDecision{Action: EventActionLog}
	}

	var best *PolicyRule
	bestScore := -1
	for i := range rules {
		r := &rules[i]
		score, ok := matchScore(r, ctx)
		if !ok {
			continue
		}
		if score > bestScore || (score == bestScore && best != nil && r.ID < best.ID) {
			best, bestScore = r, score
		}
	}

	if best == nil {
		return PolicyDecision{Action: EventActionLog}
	}
	return PolicyDecision{Action: best.Action, Rule: best}
}

// matchScore returns how many of the rule's non-empty dimensions matched
// (0 for a rule with every field left as "any"), and whether the rule
// matches ctx at all.
func matchScore(r *PolicyRule, ctx PolicyContext) (int, bool) {
	score := 0
	if r.EventType != "" {
		if r.EventType != ctx.EventType {
			return 0, false
		}
		score++
	}
	if r.CategoryID != nil {
		if ctx.CategoryID == nil || *r.CategoryID != *ctx.CategoryID {
			return 0, false
		}
		score++
	}
	if r.AppStatus != "" {
		if r.AppStatus != ctx.AppStatus {
			return 0, false
		}
		score++
	}
	if r.FileExtension != "" {
		if !strings.EqualFold(r.FileExtension, ctx.FileExtension) {
			return 0, false
		}
		score++
	}
	if r.ExecutionLocation != "" {
		if !strings.EqualFold(r.ExecutionLocation, ctx.ExecutionLocation) {
			return 0, false
		}
		score++
	}
	if r.DeviceGroup != "" {
		if !strings.EqualFold(r.DeviceGroup, ctx.DeviceGroup) {
			return 0, false
		}
		score++
	}
	return score, true
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

// watchedLocationMarkers maps an execution_location value to a path
// substring that identifies it. Deliberately simple substring matching
// rather than a configurable-paths setting — the three named folders match
// the brief's own examples and keep this from growing into a general path
// rules system.
var watchedLocationMarkers = map[string]string{
	"downloads": `\downloads\`,
	"desktop":   `\desktop\`,
	"temp":      `\temp\`,
}

// classifyExecutionLocation buckets a process path into downloads/desktop/
// temp/usb/"" (unknown or a normal Program Files/Windows install path).
// USB detection here is a heuristic, not a lookup against Module 1's live
// USB state: in this deployment every PC only has a C: system drive, so any
// executable running from another drive letter is treated as external
// media — the same thing Module 1 is watching for, without needing to
// cross-reference the two.
func classifyExecutionLocation(path string) string {
	if path == "" {
		return ""
	}
	if len(path) >= 2 && path[1] == ':' && !strings.EqualFold(path[:2], "c:") {
		return "usb"
	}
	lower := strings.ToLower(path)
	for loc, marker := range watchedLocationMarkers {
		if strings.Contains(lower, marker) {
			return loc
		}
	}
	return ""
}

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
	relevantApps, err := p.db.GetPolicyRelevantApps()
	if err != nil {
		slog.Error("policy: load policy-relevant apps failed", "error", err)
		relevantApps = nil // fall back to location-only matching, don't abort the tick
	}

	var deviceGroup string
	var groupLoaded bool

	for _, proc := range procs {
		loc := classifyExecutionLocation(proc.Path)
		app, flagged := relevantApps[proc.Name]
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
		go p.actOnExecution(agentID, hostname, proc, loc, decision)
	}
}

func (p *PolicyEngine) actOnExecution(agentID, hostname string, proc Process, loc string, decision PolicyDecision) {
	finalAction := decision.Action
	message := fmt.Sprintf("Kebijakan eksekusi: %s dijalankan dari %s di %s", proc.Name, describeExecutionLocation(loc), hostname)

	switch decision.Action {
	case PolicyActionKill:
		output, err := p.hub.KillProcess(agentID, proc.PID, proc.Name)
		if err != nil {
			slog.Warn("policy: kill failed", "agent_id", agentID, "process", proc.Name, "error", err)
			finalAction = EventActionLog
		} else {
			finalAction = EventActionKilled
			message = fmt.Sprintf("🚫 Proses %s dari %s di %s dihentikan (kebijakan eksekusi) — %s",
				proc.Name, describeExecutionLocation(loc), hostname, output)
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

	metadata, _ := json.Marshal(map[string]interface{}{
		"process":            proc.Name,
		"pid":                proc.PID,
		"path":               proc.Path,
		"execution_location": loc,
		"policy_action":      decision.Action,
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
