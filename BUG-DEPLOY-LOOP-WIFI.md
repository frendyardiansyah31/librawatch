# Bug: Command Deploy (Disable WiFi / Enable Ethernet) Berjalan Berulang

## Laporan

Command PowerShell one-shot untuk enable adapter Ethernet & disable adapter WiFi di satu PC
seharusnya jalan sekali, tapi ternyata terus berulang — tiap kali WiFi dinyalakan manual,
sistem otomatis mematikannya lagi. Tetap terjadi setelah server di-restart.

## Root Cause

Bukan command yang salah — command jalan sekali per eksekusi. Masalahnya ada di **command
queue** (`server/deploy.go`, ditambahkan di commit `986f438`): job yang sama dikirim ulang
(redispatch) berkali-kali karena ack hasil eksekusi dari agent hilang, dan sistem menganggap
job itu belum pernah jalan.

Alur kejadian:

1. Server dispatch job → `AcquireNextJob` (`server/db.go`) set `deploy_results.status='running'`,
   `lease_until = now + lease_minutes` (default 10 menit).
2. Job dikirim ke agent lewat WebSocket (`Deployer.dispatch`, `server/deploy.go`) — fire-and-forget,
   tanpa konfirmasi pengiriman.
3. Agent menjalankan PowerShell **secara sinkron** (`agent/executor.go` `executeCommand` →
   `runPSCommand`), baru setelah selesai mengirim hasil (`sendExecResult` → `wsSend`,
   `agent/main.go`).
4. **Inti masalah**: command ini mematikan adapter WiFi yang menjadi jalur koneksi WebSocket
   agent ke server. `wsSend` bersifat *fire-and-forget* (non-blocking, silently drop kalau
   channel kosong/putus) — begitu WiFi mati, ack "success" hilang begitu saja. Tidak ada retry
   atau penyimpanan lokal di sisi agent untuk hasil yang belum terkirim.
5. Karena ack tidak pernah sampai, row `deploy_results` di database tetap `status='running'`
   dengan `lease_until` yang akhirnya lewat waktu.
6. `Deployer.sweepExpiredLeases` (jalan tiap 30 detik) mendeteksi lease yang expired, menambah
   `retry_count`, mengembalikan status ke `pending`, lalu **langsung redispatch** job yang
   identik begitu agent kembali terhubung.
7. Agent menjalankan command yang sama lagi → WiFi dimatikan lagi persis saat baru dinyalakan
   manual. Berulang hingga `max_retry` (default 3, jadi ~4 kali total eksekusi), terasa seperti
   "looping tak berhenti" karena berulang tiap ~10 menit.
8. Seluruh state (`status`, `retry_count`, `lease_until`) tersimpan di SQLite (WAL) — **restart
   server tidak mereset apa pun**. Lease sweeper langsung jalan lagi saat boot dan menemukan
   lease yang sudah lewat, lalu redispatch job yang sama.

Ada juga celah desain tambahan: `UpdateDeployResult` (`server/db.go`) sebelumnya hanya menjaga
`status <> 'cancelled'` — tidak ada fencing per-attempt, sehingga ack basi dari attempt lama
bisa menimpa row attempt baru yang sudah di-redispatch oleh lease sweeper.

### File & baris kunci (sebelum fix)

- `server/deploy.go:152-179` — `sweepExpiredLeases` (retry + redispatch otomatis)
- `server/db.go:1011-1027` — `UpdateDeployResult` (tanpa fencing attempt)
- `server/db.go:1053-1094` — `AcquireNextJob`
- `agent/executor.go:72-79, 113-129` — `executeCommand` & `sendExecResult` (ack fire-and-forget)
- `agent/main.go:328-340` — `wsSend` (non-blocking, silent drop)

## Solusi yang Diterapkan

### A. Ack hasil job jadi durable (agent-side, at-least-once delivery)

- **`agent/ack.go`** (baru) — local pending-ack store: `persistPendingResult` menyimpan hasil
  job ke `C:\LibraryAgent\pending_acks.json` *sebelum* dikirim, `clearPendingResult` menghapusnya
  setelah server konfirmasi terima, `replayPendingResults` mengirim ulang semua yang belum
  di-ack setiap kali agent connect/reconnect.
- **`agent/executor.go`, `agent/deepfreeze.go`, `agent/ssh.go`** — semua pengirim hasil akhir
  job (`exec_result`, `deepfreeze_result`) sekarang lewat `sendDurableResult` alih-alih
  `wsSend` langsung.
- **`agent/main.go`** — `runSession` memanggil `replayPendingResults()` di awal setiap sesi
  (mencakup reconnect setelah network blip maupun restart proses agent); handler pesan baru
  `exec_result_ack` memanggil `clearPendingResult`.

### B. Attempt fencing di server (tanpa migrasi skema baru)

`deploy_results.retry_count` yang sudah ada dipakai sebagai token fencing:

- **`server/db.go`** — `AcquireNextJob` sekarang juga mengembalikan `retry_count` yang sedang
  diklaim; `UpdateDeployResult` menerima `expectedAttempt *int` dan menambahkan
  `AND (? IS NULL OR retry_count = ?)` ke `WHERE` clause, serta mengembalikan jumlah row yang
  benar-benar berubah (`RowsAffected`).
- **`server/hub.go`** — `IncomingMessage`/`OutgoingMessage` dapat field `Attempt *int`.
  `handleExecResult` sekarang: selalu kirim `exec_result_ack` begitu pesan berhasil diproses
  (supaya agent berhenti mengirim ulang), tapi hanya menerapkan `UpdateJobStatus`/`PumpAgent`
  kalau `UpdateDeployResult` benar-benar mengenai row (bukan ack basi untuk attempt yang sudah
  di-requeue).
- **`server/deploy.go`** — `dispatch` mengirim `attempt` (retry_count saat klaim) ke agent untuk
  semua tipe job (exec, file_deploy, deepfreeze, install_ssh — Deep Freeze juga berisiko sama
  karena bisa memicu reboot).

### Kenapa tidak bikin "classifier command berisiko"

Sengaja tidak dibuat deteksi pola command berbahaya (regex `Disable-NetAdapter` dll) dengan
grace period khusus. Akar masalahnya adalah **ack yang hilang**, bukan command tertentu — command
apa pun yang membuat koneksi terputus sesaat (reconnect router, Windows Update, dll) punya
risiko yang sama. Solusi A+B menutup celah ini untuk semua jenis command, bukan cuma satu
pola. Classifier berbasis regex hanya menambah abstraksi yang perlu terus di-maintain tanpa
menutup akar masalah.

## Mitigasi Darurat (tanpa perlu deploy fix ini)

Kalau ada job yang sedang looping sekarang: `POST /api/deploy/:job_id/cancel` langsung
menghentikannya (`server/api.go`, memanggil `db.CancelDeployJob`).

## Verifikasi

1. **Fencing**: buat job, biarkan diklaim (`attempt=0`), paksa lease expired, biarkan
   di-requeue jadi `attempt=1`. Kirim `exec_result` palsu dengan `attempt=0` (stale) →
   row **tidak berubah**, log menunjukkan "exec_result ignored: stale attempt". Kirim ack
   `attempt=1` yang benar → diterapkan, job jadi `done`.
2. **Reproduksi bug (sudah fixed)**: deploy command disable WiFi/enable Ethernet ke PC yang
   konek via WiFi. Command jalan sekali, `pending_acks.json` sempat berisi entry sebelum
   koneksi putus, setelah reconnect (lewat Ethernet) muncul log "replaying unacked result",
   dashboard menunjukkan job `success` sekali saja. Nyalakan WiFi manual → **tetap menyala**,
   tidak ada eksekusi kedua.
3. **Survive restart server**: ulangi skenario 2, restart server di antara WiFi putus dan
   agent reconnect → hasil akhir tetap satu kali eksekusi.
4. **Regresi crash/timeout asli**: matikan proses agent total (bukan cuma adapter) lebih lama
   dari `lease_minutes` → lease sweeper tetap requeue seperti biasa, `retry_count` naik,
   `max_retry` tetap membatasi jumlah percobaan.
5. **Build**: `cd server && go build ./...`, `cd agent && GOOS=windows GOARCH=amd64 go build ./...`,
   `cd test && GOOS=windows GOARCH=amd64 go build ./...` — semua sukses tanpa error (sudah
   diverifikasi saat implementasi).

### File yang diubah

- `server/hub.go`, `server/db.go`, `server/deploy.go`
- `agent/main.go`, `agent/executor.go`, `agent/config.go`, `agent/deepfreeze.go`, `agent/ssh.go`
- File baru: `agent/ack.go`
