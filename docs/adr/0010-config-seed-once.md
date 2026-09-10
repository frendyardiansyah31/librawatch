# 10. `config.yaml` seeds the `settings` table once, then the DB is authoritative

Status: Accepted

## Context

Server configuration splits into two kinds:

- Things that can only change with a restart anyway: listen port, DB path,
  admin credentials, the agent WS token, the Deep Freeze password, uploads dir.
- Things an admin should be able to change from the dashboard without touching a
  file or restarting a service: CPU/RAM thresholds, the offline timeout, the
  blacklist, Telegram/email settings, WoL networks, auto-kill toggle, the floor
  map layout.

The second kind needs a live, writable store. It also needs sane initial values
so a fresh install works before anyone opens the Settings page.

## Decision

Two config surfaces, with a defined handoff:

- `config.yaml` — read from disk. `server.*`, `auth.*`, `database.*`,
  `uploads.*`, `deepfreeze.password` are read fresh on **every** start.
- The `settings` table in SQLite — edited via the dashboard /
  `GET`+`POST /api/settings`, survives restarts.

`alerts.*`, `telegram.*`, `email.*`, `meshcentral.url`, `deploy.*`, `wol.*` in
`config.yaml` are **seed-once**: `DB.InitDefaultSettings` (`server/db.go`) writes
each to the `settings` table on first run *only if that row is still empty*.
After first run, the DB value wins and editing those keys in `config.yaml` does
nothing. Fully documented in `docs/CONFIG.md` §2–3.

## Consequences

Easier:

- An admin tunes thresholds, notifications, and the blacklist live from the
  dashboard — no file edit, no restart, no shell access to the server box.
- A fresh install has working defaults immediately, taken from `config.yaml` /
  `defaultConfigYAML`.
- Restart-only settings stay in a plain file that's easy to diff and back up.

Harder:

- **The handoff surprises people.** "I changed `cpu_threshold` in `config.yaml`
  and restarted and nothing happened" is expected behavior once the row exists.
  It needs documentation to not read as a bug.
- Two sources of truth for adjacent-looking settings. `docs/CONFIG.md` exists
  largely to make the split legible.
- The seed check is "is the row empty" — a seed-once key left blank in
  `config.yaml` keeps re-seeding from the file every start until something writes
  a non-empty value.
- `config.yaml.EXAMPLE` and `defaultConfigYAML` can drift (the EXAMPLE is
  currently missing the `deploy:` block — harmless, code defaults cover it).
