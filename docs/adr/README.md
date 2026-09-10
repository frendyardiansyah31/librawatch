# Architecture Decision Records

One file per decision, [Michael Nygard format](https://cognitect.com/blog/2011/11/15/documenting-architecture-decisions).
These record *why* a thing is the way it is. If a decision changes, add a new ADR
that supersedes the old one — don't edit or delete the old file.

The language/stack for this project is final (Go). ADRs here are not provisional.

| # | Title | Status |
|---|---|---|
| [0001](0001-go-cgo-free-binaries.md) | Go for server and agent, CGO-free static binaries | Accepted |
| [0002](0002-multi-module-go-work.md) | Three Go modules joined by `go.work`, no repo-root module | Accepted |
| [0003](0003-shared-module.md) | A `shared/` module for logic that must not diverge | Accepted |
| [0004](0004-embedded-sqlite.md) | Embedded SQLite as the only datastore, single writer connection | Accepted |
| [0005](0005-no-cascade-manual-cleanup.md) | Foreign keys on, no `ON DELETE CASCADE`, manual cleanup in `DeleteAgent` | Accepted |
| [0006](0006-in-code-migrations.md) | Schema migrations as idempotent in-code DDL, no tool, no down-migrations | Accepted |
| [0007](0007-public-ws-shared-token.md) | Public `/ws`; agents gated only by one optional shared token | Accepted |
| [0008](0008-agent-runs-as-system.md) | Agent runs as SYSTEM in Session 0; UI via a session-launched child | Accepted |
| [0009](0009-single-deploy-queue.md) | One deploy-job queue for every "command the fleet" feature | Accepted |
| [0010](0010-config-seed-once.md) | `config.yaml` seeds the `settings` table once, then the DB wins | Accepted |
| [0011](0011-phased-enforcement.md) | Enforcement is phased: catalog and policy record before they block | Accepted |
| [0012](0012-vanilla-js-dashboard.md) | Vanilla-JS dashboard served by the Go binary, no frontend build | Accepted |
| [0013](0013-veyon-external-pull.md) | Veyon integration as an external pull script (supersedes push/self-report) | Accepted |
| [0014](0014-separate-mcp-endpoint.md) | Separate `/mcp` endpoint with its own bearer token for machine clients | Accepted |
