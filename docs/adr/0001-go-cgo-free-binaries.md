# 1. Go for server and agent, CGO-free static binaries

Status: Accepted

## Context

The system needs a server and an agent that runs on ~60 library PCs (Windows 11
Home). Constraints that shaped the choice:

- The agent has to ship as a single file that a `.bat` or a WinRM push can drop
  onto a PC and register as a service — no runtime to install, no dependency
  DLLs to chase.
- The development machine is not set up for C cross-compilation. There is no
  gcc/MinGW toolchain (confirmed repeatedly — see `SESSION_MEMORY.md`
  2026-08-13). Anything needing CGO can't be built here.
- Server and agent share non-trivial logic (identity, policy matching, uninstall
  command parsing) that must behave identically on both sides.
- One maintainer. The stack has to be boring and low-ceremony.

## Decision

Write both the server and the agent in Go, and keep the whole project
**CGO-free**:

- Server: Go + Gin + `modernc.org/sqlite` (a pure-Go SQLite, no C). Builds to
  `library-server.exe`.
- Agent: Go, Win32 work done through hand-rolled `syscall` wrappers
  (`agent/internal/winapi`, `internal/ui`, `internal/usb`), never a CGO GUI
  toolkit. Builds to `deploy/agent.exe` with `-H windowsgui`.
- Shared logic lives in a Go module both import (see ADR 0003).

## Consequences

Easier:

- `go build` produces one static `.exe` per component. Deployment is a file
  copy. No installer, no VC++ redist, no Python/Node runtime on the PCs.
- Cross-compiling Windows binaries from any OS works with no toolchain setup.
- The same language and the same shared package on both sides — a policy rule or
  an uninstall string can't be parsed one way by the server and another by the
  agent.
- `kardianos/service` gives Windows Service support (install/start/stop, SCM
  integration, foreground fallback) without touching the Win32 service API
  directly.

Harder:

- Every Win32 feature the agent needs (USB enumeration, `WTSQueryUserToken` +
  `CreateProcessAsUser`, registry change notifications, drawing the USB popup
  window) is hand-written `syscall` code. It's more verbose and more error-prone
  than calling a library, and it can only really be tested on Windows hardware.
- `go test -race` doesn't run here — `-race` needs `CGO_ENABLED=1`. Concurrency
  correctness is reviewed by hand instead (`SESSION_MEMORY.md` 2026-08-13).
- Pure-Go SQLite is a smaller, less-exercised codebase than the canonical C
  library. Fine at this scale (see ADR 0004), but it's a bet.
