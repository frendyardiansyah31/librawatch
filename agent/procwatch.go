package main

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"time"

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

// startProcessStartWatch subscribes to Win32_ProcessStartTrace, a genuine
// kernel push-notification (not a poll) for "a new process started"
// machine-wide, and forwards each one to the server as a "proc_start"
// message so PolicyEngine.EvaluateProcesses (server/policy.go) can kill a
// blocked app within roughly a second of launch instead of waiting for the
// next sendInterval metrics tick (up to 30s, see main.go). If subscribing
// fails for any reason (e.g. insufficient privilege), this logs a warning
// and returns — the 30s poll keeps working unaffected, so there is no
// destructive failure mode here, unlike e.g. an IFEO registry approach.
func startProcessStartWatch(ctx context.Context, agentID string) {
	// COM STA objects are thread-affine: this goroutine must keep the same
	// OS thread for its entire lifetime, unlike the short-lived wmi.Query
	// calls elsewhere in this package (usbwatch.go, peripheralwatch.go),
	// which don't hold a subscription open across calls.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		logMsg("WARN", "procwatch: CoInitializeEx failed: %v", err)
		return
	}
	defer ole.CoUninitialize()

	locatorUnknown, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		logMsg("WARN", "procwatch: create SWbemLocator failed: %v", err)
		return
	}
	defer locatorUnknown.Release()

	locator, err := locatorUnknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		logMsg("WARN", "procwatch: SWbemLocator QueryInterface failed: %v", err)
		return
	}
	defer locator.Release()

	serviceRaw, err := oleutil.CallMethod(locator, "ConnectServer", ".", "root\\cimv2")
	if err != nil {
		logMsg("WARN", "procwatch: ConnectServer failed: %v", err)
		return
	}
	service := serviceRaw.ToIDispatch()
	if service == nil {
		logMsg("WARN", "procwatch: ConnectServer returned no service")
		return
	}
	defer service.Release()

	eventsRaw, err := oleutil.CallMethod(service, "ExecNotificationQuery", "SELECT * FROM Win32_ProcessStartTrace")
	if err != nil {
		logMsg("WARN", "procwatch: ExecNotificationQuery failed: %v", err)
		return
	}
	events := eventsRaw.ToIDispatch()
	if events == nil {
		logMsg("WARN", "procwatch: ExecNotificationQuery returned no event source")
		return
	}
	defer events.Release()

	logMsg("INFO", "procwatch: Win32_ProcessStartTrace watcher started")
	limiter := newRateLimiter(processStartRateLimit)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		eventRaw, err := oleutil.CallMethod(events, "NextEvent_", processStartEventTimeoutMs)
		if err != nil {
			// Timeout (WBEM_S_TIMEDOUT) or a transient error — either way,
			// loop back and re-check ctx.Done() rather than treating this
			// as fatal.
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
// Win32_ProcessStartTrace instance and forwards it, rate-limited, as a
// proc_start message.
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

	sendProcStart(agentID, name, pid, resolveProcessPath(pid))
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

// sendProcStart mirrors sendEvent (agent/events.go) but carries a single
// Process{name,pid,path} instead of an event_type/metadata pair — the
// server's handleProcessStart (server/hub.go) feeds it straight into
// PolicyEngine.EvaluateProcesses, the same struct shape the 30s metrics tick
// already sends via "processes".
func sendProcStart(agentID, name string, pid int, path string) {
	msg := map[string]interface{}{
		"type":     "proc_start",
		"agent_id": agentID,
		"processes": []map[string]interface{}{
			{"name": name, "pid": pid, "path": path},
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		logMsg("ERROR", "sendProcStart marshal failed: %v", err)
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
