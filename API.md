# API Endpoints — Library Monitor

Referensi cepat semua endpoint HTTP yang ada di `server/main.go` dan `server/api.go`.
Base URL: `http://<server-ip>:<port>` (default port lihat `config.yaml`).

## Auth & akses

- Semua route di bawah prefix `/api/*` (kecuali yang ditandai **public**) melewati:
  1. `adminMiddleware` — whitelist IP admin (`auth.admin_cidrs` di config.yaml), kalau kosong = tidak dibatasi.
  2. `authMgr.Middleware()` — butuh session/token dari login (bcrypt + cookie/token, lihat `server/auth*.go`).
- Login pakai rate limiter (`NewLoginRateLimiter`) untuk cegah brute force.
- `/ws` dan `/api/file/:filename` sengaja **public** (tanpa auth) karena dipakai agent, bukan dashboard user.

## Public (tanpa login)

| Method | Path | Fungsi |
|---|---|---|
| GET | `/ws` | WebSocket endpoint, dipakai 60 PC agent untuk connect & kirim metrics |
| GET | `/` | Serve dashboard `index.html` (masih kena `adminMiddleware` / IP whitelist) |
| GET | `/static/*` | Static files dashboard (html/js/css) |
| POST | `/api/login` | Login, dapat session/token |
| GET | `/api/file/:filename` | Download file upload (installer) oleh agent, ada proteksi path traversal + extension whitelist |

## Auth (login + admin IP whitelist)

### Session
| Method | Path | Fungsi |
|---|---|---|
| POST | `/api/logout` | Logout, hapus session |

### Agents
| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/agents` | List semua agent (PC) |
| GET | `/api/agents/:id` | Detail 1 agent |
| GET | `/api/agents/:id/metrics` | History metrics CPU/RAM 24 jam (untuk sparkline) |
| GET | `/api/agents/:id/processes` | List proses yang jalan di PC tsb |
| POST | `/api/agents/:id/kill` | Kill proses di PC (body: `pid` atau `name`), tercatat di audit log |
| PATCH | `/api/agents/:id` | Update `mesh_id` (link ke MeshCentral) |
| DELETE | `/api/agents/:id` | Hapus agent dari database, tercatat di audit log |
| GET | `/api/agents/:id/logs` | Ambil isi `agent.log` dari PC via WebSocket relay (query `lines`, default 50) |

### Alerts
| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/alerts` | List alert terakhir (query `limit`, default 100, max 1000) |

### Settings
| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/settings` | Ambil semua setting (threshold CPU/RAM, Telegram, Email, dll) |
| POST | `/api/settings` | Update setting (key-value, langsung berlaku tanpa restart) |

### Applications (Application Catalog — Phase 1)
| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/applications` | List katalog aplikasi (query `status`: `pending_review`\|`allowed`\|`blocked`\|`ignored`, dan/atau `category_id`) |
| GET | `/api/applications/:id` | Detail 1 aplikasi + daftar sighting per device |
| PATCH | `/api/applications/:id` | Update `status` dan/atau `category_id`, tercatat di audit log |
| GET | `/api/categories` | List kategori aplikasi (seed default: Browser, Office, Academic, Programming, Graphic Design, Multimedia, Games, Remote Access, Utilities, System) |

Setiap proses yang dilaporkan agent (termasuk yang sebelumnya difilter karena CPU/RAM
rendah — filter itu sudah dihapus di Phase 1) diproses lewat `Catalog.Observe`
(`server/catalog.go`): dedupe per executable path → upsert ke `applications` (identity
key `exe_name + company`) → aplikasi baru otomatis masuk `status=pending_review`, tidak
perlu ditambahkan manual ke blacklist. **Enforcement/aksi kill masih 100% pakai mekanisme
blacklist teks yang lama (`settings.blacklist`)** — status `blocked` di katalog ini murni
untuk pencatatan/review di Phase 1, belum otomatis memicu kill (lihat rencana Policy
Engine di fase berikutnya).

### Audit
| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/audit` | List audit log (siapa melakukan apa: kill process, delete agent, deploy, upload, dll) |

### Health & Stats
| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/health` | Status server: uptime, jumlah agent online |
| GET | `/api/stats` | Ringkasan: jumlah online, jumlah alert hari ini |

### Deploy
| Method | Path | Fungsi |
|---|---|---|
| POST | `/api/deploy` | Buat deploy job baru ke satu/banyak PC (body: `type`, `payload`, `args`, `targets[]`) |
| GET | `/api/deploy` | List semua deploy job |
| GET | `/api/deploy/:id` | Detail 1 job + hasil per PC |
| DELETE | `/api/deploy/:id` | Cancel deploy job |
| POST | `/api/upload` | Upload installer (`.exe`, `.msi`, `.bat`, `.ps1` saja) |

Deploy `type` yang didukung (divalidasi di `validateDeployRequest`, `server/api.go`):
- `exec` — jalankan PowerShell command bebas (max 8192 karakter)
- `winget` — format wajib `winget install|uninstall --id <PackageID>`
- `file_deploy` — jalankan file yang sudah diupload
- `deepfreeze` — payload harus `thaw` / `freeze` / `query_df`
- `install_ssh` — install SSH di PC target, tanpa payload

### Test Notifikasi
| Method | Path | Fungsi |
|---|---|---|
| POST | `/api/test/telegram` | Kirim pesan test ke Telegram bot |
| POST | `/api/test/email` | Kirim email test |

### Logs
| Method | Path | Fungsi |
|---|---|---|
| GET | `/api/logs` | Tail `logs/server.log` (query `lines`, default 100, max 10000) |

## MCP (Model Context Protocol) — untuk OpenClaw/bot, bukan dashboard

| Method | Path | Fungsi |
|---|---|---|
| ANY | `/mcp` | MCP server (Streamable HTTP transport, lihat `server/mcp.go`). Tool: `get_online_pcs`, `restart_pc`, `shutdown_pc`, `freeze_pc`, `thaw_pc`, `check_deepfreeze_status`, `kill_process` |

- Auth terpisah dari dashboard: header `Authorization: Bearer <auth.mcp_token>` (static token di `config.yaml`, kosong = auth nonaktif — sama pola dengan `auth.token` untuk agent WebSocket). Tetap kena `adminMiddleware` (IP whitelist) kalau `admin_cidrs` diisi.
- `get_online_pcs`: tanpa input, output `{"count": N, "pcs": [{"hostname","ip","last_seen"}, ...]}`. Field `username` (user Windows yang login) sengaja belum ada — tidak ada kode di agent/server yang mengumpulkan data itu.
- `restart_pc({"hostname"})` / `shutdown_pc({"hostname"})`: resolve hostname → agent (case-insensitive) → kirim job lewat `Deployer.CreateJob` yang sama dipakai panel deploy dashboard (`type=exec`, payload tetap `Restart-Computer -Force` / `Stop-Computer -Force`, bukan command bebas dari caller).
- `freeze_pc({"hostname"})` / `thaw_pc({"hostname"})`: sama alur, tapi `type=deepfreeze` dengan payload `"freeze"`/`"thaw"` — job ini sudah ada sebelumnya di `agent/deepfreeze.go` (`DFC.exe <password> /BOOTFROZEN` atau `/BOOTTHAWED`), MCP tool cuma nyambungin lewat hostname. Password diambil dari `deepfreeze.password` di `config.yaml`, tidak pernah dikirim balik ke caller atau masuk audit log. Kalau `deepfreeze.password` kosong, tool langsung error tanpa membuat job.
- `kill_process({"hostname","process_name"})`: resolve hostname → agent → `hub.KillProcess(agentID, 0, process_name)`, mekanisme sinkron yang sama dipakai tombol kill di dashboard (`taskkill /F /IM <process_name>` di sisi agent). **Beda dari tool aksi lain**: nunggu balasan asli dari agent (bukan fire-and-forget), timeout 10 detik. PC harus online — kalau offline atau tidak balas dalam 10 detik, tool error (`"agent not online"` / `"kill request timed out"`), tidak ada audit log untuk percobaan yang gagal. Kalau sukses, output `{"hostname","process_name","output"}` — `output` adalah teks asli dari `taskkill` (mis. `"SUCCESS: The process \"chrome.exe\" ... has been terminated."`), dicatat di `audit_logs` dengan `ip="mcp"`.
- `check_deepfreeze_status({"hostname"})`: kirim job `type=deepfreeze` payload `"query_df"` (read-only, tanpa password), lalu **nunggu** hasil balik dari agent (poll `deploy_results` tiap 300ms, max 8 detik) — beda dari 4 tool aksi di atas yang fire-and-forget. Output `{"hostname","status","detail"}` — `status` = `"frozen"` / `"thawed"` / `"offline"` (PC lagi mati, langsung balas tanpa nunggu) / `"unknown"` (PC online tapi tidak balas dalam 8 detik, atau balasannya tidak dikenali) / `"error"` (DFC.exe gagal jalan di PC, detail di field `detail`). **Catatan penting**: sebelumnya `agent/deepfreeze.go` salah interpretasi exit code DFC.exe — exit code 1 (artinya FROZEN, status valid) dianggap error. Sudah diperbaiki supaya query_df sekarang benar membedakan exit 1=FROZEN, exit 0=THAWED, kode lain=error asli. **Agent yang sudah ter-deploy di 60 PC perlu di-rebuild & redeploy** supaya perbaikan ini berlaku — sebelum itu, `check_deepfreeze_status` ke PC lama akan selalu balas `"error"` saat PC-nya benar-benar FROZEN.
- Semua 4 action tool (`restart_pc`/`shutdown_pc`/`freeze_pc`/`thaw_pc`) output `{"hostname","job_id","status"}` — `status` = `"dispatched"` (PC online, langsung terkirim) atau `"pending"` (PC offline, jalan otomatis saat reconnect, sama seperti deploy job lain). Tercatat di `audit_logs` dengan `ip="mcp"`. Hostname yang tidak ditemukan → tool error, job tidak dibuat.

---
*Generated dari source code `server/main.go` + `server/api.go`. Update file ini kalau nambah/ubah endpoint.*
