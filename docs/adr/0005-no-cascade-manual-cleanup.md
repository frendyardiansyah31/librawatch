# 5. Foreign keys enforced, no `ON DELETE CASCADE`, manual cleanup in `DeleteAgent`

Status: Accepted

## Context

Many tables reference `agents(id)` — `metrics`, `processes`, `alerts`, `events`,
`deploy_results`, `app_sightings`, `software_inventory`, network-mode state, and
more. `PRAGMA foreign_keys=ON` is set (ADR 0004), so those references are
enforced: you can't delete an agent row while child rows point at it.

Deleting an agent from the dashboard is a real, expected operation (commit
`3df5d6f`). Something has to remove the children first.

Two ways to do that:

1. Declare every child FK as `ON DELETE CASCADE` and let SQLite fan the delete
   out automatically.
2. Delete the children explicitly in code, in dependency order, inside one
   function.

## Decision

**No `ON DELETE CASCADE` anywhere in the schema.** Agent deletion goes through
`DB.DeleteAgent` (`server/db.go`), which issues an explicit
`DELETE FROM <child> WHERE agent_id = ?` for every table that references the
agent, then deletes the agent row.

The invariant, stated in `CLAUDE.md`: **any new table with
`agent_id REFERENCES agents(id)` must also get a cleanup line added to
`DeleteAgent`.**

## Consequences

Easier:

- Deleting an agent is one function you can read. What gets removed, and in what
  order, is explicit — no action-at-a-distance where adding an FK silently
  changes delete behavior.
- Cascade mistakes (a stray `ON DELETE CASCADE` on the wrong FK wiping data you
  meant to keep) can't happen.
- Deletion order and any extra logic (audit entry, logging) live in one place.

Harder:

- **The list has to be maintained by hand.** Add a table with an `agent_id` FK,
  forget the `DeleteAgent` line, and the next agent deletion fails with a foreign
  key violation once that table has rows. This has happened twice
  (`SESSION_MEMORY.md` 2026-08-13) — it's the failure in `docs/RUNBOOK.md` §K.
- No safety net if someone adds a reference and doesn't know the rule. The
  mitigation is documentation (`CLAUDE.md`) and the runbook entry, not the schema.
- Bulk/other cascade paths (deleting a deploy job, a policy rule) are likewise
  all manual.
