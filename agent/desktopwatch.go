package main

import (
	"context"
	"encoding/binary"

	"golang.org/x/sys/windows"
)

// regWatchTarget is one HKCU value Module 3 watches for changes.
type regWatchTarget struct {
	subkey    string
	valueName string
	eventType string
}

var desktopWatchTargets = []regWatchTarget{
	{`Control Panel\Desktop`, "Wallpaper", "wallpaper_changed"},
	{`Software\Microsoft\Windows\CurrentVersion\Themes`, "CurrentTheme", "theme_changed"},
}

// startDesktopWatch watches wallpaper and theme registry values for changes
// (Module 3) via RegNotifyChangeKeyValue — event-driven, no polling. Restore
// is intentionally not implemented this phase (brief: "do not aggressively
// restore unless policy requires it") — only detection + event emission.
func startDesktopWatch(ctx context.Context, agentID string) {
	for _, t := range desktopWatchTargets {
		go watchRegistryValue(ctx, agentID, t)
	}
}

func watchRegistryValue(ctx context.Context, agentID string, target regWatchTarget) {
	subkeyPtr, err := windows.UTF16PtrFromString(target.subkey)
	if err != nil {
		logMsg("ERROR", "desktopwatch: invalid subkey %s: %v", target.subkey, err)
		return
	}
	var key windows.Handle
	if err := windows.RegOpenKeyEx(windows.HKEY_CURRENT_USER, subkeyPtr, 0,
		windows.KEY_NOTIFY|windows.KEY_QUERY_VALUE, &key); err != nil {
		logMsg("WARN", "desktopwatch: cannot open HKCU\\%s, skipping: %v", target.subkey, err)
		return
	}
	defer windows.RegCloseKey(key)

	lastValue := readRegistryString(key, target.valueName)

	go func() {
		<-ctx.Done()
		windows.RegCloseKey(key) // best-effort unblock of the pending wait below
	}()

	for {
		// RegNotifyChangeKeyValue fires once per call — must be re-armed in
		// a loop to keep watching, which is what this for loop does.
		err := windows.RegNotifyChangeKeyValue(key, false, windows.REG_NOTIFY_CHANGE_LAST_SET, 0, false)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			logMsg("ERROR", "desktopwatch: RegNotifyChangeKeyValue(%s) failed: %v", target.subkey, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		newValue := readRegistryString(key, target.valueName)
		if newValue != lastValue {
			sendEvent(agentID, target.eventType, map[string]interface{}{
				"old_value": lastValue,
				"new_value": newValue,
			})
			lastValue = newValue
		}
	}
}

func readRegistryString(key windows.Handle, valueName string) string {
	namePtr, err := windows.UTF16PtrFromString(valueName)
	if err != nil {
		return ""
	}
	var valType, bufLen uint32
	if err := windows.RegQueryValueEx(key, namePtr, nil, &valType, nil, &bufLen); err != nil || bufLen == 0 {
		return ""
	}
	buf := make([]byte, bufLen)
	if err := windows.RegQueryValueEx(key, namePtr, nil, &valType, &buf[0], &bufLen); err != nil {
		return ""
	}
	u16 := make([]uint16, bufLen/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(buf[i*2 : i*2+2])
	}
	return windows.UTF16ToString(u16)
}
