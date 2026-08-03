package main

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"

	"library-monitor/agent/internal/sessionlaunch"
	"library-monitor/agent/internal/ui"
	"library-monitor/agent/internal/usb"
)

// usbPopupBusy ensures only one USB warning popup is shown at a time: set
// before spawning the popup process, cleared only once that process exits.
// Events arriving while busy are logged and dropped, never queued — once
// the popup closes, the next insertion can show another one.
var usbPopupBusy atomic.Bool

// startUSBPopupWatch wires internal/usb's event-driven detector to a local
// popup manager. This is separate from usbwatch.go's WMI-polling detector
// (which reports usb_inserted/usb_removed to the server for the audit
// trail) — this feature is local-only and never talks to the server.
func startUSBPopupWatch(ctx context.Context) {
	for ev := range usb.StartDetector(ctx, logMsg) {
		handleUSBPopupEvent(ev)
	}
}

func handleUSBPopupEvent(ev usb.DriveEvent) {
	if !usbPopupBusy.CompareAndSwap(false, true) {
		logMsg("INFO", "usbpopup: popup already showing, ignoring %s", ev.DriveLetter)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		logMsg("ERROR", "usbpopup: os.Executable failed: %v", err)
		usbPopupBusy.Store(false)
		return
	}

	var waiter interface{ Wait() error }
	if proc, err := sessionlaunch.LaunchInSession(exePath, []string{"--usb-popup"}, logMsg); err == nil {
		waiter = proc
	} else {
		// The agent normally runs as SYSTEM (service or scheduled task,
		// Session 0), where sessionlaunch is required. This fallback covers
		// the foreground/debug run (deploy/_run_test_agent.bat) where the
		// process is already interactive and sessionlaunch legitimately
		// fails (e.g. no SeTcbPrivilege as a plain user).
		logMsg("INFO", "usbpopup: session launch unavailable (%v), falling back to direct spawn", err)
		cmd := exec.Command(exePath, "--usb-popup")
		if err := cmd.Start(); err != nil {
			logMsg("ERROR", "usbpopup: direct spawn failed: %v", err)
			usbPopupBusy.Store(false)
			return
		}
		waiter = cmd
	}

	logMsg("INFO", "Displaying USB warning popup")
	go func() {
		if err := waiter.Wait(); err != nil {
			logMsg("WARN", "usbpopup: popup process wait error: %v", err)
		}
		usbPopupBusy.Store(false)
	}()
}

// runUSBPopupChild is main()'s entry point when invoked as `agent.exe
// --usb-popup` — a short-lived child process, launched either directly
// (foreground/debug) or into the logged-in user's session via
// sessionlaunch, whose only job is to show the warning window and exit.
func runUSBPopupChild() {
	initLogger()
	ui.ShowWarning(logMsg)
}
