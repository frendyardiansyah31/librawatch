# 9. One deploy-job queue for every "command the fleet" feature

Status: Accepted

## Context

The system grew a lot of "make one or many PCs do something" features, added over
time (commits `2f7c906`, `986f438`, `6503607`, `642edce`, `3347911`):

- Deploy panel: run PowerShell, `winget install/uninstall`, run an uploaded file,
  install SSH
- Deep Freeze freeze/thaw/status
- Restart / shutdown / lock / logout / sleep, broadcast/popup messages
- Software Inventory remote uninstall (MSI / quiet-uninstall / winget tiers)
- Wake-on-LAN, network-mode toggles
- The generic `/api/v1/commands` API and the `/mcp` tools

Each of these needs the same machinery: dispatch to agents that may be offline,
lease a job while it runs, time it out and retry, record a per-agent result,
survive a server restart. Building that once per feature would mean several
parallel half-correct implementations of the same hard thing.

## Decision

**One queue** — the `deploy_jobs` / `deploy_results` tables and the `Deployer`
(`server/deploy.go`) — backs every one of these features. New deploy-shaped
features **extend `validateDeployRequest`** with a new `type`; they do not add a
second queue. Stated as a rule in `CLAUDE.md`.

The queue provides, once, for everyone:

- Offline handling: a job for an offline agent waits and is dispatched on
  reconnect (reconnect pump).
- Lease + retry: `StartLeaseSweeper` (every 30 s) requeues a `running` job past
  its lease, charging a retry up to `default_max_retry`, then fails it.
- **Destructive commands are never retried on an unconfirmed lease expiry** — so
  a restart/shutdown can't fire twice off one request (commit `642edce`).
- Agent side: OS-mutating command types run one-at-a-time through a single
  worker, with 24 h dedup on job ID, so a duplicate delivery can't double-execute
  (`agent/cmdqueue.go`, `agent/cmddedup.go`, commit `b1fb8ee`).
- `expire_at` is passed through to the agent so a stale job self-cancels.

Features layer *on top*: `/api/v1/commands` maps high-level actions onto queue
job types; Software Inventory uninstall creates one job per `(agent, tier)`
because per-endpoint install metadata differs (`SESSION_MEMORY.md` 2026-08-13);
network-mode and WoL borrow the job/result rows for a pollable `job_id` but run
their own synchronous path.

## Consequences

Easier:

- Reliability work (dedup, destructive-no-retry, `expire_at`) was done once and
  every command feature got it at the same time.
- A new "command the fleet" feature is a `validateDeployRequest` case plus an
  agent handler — the queue, offline handling, retries, and audit come for free.
- One place to look when a job is stuck (`docs/RUNBOOK.md` §F), one schema for
  job history and audit.

Harder:

- `validateDeployRequest` is a chokepoint that has to understand every job type's
  payload rules — `exec` free-text, strict `winget` format, MSI GUID,
  quiet-uninstall structural parse, and so on. It's load-bearing security code.
- Features that don't quite fit the "one payload, many targets, async result"
  shape (per-target payloads, synchronous replies) still route through the queue
  and each bends it a little differently — network-mode and WoL insert result
  rows manually with a `deferred` status the generic sweeper doesn't touch.
- A bug in `Deployer` or the sweeper affects every command feature at once.
