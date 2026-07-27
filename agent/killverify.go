package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/shirou/gopsutil/v3/process"
)

// KillResult is the structured outcome of verifyAndKillPID — Priority 6's
// whole point: today's kill paths (killProcessByPID's old one-shot
// "taskkill /F" call) report success purely from the exec's exit code, with
// no re-check the process is actually gone, no retry, no graceful attempt
// first. This is what every kill path (killByIdentity, handleKillProcess)
// now produces instead.
type KillResult struct {
	Success  bool
	Reason   string // AccessDenied|AlreadyExited|VerificationFailed|Unknown ("" when Success)
	Duration time.Duration
}

// Reason values a failed KillResult can carry.
const (
	KillReasonAccessDenied       = "AccessDenied"
	KillReasonAlreadyExited      = "AlreadyExited"
	KillReasonVerificationFailed = "VerificationFailed"
	KillReasonUnknown            = "Unknown"
)

// killDeps bundles the side-effecting operations verifyAndKillPID needs,
// injected so the retry/verification state machine itself is unit-testable
// with fakes — no real Windows process syscalls needed to exercise the
// pipeline's logic (retry counts, when graceful-close kicks in, reason
// classification).
type killDeps struct {
	// terminate issues one forced-kill attempt, returning the command's
	// combined output (used for reason classification on final failure).
	terminate func() (output string, err error)
	// pidExists reports whether the target process is still alive.
	pidExists func() bool
	// gracefulClose is a best-effort WM_CLOSE attempt — most blocked/
	// blacklisted apps run headless and this simply finds no window to
	// close, which is expected, not an error.
	gracefulClose func()
	sleep         func(time.Duration)
}

// verifyAndKillPID runs: verify already-gone → terminate → wait → verify →
// retry terminate → wait → verify → graceful WM_CLOSE → wait → verify →
// final forced terminate → wait → verify. Stops as soon as any verify step
// confirms the process is gone. deps lets tests inject fakes instead of
// real process syscalls — see the real implementation in
// newRealKillDeps below.
func verifyAndKillPID(deps killDeps) KillResult {
	start := time.Now()

	if !deps.pidExists() {
		return KillResult{Success: true, Reason: KillReasonAlreadyExited, Duration: time.Since(start)}
	}

	const forceAttempts = 2
	var lastOutput string
	for attempt := 1; attempt <= forceAttempts; attempt++ {
		output, _ := deps.terminate()
		lastOutput = output
		deps.sleep(500 * time.Millisecond)
		if !deps.pidExists() {
			return KillResult{Success: true, Duration: time.Since(start)}
		}
	}

	// Still alive after forceAttempts — try a graceful close before the
	// final forced attempt (best-effort; headless processes have no window
	// to close, and that's fine, not a failure of this step).
	deps.gracefulClose()
	deps.sleep(500 * time.Millisecond)
	if !deps.pidExists() {
		return KillResult{Success: true, Duration: time.Since(start)}
	}

	output, _ := deps.terminate()
	lastOutput = output
	deps.sleep(500 * time.Millisecond)
	if !deps.pidExists() {
		return KillResult{Success: true, Duration: time.Since(start)}
	}

	return KillResult{Success: false, Reason: classifyKillFailure(lastOutput), Duration: time.Since(start)}
}

// classifyKillFailure turns taskkill's textual output into one of the
// Reason values documented on KillResult — best-effort text matching
// (taskkill doesn't expose a structured error code), falling back to
// KillReasonVerificationFailed (the process survived every attempt, cause
// unclear) or KillReasonUnknown (no output captured at all) rather than
// inventing a false-precision reason.
func classifyKillFailure(taskkillOutput string) string {
	lower := strings.ToLower(taskkillOutput)
	switch {
	case strings.Contains(lower, "access is denied") || strings.Contains(lower, "access denied"):
		return KillReasonAccessDenied
	case taskkillOutput == "":
		return KillReasonUnknown
	default:
		return KillReasonVerificationFailed
	}
}

// verifiedKillByPID runs verifyAndKillPID with real Windows process
// operations for pid — the entry point every kill path (killByIdentity,
// handleKillProcess) now uses instead of a bare one-shot taskkill.
func verifiedKillByPID(pid int) KillResult {
	return verifyAndKillPID(newRealKillDeps(pid))
}

func newRealKillDeps(pid int) killDeps {
	return killDeps{
		terminate: func() (string, error) {
			cmd := exec.Command("taskkill", "/F", "/PID", fmt.Sprintf("%d", pid))
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			out, err := cmd.CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		pidExists: func() bool {
			alive, err := process.PidExists(int32(pid))
			return err == nil && alive
		},
		gracefulClose: func() { gracefulCloseWindowsForPID(pid) },
		sleep:         time.Sleep,
	}
}

// ─── Graceful WM_CLOSE (best-effort) ────────────────────────────────────────

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
)

const wmClose = 0x0010

// gracefulCloseWindowsForPID posts WM_CLOSE to every visible top-level
// window owned by pid, giving a GUI app one last chance to shut down
// cleanly before the final forced taskkill. Purely best-effort: a headless
// process (the common case for blocked/blacklisted apps — no window at
// all) means this simply finds nothing to post to, which is expected, not
// an error — verifyAndKillPID doesn't check this step's outcome, it just
// re-verifies pidExists afterward same as every other step.
func gracefulCloseWindowsForPID(pid int) {
	targetPID := uint32(pid)
	cb := syscall.NewCallback(func(hwnd syscall.Handle, _ uintptr) uintptr {
		var windowPID uint32
		procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&windowPID)))
		if windowPID == targetPID {
			if visible, _, _ := procIsWindowVisible.Call(uintptr(hwnd)); visible != 0 {
				procPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0)
			}
		}
		return 1 // non-zero: continue enumeration
	})
	procEnumWindows.Call(cb, 0)
}
