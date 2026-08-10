package usb

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	setupapi                              = syscall.NewLazyDLL("setupapi.dll")
	procSetupDiCreateDeviceInfoList       = setupapi.NewProc("SetupDiCreateDeviceInfoList")
	procSetupDiOpenDeviceInfoW            = setupapi.NewProc("SetupDiOpenDeviceInfoW")
	procSetupDiGetDeviceRegistryPropertyW = setupapi.NewProc("SetupDiGetDeviceRegistryPropertyW")
	procSetupDiDestroyDeviceInfoList      = setupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	spdrpCompatibleIDs  = 0x00000002
	invalidHandleValue  = ^uintptr(0)
	massStorageIDPrefix = `USB\CLASS_08`
)

// spDevInfoData mirrors SP_DEVINFO_DATA.
type spDevInfoData struct {
	Size      uint32
	ClassGuid windows.GUID
	DevInst   uint32
	Reserved  uintptr
}

// isMassStorageDevice reports whether the device with the given PnP instance
// ID declares a USB Mass Storage (Class_08) compatible ID. Compatible IDs
// are derived by the bus driver directly from the device's USB descriptor
// during enumeration, so this is available even when no class driver
// (USBSTOR/UASPStor) ever binds to the device. Any failure (device already
// gone, transient SetupAPI error) is logged and treated as "not a match" —
// this is a best-effort filter, not a security boundary, so it never retries
// or blocks.
func isMassStorageDevice(instanceID string, logf LogFunc) bool {
	hDevInfo, _, err := procSetupDiCreateDeviceInfoList.Call(0, 0)
	if hDevInfo == invalidHandleValue {
		logf("WARN", "usb detector: SetupDiCreateDeviceInfoList failed: %v", err)
		return false
	}
	defer procSetupDiDestroyDeviceInfoList.Call(hDevInfo)

	idPtr, err := syscall.UTF16PtrFromString(instanceID)
	if err != nil {
		logf("WARN", "usb detector: instance id %q: %v", instanceID, err)
		return false
	}

	var devInfo spDevInfoData
	devInfo.Size = uint32(unsafe.Sizeof(devInfo))
	r, _, err := procSetupDiOpenDeviceInfoW.Call(hDevInfo, uintptr(unsafe.Pointer(idPtr)), 0, 0, uintptr(unsafe.Pointer(&devInfo)))
	if r == 0 {
		logf("WARN", "usb detector: SetupDiOpenDeviceInfoW(%s) failed: %v", instanceID, err)
		return false
	}

	// Compatible ID lists for USB devices are a handful of short strings
	// (e.g. "USB\Class_08&SubClass_06&Prot_50", "USB\Class_08&SubClass_06",
	// "USB\Class_08") — 512 UTF-16 units is generous headroom, not a tight
	// fit, so a fixed buffer (no retry-with-bigger-buffer loop) is fine here.
	buf := make([]uint16, 512)
	var required uint32
	r, _, err = procSetupDiGetDeviceRegistryPropertyW.Call(
		hDevInfo, uintptr(unsafe.Pointer(&devInfo)), spdrpCompatibleIDs,
		0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&required)),
	)
	if r == 0 {
		// Devices with no compatible IDs at all (ERROR_INVALID_DATA) hit
		// this path too — that just means "not a match", not a real error.
		logf("DEBUG", "usb detector: %s has no readable compatible IDs: %v", instanceID, err)
		return false
	}

	match := multiSZContainsPrefix(buf, massStorageIDPrefix)
	logf("DEBUG", "usb detector: %s compatible-ID mass storage match=%v", instanceID, match)
	return match
}

// multiSZContainsPrefix scans a REG_MULTI_SZ (sequence of null-terminated
// UTF-16 strings, double-null terminated) for an entry starting with prefix
// (case-insensitive — compatible IDs are conventionally upper-case but that
// isn't guaranteed).
func multiSZContainsPrefix(buf []uint16, prefix string) bool {
	start := 0
	for i, c := range buf {
		if c != 0 {
			continue
		}
		if i > start && strings.HasPrefix(strings.ToUpper(windows.UTF16ToString(buf[start:i])), prefix) {
			return true
		}
		if i+1 >= len(buf) || buf[i+1] == 0 {
			return false // double-null terminator
		}
		start = i + 1
	}
	return false
}
