# 4. Embedded SQLite as the only datastore, single writer connection

Status: Accepted

## Context

The server stores agents, metrics history, alerts, events, the application
catalog, software inventory, policy rules, deploy jobs and results, audit logs,
and settings. Load: ~60 agents, each sending metrics every 15 s, plus events and
occasional deploy jobs. That's a low write rate and a single-digit-GB dataset
ceiling.

Operationally this runs on one Windows box maintained by one person. A separate
database server (Postgres/MySQL) would mean another service to install, secure,
back up, patch, and debug — for a workload a laptop wouldn't notice.

The project is also CGO-free (ADR 0001), which rules out the canonical
C-based `mattn/go-sqlite3`.

## Decision

Use SQLite via `modernc.org/sqlite` (pure Go), one file at
`data/library.db`, opened once at startup by `initDB` (`server/db.go`):

- `SetMaxOpenConns(1)` — one connection, so every access is serialized and
  `busy_timeout` applies uniformly. SQLite has one writer anyway; this makes the
  behavior predictable instead of surfacing as sporadic `SQLITE_BUSY`.
- `PRAGMA journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`,
  `foreign_keys=ON`, a 64 MB page cache, `temp_store=MEMORY`.
- All schema and all queries live in `server/db.go`.

No ORM. Hand-written SQL against `database/sql`.

## Consequences

Easier:

- Zero database ops. Backup is copying three files (`.db`, `-wal`, `-shm`). Moving
  the server is moving a folder.
- No network hop, no connection pool tuning, no auth between app and DB.
- The whole data layer is one file you can read top to bottom.

Harder:

- **One writer.** A long write transaction blocks all other access for its
  duration (`server/db.go` notes this around the catalog upsert). Fine at current
  load; it is a real ceiling if write volume grows a lot.
- **No horizontal scale, no HA.** One process, one disk. If the box is down, the
  system is down. Acceptable for a single-site library fleet; it would not be for
  a multi-site deployment.
- **No built-in backup and no corruption recovery.** Nothing snapshots the file
  and there's no integrity check on start (`docs/RUNBOOK.md` §C flags this as a
  gap to close).
- Pure-Go SQLite is less battle-tested than the C library (see ADR 0001).
- Hand-written SQL means schema changes are manual and touch many call sites
  (see ADR 0005, ADR 0006).
