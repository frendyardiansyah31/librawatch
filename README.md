**English** · [Bahasa Indonesia](README.id.md)

# LibraWatch

Monitor and manage ~60 library PCs (Windows 11) from one dashboard: online/offline status, CPU/RAM, running processes, policy enforcement (USB, app blacklist, config changes), fleet-wide deploy, Wake-on-LAN. Two Go binaries — a **server** (one central machine) and an **agent** (one per PC, runs as a Windows Service) — talking over a single persistent WebSocket. Local network only, no internet needed.

This README is about setup and running it. Internal architecture lives in `CLAUDE.md`, the API contract in `API.md` / `docs/openapi.yaml`, every config knob in `docs/CONFIG.md`.

## Prerequisites

- **Go 1.25+.** The `server/` module needs Go 1.25, `agent/` and `shared/` need 1.23, and `go.work` pins the workspace at 1.25. To build everything, use 1.25 or newer.
- **No C toolchain needed.** The SQLite driver (`modernc.org/sqlite`) is pure Go, no CGO. The catch: `go test -race` does not work in this environment — `-race` needs `CGO_ENABLED=1` plus a gcc/MinGW that isn't installed here.
- **Runtime is Windows.** The server builds and runs on other OSes for dev, but the agent is full of Win32 syscalls (USB watch, registry watch, popups, session launch) and only really runs on Windows. Real target: Windows 11 Home, a fleet of ~60 PCs.
- The dashboard has no build step. It's plain HTML/CSS/JS and the server serves it directly — just drop the `dashboard/` folder next to the built `server.exe`.

## Run the server (local, dev)

From the repo root:

```
cd server && go build -o ../library-server.exe .
cd .. && ./library-server.exe
```

If `config.yaml` doesn't exist, the server writes one with every credential blank and **keeps running anyway** — dashboard login is off and anyone can get in. Fine for a quick look, don't leave it like that. Dashboard: `http://localhost:8080`.

The server has to run from a folder. Minimum contents:

```
library-server.exe
config.yaml          ← you prepare this (see below)
dashboard/           ← copy it from the repo as-is; without it the UI is dead (agents still connect)
```

`data/`, `logs/`, and `uploads/` are created automatically on first start.

## Set up config.yaml

This file holds the real credentials (`auth.admin_password`, `auth.mcp_token`, `deepfreeze.password`) and is **not committed**. Copy it from the template:

```
copy config.yaml.EXAMPLE config.yaml
```

Fill in at least enough to turn dashboard login on:

- `auth.admin_username` + `auth.admin_password` — if either is blank, auth is off entirely.
- Everything else is optional: `auth.mcp_token` (the `/mcp` endpoint), `deepfreeze.password` (freeze/thaw actions), `wol.networks` (Wake-on-LAN subnets — the broadcast address is derived from the CIDR, don't set it by hand), `telegram.*` / `email.*` (alerts).

Never put real secret values in the repo. For the admin password, `config.yaml` takes plaintext (the server bcrypts it at startup) or a bcrypt hash directly — `./library-server.exe hash-password <plaintext>` prints a hash you can paste into `auth.admin_password`.

Per-key detail plus the **seed-once** rule (`alerts.*`, `telegram.*`, `email.*`, `deploy.*`, `wol.*` are only read once, to seed the `settings` table on first run; after that you edit them in the dashboard, not the file) is in `docs/CONFIG.md`.

Things that trip people up:

- `config.yaml.EXAMPLE` has no `deploy:` block. Not a problem — the code falls back to defaults (`lease_minutes: 10`, `default_max_retry: 3`).
- There's a `config.yaml.EXAMPLE` and a `config.yaml.example` with identical content. Windows is case-insensitive, so it's really one file.
- `server/config.yaml` is a leftover and unused. The binary always reads `config.yaml` next to its own `.exe`.

## External dependencies

The core features (monitoring, deploy, policy, dashboard, audit) run with just the server + agent + a local network. The rest is optional:

- **Telegram / SMTP** — alert notifications only. Blank config = no notifications, everything else is normal.
- **Deep Freeze** (`DFC.exe` on the agent PC) — freeze/thaw actions only. Blank `deepfreeze.password` = the freeze/thaw endpoint returns HTTP 400; status checks still work.
- **MeshCentral** — a dashboard deep-link, nothing more. Not set up = only that link is useless.
- **WinRM** — only for `push_all.ps1` (fleet deploy). The per-PC `install.bat` doesn't need it.
- **Veyon** (`veyon_sync.py`) — a separate classroom-control integration that runs on its own on the Veyon host, read-only pull from `GET /api/v1/computers`.

## Build & deploy the agent

```
cd agent && go build -o ../deploy/agent.exe .
```

The output **must** go to `deploy/agent.exe` — the deploy scripts read it from there. Three paths, depending on scale:

| Path | For | Mechanism |
|---|---|---|
| `deploy/install.bat` (run as Admin on the target PC) | one PC | Windows Service `LibraryAgent`, server URL from `server.txt` next to the script |
| `deploy/push_all.ps1 -User <u> -Pass <p> -Server ws://<ip>:8080/ws` | the whole fleet, over WinRM | Scheduled Task `/RU SYSTEM /RL HIGHEST /SC ONSTART` to every IP in `deploy/ips.txt` |
| `deploy/_run_as_service.bat` | local dev | stop/copy/start the local service, hardcoded `ws://localhost:8080/ws` |

The agent always runs as **SYSTEM / Session 0**, not as the logged-in user. UI features (the USB popup) get past that with `agent/internal/sessionlaunch` so the window shows up in the user's session.

Setting up WinRM for `push_all.ps1` is annoying but required if you want fleet deploy — it has to be enabled on every target and the local admin account has to be the same on all PCs. To uninstall one PC: `deploy/uninstall.bat` (Admin). The agent ID stays in `C:\LibraryAgent\id.txt` so a re-install keeps the same identity.

## Production — server as a Windows Service

```
./library-server.exe install
net start "LibraryMonitor"
```

The service runs with its working directory set to the `.exe`'s folder, so put the exe + `config.yaml` + `dashboard/` in their permanent location **before** `install`, not in a temp folder.

**A rebuild is not picked up on its own.** After building, `net stop` then `net start "LibraryMonitor"` (or the `LibraryAgent` service on the agent side) so the new binary actually runs.

## Tests

Per module — there's no root `go.mod`, so `go build ./...` / `go test ./...` from the repo root fails:

```
cd server && go test ./...
cd agent  && go test ./...
cd shared && go test ./...
```

Read the server result carefully: `server/deploy_test.go` has a long-standing compile break against the current `db.go` signatures (`AcquireNextJob` / `UpdateDeployResult`). It **blocks `go test ./...` for the entire `server` package**, not just that file — so "server tests are green" means nothing until it's fixed. The workaround used in earlier sessions: move `deploy_test.go` aside, run the tests, put it back. See the `SESSION_MEMORY.md` entry for 2026-08-13.

`go test -race` doesn't work here (see Prerequisites).

## Folder layout

```
server/     Go module — Gin + SQLite, runs as the "LibraryMonitor" service
agent/      Go module — the "LibraryAgent" service; internal/ = Win32 syscalls
shared/     Go module used by both server & agent (identity, policy match, uninstall parsing)
test/       standalone multi-agent load simulator
dashboard/  static UI (index.html, app.js, style.css), served by the server
deploy/     agent.exe + install scripts (install.bat, push_all.ps1, ips.txt)
docs/       CONFIG.md, openapi.yaml  (see note below)
veyon_sync.py   pulls GET /api/v1/computers -> Veyon, read-only
config.yaml.EXAMPLE   server config template
```

## Other docs

- **`API.md`** — quick reference for every endpoint (`/api/*`, `/api/v1/*`, `/mcp`).
- **`docs/openapi.yaml`** — the formal OpenAPI 3.0 contract (no `/mcp`).
- **`docs/CONFIG.md`** — full config & secret inventory plus how to rotate each one.
- **`CLAUDE.md`** — internal architecture (Hub, Deployer, PolicyEngine, and so on) + code conventions.
- **`SESSION_MEMORY.md`** — chronological log of non-obvious decisions between sessions.

`docs/`, `CLAUDE.md`, and `SESSION_MEMORY.md` are **not in git** (see `.gitignore`) — a fresh clone won't have them. Only `API.md` is tracked.
