// Package usb provides event-driven USB storage detection via
// WM_DEVICECHANGE — no polling. Detection only: no UI, no session/process
// concerns.
package usb

import (
	"context"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"library-monitor/agent/internal/winapi"
)

// DriveEvent is published for every confirmed-removable drive arrival.
type DriveEvent struct {
	DriveLetter string
	Time        time.Time
}

// LogFunc lets this package log through the caller's existing logger
// without importing package main (which Go disallows for a "program").
type LogFunc func(level, format string, args ...interface{})

const (
	wmDeviceChange   = 0x0219
	dbtDeviceArrival = 0x8000
	dbtDevTypVolume  = 0x00000002
	dbtfMedia        = 0x0001 // media inserted into an already-known drive, not a new drive letter
	dbtfNet          = 0x0002 // network volume, not USB

	windowClassName = "LibraryAgentUSBDetector"
)

// devBroadcastHdr mirrors DEV_BROADCAST_HDR.
type devBroadcastHdr struct {
	Size       uint32
	DeviceType uint32
	Reserved   uint32
}

// devBroadcastVolume mirrors DEV_BROADCAST_VOLUME (valid only when
// devBroadcastHdr.DeviceType == dbtDevTypVolume).
type devBroadcastVolume struct {
	Size       uint32
	DeviceType uint32
	Reserved   uint32
	UnitMask   uint32
	Flags      uint16
}

// StartDetector spawns its own OS-locked goroutine, creates a hidden
// top-level window (deliberately not HWND_MESSAGE — message-only windows
// are excluded from HWND_BROADCAST, so they never receive WM_DEVICECHANGE),
// and returns a channel of confirmed-removable drive arrivals. The channel
// is closed when ctx is cancelled or setup fails; every setup failure is
// logged via logf first, and this never panics or crashes the caller.
func StartDetector(ctx context.Context, logf LogFunc) <-chan DriveEvent {
	events := make(chan DriveEvent, 8)
	go runDetector(ctx, logf, events)
	return events
}

func runDetector(ctx context.Context, logf LogFunc, events chan<- DriveEvent) {
	defer close(events)

	// HWNDs and message queues are thread-affine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, err := winapi.CurrentModuleHandle()
	if err != nil {
		logf("ERROR", "usb detector: %v", err)
		return
	}

	wndProc := syscall.NewCallback(func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
		handleMessage(msg, wParam, lParam, logf, events)
		if msg == winapi.WMDestroy {
			winapi.PostQuitMessage(0)
			return 0
		}
		return winapi.DefWindowProc(hwnd, msg, wParam, lParam)
	})

	if err := winapi.RegisterClass(windowClassName, 0, wndProc, hInstance); err != nil {
		logf("ERROR", "usb detector: %v", err)
		return
	}

	// No WS_VISIBLE — this window is never shown, it only exists to receive
	// broadcast device-change notifications. Does not require "allow
	// service to interact with desktop" (deprecated/irrelevant here).
	hwnd, err := winapi.CreateWindowEx(0, windowClassName, windowClassName, winapi.WSOverlapped, 0, 0, 0, 0, 0, 0, hInstance)
	if err != nil {
		logf("ERROR", "usb detector: %v", err)
		return
	}
	defer winapi.DestroyWindow(hwnd)

	threadID := windows.GetCurrentThreadId()
	go func() {
		<-ctx.Done()
		winapi.PostQuitToThread(threadID)
	}()

	winapi.RunMessageLoop()
}

func handleMessage(msg uint32, wParam, lParam uintptr, logf LogFunc, events chan<- DriveEvent) {
	if msg != wmDeviceChange || wParam != dbtDeviceArrival {
		return
	}
	hdr := (*devBroadcastHdr)(unsafe.Pointer(lParam))
	if hdr.DeviceType != dbtDevTypVolume {
		return
	}
	vol := (*devBroadcastVolume)(unsafe.Pointer(lParam))
	if vol.Flags&dbtfNet != 0 || vol.Flags&dbtfMedia != 0 {
		return
	}

	for i := 0; i < 26; i++ {
		if vol.UnitMask&(1<<uint(i)) == 0 {
			continue
		}
		letter := string(rune('A'+i)) + ":\\"
		pathPtr, err := syscall.UTF16PtrFromString(letter)
		if err != nil {
			continue
		}
		if windows.GetDriveType(pathPtr) != windows.DRIVE_REMOVABLE {
			continue
		}
		logf("INFO", "USB storage device detected: %s", letter)
		select {
		case events <- DriveEvent{DriveLetter: letter, Time: time.Now()}:
		default:
			logf("WARN", "usb detector: event channel full, dropping %s", letter)
		}
	}
}
