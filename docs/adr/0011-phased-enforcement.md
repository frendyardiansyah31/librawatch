# 11. Enforcement is phased: catalog and policy record before they block

Status: Accepted

## Context

The system can see a lot — every process that runs, every USB insert, every
download, wallpaper/registry/scheduled-task change, every software install. Acting
on all of it automatically is risky: a wrong rule kills a legitimate app fleet-
wide, deletes a user's file, or disables a NIC during an unattended change. On
~60 shared public PCs that's a bad afternoon.

The features were built in phases (commits `b089a08` Phase 1, `7349f04` Phase 2)
and the enforcement scope of each phase was deliberately narrowed.

## Decision

New detection lands in **record/review mode first**; real enforcement is a
separate, later, explicit step.

- **Application Catalog (Phase 1).** Every process an agent reports is deduped
  into `applications` (identity `exe_name + company`). New apps land as
  `pending_review`. Marking one `blocked` in the catalog **does not kill it** —
  actual kill/alert enforcement still runs off the separate text-based
  `settings.blacklist`. The catalog is a record layer (`server/catalog.go`,
  `CLAUDE.md`).
- **Policy Engine (Phase 2).** Events are evaluated against `policy_rules` and
  the decided `action` is stored on the event. `kill` (processes from monitored
  locations) and `delete` (files in Downloads/Desktop/Documents) **do** enforce
  — they reuse the existing kill/delete mechanisms. But `block` for
  USB / wallpaper / config changes is **logged only** — the device isn't
  disabled, the wallpaper isn't reverted. That enforcement tier was explicitly
  deferred (`SESSION_MEMORY.md`, Phase 2 notes).
- Default action for an unmatched event is `log`.

## Consequences

Easier:

- You can watch what the rules *would* do — in the catalog, in the event
  timeline's stored `action` — before letting them do it. Low blast radius while
  tuning.
- Detection features ship without waiting for a safe enforcement mechanism for
  every case (`block` for USB needs device-level work that isn't done).
- The dangerous actions that *are* wired up (`kill`, `delete`) reuse paths that
  were already tested for the manual kill button and Phase 1 auto-kill.

Harder:

- **"Blocked" doesn't always mean blocked.** A catalog `blocked` app keeps
  running unless it's also in `settings.blacklist`; a `block` policy rule on USB
  just writes a log line. This is a genuine footgun and has to be documented
  wherever the word "block" appears (`API.md` spells it out per action).
- Two enforcement lists in Phase 1 (the catalog and `settings.blacklist`) that a
  future phase is meant to unify.
- Someone has to remember to do the second step — promote a reviewed rule from
  logging to enforcing — it doesn't happen on its own.
