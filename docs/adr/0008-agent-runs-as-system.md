# 8. Agent runs as SYSTEM in Session 0; UI reaches the user via a session-launched child

Status: Accepted

## Context

The agent has to do things a normal user account can't: read HKLM, watch
registry keys and scheduled tasks, enumerate USB devices, run installers and
`msiexec`, invoke `DFC.exe`, change network adapters, restart the machine. It
also has to keep running when no one is logged in, and start on boot.

That points at a service (or a `/RU SYSTEM` scheduled task) running as
`LocalSystem`. But `LocalSystem` lives in Session 0, which since Windows Vista is
isolated from interactive desktops — a window created by a Session 0 process is
invisible to the logged-in user.

The agent also needs *some* UI: the USB-storage-blocked popup has to appear on
the screen of whoever just plugged in the drive.

## Decision

The agent **always runs as SYSTEM / Session 0** — as the `LibraryAgent` Windows
Service (`install.bat`) or as a `/RU SYSTEM /RL HIGHEST` scheduled task
(`push_all.ps1`). Never as the logged-in user.

When it needs to show UI, it does **not** create the window itself. It uses
`agent/internal/sessionlaunch` — `WTSQueryUserToken` to get the active console
session's user token, then `CreateProcessAsUser` to spawn a separate child
process **in that session** that draws the window (`internal/ui`,
`internal/winapi`, hand-rolled Win32 per ADR 0001).

## Consequences

Easier:

- Full privilege for every monitoring and enforcement task, with no UAC prompt
  and no dependency on a user being logged in.
- Starts on boot, survives logoff, restarts on failure (Service path).
- One privilege model — everything the agent does runs at the same level.

Harder:

- **Any UI is a two-process dance.** Every user-visible feature needs the
  `sessionlaunch` hop; a window drawn directly by the agent is silently
  invisible. This is a standing constraint noted in `CLAUDE.md` and project
  memory.
- `WTSQueryUserToken` + `CreateProcessAsUser` is fiddly Win32 with real failure
  modes (no active session, locked workstation, fast user switching) that only
  show up on real hardware.
- Running as SYSTEM means agent bugs run as SYSTEM. The uninstall path is
  deliberately hardened against this (server-built command + independent agent
  re-validation + LOLBin blocklist — ADR 0003, `SESSION_MEMORY.md` 2026-08-13).
- The Scheduled Task deploy path (`push_all.ps1`) has no restart-on-failure,
  unlike the Service path (`docs/RUNBOOK.md` §E).
