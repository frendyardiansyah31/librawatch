# 3. A `shared/` module for logic that must not diverge

Status: Accepted

## Context

Several pieces of logic run on both the server and the agent and have to produce
the exact same answer on each side. If they drift, the bug is subtle and
dangerous:

- **Agent identity.** Server and agent must agree on how an agent is identified.
- **Policy rule matching.** The server evaluates events against `policy_rules` as
  a backstop; the agent evaluates the same rules locally for realtime
  enforcement (commit `6c49025`). Two different matchers = the agent kills a
  process the server thinks is allowed, or vice versa.
- **Uninstall command parsing.** `POST /api/v1/software/uninstall` has the server
  classify and build an uninstall command; the agent then **re-parses and
  re-validates that same command independently** before running it, and checks it
  against its own last-reported inventory (`SESSION_MEMORY.md` 2026-08-13). Both
  sides must apply identical structural rules (absolute `.exe` path, no shell, a
  LOLBin blocklist) or the defense-in-depth check is meaningless.
- **Version comparison.** "Is 1.2 older than 1.2.0?" has to be answered the same
  way wherever it's asked.

Copy-pasting this logic into both `server/` and `agent/` guarantees eventual
drift.

## Decision

Put it in a third module, `shared/` (`library-monitor/shared`), imported by both
`server` and `agent`:

- `identity.go` — agent identity
- `policy.go` — policy rule matching
- `software.go` — `SoftwareIdentity.Key()`, `CompareVersions`,
  `ParseQuietUninstallCommand`

Rule: if server and agent must never disagree about how something is computed,
the computation lives in `shared/` and both call it. Neither side reimplements
it.

## Consequences

Easier:

- One implementation, one set of tests, for each cross-cutting rule. `shared/` is
  the one module where `go test` results are unambiguously meaningful (`server/`
  has a known test-compile break — `SESSION_MEMORY.md` 2026-08-13).
- A change to a shared contract is visibly a change to a shared contract — it's a
  commit to `shared/`, and both importers have to be rebuilt.

Harder:

- Three modules to version-bump together on a shared change (see ADR 0002's
  "Harder").
- The boundary has to be policed. It's tempting to reimplement "just this one
  small check" inline on one side; that's exactly the drift `shared/` exists to
  prevent. `CLAUDE.md` states the rule explicitly.
- `shared/` must stay dependency-light and platform-neutral — it's linked into
  both a Linux-buildable server and a Windows-only agent.
