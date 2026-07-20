# openclaw-plugin-librawatch

An OpenClaw plugin that adapts the **LibraWatch** REST API into OpenClaw tools.

This plugin is an **adapter only** — it contains no business logic. It translates
OpenClaw tool calls into HTTP requests defined by `docs/openapi.yaml` (the LibraWatch
Go server's OpenAPI contract) and translates the HTTP responses back. If a capability
isn't defined in that OpenAPI spec, it does not exist in this plugin — nothing here is
invented.

## Architecture

```
OpenClaw  →  LibraWatch Plugin (this package)  →  REST API (OpenAPI)  →  LibraWatch Server (Go)  →  Windows Agents
```

```
src/
├── config.ts             # reads LIBRAWATCH_URL / LIBRAWATCH_API_KEY
├── plugin.ts              # assembles the client + all tools
├── generated/
│   ├── models.ts           # types mirroring the OpenAPI schemas — pure data shapes
│   └── client.ts            # LibraWatchClient — HTTP communication only, no tool logic
└── tools/                    # one file per resource; each adapts OpenClaw calls to LibraWatchClient
```

## Installation

```sh
cd plugins/librawatch
npm install
npm run build
```

A host loads the built plugin via:

```ts
import createLibraWatchPlugin from "openclaw-plugin-librawatch";

const plugin = createLibraWatchPlugin(); // reads env vars
// plugin.tools is an array of { name, description, inputSchema, execute() }
```

## Configuration

| Variable | Required | Description |
|---|---|---|
| `LIBRAWATCH_URL` | Yes | Base URL of the LibraWatch Go server, e.g. `http://localhost:8080`. Must be an absolute `http://` or `https://` URL. |
| `LIBRAWATCH_USERNAME` + `LIBRAWATCH_PASSWORD` | No (recommended) | Login credentials. The client logs in lazily and **re-logs in automatically** before the session expires and on an unexpected 401. Must both be set, or both omitted. |
| `LIBRAWATCH_API_KEY` | No | A static bearer token (e.g. one already obtained via `librawatch_login`). Used as-is, **never auto-refreshed** — only use this for short-lived scripts/tests. If both this and username/password are set, this takes precedence. |

Omit all three if the server's `auth.admin_username` is left empty in its own
`config.yaml` (auth disabled server-side).

## How authentication works

The LibraWatch server has **no JWT and no refresh-token endpoint** for its REST API
(`server/auth.go`): `POST /api/login` returns an opaque random token, valid for exactly
8 hours (`sessionDuration`), tracked in the server's memory — there's no way to extend
or introspect it, only to log in again. This plugin hides that behind
`LIBRAWATCH_USERNAME`/`LIBRAWATCH_PASSWORD`:

- On the first authenticated request, it calls `POST /api/login` and caches the token.
- Before each later request, it re-logs in automatically if the cached token is within
  5 minutes of its known 8-hour expiry — so a request is never sent with a token that's
  about to lapse.
- If the server still rejects a request with `401` anyway (e.g. it restarted and lost
  its in-memory sessions early), the client forces one fresh login and retries the
  request exactly once before giving up.
- Concurrent requests hitting an expired token at the same moment share a single
  in-flight login rather than each triggering their own.

None of this applies to a statically configured `LIBRAWATCH_API_KEY` — it's sent as-is
on every request with no expiry tracking, since the client has no credentials to obtain
a replacement for it if it lapses or gets revoked.

Regardless of mode: `Authorization: Bearer <token>` is sent on every request **except**
the two endpoints the OpenAPI spec marks `security: []`: `POST /api/login` and
`GET /api/file/{filename}`. If no key/credentials are configured at all, no
`Authorization` header is sent — valid when the server has auth disabled.

**A `403` can mean two different things** that look identical over HTTP: an invalid
token, *or* the Go server's separate `admin_cidrs` IP-whitelist middleware rejecting the
plugin's host before auth is even checked. If you get unexpected 403s, check both.
- Three endpoints from the OpenAPI spec are intentionally **not** wrapped by this
  plugin, because they aren't REST data resources:
  - `GET /ws` — a WebSocket upgrade, not modelable with `fetch()`
  - `GET /` — serves the dashboard's `index.html`
  - `GET /static/{file}` — static asset serving

## Deviation from the original task brief

The task brief that specified this plugin listed `ackAlert()` as an example tool.
**No such endpoint exists anywhere in the OpenAPI contract** — only `GET /api/alerts`
(list) exists, with no acknowledge/dismiss operation. Per the brief's own "never invent
endpoints" rule, `ackAlert` was intentionally omitted rather than fabricated.

## Available tools (34)

| File | Tools |
|---|---|
| `tools/auth.ts` | `librawatch_login`, `librawatch_logout` |
| `tools/agents.ts` | `librawatch_list_agents`, `librawatch_get_agent`, `librawatch_update_agent`, `librawatch_delete_agent`, `librawatch_get_agent_metrics`, `librawatch_get_agent_processes`, `librawatch_kill_process`, `librawatch_set_agent_network_mode`, `librawatch_get_agent_logs`, `librawatch_get_agent_events` |
| `tools/alerts.ts` | `librawatch_list_alerts` |
| `tools/applications.ts` | `librawatch_list_applications`, `librawatch_get_application`, `librawatch_update_application` |
| `tools/categories.ts` | `librawatch_list_categories` |
| `tools/events.ts` | `librawatch_list_events` |
| `tools/policyRules.ts` | `librawatch_list_policy_rules`, `librawatch_create_policy_rule`, `librawatch_update_policy_rule`, `librawatch_delete_policy_rule` |
| `tools/settings.ts` | `librawatch_get_settings`, `librawatch_update_settings` |
| `tools/audit.ts` | `librawatch_list_audit_logs` |
| `tools/health.ts` | `librawatch_get_health`, `librawatch_get_stats` |
| `tools/deploy.ts` | `librawatch_list_deploy_jobs`, `librawatch_create_deploy_job`, `librawatch_get_deploy_job`, `librawatch_cancel_deploy_job` |
| `tools/notifications.ts` | `librawatch_test_telegram`, `librawatch_test_email` |
| `tools/logs.ts` | `librawatch_tail_server_logs` |
| `tools/clients.ts` | `librawatch_list_clients_v1` |
| `tools/files.ts` | `librawatch_upload_file`, `librawatch_download_file` |

Tool names use the `librawatch_<verb>_<resource>` convention, prefixed to avoid
collisions if an OpenClaw host loads multiple plugins into one flat tool namespace.
No OpenClaw host-side plugin-loader contract exists anywhere in the source repo this
plugin was built for, so the exact `Tool`/`plugin` shapes here (see `tools/types.ts`,
`plugin.ts`) are this project's own design — adjust them if the real host expects
something different; nothing else needs to change.

## Example tool calls

**List agents**
```jsonc
// input
{}
// output
{
  "ok": true,
  "data": [
    {
      "id": "pc-01",
      "hostname": "LIB-PC-01",
      "status": "online",
      "cpu": 23.5,
      "ram": 61.2,
      "top_process": "chrome.exe",
      "floor": "2",
      "...": "..."
    }
  ]
}
```

**Kill a process**
```jsonc
// input
{ "id": "pc-01", "name": "chrome.exe" }
// output
{ "ok": true, "data": { "output": "SUCCESS: The process \"chrome.exe\" ... has been terminated." } }
```

**Create a deploy job**
```jsonc
// input
{
  "type": "winget",
  "payload": "winget install --id Mozilla.Firefox",
  "targets": ["pc-01", "pc-02"]
}
// output
{
  "ok": true,
  "data": { "id": "job-123", "type": "winget", "status": "pending", "priority": 0, "...": "..." }
}
```

**Error case — agent not found**
```jsonc
// input (librawatch_get_agent)
{ "id": "does-not-exist" }
// output
{ "ok": false, "error": { "status": 404, "message": "Not found (GET /api/agents/does-not-exist)" } }
```

## Error handling

`generated/client.ts` throws on failure (idiomatic for an HTTP client library):
- `LibraWatchApiError` — non-2xx response, with `status`, `endpoint`, and the parsed
  response `body`. Status codes with a specific message: `400`, `401`, `403`, `404`,
  `409` (not currently emitted by the server, kept for forward compatibility), `413`
  (real — `POST /api/upload` file-too-large), `429` (real — `POST /api/login` rate
  limit), `500`. Any other status falls back to a generic `HTTP <status>` message.
- `LibraWatchNetworkError` — the request never got a response at all (DNS failure,
  connection refused, or the internal ~15s request timeout).

`tools/*.ts` never lets either of these escape a tool call: every tool's `execute()`
catches and returns a `ToolOutcome<T>` discriminated union —
`{ ok: true, data: T } | { ok: false, error: { status?, message } }` — via the shared
`toToolError()` helper in `tools/types.ts`. This is the one place error-normalization
logic lives; no tool file duplicates it.

## Development

- `docs/openapi.yaml` (repo root) is the single source of truth for this plugin. If the
  Go server's API changes, update that spec first, then this plugin.
- `npm run typecheck` — strict TypeScript, no `any` anywhere in `generated/models.ts`.
- `npm run build` — emits `dist/` with declaration files.
- No runtime dependencies: no axios, no ORM, no DI framework. Uses Node ≥18 global
  `fetch`/`FormData`/`Blob`/`AbortController`. `@types/node` is a type-only
  `devDependency` (needed for these globals to typecheck under `strict: true`).
