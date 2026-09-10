# Config & Secrets

Every config value and secret read by any part of Librawatch — the Go server, the
Go agent, the `plugins/librawatch` Node client, `veyon_sync.py`, and the deploy
scripts. Use it to find out what a knob does, whether it's required, and where a
secret is supposed to live.

**No real secret values are in this file** — names, formats, and handling rules
only. The live `config.yaml` on the server host has the actual credentials and is
gitignored; keep it that way.

---

## Where config lives

| Surface | Who reads it | Location | In git? |
|---|---|---|---|
| `config.yaml` | Server | Same folder as `library-server.exe`. The server `chdir`s to the exe's directory on startup, then opens `config.yaml` — there is no `-config` flag, no search path. | No — gitignored. Template: `config.yaml.EXAMPLE` |
| `settings` table | Server, at runtime | Inside the SQLite DB (`database.path`) | No (it's DB data) |
| Env vars + `C:\LibraryAgent\*.txt` | Agent | The target PC | No — written per machine at install / first run |
| `LIBRAWATCH_*` env vars | `plugins/librawatch` Node client | Process env / MCP host config | No |
| `veyon_sync_config.json` | `veyon_sync.py` | Repo root, or `--config <path>` | No — gitignored. Template: `veyon_sync_config.example.json` |
| Script params / sidecar files | `deploy/push_all.ps1`, `deploy/install.bat` | Command line, or files next to the script | Partly — `deploy/server.txt` and `deploy/ips.txt` are tracked |

### `config.yaml` vs the `settings` table

Some `config.yaml` keys only exist to **seed the `settings` table on first run**
(`DB.InitDefaultSettings`, `server/db.go`). Once the key has a row in `settings`,
editing `config.yaml` does nothing — the dashboard (`POST /api/settings`) or a
direct DB write wins from then on. Those keys are tagged **seed-once** below.
Everything else in `config.yaml` is read fresh from the file on every start.

The seeding check is "is the `settings` row empty" — so a seed-once key with a
blank value in `config.yaml` will re-seed from `config.yaml` again next start
until something writes a non-empty value.

---

## Server — `config.yaml`

Read by `server/config.go` (`loadConfig`). If the file is missing on startup the
server writes `defaultConfigYAML` (every secret blank) to that path, prints a
"edit it and restart" line, and **keeps running with the blank defaults**. Blank
defaults mean no dashboard login and no agent token check — anyone on the network
gets in. A freshly auto-generated `config.yaml` is insecure until you fill it in.

### `server:`

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `server.port` | HTTP port for dashboard, API, `/ws`, `/mcp` | Optional | config | Default `8080` |
| `server.host` | Bind address | Optional | config | Default `0.0.0.0` |

### `auth:`

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `auth.token` | Shared bearer token agents send on the `/ws` WebSocket | Optional | **secret** | Empty = check disabled, any agent connects. Must match each agent's `C:\LibraryAgent\token.txt`. Read fresh each start. |
| `auth.admin_cidrs` | CIDRs allowed to reach `/api/*`, `/api/v1/*`, `/mcp` | Optional | config | Empty = no IP restriction. Example: `["10.5.39.88/32"]` |
| `auth.admin_username` | Dashboard login username | Optional | config | Empty = **dashboard auth fully disabled** |
| `auth.admin_password` | Dashboard login password | Optional (required if `admin_username` is set) | **secret** | Plain text is fine — the server bcrypts it at startup (`server/auth.go`, `NewAuthManager`). If the value already starts with `$2` it's treated as a pre-computed bcrypt hash and used as-is, so you can keep the hash in the file instead of plaintext. Generate one with `library-server.exe hash-password <plaintext>`. |
| `auth.mcp_token` | Bearer token for `/mcp` (machine clients like OpenClaw) | Optional | **secret** | Empty = `/mcp` auth disabled. Separate from the dashboard session. `openssl rand -hex 32` is a fine generator. Read fresh each start. |

### `database:`

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `database.path` | SQLite file path, relative to the exe's working dir | Optional | config | Default `./data/library.db` |

### `alerts:` — **seed-once**

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `alerts.cpu_threshold` | CPU % that fires an alert after 3 checks in a row | Optional | config | Default `85`. Seeds `settings.cpu_threshold`. |
| `alerts.ram_threshold` | RAM % alert threshold | Optional | config | Default `85`. Seeds `settings.ram_threshold`. |
| `alerts.offline_after_minutes` | Minutes of silence before an agent counts as offline | Optional | config | Default `5`. Seeds `settings.offline_after_minutes`. |
| `alerts.blacklist` | Process exe names that raise a blacklisted-app alert | Optional | config | Default: `steam.exe`, `epicgameslauncher.exe`, `discord.exe`, `battle.net.exe`, `leagueoflegends.exe`. Seeds `settings.blacklist` (JSON array). This is the text-based kill/alert list, separate from the Application Catalog. |

### `telegram:` — **seed-once**

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `telegram.token` | Bot token from @BotFather | Optional | **secret** | Empty = Telegram notifications off. Seeds `settings.telegram_token`. |
| `telegram.chat_id` | Destination chat ID | Optional | config | Seeds `settings.telegram_chat_id`. |

### `email:` — **seed-once**

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `email.smtp_host` | SMTP host | Optional | config | Blank host/user/to = email notifications off (`buildDialer`, `server/alert.go`). Seeds `settings.smtp_host`. |
| `email.smtp_port` | SMTP port | Optional | config | Default `587`. Seeds `settings.smtp_port`. |
| `email.smtp_user` | SMTP username, also used as the `From` header | Optional | **secret** (credential pair) | Seeds `settings.smtp_user`. |
| `email.smtp_pass` | SMTP password | Optional | **secret** | Seeds `settings.smtp_pass`. Plain text. |
| `email.smtp_to` | Alert recipient | Optional | config | Seeds `settings.smtp_to`. |

### `meshcentral:` — **seed-once**

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `meshcentral.url` | Base URL of the MeshCentral instance, used for dashboard deep-links | Optional | config | Default `http://192.168.1.10:4430`. Seeds `settings.mesh_url`. |

### `deepfreeze:`

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `deepfreeze.password` | Deep Freeze admin password handed to `DFC.exe` on agents | Optional (required for freeze/thaw) | **secret** | Read fresh each start (`cfg.DeepFreeze.Password`, `server/main.go`). Injected server-side into freeze/thaw jobs; never echoed in a response, log, or audit row. Empty = `freeze`/`thaw` return HTTP 400, `status` still works. Env-var / `.env` support was considered and rejected (`SESSION_MEMORY.md` 2026-09-07) — `config.yaml` is the only way in. |

### `uploads:`

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `uploads.path` | Directory for deploy-file uploads | Optional | config | Default `./uploads`. Created on startup. |
| `uploads.max_size_mb` | Max upload size, MB | Optional | config | Default `500` |

### `deploy:` — **seed-once**

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `deploy.lease_minutes` | How long a dispatched command may run before it's requeued | Optional | config | Default `10`. Seeds `settings.lease_minutes`. Missing from `config.yaml.EXAMPLE` — see "Known drift". |
| `deploy.default_max_retry` | Retry budget for jobs that don't set their own | Optional | config | Default `3`. Seeds `settings.default_max_retry`. |

### `wol:` — partly **seed-once**

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `wol.enabled` | Turn Wake-on-LAN on | Optional | config | Default `true`. Seeds `settings.wol_enabled`. |
| `wol.port` | WoL UDP destination port | Optional | config | Default `9`. Seeds `settings.wol_port`. |
| `wol.networks` | List of `{name, subnet}` CIDR profiles; broadcast is derived, never stored | Optional | config | Default `[]`. Seeds `settings.wol_networks` (JSON) **only while that DB key is still empty**. If a legacy `wol_broadcast_address` row exists and `wol.networks` is empty, the server logs a warning and sends no WoL packets until you add a profile. |

---

## Server — `settings` table keys with no `config.yaml` equivalent

These only ever live in the DB. Edit them on the dashboard Settings page or via
`GET`/`POST /api/settings`. Everything else in a live `settings` table is one of
the seed-once keys above; `GET /api/settings` is the live source of truth.

| Key | Description | Default | Category | Notes |
|---|---|---|---|---|
| `auto_kill_enabled` | Whether a blacklisted-app alert also kills the process | `false` | config | Dashboard toggle only |
| `smtp_tls` | Email transport security | `starttls` | config | `ssl` (implicit TLS) or `starttls`/empty (`buildDialer`, `server/alert.go`) |
| `wol_broadcast_address` | Old single-broadcast WoL setting, pre-`wol_networks` | — | config | **DEPRECATED**, superseded by `wol_networks`. Current code only reads it to emit a migration warning; never writes it. Left documented so nobody wonders what it was. |

---

## Agent — env vars and `C:\LibraryAgent\` files

The agent runs as SYSTEM (Windows Service "LibraryAgent", or a WinRM-pushed
Scheduled Task). All its state is under `C:\LibraryAgent\`. Resolution lives in
`agent/config.go`.

### Environment variables

| Variable | Description | Required | Category | Resolution / default |
|---|---|---|---|---|
| `LIBRARY_SERVER_URL` | WebSocket server URL (`ws://host:port/ws`) | Optional | config | Priority 1, then `C:\LibraryAgent\server.txt`, then hardcoded `ws://10.5.39.86:8080/ws`. Set by `deploy/_run_test_agent.bat`. A running service only sees machine/system env vars, and only after a restart. |
| `LIBRARY_WIFI_ADAPTER` | Exact Windows name of the Wi-Fi adapter for network-mode reconciliation | Optional | config | Priority 1, then `C:\LibraryAgent\wifi_adapter.txt`, then `"Wi-Fi"` |
| `LIBRARY_ETHERNET_ADAPTER` | Exact Windows name of the Ethernet adapter | Optional | config | Priority 1, then `C:\LibraryAgent\ethernet_adapter.txt`, then `"Ethernet"` |

### Files under `C:\LibraryAgent\`

| File | Description | Required | Category | Notes |
|---|---|---|---|---|
| `server.txt` | WebSocket server URL, one line | Optional | config | Used when `LIBRARY_SERVER_URL` is unset. Written by `install.bat` / `push_all.ps1` / `_run_as_service.bat`. |
| `token.txt` | Agent WebSocket auth token | Optional | **secret** | Must equal the server's `auth.token`. Missing = agent connects with no token (only works if the server's `auth.token` is also empty). `install.bat` copies it if it sits next to the script. |
| `id.txt` | Agent UUID | Auto | config | Generated on first run, stable after. Don't hand-edit. |
| `mesh_id.txt` | MeshCentral device ID | Optional | config | Written by the MeshCentral agent installer, read here to cross-link. Missing = empty. |
| `wifi_adapter.txt` / `ethernet_adapter.txt` | Per-machine adapter-name overrides | Optional | config | See env vars above |
| `pending_acks.json`, `completed_jobs.json`, `pending_self_update.json`, `policy_cache*`, `agent.log` | Runtime state / logs | Auto | — | Not config; listed so nobody mistakes them for it |

---

## `plugins/librawatch` — Node client env vars

Read by `plugins/librawatch/src/config.ts` (`loadConfig`) from `process.env`.

| Variable | Description | Required | Category | Notes |
|---|---|---|---|---|
| `LIBRAWATCH_URL` | Base URL of the Go server, e.g. `http://localhost:8080` (no trailing slash, `http`/`https` only) | **Required** | config | Throws `LibraWatchConfigError` if missing or not an absolute http(s) URL |
| `LIBRAWATCH_API_KEY` | Static bearer token, sent as-is, never auto-refreshed | Optional | **secret** | Wins over username/password. For anything long-running prefer username/password — server login tokens expire after 8h. Can be omitted entirely when the server's `auth.admin_username` is empty. |
| `LIBRAWATCH_USERNAME` | Dashboard login username for lazy login + auto re-login | Optional | config | Must be set together with `LIBRAWATCH_PASSWORD` — setting exactly one throws |
| `LIBRAWATCH_PASSWORD` | Dashboard login password | Optional | **secret** | Must be set together with `LIBRAWATCH_USERNAME` |

---

## `veyon_sync.py` — `veyon_sync_config.json`

Standalone script at repo root, not part of either Go module. Config file is
gitignored; template is `veyon_sync_config.example.json`. No env vars.

| Key | Description | Required | Category | Notes |
|---|---|---|---|---|
| `base_url` | Librawatch server base URL | Optional | config | Default `http://localhost:8080` |
| `admin_username` | Dashboard login used to call `GET /api/v1/computers` | Optional | config | Default `admin` |
| `admin_password` | Dashboard login password | Required in practice | **secret** | Script aborts with a "copy the example and fill `admin_password`" message if the config file is missing. Template ships a placeholder. |
| `veyon_cli_path` | Path to `veyon-cli.exe` | Optional | config | Default `C:\Program Files\Veyon\veyon-cli.exe` |
| `csv_path` | Working CSV handed to `veyon-cli networkobjects import`, rewritten every run | Optional | config | Default `./veyon_export/daftar_pc_veyon.csv`. A sibling `<csv_path>.synced` marker holds the last **successful** sync baseline — don't delete it or the next run does a pointless clear+import. |
| `log_file` | Log output path | Optional | config | Default `./veyon_export/log_veyon_sync.txt` |
| `max_drop_percent` | Abort clear+import if the fetched PC count drops more than this % vs the last synced baseline | Optional | config | Default `50`. Guard against wiping a curated Veyon list on a bad API response. |

Must run elevated ("Run as Administrator" / scheduled task `/RL HIGHEST`) —
`veyon-cli.exe networkobjects clear` needs a writable system config.

---

## Deploy scripts

| Script | Param / file | Description | Category | Notes |
|---|---|---|---|---|
| `deploy/push_all.ps1` | `-User` | Local admin username, same on every fleet PC | config | Mandatory param |
| `deploy/push_all.ps1` | `-Pass` | Local admin password | **secret** | Mandatory param, passed on the command line. Keep it out of shell history — prefer an elevated interactive session. The script doesn't persist it. |
| `deploy/push_all.ps1` | `-Server` | WebSocket URL written to each PC's `server.txt` | config | Default `ws://192.168.1.10:8080/ws` — note this does **not** match `deploy/server.txt` (see "Known drift") |
| `deploy/push_all.ps1` | `deploy/ips.txt` | Fleet target IPs, one per line | config | Tracked in repo |
| `deploy/install.bat` | `server.txt` / `token.txt` next to the script | Copied to `C:\LibraryAgent\` if present | config / **secret** (`token.txt`) | Run as Administrator on the target PC |
| `deploy/server.txt` | — | Default server URL shipped with the installer folder | config | Tracked. Currently `ws://10.5.39.86:8080/ws`. Edit before handing the installer folder out. |
| `deploy/_run_as_service.bat` | — | Dev loop only; hardcodes `ws://localhost:8080/ws` | config | Local machine only |
| `deploy/_run_test_agent.bat` | — | Dev loop; sets `LIBRARY_SERVER_URL=ws://localhost:8080/ws` and runs the agent in the foreground | config | Local machine only |

---

## Known drift and formerly-hardcoded values

- **Two different "default" server URLs.** `agent/config.go` (`defaultServer`) and
  `deploy/server.txt` both say `ws://10.5.39.86:8080/ws`, but
  `push_all.ps1 -Server` defaults to `ws://192.168.1.10:8080/ws`. Neither is a
  secret, both are environment-specific — set the real URL explicitly per
  deployment instead of trusting whichever default a given path uses.
- **Deep Freeze password** lives only in `config.yaml` `deepfreeze.password` and
  is injected server-side into every freeze/thaw job. It is **not** hardcoded
  anywhere. The dashboard's manual `df-password` field is a separate UI input,
  not a stored value. Don't reintroduce it as a literal in `mcp.go`,
  `deepfreeze.go`, or a script.
- **MeshCentral URL** default `http://192.168.1.10:4430` shows up in
  `server/config.go` and `config.yaml.EXAMPLE`. Harmless — it only drives a
  dashboard deep-link — but set it to the real instance or ignore it.
- **`config.yaml.EXAMPLE` has no `deploy:` section**, though `server/config.go`
  defines one (`lease_minutes`, `default_max_retry`). The server falls back to
  the code defaults (10, 3), so nothing breaks, but the template should be
  brought in line with `defaultConfigYAML`.
- **`server/config.yaml` is a stale leftover.** The server is built to the repo
  root (`library-server.exe`) and reads `config.yaml` next to the exe, i.e. the
  repo-root copy. The `server/config.yaml` file is gitignored and unused by the
  normal build. Ignore it, or delete it so nobody edits the wrong one.

---

## Secret handling and rotation

There is **no secret manager** here. Every secret is a plain-text file on the
host, protected by filesystem ACLs only. `config.yaml`, `server/config.yaml`,
`veyon_sync_config.json`, and `C:\LibraryAgent\token.txt` are all gitignored or
machine-local by design. Secrets are never logged — `deepfreeze.password` in
particular is kept out of responses, logs, and the audit trail.

| Secret | Where it should live | How to rotate |
|---|---|---|
| `auth.admin_password` | `config.yaml` on the server host (gitignored) | Edit `config.yaml` (plaintext or a `$2` bcrypt hash from `library-server.exe hash-password`), restart the `LibraryMonitor` service. Existing dashboard sessions stay valid up to 8h. |
| `auth.token` (agent WS token) | `config.yaml` on the server **and** `C:\LibraryAgent\token.txt` on every PC | Change both sides. The server accepts exactly one value at a time, so there's no grace window — do it in a maintenance window: set the new `auth.token`, restart the server, push the new `token.txt` fleet-wide (`push_all.ps1` with `token.txt` beside it, or a targeted copy), agents reconnect. |
| `auth.mcp_token` | `config.yaml` only | Edit, restart the server, update every `/mcp` client's bearer config (`LIBRAWATCH_API_KEY` for the Node plugin, OpenClaw config, and so on). |
| `deepfreeze.password` | `config.yaml` only | Must match what `DFC.exe` expects on the agents — rotate the Deep Freeze password itself first, then update `config.yaml`, then restart the server. Never commit. |
| `telegram.token` | `config.yaml` on first run, then `settings.telegram_token` | Regenerate via @BotFather, then update it on the **dashboard Settings page** — editing `config.yaml` after first run does nothing. |
| `email.smtp_pass` (+ `smtp_user`) | `config.yaml` on first run, then `settings.smtp_pass` | Rotate at the mail provider, update via the **dashboard Settings page**. |
| `veyon_sync_config.json` `admin_password` | `veyon_sync_config.json` on the Veyon master host (gitignored) | Update the file whenever the dashboard admin password changes. A dedicated read-only account would be better — see TODO. |
| `push_all.ps1 -Pass` | Not stored — supplied per run | Rotate the local-admin password through your normal fleet process; nothing in this repo caches it. |
| `LIBRAWATCH_API_KEY` / `LIBRAWATCH_PASSWORD` | Host env / MCP host secret store | Tied to the server-side credential they stand in for — rotate that credential, then update the env. |

---

## TODO / needs confirmation

- **`veyon_sync.py` account** — the template uses the `admin` dashboard account,
  so the script holds full admin credentials on the Veyon master host for
  something that only needs `GET /api/v1/computers`. Should there be a dedicated
  least-privilege account?
- **`auth.token` rotation with a live fleet** — is a maintenance-window cutover
  acceptable, or do we need a dual-token grace period? The server accepts exactly
  one token value today, so there's no overlap.
- **`smtp_tls = ssl` path** — read from code (`buildDialer`) but not known to
  have been run against a real implicit-TLS mail server. Confirm before relying
  on it.
- **MeshCentral integration depth** — `meshcentral.url` / `mesh_id.txt` are only
  used for dashboard deep-links as far as the code shows. Confirm there's no
  other MeshCentral credential (an API login, say) expected elsewhere and
  currently missing from config.
