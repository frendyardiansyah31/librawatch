# 7. Public `/ws`; agents gated only by one optional shared token

Status: Accepted

## Context

Every agent in the fleet holds one persistent WebSocket to the server at
`GET /ws` — that connection carries metrics, events, inventory snapshots, and
command dispatch. ~60 identically-imaged PCs on the library's own LAN/WLAN, no
internet dependency.

The dashboard/API auth (bcrypt login, session tokens, IP allowlist) is built for
a handful of human admins. Reusing it for 60 headless machine connections would
mean provisioning and rotating 60 credentials, or issuing per-agent certificates
and running a CA — real work for a closed, single-site network.

## Decision

`GET /ws` is **not** behind the admin IP allowlist and **not** behind the session
auth middleware (`server/main.go` registers it before the protected groups). Any
host that can reach the port can open the socket.

The only optional gate is `auth.token` in `config.yaml` — one shared bearer
string. If set, an agent must present the matching value (from
`C:\LibraryAgent\token.txt`) in its first message after the upgrade. If blank
(the default), no check at all.

Agent identity (`id.txt`, a UUID generated on first run) is for *tracking* which
PC is which — it is not an authentication credential.

## Consequences

Easier:

- Deploying an agent is copying a binary and a `server.txt`. No per-agent
  secret, no enrollment step, no certificate lifecycle.
- One value to rotate fleet-wide if needed (`docs/CONFIG.md` §9), not 60.
- The dashboard's human-oriented auth stays simple because it doesn't also have
  to serve machines.

Harder:

- **`/ws` is only as protected as the network.** On a flat network anyone who can
  reach the port can connect as a fake agent, send bogus metrics/events, or
  receive commands intended for real agents. The shared token raises the bar
  slightly; it does not authenticate individual agents and the same token sits on
  every PC.
- Rotating `auth.token` has no grace window — the server accepts exactly one
  value, so a rotation is a brief fleet-wide maintenance window
  (`docs/CONFIG.md` §10).
- This decision is sound for a single-site LAN and would need revisiting
  (per-agent auth, TLS, mutual auth) before any deployment that crosses an
  untrusted network.
