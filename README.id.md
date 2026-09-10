[English](README.md) · **Bahasa Indonesia**

# LibraWatch

Monitoring dan manajemen ±60 PC perpustakaan (Windows 11) dari satu dashboard: status online/offline, CPU/RAM, proses berjalan, penegakan kebijakan (USB, blacklist aplikasi, perubahan config), deploy massal, Wake-on-LAN. Dua binary Go — **server** (satu mesin pusat) dan **agent** (tiap PC, jalan sebagai Windows Service) — ngobrol lewat satu koneksi WebSocket persisten. Jaringan lokal saja, tidak perlu internet.

README ini soal cara setup dan menjalankan. Arsitektur internal ada di `CLAUDE.md`, kontrak API di `API.md` / `docs/openapi.yaml`, seluruh knob config di `docs/CONFIG.md`.

## Prasyarat

- **Go 1.25+.** Modul `server/` minta Go 1.25, `agent/` dan `shared/` minta 1.23, `go.work` menyatukannya di 1.25. Untuk build semuanya, pakai 1.25 ke atas.
- **Tidak perlu C toolchain.** Driver SQLite (`modernc.org/sqlite`) pure-Go, tanpa CGO. Konsekuensinya `go test -race` tidak jalan di environment ini — `-race` butuh `CGO_ENABLED=1` plus gcc/MinGW yang memang tidak dipasang.
- **Runtime = Windows.** Server bisa di-build/di-run di OS lain buat dev, tapi agent penuh syscall Win32 (USB watch, registry watch, popup, session launch) — jalan beneran cuma di Windows. Target asli: Windows 11 Home, fleet ±60 PC.
- Dashboard tidak perlu di build. Vanilla HTML/CSS/JS, bisa di-serve langsung oleh server. Cukup copy paste di satu directory dengan file server.exe hasil build nanti.

## Jalankan server (lokal, dev)

Dari root repo:

```
cd server && go build -o ../library-server.exe .
cd .. && ./library-server.exe
```

Kalau `config.yaml` belum ada, server membuatnya sendiri dengan semua kredensial kosong dan **tetap lanjut jalan** — login dashboard nonaktif, siapa pun bisa masuk. Enak buat coba cepat, jangan dibiarkan begitu untuk seterusnya. Dashboard: `http://localhost:8080`.

Server harus dijalankan dari sebuah folder, minimal isinya:

```
library-server.exe
config.yaml          ← siapkan sendiri (lihat bawah)
dashboard/           ← copy apa adanya dari repo; tanpa ini UI mati (agent tetap bisa connect)
```

`data/`, `logs/`, `uploads/` dibuat otomatis saat start pertama.

## Setup config.yaml

File ini menyimpan kredensial asli (`auth.admin_password`, `auth.mcp_token`, `deepfreeze.password`) dan **tidak ikut di-commit**. Copy dari template:

```
copy config.yaml.EXAMPLE config.yaml
```

Isi minimal supaya login dashboard aktif:

- `auth.admin_username` + `auth.admin_password` — kalau salah satu kosong, auth mati total.
- Sisanya opsional: `auth.mcp_token` (endpoint `/mcp`), `deepfreeze.password` (aksi freeze/thaw), `wol.networks` (subnet Wake-on-LAN — broadcast dihitung otomatis dari CIDR, jangan diisi manual), `telegram.*` / `email.*` (alert).

Jangan taruh nilai secret asli di repo. Untuk password admin, `config.yaml` menerima plaintext (server yang hash pakai bcrypt saat start) atau hash bcrypt langsung — `./library-server.exe hash-password <plaintext>` mencetak hash yang bisa ditempel ke `auth.admin_password`.

Rincian tiap key plus aturan **seed-once** (`alerts.*`, `telegram.*`, `email.*`, `deploy.*`, `wol.*` cuma dipakai sekali untuk mengisi tabel `settings` pada run pertama; sesudah itu diedit lewat dashboard, bukan file) ada di `docs/CONFIG.md`.

Hal yang bikin bingung:

- `config.yaml.EXAMPLE` belum punya blok `deploy:`. Tidak masalah — kode fallback ke default (`lease_minutes: 10`, `default_max_retry: 3`).
- Ada `config.yaml.EXAMPLE` dan `config.yaml.example`, isi identik. Windows tidak membedakan huruf besar-kecil, jadi praktisnya satu file.
- `server/config.yaml` itu sisa lama, tidak dipakai. Binary selalu baca `config.yaml` di sebelah `.exe`-nya.

## Dependency eksternal

Fitur inti (monitoring, deploy, policy, dashboard, audit) jalan cuma dengan server + agent + jaringan lokal. Yang lain opsional:

- **Telegram / SMTP** — hanya untuk notifikasi alert. Config kosong = notifikasi mati, sisanya normal.
- **Deep Freeze** (`DFC.exe` di PC agent) — hanya untuk aksi freeze/thaw. `deepfreeze.password` kosong = endpoint freeze/thaw balas HTTP 400; cek status tetap jalan.
- **MeshCentral** — hanya deep-link dari dashboard. Tidak dipasang = link-nya saja yang tidak berguna.
- **WinRM** — hanya untuk `push_all.ps1` (deploy massal). `install.bat` per-PC tidak butuh.
- **Veyon** (`veyon_sync.py`) — integrasi classroom control terpisah, jalan sendiri di host Veyon, pull read-only dari `GET /api/v1/computers`.

## Build & deploy agent

```
cd agent && go build -o ../deploy/agent.exe .
```

Output **wajib** ke `deploy/agent.exe` — dipakai script deploy. Tiga jalur, tergantung skala:

| Cara | Untuk | Mekanisme |
|---|---|---|
| `deploy/install.bat` (jalankan sebagai Admin di PC target) | satu PC | Windows Service `LibraryAgent`, URL server dari `server.txt` di sebelah script |
| `deploy/push_all.ps1 -User <u> -Pass <p> -Server ws://<ip>:8080/ws` | seluruh fleet, via WinRM | Scheduled Task `/RU SYSTEM /RL HIGHEST /SC ONSTART` ke tiap IP di `deploy/ips.txt` |
| `deploy/_run_as_service.bat` | dev lokal | stop/copy/start service lokal, hardcode `ws://localhost:8080/ws` |

Agent selalu jalan sebagai **SYSTEM / Session 0**, bukan sebagai user yang login. Fitur ber-UI (popup USB) harus menembus batasan ini lewat `agent/internal/sessionlaunch` supaya muncul di sesi user.

Menyiapkan WinRM untuk `push_all.ps1` merepotkan tapi wajib kalau mau deploy massal — harus aktif di tiap target dan akun admin lokal harus sama di semua PC. Untuk uninstall satu PC: `deploy/uninstall.bat` (Admin). ID agent tetap tersimpan di `C:\LibraryAgent\id.txt` supaya re-install memakai identitas yang sama.

## Produksi — server sebagai Windows Service

```
./library-server.exe install
net start "LibraryMonitor"
```

Service jalan dengan working directory = folder `.exe`-nya, jadi taruh exe + `config.yaml` + `dashboard/` di lokasi permanennya **sebelum** `install`, bukan di folder sementara.

**Build ulang tidak otomatis kepakai.** Sesudah build, `net stop` lalu `net start "LibraryMonitor"` (atau service `LibraryAgent` di sisi agent) supaya binary baru benar-benar dijalankan.

## Test

Per modul — tidak ada root `go.mod`, jadi `go build ./...` / `go test ./...` dari root repo gagal:

```
cd server && go test ./...
cd agent  && go test ./...
cd shared && go test ./...
```

Baca hasil server hati-hati: `server/deploy_test.go` punya compile break lama terhadap signature `db.go` sekarang (`AcquireNextJob` / `UpdateDeployResult`). Ini **memblokir `go test ./...` untuk seluruh paket `server`**, bukan cuma file itu — jadi "server tests hijau" tidak berarti apa-apa sampai itu dibetulkan. Workaround yang dipakai sesi sebelumnya: pindahkan `deploy_test.go` sementara, jalankan test, kembalikan. Lihat `SESSION_MEMORY.md` entri 2026-08-13.

`go test -race` tidak jalan di sini (lihat Prasyarat).

## Struktur folder

```
server/     modul Go — Gin + SQLite, service "LibraryMonitor"
agent/      modul Go — service "LibraryAgent"; internal/ = syscall Win32
shared/     modul Go dipakai server & agent (identity, policy match, parse uninstall)
test/       simulator multi-agent untuk uji beban (standalone)
dashboard/  UI statis (index.html, app.js, style.css), di-serve server
deploy/     agent.exe + script instalasi (install.bat, push_all.ps1, ips.txt)
docs/       CONFIG.md, openapi.yaml  (lihat catatan di bawah)
veyon_sync.py   pull GET /api/v1/computers -> Veyon, read-only
config.yaml.EXAMPLE   template config server
```

## Dokumen lain

- **`API.md`** — referensi cepat semua endpoint (`/api/*`, `/api/v1/*`, `/mcp`).
- **`docs/openapi.yaml`** — kontrak formal OpenAPI 3.0 (tanpa `/mcp`).
- **`docs/CONFIG.md`** — inventaris lengkap config & secret plus cara rotasinya.
- **`CLAUDE.md`** — arsitektur internal (Hub, Deployer, PolicyEngine, dst) + konvensi kode.
- **`SESSION_MEMORY.md`** — log kronologis keputusan non-obvious antar sesi.

`docs/`, `CLAUDE.md`, dan `SESSION_MEMORY.md` **tidak masuk git** (lihat `.gitignore`) — clone baru tidak akan punya file-file itu. Hanya `API.md` yang ter-track.
