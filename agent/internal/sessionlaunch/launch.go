// Package sessionlaunch launches a process inside the currently active
// console session's interactive desktop from a SYSTEM/Session-0 process
// (a Windows service or a SYSTEM-context scheduled task) by duplicating the
// logged-in user's token — the standard way for a non-interactive process
// to show UI to the logged-in user. Built entirely on golang.org/x/sys/windows,
// which already wraps the full WTS/token/CreateProcessAsUser chain.
package sessionlaunch

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LogFunc lets this package log through the caller's existing logger
// without importing package main (which Go disallows for a "program").
type LogFunc func(level, format string, args ...interface{})

// Process wraps a CreateProcessAsUser'd process handle.
type Process struct {
	handle windows.Handle
}

// Wait blocks until the process exits, then releases its handle.
func (p *Process) Wait() error {
	defer windows.CloseHandle(p.handle)
	event, err := windows.WaitForSingleObject(p.handle, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("WaitForSingleObject: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("WaitForSingleObject: unexpected result %#x", event)
	}
	return nil
}

const seTcbPrivilege = "SeTcbPrivilege"

// enableTcbPrivilege best-effort enables SeTcbPrivilege on the current
// process token — WTSQueryUserToken is documented to require it enabled,
// not just present. Never fatal: any failure here is logged and the real
// problem (if any) will surface clearly at WTSQueryUserToken below.
func enableTcbPrivilege(logf LogFunc) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		logf("WARN", "sessionlaunch: OpenProcessToken failed: %v", err)
		return
	}
	defer tok.Close()

	namePtr, err := syscall.UTF16PtrFromString(seTcbPrivilege)
	if err != nil {
		return
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, namePtr, &luid); err != nil {
		logf("WARN", "sessionlaunch: LookupPrivilegeValue(%s) failed: %v", seTcbPrivilege, err)
		return
	}
	priv := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED},
		},
	}
	if err := windows.AdjustTokenPrivileges(tok, false, &priv, 0, nil, nil); err != nil {
		logf("WARN", "sessionlaunch: AdjustTokenPrivileges(%s) failed: %v", seTcbPrivilege, err)
	}
}

// LaunchInSession spawns exePath+args inside the currently active console
// session's interactive desktop (winsta0\default) by duplicating the
// logged-in user's shell token. Returns an error — never panics — if there
// is no active session (nobody logged in — nothing to warn) or any step of
// the WTS/token/CreateProcessAsUser chain fails.
func LaunchInSession(exePath string, args []string, logf LogFunc) (*Process, error) {
	enableTcbPrivilege(logf)

	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == 0xFFFFFFFF {
		return nil, fmt.Errorf("no active console session")
	}

	var userToken windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &userToken); err != nil {
		return nil, fmt.Errorf("WTSQueryUserToken: %w", err)
	}
	defer userToken.Close()

	var dupToken windows.Token
	if err := windows.DuplicateTokenEx(userToken, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &dupToken); err != nil {
		return nil, fmt.Errorf("DuplicateTokenEx: %w", err)
	}
	defer dupToken.Close()

	var envBlock *uint16
	if err := windows.CreateEnvironmentBlock(&envBlock, dupToken, false); err != nil {
		logf("WARN", "sessionlaunch: CreateEnvironmentBlock failed: %v", err)
		envBlock = nil
	} else {
		defer windows.DestroyEnvironmentBlock(envBlock)
	}

	// Intentionally not a general-purpose quoting routine — args here are
	// only ever a literal flag like "--usb-popup" (no spaces/special chars).
	cmdLine := fmt.Sprintf(`"%s" %s`, exePath, strings.Join(args, " "))
	cmdLinePtr, err := syscall.UTF16PtrFromString(cmdLine)
	if err != nil {
		return nil, fmt.Errorf("command line: %w", err)
	}
	desktopPtr, err := syscall.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return nil, fmt.Errorf("desktop name: %w", err)
	}

	si := windows.StartupInfo{
		Desktop:    desktopPtr,
		Flags:      windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_SHOWNORMAL,
	}
	si.Cb = uint32(unsafe.Sizeof(si))

	var pi windows.ProcessInformation
	// CREATE_NO_WINDOW: agent.exe is a console-subsystem binary — without
	// this the child would flash a console window before its own window
	// appears.
	creationFlags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcessAsUser(dupToken, nil, cmdLinePtr, nil, nil, false, creationFlags, envBlock, nil, &si, &pi); err != nil {
		return nil, fmt.Errorf("CreateProcessAsUser: %w", err)
	}
	windows.CloseHandle(pi.Thread)

	return &Process{handle: pi.Process}, nil
}
