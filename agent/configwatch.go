package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const scheduledTaskPoll = 5 * time.Minute

type runKeyTarget struct {
	root   windows.Handle
	subkey string
	label  string // included in event metadata, e.g. "HKCU\Run"
}

var runKeyTargets = []runKeyTarget{
	{windows.HKEY_CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, `HKCU\Run`},
	{windows.HKEY_CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\RunOnce`, `HKCU\RunOnce`},
	{windows.HKEY_LOCAL_MACHINE, `Software\Microsoft\Windows\CurrentVersion\Run`, `HKLM\Run`},
	{windows.HKEY_LOCAL_MACHINE, `Software\Microsoft\Windows\CurrentVersion\RunOnce`, `HKLM\RunOnce`},
}

// startConfigWatch implements Module 4: event-driven watching of the four
// Run/RunOnce registry locations (RegNotifyChangeKeyValue, same technique as
// Module 3), plus a 5-minute schtasks poll — Scheduled Tasks has no cheap
// change-notification API, and building a full ITaskService COM client just
// to get one instead of polling isn't worth the complexity for this phase
// (brief: "do not overcomplicate implementation").
func startConfigWatch(ctx context.Context, agentID string) {
	for _, t := range runKeyTargets {
		go watchRunKey(ctx, agentID, t)
	}
	go watchScheduledTasks(ctx, agentID)
}

func watchRunKey(ctx context.Context, agentID string, target runKeyTarget) {
	subkeyPtr, err := windows.UTF16PtrFromString(target.subkey)
	if err != nil {
		logMsg("ERROR", "configwatch: invalid subkey %s: %v", target.subkey, err)
		return
	}
	var key windows.Handle
	if err := windows.RegOpenKeyEx(target.root, subkeyPtr, 0,
		windows.KEY_NOTIFY|windows.KEY_QUERY_VALUE, &key); err != nil {
		logMsg("WARN", "configwatch: cannot open %s, skipping: %v", target.label, err)
		return
	}
	defer windows.RegCloseKey(key)

	lastValues := enumerateRegistryValues(key)

	go func() {
		<-ctx.Done()
		windows.RegCloseKey(key)
	}()

	for {
		err := windows.RegNotifyChangeKeyValue(key, false, windows.REG_NOTIFY_CHANGE_LAST_SET, 0, false)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			logMsg("ERROR", "configwatch: RegNotifyChangeKeyValue(%s) failed: %v", target.label, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		newValues := enumerateRegistryValues(key)
		if changes := diffRegistryValues(lastValues, newValues); len(changes) > 0 {
			sendEvent(agentID, "config_changed", map[string]interface{}{
				"location": target.label,
				"changes":  changes,
			})
		}
		lastValues = newValues
	}
}

// diffRegistryValues reports added/removed/changed entry names between two
// snapshots — not full before/after content, to keep event metadata compact
// (a startup entry's command line can be long).
func diffRegistryValues(before, after map[string]string) []string {
	var changes []string
	for name, val := range after {
		if old, ok := before[name]; !ok {
			changes = append(changes, fmt.Sprintf("added: %s", name))
		} else if old != val {
			changes = append(changes, fmt.Sprintf("modified: %s", name))
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			changes = append(changes, fmt.Sprintf("removed: %s", name))
		}
	}
	return changes
}

// enumerateRegistryValues reads every value under key by name, using the
// higher-level registry package (registry.Key shares its underlying handle
// type with windows.Handle, so the same open key used for
// RegNotifyChangeKeyValue above is reused here without reopening). Values
// that aren't strings (rare for Run/RunOnce, which are almost always
// REG_SZ/REG_EXPAND_SZ command lines) are still tracked by name so
// added/removed detection works even if the content diff doesn't.
func enumerateRegistryValues(key windows.Handle) map[string]string {
	rk := registry.Key(key)
	names, err := rk.ReadValueNames(-1)
	if err != nil {
		return map[string]string{}
	}
	values := make(map[string]string, len(names))
	for _, name := range names {
		if s, _, err := rk.GetStringValue(name); err == nil {
			values[name] = s
		} else {
			values[name] = ""
		}
	}
	return values
}

// ─── Scheduled Tasks (polling) ──────────────────────────────────────────────

func watchScheduledTasks(ctx context.Context, agentID string) {
	last := queryScheduledTaskNames()

	ticker := time.NewTicker(scheduledTaskPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := queryScheduledTaskNames()
			var changes []string
			for name := range current {
				if !last[name] {
					changes = append(changes, "added: "+name)
				}
			}
			for name := range last {
				if !current[name] {
					changes = append(changes, "removed: "+name)
				}
			}
			if len(changes) > 0 {
				sendEvent(agentID, "config_changed", map[string]interface{}{
					"location": "Scheduled Tasks",
					"changes":  changes,
				})
			}
			last = current
		}
	}
}

func queryScheduledTaskNames() map[string]bool {
	cmd := exec.Command("schtasks", "/query", "/fo", "csv", "/nh")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		logMsg("WARN", "configwatch: schtasks query failed: %v", err)
		return map[string]bool{}
	}

	names := map[string]bool{}
	r := csv.NewReader(strings.NewReader(string(out)))
	for {
		row, err := r.Read()
		if err != nil {
			break
		}
		if len(row) > 0 && row[0] != "" {
			names[row[0]] = true
		}
	}
	return names
}
