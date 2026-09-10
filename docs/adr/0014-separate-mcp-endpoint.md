# 14. Separate `/mcp` endpoint with its own bearer token for machine clients

Status: Accepted

## Context

There's a want for machine/AI clients (an OpenClaw bot, Telegram via OpenClaw,
future CLI) to run a few high-value fleet actions: list online PCs, restart /
shutdown, Deep Freeze freeze/thaw/status, kill a process by name. A TypeScript
adapter over the REST API already exists (`plugins/librawatch`, commit `a7b3a49`).

The dashboard auth is a bcrypt login that mints an 8-hour session token
(`server/auth.go`). That's wrong for an unattended machine client — it can't do
an interactive login, and an 8-hour expiry means it breaks nightly. Handing a bot
a real admin session also couples two very different trust levels.

## Decision

A separate endpoint, `POST/GET /mcp` (`server/mcp.go`), speaking MCP over
Streamable HTTP, with **its own** static bearer token `auth.mcp_token` from
`config.yaml` — checked by a dedicated `mcpAuth` middleware, independent of the
dashboard session middleware (`server/main.go`). Empty token = `/mcp` auth
disabled, same convention as the agent WS token (ADR 0007).

It still sits behind the admin IP allowlist (`auth.admin_cidrs`) if that's set.

Tools are a deliberately small, fixed set: `get_online_pcs`, `restart_pc`,
`shutdown_pc`, `freeze_pc`, `thaw_pc`, `check_deepfreeze_status`, `kill_process`.
Each resolves a hostname to an agent and dispatches through the **same deploy
queue** as the dashboard (ADR 0009) — canned payloads, never free-text from the
caller. Deep Freeze password is injected server-side, never returned. Actions are
audited with `ip="mcp"`.

## Consequences

Easier:

- Machine clients authenticate with one long-lived static token they can hold in
  env/secret config — no login flow, no refresh, no nightly breakage.
- The bot's blast radius is the seven tools, not the whole API. It can't create
  arbitrary deploy jobs or edit settings.
- Rotating the MCP token doesn't touch dashboard sessions and vice versa
  (`docs/CONFIG.md` §9).
- MCP is a standard protocol, so off-the-shelf MCP clients work.

Harder:

- A second auth mechanism to keep straight (`docs/CONFIG.md` lists both).
- A static non-expiring token is a weaker credential than a short session — its
  security rests on `config.yaml` file permissions and the IP allowlist.
- The tool set is intentionally rigid — anything new is a code change in
  `mcp.go`, not a config toggle. That's the point, but it means the endpoint
  isn't a general API.
- `/mcp` is excluded from `docs/openapi.yaml` (different protocol), so it's
  documented only in `API.md` and here.
