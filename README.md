# LibraWatch

Sistem monitoring & manajemen PC untuk perpustakaan atau lab komputer — memantau ±60 PC lab/perpustakaan (Windows 11 Home) dari satu dashboard terpusat: status online/offline, CPU/RAM, aplikasi yang berjalan, penegakan kebijakan (USB, blacklist aplikasi, dsb), deploy software massal, dan Wake-on-LAN.

## Latar Belakang

Perpustakaan atau lab komputer publik biasanya mengoperasikan puluhan PC yang sebelumnya dipantau/dikelola secara manual atau lewat kombinasi tool terpisah (Veyon untuk classroom control, cek fisik untuk software terpasang, dsb) dan membutuhkan koneksi via LAN/WLAN, sehingga tidak harus terhubung internet terlebih dahulu. LibraWatch dibangun untuk menyatukan kebutuhan itu ke satu sistem in-house:

- **Visibilitas real-time** — tahu PC mana yang online, siapa yang login, proses apa yang berjalan, tanpa keliling fisik.
- **Penegakan kebijakan otomatis** — blokir/catat aplikasi terlarang, pantau USB storage, deteksi perubahan konfigurasi (wallpaper, Run key, scheduled task) tanpa intervensi manual per PC.
- **Operasional massal** — deploy installer/patch, jalankan perintah, restart/shutdown/Wake-on-LAN, uninstall software, ke satu PC atau seluruh fleet sekaligus dari dashboard.
- **Audit trail** — semua aksi admin (kill process, hapus agent, deploy) tercatat untuk akuntabilitas.

Sistem terdiri dari dua komponen Go independen: **server** (jalan di satu mesin pusat) dan **agent** (jalan di tiap PC target sebagai Windows Service), berkomunikasi lewat WebSocket persisten.

## Fitur Utama

- Monitoring CPU/RAM/proses per PC + histori (sparkline 24 jam)
- Application Catalog — inventaris aplikasi yang pernah terdeteksi jalan, review status (allowed/blocked/ignored)
- Software Inventory + remote uninstall (MSI/quiet-uninstall/winget, tier otomatis)
- Policy Engine — event USB/download/desktop/config/install/exec dievaluasi terhadap rule yang bisa diatur admin (log/notify/block/delete/kill)
- Peripheral Tamper Detection (keyboard/mouse terlepas)
- Deploy panel — exec PowerShell, install via winget, jalankan file upload, Deep Freeze freeze/thaw, install SSH — ke satu PC/grup/lantai/semua PC
- Wake-on-LAN multi-subnet (broadcast dihitung otomatis dari CIDR per network profile)
- Alert Telegram/Email (CPU/RAM tinggi, aplikasi blacklist, offline/recovery, USB, peripheral lepas)
- Audit log semua aksi admin
- Integrasi MCP (Model Context Protocol) untuk kontrol via bot/AI (OpenClaw) — restart/shutdown/freeze/thaw/kill/cek status
- Sinkronisasi read-only ke Veyon (`veyon_sync.py`) untuk classroom control

## Tech Stack

**Server** (`server/`)
- Go 1.25, [Gin](https://github.com/gin-gonic/gin) (HTTP router)
- [gorilla/websocket](https://github.com/gorilla/websocket) — koneksi persisten ke tiap agent
- [modernc.org/sqlite](https://modernc.org/sqlite) — SQLite pure-Go (tanpa CGO), penyimpanan data di `data/library.db`
- [kardianos/service](https://github.com/kardianos/service) — jalan sebagai Windows Service ("Library Monitor Server")
- MCP server (`github.com/modelcontextprotocol/go-sdk`) untuk endpoint `/mcp`
- bcrypt untuk hash password admin, YAML (`gopkg.in/yaml.v3`) untuk `config.yaml`

**Agent** (`agent/`)
- Go 1.23, [kardianos/service](https://github.com/kardianos/service) — jalan sebagai Windows Service ("LibraryAgent") atau Scheduled Task
- [gorilla/websocket](https://github.com/gorilla/websocket) — koneksi ke server
- [shirou/gopsutil](https://github.com/shirou/gopsutil) — metrik CPU/RAM/proses
- Win32 API langsung (syscall, tanpa CGO) untuk USB detection, popup GUI, session launch (SYSTEM → sesi user yang login), registry watch, dsb — lihat `agent/internal/`

**Dashboard** (`dashboard/`)
- Vanilla HTML/CSS/JS (tanpa framework, tanpa build step) — di-serve langsung oleh server Go di `/` dan `/static/*`

**Shared** (`shared/`) — modul Go kecil yang dipakai bareng oleh server & agent (identitas software, policy matching, parsing command uninstall) supaya logika kritikal tidak bisa berbeda antara kedua sisi.

**Integrasi eksternal**: Telegram Bot API, SMTP (email), Deep Freeze (`DFC.exe`), MeshCentral (link saja), Veyon (`veyon_sync.py`, Python, read-only pull dari `GET /api/v1/computers`).

## Arsitektur Singkat

```
Dashboard (browser) ──HTTP/session──> Server (Gin, :8080)
                                          │
                                          ├── SQLite (data/library.db)
                                          │
                                          └──WebSocket (/ws)──> Agent #1 (PC lab)
                                                             └─> Agent #2 (PC lab)
                                                             └─> ... (~60 agent)
```

Server dan agent adalah **dua modul Go terpisah**, masing-masing punya `go.mod` sendiri (disatukan lewat `go.work` hanya untuk tooling/editor, bukan untuk build). Detail arsitektur lebih dalam (Hub, Deployer, PolicyEngine, dst) ada di `CLAUDE.md`, dan referensi lengkap endpoint HTTP ada di `API.md`.

## Deploy — Server

### Struktur Folder

Server butuh berjalan dari sebuah folder (bukan cuma satu file exe) — minimal harus ada:

```
library-server/
├── library-server.exe   ← hasil build (lihat langkah Build & Jalankan di bawah)
├── config.yaml           ← wajib disiapkan sendiri, lihat "Setup config.yaml" di bawah
└── dashboard/             ← wajib di-copy dari repo (index.html, app.js, style.css) — bukan di-generate otomatis
```

`dashboard/` **harus** ikut di-copy persis seperti isinya di repo ini — server serve UI dashboard langsung dari folder itu (`/` dan `/static/*`), kalau folder ini tidak ada, dashboard tidak bisa diakses (walau agent tetap bisa connect lewat `/ws`).

Sub-folder berikut **dibuat otomatis** oleh server saat pertama kali jalan (tidak perlu disiapkan manual, tapi boleh tahu isinya):

```
data/       ← library.db (SQLite)
logs/       ← server.log (auto-rotate)
uploads/    ← file installer yang di-upload lewat panel Deploy
```

Kalau deploy pakai `.\library-server.exe install` sebagai Windows Service, service jalan dengan working directory di folder tempat exe ini berada (bukan `C:\Windows\System32`) — jadi pastikan seluruh isi folder di atas (exe + `config.yaml` + `dashboard/`) memang ditaruh bersebelahan di lokasi permanennya sebelum `install`, bukan di folder sementara.

### Setup `config.yaml`

`config.yaml` (root repo) menyimpan kredensial asli (password login dashboard, `mcp_token`, password Deep Freeze) — file ini **sengaja tidak ikut di-commit** (lihat `.gitignore`). Ada dua cara menyiapkannya:

1. **Copy dari template** — cara yang direkomendasikan:

   ```bash
   copy config.yaml.EXAMPLE config.yaml
   ```

   lalu edit `config.yaml` dan isi minimal:
   - `auth.admin_password` — ganti dari `CHANGE_ME`, ini password login dashboard.
   - `auth.mcp_token` — generate token acak kalau mau pakai endpoint `/mcp` (mis. `openssl rand -hex 32`), kosongkan kalau tidak dipakai.
   - `deepfreeze.password` — isi kalau PC target pakai Deep Freeze, kosongkan kalau tidak.
   - `wol.networks` — isi subnet PC yang mau di-Wake-on-LAN (lihat contoh di dalam file); broadcast address dihitung otomatis dari CIDR, jangan diisi manual.
   - Bagian lain (`telegram.*`, `email.*`, `meshcentral.url`, `alerts.*`) opsional, isi kalau fitur terkait mau dipakai.

2. **Biarkan server generate otomatis** — kalau `config.yaml` belum ada saat pertama kali dijalankan, server otomatis membuatnya dengan nilai default aman (semua kredensial kosong) dan **tetap langsung jalan** dengan default itu (`admin_username`/`admin_password` kosong = login dashboard nonaktif, siapa saja bisa akses). Server cuma cetak pesan pengingat di log, tidak berhenti — jadi segera stop, isi `config.yaml` sesuai poin di atas, lalu restart.

`server/config.yaml` (di dalam folder `server/`) adalah file leftover yang **tidak dipakai** — binary selalu baca `config.yaml` relatif ke lokasi `.exe`-nya sendiri (root repo kalau dijalankan dari sana).

### Build & Jalankan

Build dari root repo:

```bash
go build -ldflags="-s -w" -o library-server.exe .\server\
```

Jalankan (foreground, untuk dev/testing):

```bash
.\library-server.exe
```

Untuk produksi, install sebagai Windows Service (butuh Administrator):

```bash
.\library-server.exe install
net start "LibraryMonitor"
```

Dashboard bisa diakses di `http://<ip-server>:8080` (default port, lihat `config.yaml`). Setelah build ulang binary, **service yang sedang jalan harus di-restart** (`net stop`/`net start "LibraryMonitor"`) supaya perubahan kode/config benar-benar dipakai — build saja tidak cukup.

## Deploy — Client/Agent

Build dari root repo (output **harus** ke `deploy\agent.exe`, dipakai script di bawah):

```bash
go build -ldflags="-H windowsgui -s -w" -o deploy\agent.exe .\agent\
```

Ada tiga cara deploy agent ke PC target, tergantung skala:

### 1. Satu PC, manual (`deploy/install.bat`)

Copy folder `deploy/` (berisi `agent.exe`, `install.bat`, dan opsional `server.txt` berisi URL WebSocket server) ke PC target, lalu jalankan **sebagai Administrator**:

```bat
install.bat
```

Ini meng-install agent sebagai Windows Service `LibraryAgent` (auto-start, restart on failure). Untuk uninstall: jalankan `uninstall.bat` (Administrator) — ID agent tetap disimpan di `C:\LibraryAgent\id.txt` supaya re-install nanti pakai identitas yang sama.

### 2. Mass deploy ke seluruh fleet via WinRM (`deploy/push_all.ps1`)

Isi `deploy/ips.txt` dengan daftar IP target (satu per baris), pastikan WinRM aktif di semua target, lalu dari mesin admin:

```powershell
.\deploy\push_all.ps1 -User "Administrator" -Pass "secret" -Server "ws://<ip-server>:8080/ws"
```

Script ini push `agent.exe` + daftarkan sebagai **Scheduled Task** (`/RU SYSTEM /RL HIGHEST /SC ONSTART`, bukan Windows Service) ke tiap PC di `ips.txt`, lalu langsung menjalankannya. Log hasil deploy tersimpan di `deploy/deploy_log.txt`.

### 3. Dev/test lokal (`deploy/_run_as_service.bat`)

Untuk iterasi cepat di mesin development yang sama dengan server (agent connect ke `ws://localhost:8080/ws`): stop service lokal, copy `agent.exe` terbaru, start ulang.

Baik service (`install.bat`) maupun scheduled task (`push_all.ps1`) sama-sama jalan sebagai **SYSTEM / Session 0** — fitur yang butuh UI (mis. popup peringatan USB) menembus batasan ini lewat `agent/internal/sessionlaunch` untuk menampilkannya di sesi user yang sedang login.

## Dokumentasi Lain

- **`API.md`** — referensi lengkap semua endpoint HTTP (`/api/*`, `/api/v1/*`, `/mcp`).
- **`CLAUDE.md`** — panduan arsitektur & konvensi untuk kontributor/AI coding agent (struktur Hub/Deployer/PolicyEngine, aturan build, dsb).
- **`SESSION_MEMORY.md`** — log kronologis keputusan & temuan non-obvious dari sesi pengembangan sebelumnya.
