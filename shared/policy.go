package shared

import (
	"strings"
	"time"
)

// PolicyRuleAction values a PolicyRule.Action can hold, validated in
// server/api.go.
const (
	PolicyActionLog    = "log"
	PolicyActionNotify = "notify"
	PolicyActionBlock  = "block"
	PolicyActionDelete = "delete"
	PolicyActionKill   = "kill"
)

// PolicyRule is one data-driven rule the policy matcher (Evaluate/MatchScore
// below) checks a PolicyContext against. Empty string fields mean "any" for
// that dimension. Lives here (not server- or agent-specific) so the server
// (source of truth, server/policy.go) and the agent (local enforcement
// cache, agent/policycache.go) run exactly the same matching logic instead
// of two hand-kept copies that can silently drift apart — the same
// reliability problem this whole refactor exists to fix.
type PolicyRule struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	EventType         string    `json:"event_type"`
	CategoryID        *int64    `json:"category_id"`
	AppStatus         string    `json:"app_status"`
	FileExtension     string    `json:"file_extension"`
	ExecutionLocation string    `json:"execution_location"`
	DeviceGroup       string    `json:"device_group"`
	Action            string    `json:"action"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
}

// PolicyContext is what a running process (or an event) is evaluated
// against. Empty string / nil fields mean "not applicable" for that
// dimension, and a PolicyRule with the same field left empty means "any" —
// see MatchScore.
type PolicyContext struct {
	AgentID           string
	DeviceGroup       string
	EventType         string // '' when evaluating a running process, not an event
	CategoryID        *int64
	AppStatus         string // applications.status ('' when unset or not applicable)
	FileExtension     string
	ExecutionLocation string // downloads|desktop|temp|usb|'' (unknown/other)
}

// PolicyDecision is the outcome of Evaluate: the action to take, and which
// rule (if any) produced it.
type PolicyDecision struct {
	Action string
	Rule   *PolicyRule
}

// PolicyRelevantApp is the per-exe_name status/category_id/company an
// AppStatus-scoped policy rule needs to match against — the server's source
// of truth is server/db.go's applications table (GetPolicyRelevantApps);
// the agent gets the same data pushed alongside PolicyRule in policy_update
// (agent/policycache.go's PolicyCache.RelevantApps) so it can evaluate
// AppStatus-scoped rules locally without a DB round-trip.
type PolicyRelevantApp struct {
	Status     string
	CategoryID *int64
	// Company is applications.company for the matched row — carried through
	// so a kill decision can tell the agent exactly which vendor's exe_name
	// to terminate (killByIdentity), instead of killing any process that
	// merely shares the exe_name. Empty is a valid, exact value: an app with
	// no recorded company only matches processes whose own file metadata
	// also reports no CompanyName, not to a random unrelated app.
	Company string
}

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

// ClassifyExecutionLocation buckets a process path into downloads/desktop/
// temp/usb/"" (unknown or a normal Program Files/Windows install path). Used
// identically by the server (30s metrics tick) and the agent (Priority 5's
// realtime local enforcement) so both classify a path the same way. USB
// detection here is a heuristic, not a lookup against a live USB-device
// list: in this deployment every PC only has a C: system drive, so any
// executable running from another drive letter is treated as external
// media.
func ClassifyExecutionLocation(path string) string {
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

// Evaluate matches ctx against every rule in rules (callers filter to
// enabled-only beforehand — see MatchScore for the per-rule matching rule
// and most-specific-wins scoring). No match (or an empty rules slice)
// defaults to PolicyActionLog, so a caller never silently drops a decision.
func Evaluate(rules []PolicyRule, ctx PolicyContext) PolicyDecision {
	var best *PolicyRule
	bestScore := -1
	for i := range rules {
		r := &rules[i]
		score, ok := MatchScore(r, ctx)
		if !ok {
			continue
		}
		if score > bestScore || (score == bestScore && best != nil && r.ID < best.ID) {
			best, bestScore = r, score
		}
	}

	if best == nil {
		return PolicyDecision{Action: PolicyActionLog}
	}
	return PolicyDecision{Action: best.Action, Rule: best}
}

// MatchScore returns how many of rule's non-empty dimensions matched ctx (0
// for a rule with every field left as "any"), and whether the rule matches
// ctx at all. A rule matches only if every one of its non-empty fields
// equals the corresponding ctx field; among matching rules passed to
// Evaluate, the one with the most non-empty fields set wins
// (most-specific-wins), ties broken by lowest ID (oldest rule first).
func MatchScore(r *PolicyRule, ctx PolicyContext) (int, bool) {
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
