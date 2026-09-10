# 6. Schema migrations as idempotent in-code DDL, no tool, no down-migrations

Status: Accepted

## Context

The schema evolves as features land (Phase 1 catalog, Phase 2 events/policy,
Software Inventory, network mode, floor, MAC address, WoL networks…). Something
has to bring an existing `data/library.db` up to the current shape on deploy.

Options considered: a migration framework (golang-migrate, goose, Atlas) with
numbered up/down SQL files; or applying DDL directly from the server on startup.

This is a single-binary, single-DB, single-operator system (ADR 0004). A
migration tool is another dependency, another CLI step in the deploy, and a
migrations table to keep honest.

## Decision

`DB.migrate()` in `server/db.go` runs on every startup and issues the full schema
as **idempotent DDL** — `CREATE TABLE IF NOT EXISTS` blocks, and additive
`ALTER TABLE ... ADD COLUMN` guarded so re-running is a no-op. New columns are
added with defaults so existing rows stay valid.

There is no migrations tool, no version table, and **no down-migrations**.
Rollback is handled at the binary level (keep the previous `.exe` — ADR 0009's
sibling concern, `docs/RUNBOOK.md` §D), not by reversing schema changes.

## Consequences

Easier:

- Deploy is "replace the exe, restart the service." The schema catches up by
  itself. No separate migrate step to forget.
- No dependency, no migrations table, no ordering file to merge-conflict on.
- An older binary against a newer schema generally still works — it ignores
  columns it doesn't know about (`docs/RUNBOOK.md` §D).

Harder:

- **Only additive changes are safe.** Renaming or dropping a column, changing a
  type, or splitting a table can't be expressed as idempotent guarded DDL — it
  needs a real migration written by hand, carefully, as a one-off.
- **No rollback of a schema change.** If a new migration adds a `NOT NULL` column
  the old binary doesn't populate, rolling the binary back breaks inserts. You
  have to diff the schema before assuming a clean binary rollback.
- `migrate()` grows monotonically — it's a 2000+-line file and the DDL is a large
  part of it. It's readable but it's a lot.
- No record of *when* each table/column was added beyond git history.
