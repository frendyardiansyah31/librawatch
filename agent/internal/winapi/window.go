// Package winapi holds the low-level Win32 window/message-loop plumbing
// shared by internal/usb (a hidden detector window) and internal/ui (a
// visible warning popup). Neither the syscall bindings nor the struct
// layouts are specific to either caller — factoring them out here avoids
// declaring the same user32.dll procs twice.
package winapi

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procShowWindow          = user32.NewProc("ShowWindow")
	procUpdateWindow        = user32.NewProc("UpdateWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procFlashWindowEx       = user32.NewProc("FlashWindowEx")
	procMessageBeep         = user32.NewProc("MessageBeep")
	procSendMessageW        = user32.NewProc("SendMessageW")
	procLoadIconW           = user32.NewProc("LoadIconW")
)

// Generic window messages/styles needed by both callers. Business-specific
// constants (WM_DEVICECHANGE/DBT_* for the detector, WM_COMMAND control IDs
// for the popup) live with their callers, not here.
const (
	WMDestroy = 0x0002
	WMClose   = 0x0010
	WMQuit    = 0x0012
	WMCreate  = 0x0001
	WMCommand = 0x0111

	WSOverlapped = 0x00000000
	WSCaption    = 0x00C00000
	WSSysMenu    = 0x00080000
	WSChild      = 0x40000000
	WSVisible    = 0x10000000

	WSExTopmost = 0x00000008

	CSHRedraw = 0x0002
	CSVRedraw = 0x0001

	SWShowNormal = 1

	SMCXScreen = 0
	SMCYScreen = 1

	hwndTopmost   = ^uintptr(0) // -1, HWND_TOPMOST
	swpNoMove     = 0x0002
	swpNoSize     = 0x0001
	swpShowWindow = 0x0040

	flashwAll       = 0x00000003
	flashwTimerNoFG = 0x0000000C

	// MBIconExclamation is MB_ICONEXCLAMATION, reused as a MessageBeep sound ID.
	MBIconExclamation = 0x00000030
)

// WndClassExW mirrors WNDCLASSEXW — field order/size matter for the syscall.
type WndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     syscall.Handle
}

type point struct{ X, Y int32 }

// MsgW mirrors MSG.
type MsgW struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// FlashwInfo mirrors FLASHWINFO.
type FlashwInfo struct {
	Size    uint32
	Hwnd    syscall.Handle
	Flags   uint32
	Count   uint32
	Timeout uint32
}

// CurrentModuleHandle returns the current process's module handle, suitable
// as the hInstance for RegisterClass/CreateWindow.
func CurrentModuleHandle() (syscall.Handle, error) {
	var h windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &h); err != nil {
		return 0, fmt.Errorf("GetModuleHandleEx: %w", err)
	}
	return syscall.Handle(h), nil
}

// RegisterClass registers a window class with the given WNDPROC (build one
// via syscall.NewCallback(func(hwnd syscall.Handle, msg uint32, wParam,
// lParam uintptr) uintptr {...})).
func RegisterClass(className string, style uint32, wndProc uintptr, hInstance syscall.Handle) error {
	namePtr, err := syscall.UTF16PtrFromString(className)
	if err != nil {
		return fmt.Errorf("class name: %w", err)
	}
	wc := WndClassExW{
		Style:     style,
		WndProc:   wndProc,
		Instance:  hInstance,
		ClassName: namePtr,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if r == 0 {
		return fmt.Errorf("RegisterClassExW: %w", err)
	}
	return nil
}

// CreateWindowEx wraps CreateWindowExW for both top-level windows (parent=0,
// idOrMenu=0) and child controls (parent=owning hwnd, idOrMenu=control ID).
func CreateWindowEx(exStyle uint32, className, windowName string, style uint32, x, y, w, h int32, parent syscall.Handle, idOrMenu uintptr, hInstance syscall.Handle) (syscall.Handle, error) {
	clsPtr, err := syscall.UTF16PtrFromString(className)
	if err != nil {
		return 0, fmt.Errorf("class name: %w", err)
	}
	namePtr, err := syscall.UTF16PtrFromString(windowName)
	if err != nil {
		return 0, fmt.Errorf("window name: %w", err)
	}
	r, _, err := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(clsPtr)),
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(parent), idOrMenu, uintptr(hInstance), 0,
	)
	if r == 0 {
		return 0, fmt.Errorf("CreateWindowExW(%s): %w", className, err)
	}
	return syscall.Handle(r), nil
}

func DefWindowProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func DestroyWindow(hwnd syscall.Handle) {
	procDestroyWindow.Call(uintptr(hwnd))
}

func PostQuitMessage(exitCode int32) {
	procPostQuitMessage.Call(uintptr(exitCode))
}

// RunMessageLoop pumps GetMessageW/TranslateMessage/DispatchMessageW until
// WM_QUIT. Must run on the same locked OS thread that created the window(s)
// whose messages it's pumping.
func RunMessageLoop() {
	var msg MsgW
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// PostQuitToThread unblocks a RunMessageLoop running on the given OS thread
// (captured via windows.GetCurrentThreadId() before the loop starts) — the
// only safe way to stop GetMessageW from a different goroutine.
func PostQuitToThread(threadID uint32) {
	procPostThreadMessageW.Call(uintptr(threadID), WMQuit, 0, 0)
}

func GetSystemMetrics(index int32) int32 {
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int32(r)
}

func ShowWindow(hwnd syscall.Handle, cmdShow int32) {
	procShowWindow.Call(uintptr(hwnd), uintptr(cmdShow))
}

func UpdateWindow(hwnd syscall.Handle) {
	procUpdateWindow.Call(uintptr(hwnd))
}

func SetForegroundWindow(hwnd syscall.Handle) {
	procSetForegroundWindow.Call(uintptr(hwnd))
}

// SetWindowTopmost is best-effort attention-grabbing: SetForegroundWindow
// from a background-launched process is subject to Windows' foreground-lock
// heuristic and may be silently ignored — FlashWindow (below) is the
// reliable attention-getter, this is a bonus on top of it.
func SetWindowTopmost(hwnd syscall.Handle) {
	procSetWindowPos.Call(uintptr(hwnd), hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
}

func FlashWindow(hwnd syscall.Handle, count uint32) {
	fw := FlashwInfo{Hwnd: hwnd, Flags: flashwAll | flashwTimerNoFG, Count: count}
	fw.Size = uint32(unsafe.Sizeof(fw))
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fw)))
}

func MessageBeep(beepType uint32) {
	procMessageBeep.Call(uintptr(beepType))
}

func SendMessage(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := procSendMessageW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

// LoadSystemIcon loads a built-in system icon (e.g. IDI_WARNING = 32515).
func LoadSystemIcon(iconID uintptr) syscall.Handle {
	r, _, _ := procLoadIconW.Call(0, iconID)
	return syscall.Handle(r)
}
