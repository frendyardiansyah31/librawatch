// Package usb provides USB mass-storage device detection via periodic
// SetupDiGetClassDevs enumeration — not WM_DEVICECHANGE. Detection only: no
// UI, no session/process concerns.
//
// This deployment disables USB storage by setting the USBSTOR/UASPStor
// class-driver services to Start=4. An earlier version of this package
// listened for WM_DEVICECHANGE / GUID_DEVINTERFACE_USB_DEVICE instead of
// polling, on the assumption that the USB bus/hub driver publishes that
// device interface independently of whether any class driver binds.
// Verified wrong on real hardware (2026-08-10): RegisterDeviceNotificationW
// registered successfully and the message loop ran, but zero
// WM_DEVICECHANGE arrivals were ever delivered across several confirmed
// physical replugs — even though Device Manager (Show hidden devices) still
// showed the device appearing every time. That device interface is
// apparently only published by whichever driver ends up binding to the
// device, not by the bus/hub driver itself — so with USBSTOR/UASPStor
// disabled and no driver ever binding, it's simply never registered.
//
// Polling the same enumeration Device Manager itself uses
// (SetupDiGetClassDevs with DIGCF_ALLCLASSES|DIGCF_PRESENT, Enumerator
// "USB") sidesteps that: it reflects bus enumeration directly, independent
// of whether any driver ever binds.
package usb

import (
	"context"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Event is published for every confirmed USB mass-storage device arrival.
type Event struct {
	DeviceID string // PnP device instance ID, e.g. USB\VID_0781&PID_5567\0123456789AB
	Time     time.Time
}

// LogFunc lets this package log through the caller's existing logger
// without importing package main (which Go disallows for a "program").
type LogFunc func(level, format string, args ...interface{})

// pollInterval trades a little detection latency for a lot of simplicity —
// a few hundred USB-enumerator device nodes queried every couple seconds is
// negligible next to the rest of the agent's periodic work (metrics,
// process-start tracing, etc).
const pollInterval = 2 * time.Second

const (
	digcfPresent    = 0x00000002
	digcfAllClasses = 0x00000004
)

// setupapi, spDevInfoData, invalidHandleValue, isMassStorageDevice, and
// procSetupDiDestroyDeviceInfoList come from classcheck.go (same package) —
// this file only adds the enumeration-specific procs.
var (
	procSetupDiGetClassDevsW        = setupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInfo       = setupapi.NewProc("SetupDiEnumDeviceInfo")
	procSetupDiGetDeviceInstanceIdW = setupapi.NewProc("SetupDiGetDeviceInstanceIdW")
)

// StartDetector spawns a polling goroutine and returns a channel of
// confirmed USB mass-storage device arrivals. The channel is closed when
// ctx is cancelled; every enumeration failure is logged via logf and
// treated as "no devices this tick" — this never panics or crashes the
// caller.
func StartDetector(ctx context.Context, logf LogFunc) <-chan Event {
	events := make(chan Event, 8)
	go runDetector(ctx, logf, events)
	return events
}

func runDetector(ctx context.Context, logf LogFunc, events chan<- Event) {
	defer close(events)

	logf("DEBUG", "usb detector: polling every %s for USB device arrivals", pollInterval)

	// Devices already present at startup aren't "arrivals" — only devices
	// that show up after this point should trigger a popup.
	known := enumerateUSBInstanceIDs(logf)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := enumerateUSBInstanceIDs(logf)

			for id := range current {
				if known[id] {
					continue
				}
				logf("DEBUG", "usb detector: new USB device %s, checking mass storage class", id)
				if !isMassStorageDevice(id, logf) {
					logf("DEBUG", "usb detector: %s is not a mass storage device, ignoring", id)
					continue
				}
				logf("INFO", "USB mass storage device detected: %s", id)
				select {
				case events <- Event{DeviceID: id, Time: time.Now()}:
				default:
					logf("WARN", "usb detector: event channel full, dropping %s", id)
				}
			}

			for id := range known {
				if !current[id] {
					logf("INFO", "USB device removed: %s", id)
				}
			}

			known = current
		}
	}
}

// enumerateUSBInstanceIDs returns the PnP instance IDs of every currently
// present device under the "USB" enumerator — the same device tree
// Device Manager itself displays (SetupDiGetClassDevs with
// DIGCF_ALLCLASSES|DIGCF_PRESENT), independent of whether any function
// driver has bound to the device.
func enumerateUSBInstanceIDs(logf LogFunc) map[string]bool {
	ids := map[string]bool{}

	enumPtr, err := syscall.UTF16PtrFromString("USB")
	if err != nil {
		logf("WARN", "usb detector: enumerator string: %v", err)
		return ids
	}

	hDevInfo, _, err := procSetupDiGetClassDevsW.Call(0, uintptr(unsafe.Pointer(enumPtr)), 0, uintptr(digcfAllClasses|digcfPresent))
	if hDevInfo == invalidHandleValue {
		logf("WARN", "usb detector: SetupDiGetClassDevsW failed: %v", err)
		return ids
	}
	defer procSetupDiDestroyDeviceInfoList.Call(hDevInfo)

	buf := make([]uint16, 512)
	for i := uint32(0); ; i++ {
		var devInfo spDevInfoData
		devInfo.Size = uint32(unsafe.Sizeof(devInfo))
		r, _, _ := procSetupDiEnumDeviceInfo.Call(hDevInfo, uintptr(i), uintptr(unsafe.Pointer(&devInfo)))
		if r == 0 {
			break // ERROR_NO_MORE_ITEMS — normal end of enumeration.
		}

		var required uint32
		r, _, err = procSetupDiGetDeviceInstanceIdW.Call(
			hDevInfo, uintptr(unsafe.Pointer(&devInfo)),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&required)),
		)
		if r == 0 {
			logf("WARN", "usb detector: SetupDiGetDeviceInstanceIdW failed: %v", err)
			continue
		}
		ids[strings.ToUpper(syscall.UTF16ToString(buf))] = true
	}
	return ids
}
