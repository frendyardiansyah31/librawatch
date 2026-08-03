// Package ui shows the USB warning window — pure Win32 (no CGO, no external
// GUI toolkit), built on internal/winapi. Only ever imported by the
// "--usb-popup" child-process entry point, never by the long-running agent
// service itself.
package ui

import (
	"runtime"
	"syscall"

	"library-monitor/agent/internal/winapi"
)

const (
	popupTitle = "USB Flash Drive Detected"
	popupBody  = "USB flash drives are not permitted on this computer.\r\n\r\n" +
		"Please remove the flash drive.\r\n\r\n" +
		"If you need assistance, please contact the library staff."

	windowClassName = "LibraryAgentUSBPopup"

	windowWidth  = 420
	windowHeight = 220

	idOKButton = 1001

	ssIcon          = 0x00000003
	ssLeft          = 0x00000000
	bsDefPushButton = 0x00000001
	bnClicked       = 0
	idiWarning      = 32515 // MAKEINTRESOURCE(IDI_WARNING)
	stmSetIcon      = 0x0170

	wsExDlgModalFrame = 0x00000001
)

// LogFunc lets this package log through the caller's existing logger
// without importing package main (which Go disallows for a "program").
type LogFunc func(level, format string, args ...interface{})

// ShowWarning builds and displays the USB warning popup window, blocking
// until the user dismisses it (OK button, or the system close button —
// both end up destroying the window the same way). Never panics or crashes
// the caller: every syscall here is best-effort with logging on failure; if
// the popup truly cannot be displayed, this logs and returns.
func ShowWarning(logf LogFunc) {
	defer func() {
		if r := recover(); r != nil {
			logf("ERROR", "usb popup: panic in ShowWarning: %v", r)
		}
	}()

	// HWNDs and message queues are thread-affine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hInstance, err := winapi.CurrentModuleHandle()
	if err != nil {
		logf("ERROR", "usb popup: %v", err)
		return
	}

	wndProc := syscall.NewCallback(func(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case winapi.WMCreate:
			createControls(hwnd, hInstance, logf)
			return 0
		case winapi.WMCommand:
			if loWord(wParam) == idOKButton && hiWord(wParam) == bnClicked {
				winapi.DestroyWindow(hwnd)
			}
			return 0
		case winapi.WMDestroy:
			logf("INFO", "Popup closed by user")
			winapi.PostQuitMessage(0)
			return 0
		}
		return winapi.DefWindowProc(hwnd, msg, wParam, lParam)
	})

	if err := winapi.RegisterClass(windowClassName, winapi.CSHRedraw|winapi.CSVRedraw, wndProc, hInstance); err != nil {
		logf("ERROR", "usb popup: %v", err)
		return
	}

	// Centered on the primary screen — computed directly at creation time,
	// no need to locate the window afterward.
	screenW := winapi.GetSystemMetrics(winapi.SMCXScreen)
	screenH := winapi.GetSystemMetrics(winapi.SMCYScreen)
	x := (screenW - windowWidth) / 2
	y := (screenH - windowHeight) / 2

	style := uint32(winapi.WSCaption | winapi.WSSysMenu) // fixed-size dialog: no minimize/maximize/resize
	exStyle := uint32(winapi.WSExTopmost | wsExDlgModalFrame)

	hwnd, err := winapi.CreateWindowEx(exStyle, windowClassName, popupTitle, style, x, y, windowWidth, windowHeight, 0, 0, hInstance)
	if err != nil {
		logf("ERROR", "usb popup: %v", err)
		return
	}

	winapi.ShowWindow(hwnd, winapi.SWShowNormal)
	winapi.UpdateWindow(hwnd)
	// Attention-grabbing: FlashWindow is the reliable one — SetForegroundWindow
	// from a background-launched process is subject to Windows' foreground-lock
	// heuristic and may be silently ignored, which is expected, not a bug.
	winapi.SetForegroundWindow(hwnd)
	winapi.SetWindowTopmost(hwnd)
	winapi.FlashWindow(hwnd, 5)
	winapi.MessageBeep(winapi.MBIconExclamation)

	// "Displaying USB warning popup" is logged by the caller (usbpopup.go's
	// handleUSBPopupEvent), right after a successful spawn — not here, so
	// there's exactly one log line regardless of which branch spawned this
	// process.
	winapi.RunMessageLoop()
}

func createControls(parent, hInstance syscall.Handle, logf LogFunc) {
	iconCtrl, err := winapi.CreateWindowEx(0, "STATIC", "", winapi.WSChild|winapi.WSVisible|ssIcon,
		20, 20, 32, 32, parent, 0, hInstance)
	if err != nil {
		logf("WARN", "usb popup: create icon control failed: %v", err)
	} else if icon := winapi.LoadSystemIcon(idiWarning); icon != 0 {
		winapi.SendMessage(iconCtrl, stmSetIcon, uintptr(icon), 0)
	}

	if _, err := winapi.CreateWindowEx(0, "STATIC", popupBody, winapi.WSChild|winapi.WSVisible|ssLeft,
		70, 20, windowWidth-100, 110, parent, 0, hInstance); err != nil {
		logf("WARN", "usb popup: create text control failed: %v", err)
	}

	if _, err := winapi.CreateWindowEx(0, "BUTTON", "OK", winapi.WSChild|winapi.WSVisible|bsDefPushButton,
		(windowWidth-90)/2, 150, 90, 30, parent, idOKButton, hInstance); err != nil {
		logf("WARN", "usb popup: create OK button failed: %v", err)
	}
}

func loWord(v uintptr) uint16 { return uint16(v & 0xFFFF) }
func hiWord(v uintptr) uint16 { return uint16((v >> 16) & 0xFFFF) }
