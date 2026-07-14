# Reliable Command Queue — Test Plan

Companion to `manual_test.md`, scoped to the priority/retry/lease-timeout/
expiry/one-command-at-a-time extension of the existing Deploy system
(`server/deploy.go`, `server/db.go`, `server/hub.go`, `server/mcp.go`,
`agent/executor.go`, `dashboard/app.js`). Every scenario is written as
**Arrange / Act / Assert**, and tagged **Positive** (feature works as
intended) or **Negative** (bad/edge input, failure path, or a regression
guard) — same convention already used in `server/auth_test.go` and
`server/sanitize_test.go`.

Run from the project root. Server DB is SQLite at `server/data/library.db` —
delete it (or point `database.path` in `config.yaml` at a scratch file)
before a clean run if you need a blank slate.

---

## 0. Automated unit tests (run first — fastest feedback)

`server/deploy_test.go` covers the pure logic without needing a running
agent. Run it before any manual scenario below:

```bash
cd server && go test ./... -run 'TestIsPendingLikeStatus|TestAcquireNextJob|TestUpdateDeployResult' -v
```

| Test | Type | Arrange | Act | Assert |
|---|---|---|---|---|
| `TestIsPendingLikeStatus` | Positive | table of every status string | call `isPendingLikeStatus` | `pending`/`running` → true, all terminal values → false |
| `TestAcquireNextJob_HigherPriorityFirst` | Positive | two pending jobs for one agent, priority 0 and 5 | `AcquireNextJob` | the priority-5 job is claimed, regardless of creation order |
| `TestAcquireNextJob_FIFOWithinSamePriority` | Positive | two pending jobs, same priority, different `created_at` | `AcquireNextJob` | the older job is claimed |
| `TestAcquireNextJob_NothingClaimableWhileOneRunning` | Negative | one job already claimed (`running`) | `AcquireNextJob` again for the same agent | returns `(nil, nil)` — no second claim |
| `TestAcquireNextJob_NothingPending_ReturnsNil` | Negative | agent with zero queued jobs | `AcquireNextJob` | returns `(nil, nil)`, no error |
| `TestUpdateDeployResult_RetryBoundary` | Negative (boundary) | job with `max_retry=2`, lease already expired | requeue twice, then let the 3rd timeout hit | 3rd timeout produces terminal `failed`, `executed_at` set |
| `TestUpdateDeployResult_CancelledCannotBeOverwritten` | Negative (regression guard) | job claimed then cancelled | a "late" `UpdateDeployResult(..., "success", ...)` call arrives after cancel | status stays `cancelled`, not overwritten |

`go build ./...` currently fails on a **pre-existing, unrelated** gap
(`MetricsBatcher` undefined in `hub.go`/`main.go` — confirmed present before
this feature's changes too). If your checkout has that file, ignore this
note; otherwise the manual scenarios below need a build that resolves it.

---

## 1. Setup

- [ ] `cd server && go build ./...` — no errors (beyond the pre-existing `MetricsBatcher` gap noted above)
- [ ] `cd agent && GOOS=windows GOARCH=amd64 go build ./...` — no errors
- [ ] Start the server, confirm `GET /api/settings` includes `lease_minutes` and `default_max_retry` (seeded from `config.yaml`'s new `deploy:` section, defaults 10 and 3)
- [ ] For fast iteration, temporarily set `lease_minutes` low via `POST /api/settings` (e.g. `1`) so lease-timeout scenarios don't take 10 minutes to observe
- [ ] Have at least one real or simulated agent available (`go run ./test/ -n 1 -server ws://localhost:8080/ws` works for everything except the actual PowerShell output content)

---

## 2. Core queue behavior

### 2.1 Priority ordering — Positive

- **Arrange**: one agent, currently idle (no in-flight command). Prepare two `exec` payloads, e.g. `Start-Sleep 5; Write-Output "low"` and `Write-Output "high"`.
- **Act**: `POST /api/deploy` the low-priority one first (`priority: 0`), then immediately the high-priority one (`priority: 5`), same target.
- **Assert**: `GET /api/deploy/:id` for the high-priority job shows it reached `running` before the low-priority job's result row leaves `pending` — i.e. the high-priority job runs first even though it was created second.

### 2.2 FIFO within the same priority — Positive

- **Arrange**: same as above but both jobs at `priority: 0`.
- **Act**: create job A, then job B (same target, both `priority: 0`).
- **Assert**: job A's result reaches `running` before job B's.

### 2.3 One command at a time per computer — Positive

- **Arrange**: one agent, idle. Queue 3 `exec` jobs to it back-to-back (e.g. each `Start-Sleep 3; Write-Output "N"`).
- **Act**: watch `GET /api/deploy/:id` for all three jobs over the next ~15s.
- **Assert**: at any given moment, at most one of the three has status `running` for that agent — they execute strictly one after another, never concurrently (this is the behavior that replaces the old "dispatch every pending job at once" fire-and-forget loop).

### 2.4 Offline queuing + auto-delivery on reconnect — Positive

- **Arrange**: stop the agent (or disconnect its network). Queue an `exec` job targeting it.
- **Act**: confirm the result row is `pending` (agent offline, nothing to claim). Bring the agent back online.
- **Assert**: within one heartbeat cycle, the result transitions `pending → running → success`, with no manual re-trigger needed.

---

## 3. Reliability: lease timeout & retry

### 3.1 Crash recovery via lease timeout — Positive

- **Arrange**: `lease_minutes` set low (e.g. 1). Queue an `exec` job with a long-running payload (e.g. `Start-Sleep 300`). Once it reaches `running`, kill the agent process (simulating a crash mid-command) without it sending a result.
- **Act**: wait past the lease deadline (the sweep runs every 30s).
- **Assert**: the result row flips back to `pending`, `retry_count` increments by 1, and `output` contains a reason like `"Lease timeout: retrying"`. Restart the agent.
- **Assert (continued)**: the job is redelivered and runs again without any admin action.

### 3.2 Retry budget exhausted — Negative (boundary)

- **Arrange**: `max_retry: 0` on the job (`POST /api/deploy` with `"max_retry": 0`), target a PC that will stay unreachable past its lease (keep it powered off).
- **Act**: wait past the lease deadline once.
- **Assert**: the result immediately reaches terminal `failed` (no requeue, since `retry_count+1 > max_retry` on the very first timeout) with `output` = `"Lease timeout: retry limit exceeded"`. The job's own status reaches `done`.

### 3.3 Expiry before ever dispatched — Negative (boundary)

- **Arrange**: target an offline PC. `POST /api/deploy` with `"expire_at"` set ~60–90 seconds in the future.
- **Act**: do not bring the PC online; wait past `expire_at`.
- **Assert**: the result reaches `expired` with `output` = `"Expired before execution"`, and the command is never sent even if the PC comes online afterward (its result is already terminal).

### 3.4 `expire_at` validation — Negative

- **Arrange**: none.
- **Act**: `POST /api/deploy` with `"expire_at"` set to a timestamp in the past, and separately with a malformed (non-RFC3339) string.
- **Assert**: both requests return `400` with a descriptive error (`"expire_at must be in the future"` / `"expire_at must be RFC3339"`); no job is created either time.

---

## 4. Cancellation

### 4.1 Cancel a queued (pending) job — Positive

- **Arrange**: queue a job to an offline agent (stays `pending`).
- **Act**: `DELETE /api/deploy/:id`.
- **Assert**: result status → `cancelled`, `output` = `"Job dibatalkan oleh admin"`.

### 4.2 Cancel a job that's currently running — Positive

- **Arrange**: queue a long-running `exec` job (e.g. `Start-Sleep 60`), wait for it to reach `running`.
- **Act**: `DELETE /api/deploy/:id` while it's still running.
- **Assert**: result status → `cancelled` immediately (this is the widened `CancelDeployJob` behavior — previously only `pending` rows could be cancelled).

### 4.3 Late agent reply after cancel — Negative (regression guard)

- **Arrange**: same as 4.2 — cancel a running job.
- **Act**: let the agent's real command finish and send its `exec_result` (it doesn't know it was cancelled).
- **Assert**: the result **stays** `cancelled` — the late `success`/`error` reply must not overwrite it (covered by `TestUpdateDeployResult_CancelledCannotBeOverwritten`, re-verify here against the real WebSocket path).

---

## 5. Regression checks — existing deploy types unaffected

### 5.1 winget / file_deploy / deepfreeze / install_ssh still work — Positive

- **Arrange**: one online agent.
- **Act**: run each of the 4 existing dashboard tabs once (winget install of a small package, file_deploy of a `.bat`, deepfreeze `query_df`, install_ssh).
- **Assert**: all four behave exactly as before — no new required fields, no changed payload shapes, results display normally.

### 5.2 `check_deepfreeze_status` MCP tool — Positive (validates the required `mcp.go` fix)

- **Arrange**: agent online, not busy with another command.
- **Act**: call the `check_deepfreeze_status` MCP tool.
- **Assert**: returns `frozen`/`thawed` correctly within the 8s poll window — confirms `!isPendingLikeStatus(r.Status)` (not the old `r.Status != "pending"`) correctly treats the new `running` state as "still waiting," not "agent replied."

### 5.3 `check_deepfreeze_status` against a busy agent — Negative (documented tradeoff, not a bug)

- **Arrange**: queue a long-running `exec` job to an agent so it's `running` another command.
- **Act**: call `check_deepfreeze_status` for the same PC.
- **Assert**: the query_df job is created but queues behind the running command (one-at-a-time enforcement); if it doesn't get a result within the 8s poll window, the tool reports `"unknown"` — expected behavior now that a busy agent can't run two commands at once, not a regression.

### 5.4 Dashboard job-card polling — Positive (validates the required `app.js` fix)

- **Arrange**: queue any `exec` job.
- **Act**: watch the Deploy tab from creation through completion.
- **Assert**: the job card keeps polling/updating while status is `pending` or `running`, and only stops once it reaches a terminal state (`success`/`error`/`ok`/`cancelled`/`expired`/`failed`) — confirms `startJobPoller`'s fixed `!['pending','running'].includes(r.status)` check.

---

## 6. Agent-side execution details

### 6.1 Exit code and duration captured — Positive

- **Arrange**: queue `exec` payload `exit 3` (or equivalent — a command with a known non-zero exit code) and separately `Write-Output ok` (exit 0).
- **Act**: let both run to completion.
- **Assert**: `GET /api/deploy/:id` results show `exit_code: 3` / `exit_code: 0` respectively, and a plausible `duration_ms` > 0 for both.

### 6.2 Command timeout — Negative

- **Arrange**: queue `exec` payload `Start-Sleep 700` (exceeds the 10-minute `execTimeout` in `agent/executor.go`).
- **Act**: wait for the timeout to fire.
- **Assert**: result status `error`, output contains `"[timeout: command exceeded execution time limit]"`, `exit_code: -1`.

### 6.3 `deepfreeze`/`install_ssh` results carry no exit_code/duration — Positive (backward compatibility)

- **Arrange**: run a deepfreeze `query_df` or install_ssh job.
- **Act**: inspect the result via `GET /api/deploy/:id`.
- **Assert**: `exit_code`/`duration_ms` are absent/null (these agent handlers were intentionally left untouched and never send them) — confirms no crash or default-zero misrepresentation server-side.

---

## 7. Settings

### 7.1 `lease_minutes` / `default_max_retry` are live-tunable — Positive

- **Arrange**: note current lease behavior (e.g. 10 minutes).
- **Act**: `POST /api/settings` with a new `lease_minutes` value, no server restart.
- **Assert**: the next lease-sweep cycle uses the new value (verify by timing scenario 3.1 again with the updated setting).

---

## Pass/Fail summary

All sections above must pass with no regressions in section 5 before this feature is considered done. Section 0 (automated tests) should be part of CI/pre-merge; sections 1–4 and 6–7 are manual/exploratory and should be re-run once after any further change to `server/deploy.go`, `server/db.go`, or `agent/executor.go`.
