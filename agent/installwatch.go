package main

import (
	"context"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var uninstallKeyPaths = []string{
	`Software\Microsoft\Windows\CurrentVersion\Uninstall`,
	`Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
}

type installedApp struct {
	DisplayName    string
	Publisher      string
	DisplayVersion string
}

// startInstallWatch implements Module 5: watches the Uninstall registry
// locations (32-bit and 64-bit views) for installs/removals/version changes,
// event-driven via RegNotifyChangeKeyValue with watchSubtree=true (so value
// changes inside a subkey — e.g. DisplayVersion being bumped by an updater —
// are also caught, not just subkeys being added/removed).
func startInstallWatch(ctx context.Context, agentID string) {
	for _, path := range uninstallKeyPaths {
		go watchUninstallKey(ctx, agentID, path)
	}
}

func watchUninstallKey(ctx context.Context, agentID, subkeyPath string) {
	subkeyPtr, err := windows.UTF16PtrFromString(subkeyPath)
	if err != nil {
		logMsg("ERROR", "installwatch: invalid subkey %s: %v", subkeyPath, err)
		return
	}
	var key windows.Handle
	if err := windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, subkeyPtr, 0,
		windows.KEY_NOTIFY|windows.KEY_READ, &key); err != nil {
		logMsg("WARN", "installwatch: cannot open HKLM\\%s, skipping: %v", subkeyPath, err)
		return
	}
	defer windows.RegCloseKey(key)

	lastApps := snapshotInstalledApps(key)

	go func() {
		<-ctx.Done()
		windows.RegCloseKey(key)
	}()

	for {
		// watchSubtree=true: also fires when a value changes inside a
		// subkey (e.g. an installer bumping its own DisplayVersion), not
		// just when a subkey itself is added/removed.
		err := windows.RegNotifyChangeKeyValue(key, true,
			windows.REG_NOTIFY_CHANGE_NAME|windows.REG_NOTIFY_CHANGE_LAST_SET, 0, false)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			logMsg("ERROR", "installwatch: RegNotifyChangeKeyValue(%s) failed: %v", subkeyPath, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		newApps := snapshotInstalledApps(key)
		emitInstallDiff(agentID, lastApps, newApps)
		lastApps = newApps
	}
}

// snapshotInstalledApps enumerates every subkey under an Uninstall root and
// reads its DisplayName/Publisher/DisplayVersion. Subkeys without a
// DisplayName (component/patch entries not meant to be user-visible) are
// skipped, matching how Windows' own "Add/Remove Programs" filters them.
func snapshotInstalledApps(key windows.Handle) map[string]installedApp {
	rk := registry.Key(key)
	subkeyNames, err := rk.ReadSubKeyNames(-1)
	if err != nil {
		return map[string]installedApp{}
	}

	apps := make(map[string]installedApp, len(subkeyNames))
	for _, name := range subkeyNames {
		sub, err := registry.OpenKey(rk, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		displayName, _, err := sub.GetStringValue("DisplayName")
		if err != nil || displayName == "" {
			sub.Close()
			continue
		}
		publisher, _, _ := sub.GetStringValue("Publisher")
		version, _, _ := sub.GetStringValue("DisplayVersion")
		sub.Close()
		apps[name] = installedApp{DisplayName: displayName, Publisher: publisher, DisplayVersion: version}
	}
	return apps
}

func emitInstallDiff(agentID string, before, after map[string]installedApp) {
	for name, app := range after {
		old, existed := before[name]
		if !existed {
			sendEvent(agentID, "software_installed", map[string]interface{}{
				"display_name": app.DisplayName,
				"publisher":    app.Publisher,
				"version":      app.DisplayVersion,
			})
		} else if old.DisplayVersion != app.DisplayVersion {
			sendEvent(agentID, "software_updated", map[string]interface{}{
				"display_name": app.DisplayName,
				"publisher":    app.Publisher,
				"version":      app.DisplayVersion,
				"old_version":  old.DisplayVersion,
			})
		}
	}
	for name, app := range before {
		if _, stillPresent := after[name]; !stillPresent {
			sendEvent(agentID, "software_removed", map[string]interface{}{
				"display_name": app.DisplayName,
				"publisher":    app.Publisher,
				"version":      app.DisplayVersion,
			})
		}
	}
}
