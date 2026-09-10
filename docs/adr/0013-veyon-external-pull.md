# 13. Veyon integration as an external pull script (supersedes push/self-report)

Status: Accepted

## Context

The library also runs Veyon for classroom control, which keeps its own
per-machine list of network objects (hostname, IP, MAC, location). That list has
to stay in sync with the actual fleet.

The prior approach (in a different repo, `Z:\monitoring-veyon\`): each PC
self-reported to a Flask receiver `server.py`, which accumulated
`database_ip.json` and regenerated a CSV; a `sync_veyon.py` periodically
`clear` + `import`ed that CSV into Veyon. That's a whole separate collection
pipeline — a receiver service, per-PC report agents, an intermediate JSON store —
duplicating data LibraWatch agents already send over their WebSocket.

## Decision

Sync Veyon by **pulling** from LibraWatch, with a standalone script:

- `veyon_sync.py` (repo root, Python, `requests` only — **not** part of either Go
  module) calls `GET /api/v1/computers`, writes a CSV, and runs
  `veyon-cli.exe networkobjects clear` + `import` — but only when the data
  actually changed since the last **successful** sync (commits `8141d40`,
  `227...`, `191...` in `SESSION_MEMORY.md` 2026-08-07).
- The server side is just a read-only endpoint (`server/computers.go`) over the
  existing `agents` table. No new table, no receiver, no self-report step —
  agents already report hostname/IP/MAC/floor over their WS connection.
- Safety guards before ever calling `clear`: abort on an empty API response;
  abort if the fetched count dropped more than `max_drop_percent` (default 50 %)
  vs the last baseline — the sync host doubles as a real Veyon Master with a
  curated list that a bad API response must not wipe.
- Runs elevated (`veyon-cli` needs a writable system config), on a schedule, on
  the Veyon host. The old `Z:\monitoring-veyon\` setup was left untouched;
  cutover timing is the operator's call.

## Consequences

Easier:

- No separate collection infrastructure. The data already exists in LibraWatch;
  Veyon sync is a thin consumer of one endpoint.
- `veyon-cli` requires a file for `import` (confirmed — no stdin/in-memory mode),
  so a CSV step is unavoidable either way; the pull script keeps that as its only
  moving part.
- Kept out of the Go server entirely — a Python script the Veyon admin can read,
  run with `--dry-run`, and schedule independently. A bug in it can't take the
  server down.

Harder:

- Another language and runtime in the repo (Python), though isolated to one file
  + `requirements.txt`.
- The script owns real destructive power (`networkobjects clear`) against a live
  curated list — hence the guards, the "successful-sync" baseline marker (a
  failed run must not be remembered as synced — `SESSION_MEMORY.md` 2026-08-07),
  and the standing rule to never run it for real against production with test
  data.
- MAC format differs between stores (`agents.mac_address` is dash-separated,
  Veyon wants colons); the script normalizes, and agents with no `floor` set land
  in Veyon with an empty location.
- Config in `veyon_sync_config.json` holds a dashboard admin password
  (gitignored); a dedicated read-only account would be better (`docs/CONFIG.md`
  §10 TODO).
