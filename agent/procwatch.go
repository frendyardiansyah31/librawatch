package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"library-monitor/shared"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/shirou/gopsutil/v3/process"
)

// processStartEventTimeoutMs bounds how long each NextEvent_ call blocks
// before returning control to the loop so ctx.Done() can be checked —
// this is what makes the watcher's shutdown responsive without needing to
// tear down the COM event subscription from another goroutine.
const processStartEventTimeoutMs = 2000

// processStartRateLimit caps proc_start messages sent per second, as a
// safety valve against a pathological fork-bomb-style spike saturating the
// websocket — Win32_ProcessStartTrace fires once per process launch
// machine-wide, so this is defense-in-depth, not expected to trigger under
// normal desktop use.
const processStartRateLimit = 50

// processStartWatchInitialBackoff/MaxBackoff bound startProcessStartWatch's
// retry delay after a subscription-setup failure — capped exponential
// backoff so a persistent failure (e.g. a process that will genuinely never
// have WMI process-trace privilege) doesn't hammer WMI/COM, while a
// transient one (WMI service not up yet at boot, a momentary COM hiccup)
// still recovers promptly.
const (
	processStartWatchInitialBackoff = 5 * time.Second
	processStartWatchMaxBackoff     = 2 * time.Minute
)

// startProcessStartWatch drives runProcessStartWatchOnce with retry +
// backoff. Before this, a subscription-setup failure (e.g. the "Access
// denied" this exact code hit when run without sufficient privilege) was
// permanent for the agent process's entire lifetime — logged once as a WARN
// and never retried, silently leaving the agent on the 30s metrics-tick
// fallback with no ongoing signal anything was wrong. This does not fix a
// genuine, permanent privilege denial (that will just keep failing and
// retrying forever, logging periodically) — only a transient failure or a
// boot-order race actually self-heals. See runProcessStartWatchOnce for the
// subscription/event-loop logic itself.
func startProcessStartWatch(ctx context.Context, agentID string) {
	backoff := processStartWatchInitialBackoff
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if subscribed := runProcessStartWatchOnce(ctx, agentID); subscribed {
			backoff = processStartWatchInitialBackoff // reset after a clean, established run
		} else {
			logMsg("WARN", "procwatch: subscription failed, retrying in %v", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > processStartWatchMaxBackoff {
			backoff = processStartWatchMaxBackoff
		}
	}
}

// runProcessStartWatchOnce subscribes to Win32_ProcessStartTrace, a genuine
// kernel push-notification (not a poll) for "a new process started"
// machine-wide, and evaluates each one against the local policy cache
// (Priority 5 — enforceLocalPolicy) so a blocked app gets killed within
// roughly a second of launch instead of waiting for the next sendInterval
// metrics tick (up to 30s, see main.go). Returns true if the subscription
// was established (regardless of how the event loop below eventually ends —
// ctx.Done() is the only path out of that loop once subscribed, so a false
// return here specifically means setup itself failed) — startProcessStartWatch
// uses this to decide whether to reset its retry backoff.
func runProcessStartWatchOnce(ctx context.Context, agentID string) bool {
	// COM STA objects are thread-affine: this goroutine must keep the same
	// OS thread for its entire lifetime, unlike the short-lived wmi.Query
	// calls elsewhere in this package (usbwatch.go, peripheralwatch.go),
	// which don't hold a subscription open across calls.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		logMsg("WARN", "procwatch: CoInitializeEx failed: %v", err)
		return false
	}
	defer ole.CoUninitialize()

	locatorUnknown, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		logMsg("WARN", "procwatch: create SWbemLocator failed: %v", err)
		return false
	}
	defer locatorUnknown.Release()

	locator, err := locatorUnknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		logMsg("WARN", "procwatch: SWbemLocator QueryInterface failed: %v", err)
		return false
	}
	defer locator.Release()

	serviceRaw, err := oleutil.CallMethod(locator, "ConnectServer", ".", "root\\cimv2")
	if err != nil {
		logMsg("WARN", "procwatch: ConnectServer failed: %v", err)
		return false
	}
	service := serviceRaw.ToIDispatch()
	if service == nil {
		logMsg("WARN", "procwatch: ConnectServer returned no service")
		return false
	}
	defer service.Release()

	eventsRaw, err := oleutil.CallMethod(service, "ExecNotificationQuery", "SELECT * FROM Win32_ProcessStartTrace")
	if err != nil {
		logMsg("WARN", "procwatch: ExecNotificationQuery failed: %v", err)
		return false
	}
	events := eventsRaw.ToIDispatch()
	if events == nil {
		logMsg("WARN", "procwatch: ExecNotificationQuery returned no event source")
		return false
	}
	defer events.Release()

	logMsg("INFO", "procwatch: Win32_ProcessStartTrace watcher started")
	limiter := newRateLimiter(processStartRateLimit)

	for {
		select {
		case <-ctx.Done():
			return true
		default:
		}

		eventRaw, err := oleutil.CallMethod(events, "NextEvent_", processStartEventTimeoutMs)
		if err != nil {
			// Timeout (WBEM_S_TIMEDOUT) or a transient error — either way,
			// loop back and re-check ctx.Done() rather than treating this
			// as fatal. See startProcessStartWatch's doc comment: distinguishing
			// a normal timeout from a genuinely broken established subscription
			// isn't reliable from this error alone, so this loop's behavior is
			// deliberately unchanged — only the setup phase above gets retried.
			continue
		}
		eventObj := eventRaw.ToIDispatch()
		if eventObj == nil {
			continue
		}
		handleProcessStartEvent(agentID, eventObj, limiter)
		eventObj.Release()
	}
}

// handleProcessStartEvent reads ProcessID/ProcessName off one
// Win32_ProcessStartTrace instance and evaluates it immediately against the
// agent's local policy cache (Priority 5 — evaluateProcessLocally, using
// the cache Priority 4 keeps synced), killing inline when matched, instead
// of only reporting to the server and waiting for it to decide. The server
// is notified afterward, for audit/logging only (reportPolicyEnforcement) —
// enforcement never waits on that round-trip. The 30s metrics tick
// (server/policy.go's EvaluateProcesses, driven by handleMetrics) is
// untouched and keeps running server-side as a telemetry/eventual-consistency
// backstop, per spec.
func handleProcessStartEvent(agentID string, eventObj *ole.IDispatch, limiter *rateLimiter) {
	if !limiter.allow() {
		return
	}

	pidVar, err := oleutil.GetProperty(eventObj, "ProcessID")
	if err != nil {
		return
	}
	defer pidVar.Clear()
	nameVar, err := oleutil.GetProperty(eventObj, "ProcessName")
	if err != nil {
		return
	}
	defer nameVar.Clear()

	pid, ok := toInt(pidVar.Value())
	name := nameVar.ToString()
	if !ok || name == "" {
		return
	}

	path := resolveProcessPath(pid)
	enforceLocalPolicy(agentID, name, pid, path)
}

// enforceLocalPolicy is Priority 5's realtime enforcement step: evaluate
// against the local cache and, for anything other than the default "log"
// decision, act on it immediately (kill for PolicyActionKill; everything
// else — notify/block/delete — is recorded only, no agent-side enforcement
// hook exists for those, same as the server-side path), then report the
// outcome to the server for audit. A "log" decision (the common case, no
// rule matched or evaluation was skipped) is neither acted upon nor
// reported — matches EvaluateProcesses' own "don't spam the events table
// for the default" behavior.
func enforceLocalPolicy(agentID, name string, pid int, path string) {
	start := time.Now()
	decision, app, matched := evaluateProcessLocally(name, path)
	if !matched || decision.Action == shared.PolicyActionLog {
		return
	}

	// finalAction mirrors server/policy.go's actOnExecution exactly,
	// including on failure: "killed" only once verifyAndKillPID (killverify.go)
	// actually confirms the process is gone, not merely that a kill was
	// decided — a failed kill (result.Success == false) falls back to "log",
	// same as the server-side path already does on a kill error. block/delete
	// are recorded only, no agent-side enforcement hook exists for those, same
	// as server-side actOnExecution.
	finalAction := decision.Action
	var result KillResult
	if decision.Action == shared.PolicyActionKill {
		var output string
		output, result = killByIdentity(name, app.Company, pid)
		logMsg("INFO", "realtime enforcement: %s", output)
		if result.Success {
			finalAction = "killed"
		} else {
			finalAction = shared.PolicyActionLog
		}
	} else if decision.Action == shared.PolicyActionBlock || decision.Action == shared.PolicyActionDelete {
		finalAction = "blocked"
	}

	ruleName := ""
	if decision.Rule != nil {
		ruleName = decision.Rule.Name
	}
	slog.Info("realtime policy enforcement",
		"policy_version", currentPolicyCache().Version,
		"evaluation_source", "realtime_local_cache",
		"pid", pid,
		"exe", name,
		"company", app.Company,
		"rule", ruleName,
		"matched", matched,
		"action", decision.Action,
		"result", successLabel(result.Success),
		"reason", result.Reason,
		"duration", time.Since(start),
	)

	reportPolicyEnforcement(agentID, finalAction, map[string]interface{}{
		"process":            name,
		"pid":                pid,
		"path":               path,
		"execution_location": shared.ClassifyExecutionLocation(path),
		"policy_action":      decision.Action,
		"policy_version":     currentPolicyCache().Version,
		"evaluation_source":  "realtime_local_cache",
		"rule":               ruleName,
		"kill_result":        successLabel(result.Success),
		"kill_reason":        result.Reason,
		"kill_duration_ms":   result.Duration.Milliseconds(),
	})
}

// resolveProcessPath best-effort resolves a PID to its executable's full
// path via gopsutil (already an agent dependency), matching the
// "empty-on-failure" tolerance already used elsewhere in the agent (MAC
// address, app metadata) — a very short-lived process may have already
// exited by the time this runs, in which case EvaluateProcesses still gets
// an AppStatus/CategoryID match by exe name, just not ExecutionLocation.
func resolveProcessPath(pid int) string {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return ""
	}
	path, err := proc.Exe()
	if err != nil {
		return ""
	}
	return path
}

func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case int:
		return n, true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

// reportPolicyEnforcement tells the server about a policy decision already
// evaluated AND enforced locally (enforceLocalPolicy above) — audit/logging
// only, server/hub.go's handlePolicyEnforcement just persists this via
// InsertEvent without re-evaluating (unlike the generic "event" message
// type, which would run it through EventRecorder.Record → PolicyEngine.Evaluate
// again and could silently overwrite an already-correct "killed" outcome —
// see handlePolicyEnforcement's doc comment).
func reportPolicyEnforcement(agentID, action string, metadata map[string]interface{}) {
	msg := map[string]interface{}{
		"type":       "policy_enforcement",
		"agent_id":   agentID,
		"event_type": "exec_policy",
		"status":     action,
		"metadata":   metadata,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		logMsg("ERROR", "reportPolicyEnforcement marshal failed: %v", err)
		return
	}
	wsSend(data)
}

// rateLimiter is a reset-every-second counter — not a token bucket, just
// enough to stop a pathological fork-bomb-style spike from saturating the
// websocket. Guarded by a mutex even though only this watcher's single
// goroutine calls it today.
type rateLimiter struct {
	mu       sync.Mutex
	max      int
	count    int
	windowAt time.Time
}

func newRateLimiter(max int) *rateLimiter {
	return &rateLimiter{max: max, windowAt: time.Now()}
}

func (r *rateLimiter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.windowAt) >= time.Second {
		r.windowAt = time.Now()
		r.count = 0
	}
	r.count++
	if r.count > r.max {
		if r.count == r.max+1 {
			logMsg("WARN", "procwatch: rate limit exceeded (%d/s), dropping further proc_start events this window", r.max)
		}
		return false
	}
	return true
}
