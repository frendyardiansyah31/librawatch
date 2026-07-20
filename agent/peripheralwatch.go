package main

import (
	"context"
	"time"

	"github.com/yusufpapurcu/wmi"
)

const (
	peripheralPollInterval = 5 * time.Second
	// peripheralMissingStreak: number of consecutive polls a device must be
	// absent for before it's reported as removed. Wireless dongles and
	// driver re-enumeration can make a device blip out of WMI for a single
	// poll without it actually being unplugged, so we don't fire on the
	// first miss.
	peripheralMissingStreak = 2
)

// Win32_Keyboard has no Manufacturer property (unlike Win32_PointingDevice) —
// including it in the SELECT clause makes the whole WMI query throw "Invalid
// query" and silently return zero keyboards.
type win32Keyboard struct {
	DeviceID string
	Name     string
}

type win32PointingDevice struct {
	DeviceID     string
	Name         string
	Manufacturer string
}

type peripheralDevice struct {
	DeviceID     string
	Name         string
	Manufacturer string
	Class        string // "keyboard" or "pointing_device"
}

// startPeripheralWatch polls for attached keyboards/mice every
// peripheralPollInterval and emits peripheral_connected/peripheral_removed
// events on change — a tamper-detection signal for library PCs where an
// external mouse/keyboard disappearing usually means it was unplugged and
// pocketed. Win32_Keyboard/Win32_PointingDevice are used instead of
// Win32_PnPEntity + class filtering: Windows reports a device under these
// classes regardless of whether it's PS/2, USB, or Bluetooth, so one query
// pair covers all three transports for free.
func startPeripheralWatch(ctx context.Context, agentID string) {
	known := map[string]peripheralDevice{}
	missingStreak := map[string]int{}

	poll := func() {
		current := queryPeripherals()

		for id, d := range current {
			if _, ok := known[id]; !ok {
				sendEvent(agentID, "peripheral_connected", map[string]interface{}{
					"device_name":  d.Name,
					"device_id":    d.DeviceID,
					"device_class": d.Class,
					"manufacturer": d.Manufacturer,
				})
				known[id] = d
			}
			delete(missingStreak, id)
		}

		for id, d := range known {
			if _, ok := current[id]; ok {
				continue
			}
			missingStreak[id]++
			if missingStreak[id] < peripheralMissingStreak {
				continue
			}
			sendEvent(agentID, "peripheral_removed", map[string]interface{}{
				"device_name":  d.Name,
				"device_id":    d.DeviceID,
				"device_class": d.Class,
				"manufacturer": d.Manufacturer,
			})
			delete(known, id)
			delete(missingStreak, id)
		}
	}

	poll() // establish baseline immediately, don't wait a full interval
	ticker := time.NewTicker(peripheralPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

func queryPeripherals() map[string]peripheralDevice {
	current := map[string]peripheralDevice{}

	var kbs []win32Keyboard
	if err := wmi.Query("SELECT DeviceID, Name FROM Win32_Keyboard", &kbs); err != nil {
		logMsg("ERROR", "peripheralwatch: query Win32_Keyboard failed: %v", err)
	} else {
		for _, k := range kbs {
			current[k.DeviceID] = peripheralDevice{
				DeviceID: k.DeviceID, Name: k.Name, Class: "keyboard",
			}
		}
	}

	var pds []win32PointingDevice
	if err := wmi.Query("SELECT DeviceID, Name, Manufacturer FROM Win32_PointingDevice", &pds); err != nil {
		logMsg("ERROR", "peripheralwatch: query Win32_PointingDevice failed: %v", err)
	} else {
		for _, p := range pds {
			current[p.DeviceID] = peripheralDevice{
				DeviceID: p.DeviceID, Name: p.Name, Manufacturer: p.Manufacturer, Class: "pointing_device",
			}
		}
	}

	return current
}
