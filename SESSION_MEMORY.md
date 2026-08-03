# Session Memory

Chronological log of non-obvious decisions and findings from AI-assisted work sessions on this repo, kept alongside `CLAUDE.md` so future sessions don't have to rediscover the same things. Newest entries on top.

---

## 2026-08-03 — USB insertion warning popup

### What was built

Event-driven USB flash-drive warning popup for the agent. Windows policy already blocks USB storage — this is purely a "hey, that's why it's not working" notice for the user. New files, all under `agent/`, **no `go.mod` changes**:

- `internal/winapi/window.go` — shared low-level Win32 window/message-loop bindings (`RegisterClassExW`, `CreateWindowExW`, message loop, etc.), used by both packages below to avoid duplicating the same `user32.dll` syscalls twice.
- `internal/usb/detector.go` — hidden top-level window (not `HWND_MESSAGE` — message-only windows don't get broadcast `WM_DEVICECHANGE`) that listens for `DBT_DEVICEARRIVAL`, filters to `DRIVE_REMOVABLE` volumes only, publishes a `DriveEvent` channel. No polling.
- `internal/sessionlaunch/launch.go` — `WTSQueryUserToken` + `CreateProcessAsUser` to launch a process into the currently logged-in user's interactive session from a SYSTEM/Session-0 process. Built entirely on `golang.org/x/sys/windows` (already a dependency) — no hand-rolled WTS/advapi32 bindings needed, that package already wraps the whole chain.
- `internal/ui/popup.go` — the actual warning window: pure Win32 (`STATIC`/`BUTTON` controls, `WS_EX_TOPMOST`, centered via `GetSystemMetrics`, `FlashWindowEx` + `MessageBeep` for attention). No CGO, no external GUI toolkit.
- `agent/usbpopup.go` — orchestration: a single `atomic.Bool` dedupes concurrent popups (busy before spawn, cleared only when the popup process exits); always tries `sessionlaunch.LaunchInSession` first, falls back to a direct `exec.Command` spawn if that errors (covers the foreground/debug run where the process is already interactive).
- `agent/main.go` — two insertion points: a `--usb-popup` branch at the very top of `main()` (child-process entry point, must come before the kardianos service-control-verb check), and one line in `startEventWatchers`.

This is entirely separate from the existing `agent/usbwatch.go` (WMI-polling, reports `usb_inserted`/`usb_removed` to the server for the audit trail) — that file is untouched. The new feature is local-only, never talks to the server.

Full original design plan (file-by-file, with the Win32 struct layouts and syscall sequencing reasoning): `C:\Users\libra\.claude\plans\giggly-percolating-lantern.md` (on the machine this was built on — not part of this repo).

### Key findings (worth re-verifying if anything here seems off later)

1. **`agent.exe` never runs as the logged-in user.** It's always SYSTEM, via one of two mechanisms — see "Deploy paths" below. This is why any GUI popup needs `internal/sessionlaunch`: a window created directly by the agent process is invisible to the user (Session 0 isolation), no matter what GUI toolkit is used.
2. **This dev machine has `CGO_ENABLED=0` and no gcc/MinGW/winget/choco available.** The task originally asked for Fyne (CGO-dependent); it was dropped in favor of hand-rolled Win32 syscalls (same style as the existing `agent/killverify.go`/`agent/desktopwatch.go`) specifically because of this. If a C toolchain gets installed later and CGO-based UI work is wanted again, that constraint no longer applies — but check `go env CGO_ENABLED` and `where gcc` again before assuming either way.
3. **`go vet ./...` reports two "possible misuse of unsafe.Pointer" warnings in `internal/usb/detector.go`** (casting the WNDPROC's `lParam` to `*devBroadcastHdr`/`*devBroadcastVolume`). This is the standard, unavoidable way to interpret an OS-supplied `LPARAM` in a Go Win32 callback — not a real bug, don't "fix" it by restructuring the pointer cast.

### Deploy paths — which script copies where

Came up mid-session because it's easy to mix these up:

| Script | Target | Mechanism | Server URL |
|---|---|---|---|
| `deploy/_run_as_service.bat` | **local machine only** (dev/test loop) | `taskkill` + `net stop`/`net start` against the local Windows Service "LibraryAgent", copies `deploy/agent.exe` → `C:\LibraryAgent\agent.exe` | hardcoded `ws://localhost:8080/ws` |
| `deploy/install.bat` | one target PC, run manually as Admin **on that PC** | `agent.exe install` (kardianos, registers as a real Windows Service, LocalSystem, Session 0) | from `server.txt` next to the installer, or default in `agent/config.go` |
| `deploy/push_all.ps1` | the whole fleet, from `deploy/ips.txt`, over WinRM | `schtasks /Create ... /RU SYSTEM /RL HIGHEST /SC ONSTART` — a **Scheduled Task**, not a service | `-Server` param, default `ws://192.168.1.10:8080/ws` |

Both `install.bat`'s service and `push_all.ps1`'s scheduled task run as SYSTEM/Session 0 — the `sessionlaunch` popup mechanism above has to (and does) work for both, since it doesn't care how the process became SYSTEM, only that it's able to duplicate the console session's token.

Pre-existing, unrelated gap noticed while reading `push_all.ps1`: it `Copy-Item -Force`s over a running `agent.exe` without a `taskkill` first, which can intermittently fail with a locked-file error. Not fixed as part of this session — flagging here in case it bites during a future fleet push.

### Verified this session / still needs real-hardware testing

Verified locally: build is clean (no CGO, `go.mod`/`go.sum` untouched), `agent.exe --usb-popup` shows a centered/topmost window under the real logged-in user session, `WM_CLOSE` correctly logs `"Popup closed by user"` exactly once and the process exits cleanly.

**Not yet verified** — needs an actual deployed service/scheduled task and a real USB drive: the `WTSQueryUserToken`/`CreateProcessAsUser` session-launch path when the agent is genuinely running as SYSTEM, real USB flash drive insertion end-to-end, and the "nobody logged in" edge case (no active console session).
