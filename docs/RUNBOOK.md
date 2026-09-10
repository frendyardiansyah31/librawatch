# Runbook

What to do when something breaks. Each entry: what you'll see, whether the code
already handles it, the steps to fix it, and how to confirm it's back.

Written for the person on the spot, not for background reading. Scenarios D, E,
and I are marked **PROVISIONAL** — the recovery path is planned, not something
that's been run for real at fleet scale.

---

## Before you touch anything

| Thing | Where |
|---|---|
| Server log | `logs/server.log` (next to `library-server.exe`), auto-rotates at 10 MB × 3 |
| Server health | `GET http://<server>:8080/api/health` → `{ok, uptime, agents_online}` |
| Server stats | `GET /api/stats` → `{online, today_alerts}` |
| Agent log (per PC) | `C:\LibraryAgent\agent.log` |
| Server service | `sc query LibraryMonitor` · `net stop` / `net start "LibraryMonitor"` |
| Agent service | `sc query LibraryAgent` (Service installs) — Scheduled Task installs have no service |
| Config | `config.yaml` next to the server exe. `server/config.yaml` is a dead leftover. |
| DB | `data/library.db` (+ `-wal`, `-shm`) |

Quick triage: is the server process up? Does `/api/health` answer? Is
`agents_online` roughly what you expect? That narrows it to one of the sections
below.

To see why a dying service won't stay up, stop it and run the exe in the
foreground — `.\library-server.exe` — so startup errors print to the console
instead of vanishing.

---

## A. Server is down / won't start

**Symptom.** Dashboard unreachable. Every agent shows offline. No new metrics.
`/api/health` times out. `sc query LibraryMonitor` shows STOPPED, or the service
starts then stops within seconds.

**Handled?** No. Startup is fail-fast — on a bad config, an unusable DB path, a
port already in use, or an uncreatable `uploads/` dir, the server logs one line
and exits (`os.Exit(1)`). It does not retry or fall back.

**Fix.**
1. `Get-Content logs\server.log -Tail 40` — the last line names the cause:
   - `config error: ...` → YAML is malformed or a value is the wrong type. Fix
     `config.yaml`. Compare against `config.yaml.EXAMPLE`.
   - `database init failed: ...` → go to section C.
   - `listen tcp :8080: bind: ... address already in use` → another process (or
     an old server instance) holds the port. Find it (`netstat -ano | findstr :8080`),
     stop it, or change `server.port`.
   - `uploads dir creation failed` / `logs dir creation failed` → filesystem
     permissions on the server's working directory.
2. If the service was launched with the wrong working directory, relative paths
   (`./data`, `./dashboard`) resolve against the wrong place. The service is
   supposed to `chdir` to the exe's folder on start; confirm the exe, `config.yaml`,
   and `dashboard/` actually sit together in a permanent location.
3. Fix the cause, then `net start "LibraryMonitor"`.

**Verify.** `/api/health` returns `ok: true`. Agents reconnect on their own
within ~60 s (their backoff cap). `agents_online` climbs back. Metrics tiles
start updating.

---

## B. All agents show offline, but the server is up

**Symptom.** `/api/health` answers and `agents_online` is 0 or far below normal.
Dashboard is a wall of grey tiles. A burst of `offline` alerts.

**Handled?** Partly. Each agent's `connectLoop` retries forever with exponential
backoff (1 s → 60 s cap, plus jitter), so a transient outage self-heals and
fires `recovery` alerts on reconnect. What is **not** recovered: metrics and
events generated while an agent was disconnected are dropped, never queued — a
gap in the timeline is expected after any outage.

**Fix.** Work out which side broke:
1. **Server was just restarted / rebuilt** → nothing to do. Agents come back
   within ≤ 60 s. If they don't, keep going.
2. **`auth.token` was changed on the server only** → every agent is now rejected
   at connect. Either blank `auth.token` again and restart, or push a matching
   `C:\LibraryAgent\token.txt` to the fleet (`push_all.ps1` with `token.txt`
   beside it, or a targeted copy) — see `docs/CONFIG.md` §9 for the cutover.
3. **Server bound to the wrong interface/port** after a `config.yaml` edit
   (`server.host` / `server.port`) → agents are dialing the old address. Restore
   the address or update `server.txt` fleet-wide.
4. **Network** — VLAN, switch, firewall, DHCP change. Out of scope for the app.
   From a PC that shows offline: can it reach `ws://<server>:8080/ws` at all?
   `Test-NetConnection <server> -Port 8080`.

**Verify.** `agents_online` returns to the expected count. `recovery` alerts
land. Pick one PC, confirm its tile updates and its `agent.log` shows
`Connected`.

---

## C. Database problems — locked, corrupt, or disk full

**Symptom.** Server log shows `database is locked`, `disk I/O error`, or
`database disk image is malformed`. Dashboard reads work but writes (settings,
deploy, delete) fail. On a `malformed` error at startup, section A applies too.

**Handled?** Locking: mostly. WAL mode + `busy_timeout=5000` + a single write
connection (`SetMaxOpenConns(1)`) serialize writers, so `locked` should be rare
and momentary. Corruption: **not handled at all** — there is no integrity check
on start, no auto-repair, and **no scheduled backup**. Disk space: logs
auto-rotate and metrics auto-purge hourly, but `uploads/` never self-cleans.

**Fix — `locked` that won't clear.**
1. Something else has the file open: a second `library-server.exe`, a SQLite
   browser, a backup tool mid-copy. Check Task Manager and `sc query`.
2. Close it. The busy server recovers on its own once the other writer releases.

**Fix — `malformed` / corruption.**
1. `net stop "LibraryMonitor"`.
2. Copy `data\library.db`, `data\library.db-wal`, `data\library.db-shm` somewhere
   safe before doing anything else.
3. If you have a `sqlite3.exe` (not shipped — grab the SQLite tools bundle):
   `sqlite3 library.db "PRAGMA integrity_check;"`. If it reports rows, try
   `sqlite3 library.db ".recover" | sqlite3 recovered.db` and swap `recovered.db`
   in as `library.db`.
4. If recovery fails and there's no backup, the pragmatic reset is to delete
   `library.db*` and let the server rebuild an empty schema on next start —
   **you lose all history, agents, alerts, policy rules, and settings**. Agents
   re-register on reconnect; everything else is gone.

**Fix — disk full.**
1. Free space on the server's drive. Check `data/`, `logs/`, and especially
   `uploads/` (deploy installers pile up there and nothing prunes them).
2. Delete old files from `uploads/` by hand.

**Verify.** Server starts clean. `/api/health` is `ok`. Change a value on the
dashboard Settings page, restart the server, confirm it persisted.

---

## D. A new server build is worse than the old one — roll back  *(PROVISIONAL)*

**Symptom.** Right after replacing `library-server.exe` and `net start`, health
fails, the dashboard is broken, or agents can't connect.

**Handled?** No. There is no versioned release, no blue/green. The only
mitigation is keeping the previous exe.

**Fix.**
1. `net stop "LibraryMonitor"`.
2. Put the previous `library-server.exe` back. (You kept a copy before
   overwriting. If not — rebuild from the last known-good commit.)
3. `net start "LibraryMonitor"`.
4. Schema note: migrations are additive and idempotent
   (`CREATE TABLE IF NOT EXISTS`, no down-migrations). An older binary against a
   newer schema is normally fine — it just ignores columns it doesn't know. The
   one real risk is a migration that added a `NOT NULL` column the old binary
   doesn't populate on insert; check the schema diff between the two builds
   before assuming a clean rollback.

**Verify.** `/api/health` `ok`. Agents reconnect. Spot-check the feature that
broke.

---

## E. A bad agent build went out to the fleet  *(PROVISIONAL)*

**Symptom.** After a `push_all.ps1` run, agents drop offline and don't return,
or a PC spikes CPU, or `C:\LibraryAgent\agent.log` shows a crash loop.

**Handled?** Depends on the install type. The **Service** install (`install.bat`)
is set to restart on failure — a crashing agent just restarts and crashes again.
The **Scheduled Task** install (`push_all.ps1`) has no restart-on-failure — a
crashed agent stays down until the next boot.

**Fix.**
1. Scope it: one or two PCs, or the whole fleet? Check `agents_online`.
2. On one affected PC (console or RDP), read `C:\LibraryAgent\agent.log` for the
   panic / error.
3. Roll back by pushing the previous `agent.exe` again — same `push_all.ps1` or
   `install.bat` with the old binary. There is no version pin. `C:\LibraryAgent\id.txt`
   survives, so each PC keeps its identity and history.
4. If a self-update was interrupted mid-swap, `C:\LibraryAgent\pending_self_update.json`
   is the checkpoint the agent uses to finish or unwind on next start. This path
   is lightly tested — if a PC is stuck, deleting that file and dropping a
   known-good `agent.exe` in place by hand is the safe manual reset.

**Verify.** Affected agents reconnect. `agent.log` is quiet. Metrics resume on
those tiles.

---

## F. Deploy / command jobs are stuck

**Symptom.** A job in the Deploy panel sits at `pending` or `running` and never
produces results.

**Handled?** Yes, several ways — most "stuck" jobs are actually working as
designed:
- **Target offline** → the job waits in the queue and is dispatched when that
  agent reconnects (reconnect pump). Nothing to do.
- **`running` past the lease** (`lease_minutes`, default 10) → the lease sweeper
  (every 30 s) requeues it and charges one retry, up to `default_max_retry`
  (default 3), then fails it permanently with `Lease timeout: retry limit
  exceeded`.
- **A destructive command whose lease expired unconfirmed** (restart, shutdown)
  → failed, **not** retried, on purpose — so a PC never reboots twice off one
  request.
- **`expire_at` passed before dispatch** → expired by the pending sweep.
- The agent runs OS-mutating commands one at a time through a single worker, and
  dedupes repeat deliveries for 24 h (`completed_jobs.json`), so a double-send
  won't double-execute.

**Fix.**
1. `GET /api/deploy/:id` — look at per-agent `results` and the job age.
2. Target online? If offline, that's expected; wait or cancel.
3. Genuinely wedged (target online, well past lease, no result, sweeper not
   moving it): `DELETE /api/deploy/:id` to cancel, then re-issue. Check
   `logs/server.log` for `lease sweep query failed` — a sweeper that's erroring
   won't recover jobs.

**Verify.** Re-issued job reaches `done` and `results` fill in per agent.

---

## G. Dashboard is wide open — no login prompt

**Symptom.** The dashboard loads straight to the UI with no login screen.
`/api/*` works with no token.

**Handled?** No — this is the documented behavior when credentials are blank. A
freshly auto-generated `config.yaml` has every secret empty and the server runs
**with auth disabled**, logging only a reminder.

**Fix.**
1. Set `auth.admin_username` and `auth.admin_password` in `config.yaml` (both —
   if either is empty, auth stays off). Plaintext is fine; the server bcrypts it
   at start.
2. Set `auth.admin_cidrs` to restrict `/api/*` to your admin IP(s).
3. `net stop` / `net start "LibraryMonitor"`.
4. If the open window was on a reachable network for any length of time, rotate
   `auth.token` and `auth.mcp_token` too (`docs/CONFIG.md` §9).

**Verify.** Dashboard now prompts for login. `curl /api/health` still works
(it's meant to), but `curl /api/agents` without a token returns 401.

---

## H. Wake-on-LAN sends nothing

**Symptom.** The WoL action does nothing; PCs don't power on.

**Handled?** Yes, with a warning. If a legacy `wol_broadcast_address` setting
exists but `wol.networks` is empty, the server logs a warning at startup and
sends **no** WoL packets until a network profile is added — it will not guess a
subnet from a bare broadcast address.

**Fix.**
1. Add a profile with the target PCs' subnet in CIDR form — `wol.networks` in
   `config.yaml` on first run, or the dashboard Settings page after that
   (`wol_networks` is seed-once; post-first-run the DB value wins).
2. Do not set a broadcast address by hand — it's derived from the CIDR.
3. Confirm the target NICs/BIOS have WoL enabled and the switch forwards
   directed broadcast to that subnet.

**Verify.** `logs/server.log` shows `wake-on-lan request ... broadcast=<addr>
port=9` when you trigger it. The PC powers on.

---

## I. Deep Freeze freeze/thaw returns HTTP 400  *(PROVISIONAL — REST path not yet run against a live agent)*

**Symptom.** `POST /api/agents/:id/deepfreeze` with `action: freeze` or `thaw`
returns 400 `deepfreeze password not configured`. `action: status` still works.

**Handled?** Yes, deliberately. With `deepfreeze.password` blank, freeze/thaw are
refused and only the read-only status check runs.

**Fix.**
1. Set `deepfreeze.password` in `config.yaml` to the password `DFC.exe` expects
   on the agents. It's read fresh each start.
2. `net stop` / `net start "LibraryMonitor"`. Never commit the value.
3. Separately: agents built before the DFC exit-code fix (SESSION_MEMORY
   2026-09-07) always answer `error` when a PC is genuinely FROZEN. If
   `check_deepfreeze_status` / `action: status` returns `error` on a PC you know
   is frozen, that agent needs a rebuild + redeploy.

**Verify.** `freeze` returns `{job_id, status}` with `status` `dispatched` or
`pending`. A follow-up `action: status` returns `frozen` / `thawed`.

---

## J. Telegram / email alerts are silent

**Symptom.** A CPU/RAM threshold or blacklist hit produces a dashboard alert but
no Telegram message / email.

**Handled?** Yes — expected when not configured. A blank Telegram token or blank
SMTP host/user/to disables that channel; everything else keeps running.

**Fix.**
1. These are seed-once keys. After first run, set them on the **dashboard
   Settings page**, not `config.yaml` (edits there do nothing post-seed).
2. `POST /api/test/telegram` / `POST /api/test/email` to test in isolation.
3. Email transport: `smtp_tls` is `starttls` (default) or `ssl`. The `ssl` path
   is coded but not known to have been used against a real server — see
   `docs/CONFIG.md` §10.

**Verify.** The test endpoint returns `ok: true` and a message actually arrives.

---

## K. An agent won't delete — foreign key violation

**Symptom.** `DELETE /api/agents/:id` returns 500, `FOREIGN KEY constraint
failed`.

**Handled?** Mostly. `DeleteAgent` removes child rows explicitly (there is no
`ON DELETE CASCADE` anywhere). But any table added later with
`agent_id REFERENCES agents(id)` must get its own cleanup line in `DeleteAgent`
or this breaks once that table has rows — it has happened twice (SESSION_MEMORY
2026-08-13).

**Fix.**
1. The error names the table. Add a `DELETE FROM <table> WHERE agent_id = ?`
   line to `DeleteAgent` in `server/db.go`, in dependency order.
2. Rebuild the server, restart, retry the delete.

**Verify.** `DELETE /api/agents/:id` returns `{ok: true}`; the agent is gone
from the dashboard.

---

## Recurring gaps to fix properly

Not incidents — known holes that make the incidents above worse:

- **No database backup.** Nothing snapshots `data/library.db`. Add a scheduled
  copy (stop-copy-start, or `VACUUM INTO` against a live DB) before you need one.
- **`uploads/` never self-cleans.** It grows until the disk fills.
- **No versioned releases** for server or agent. Rollback = "keep the old exe".
  Keep the previous binary of each, labelled, before every deploy.
- **Scheduled Task agent installs have no restart-on-failure.** A crashed agent
  on that path stays down until reboot.
