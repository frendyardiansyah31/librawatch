# 2. Three Go modules joined by `go.work`, no repo-root module

Status: Accepted

## Context

Server and agent are one product but two programs with very different dependency
sets — the server pulls in Gin, an HTTP router, a SQLite driver, an MCP SDK; the
agent pulls in `gopsutil` and a pile of `golang.org/x/sys/windows`. They release
and deploy separately. They also need to share a small amount of code that must
not drift between them.

A single module for the whole repo would force one `go.mod` with the union of
both dependency trees, and `go build ./...` would try to build everything at once
(including Windows-only agent packages) regardless of what you're working on.

## Decision

Four independent modules, each with its own `go.mod`:

- `server/` — `library-monitor/server`
- `agent/` — `library-monitor/agent`
- `shared/` — `library-monitor/shared`, imported by both (ADR 0003)
- `test/` — a standalone multi-agent load simulator

A repo-root `go.work` ties them together **for editor tooling only** (cross-module
jump-to-definition, one gopls session). There is deliberately **no `go.mod` at the
repo root**.

Build and test are always per-module:

```
cd server && go build -o ../library-server.exe .
cd agent  && go build -o ../deploy/agent.exe .
cd server && go test ./...
```

## Consequences

Easier:

- Each binary carries only the dependencies it actually uses. A server-only
  library never enters the agent's build or its attack surface.
- You can work on, build, and test one side without the other compiling.
- `shared/` is a real versioned import boundary, not just a folder — a change
  there is a deliberate change to a shared contract (ADR 0003).

Harder:

- `go build ./...` / `go test ./...` / `go vet ./...` from the repo root **do not
  work** — there's no module there. Newcomers hit this immediately. It's called
  out in `CLAUDE.md` and `README.md`.
- A shared-code change isn't picked up by the other module until its `go.mod`
  requirement resolves to the new code (`go.work` papers over this locally, which
  can hide a missing `go mod tidy` until CI or a clean build).
- Four `go.mod` files to keep on compatible Go versions. They currently span
  1.22–1.25; `go.work` pins the workspace at 1.25.
