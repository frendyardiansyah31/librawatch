package main

import (
	"context"
	"time"

	"github.com/yusufpapurcu/wmi"
)

const usbPollInterval = 8 * time.Second

type win32LogicalDisk struct {
	DeviceID   string
	VolumeName string
	FileSystem string
	Size       uint64
}

type win32DiskDriveUSB struct {
	Model        string
	PNPDeviceID  string
	SerialNumber string
	Size         uint64
}

// startUSBWatch polls for removable drives every usbPollInterval and emits
// usb_inserted/usb_removed events on change (Module 1). WMI polling was
// chosen over a native WM_DEVICECHANGE message-loop window: it's far less
// code and far lower risk for a first implementation, at the cost of a
// detection delay bounded by usbPollInterval instead of being instant.
//
// Win32_LogicalDisk (DriveType=2) is the authoritative signal for "a
// removable volume is mounted" — drive letter, file system, and capacity all
// come from it directly. Vendor/product/serial don't have a cheap direct
// join to a drive letter without WMI associator queries, so on a new drive
// letter appearing this snapshots every currently-attached USB disk
// (Win32_DiskDrive WHERE InterfaceType='USB') and reports them alongside —
// exact for the common single-USB-stick case, best-effort if several USB
// drives are plugged in in the same poll window.
func startUSBWatch(ctx context.Context, agentID string) {
	known := map[string]win32LogicalDisk{}

	poll := func() {
		var disks []win32LogicalDisk
		if err := wmi.Query("SELECT DeviceID, VolumeName, FileSystem, Size FROM Win32_LogicalDisk WHERE DriveType = 2", &disks); err != nil {
			logMsg("ERROR", "usbwatch: query Win32_LogicalDisk failed: %v", err)
			return
		}

		current := make(map[string]win32LogicalDisk, len(disks))
		for _, d := range disks {
			current[d.DeviceID] = d
		}

		for id, d := range current {
			if _, ok := known[id]; !ok {
				usbSources := queryUSBDiskDrives()
				sendEvent(agentID, "usb_inserted", map[string]interface{}{
					"drive_letter": d.DeviceID,
					"volume_name":  d.VolumeName,
					"file_system":  d.FileSystem,
					"capacity":     d.Size,
					"sources":      usbSources,
					"location":     "usb",
				})
			}
		}
		for id, d := range known {
			if _, ok := current[id]; !ok {
				sendEvent(agentID, "usb_removed", map[string]interface{}{
					"drive_letter": d.DeviceID,
					"volume_name":  d.VolumeName,
					"location":     "usb",
				})
			}
		}
		known = current
	}

	poll() // establish baseline immediately, don't wait a full interval
	ticker := time.NewTicker(usbPollInterval)
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

func queryUSBDiskDrives() []map[string]interface{} {
	var drives []win32DiskDriveUSB
	if err := wmi.Query("SELECT Model, PNPDeviceID, SerialNumber, Size FROM Win32_DiskDrive WHERE InterfaceType = 'USB'", &drives); err != nil {
		logMsg("WARN", "usbwatch: query Win32_DiskDrive failed: %v", err)
		return nil
	}
	out := make([]map[string]interface{}, 0, len(drives))
	for _, d := range drives {
		out = append(out, map[string]interface{}{
			"model":         d.Model,
			"pnp_device_id": d.PNPDeviceID,
			"serial_number": d.SerialNumber,
			"capacity":      d.Size,
		})
	}
	return out
}
