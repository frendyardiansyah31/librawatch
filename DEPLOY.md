# Panduan Deployment — Library Monitor UIII

## Arsitektur

```
Dell T40  (Windows 10 Pro, IP 192.168.1.10)
  └─ C:\LibraryMonitor\library-server.exe   ← server, jalan terus
       └─ :8080  dashboard  →  buka di browser
       └─ /ws    WebSocket  ←  agent PC konek ke sini

60 PC Windows 11 Home
  └─ C:\LibraryAgent\agent.exe   ← invisible, auto-start via Task Scheduler
```

**Go hanya dibutuhkan di mesin developer (saat ini).** Dell T40 dan semua PC Windows cukup menjalankan binary — tidak perlu install Go.

---

## Bagian 1 — Build (di PC Developer — mesin ini)

### 1.1 Build Server (untuk Dell T40, Windows)

```powershell
cd C:\Development\go-rmm\server
go build -ldflags="-s -w" -o ..\library-server.exe .
```

### 1.2 Build Agent (untuk 60 PC Windows 11)

```powershell
cd C:\Development\go-rmm\agent
go build -ldflags="-H windowsgui -s -w" -o ..\deploy\agent.exe .
# -H windowsgui = tidak muncul console/window di PC user
```

### 1.3 Hasil Build

```
go-rmm\
  library-server.exe    ← untuk Dell T40
  deploy\
    agent.exe           ← untuk 60 PC
    install.bat
    uninstall.bat
    push_all.ps1
    ips.txt
```

---

## Bagian 2 — Setup Server di Dell T40

### 2.1 Buat Folder dan Copy File ke T40

Buat folder `C:\LibraryMonitor\` di Dell T40, lalu copy file-file ini:

```
C:\LibraryMonitor\
  library-server.exe
  config.yaml            ← copy dari go-rmm\config.yaml
  dashboard\
    index.html
    app.js
    style.css
```

> Cara copy: share folder, USB, atau robocopy via jaringan:
> ```powershell
> robocopy C:\Development\go-rmm \\192.168.1.10\C$\LibraryMonitor `
>   library-server.exe config.yaml /E /XD .git data logs uploads deploy agent test
> robocopy C:\Development\go-rmm\dashboard \\192.168.1.10\C$\LibraryMonitor\dashboard /E
> ```

### 2.2 Edit `config.yaml` di T40

Buka `C:\LibraryMonitor\config.yaml` dan sesuaikan:

```yaml
server:
  host: "0.0.0.0"   # tidak perlu diubah
  port: 8080         # port dashboard & WebSocket agent

database:
  path: "./data/library.db"   # dibuat otomatis

alerts:
  cpu_threshold: 85
  ram_threshold: 85
  offline_after_minutes: 5
  blacklist:
    - "steam.exe"
    - "discord.exe"
    - "epicgameslauncher.exe"

telegram:
  token: ""          # isi token bot Telegram jika dipakai
  chat_id: ""

email:
  smtp_host: ""      # isi jika pakai alert email
  smtp_port: 587
  smtp_user: ""
  smtp_pass: ""
  smtp_to: ""

meshcentral:
  url: "http://192.168.1.10:4430"

uploads:
  path: "./uploads"
  max_size_mb: 500
```

### 2.3 Tes Jalankan Server (sekali dulu)

Buka PowerShell **as Administrator** di T40:

```powershell
cd C:\LibraryMonitor
.\library-server.exe
```

Buka browser: `http://localhost:8080` — pastikan dashboard muncul.
Tekan `Ctrl+C` untuk stop setelah tes berhasil.

### 2.4 Daftarkan ke Task Scheduler (auto-start saat boot)

Jalankan di PowerShell **as Administrator** di T40:

```powershell
$action  = New-ScheduledTaskAction `
    -Execute "C:\LibraryMonitor\library-server.exe" `
    -WorkingDirectory "C:\LibraryMonitor"

$trigger = New-ScheduledTaskTrigger -AtStartup

$settings = New-ScheduledTaskSettingsSet `
    -ExecutionTimeLimit 0 `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask `
    -TaskName "LibraryMonitor" `
    -Action $action `
    -Trigger $trigger `
    -RunLevel Highest `
    -User "SYSTEM" `
    -Settings $settings `
    -Force

# Langsung jalankan sekarang
Start-ScheduledTask -TaskName "LibraryMonitor"
```

### 2.5 Buka Port di Windows Firewall T40

```powershell
New-NetFirewallRule `
    -DisplayName "Library Monitor" `
    -Direction Inbound `
    -Protocol TCP `
    -LocalPort 8080 `
    -Action Allow
```

---

## Bagian 3 — Deploy Agent ke 60 PC

### 3.1 Buat `deploy\server.txt`

Buat file `C:\Development\go-rmm\deploy\server.txt` berisi:

```
ws://192.168.1.10:8080/ws
```

> Ini adalah URL yang akan ditulis ke tiap PC agar agent tahu ke mana harus konek.

### 3.2 Edit `deploy\ips.txt`

Isi dengan IP semua PC yang akan di-deploy:

```
192.168.1.101
192.168.1.102
192.168.1.103
# ... dst
```

### 3.3 Cara A — Deploy Manual (1 PC)

1. Copy `deploy\agent.exe`, `deploy\install.bat`, `deploy\server.txt` ke PC target (USB / share folder).
2. Klik kanan `install.bat` → **Run as administrator**.
3. Agent langsung jalan — muncul di dashboard dalam 30 detik.

### 3.4 Cara B — Mass Deploy via WinRM (banyak PC sekaligus)

Dijalankan dari **PC developer ini**, bukan dari T40.

**Aktifkan WinRM di semua PC target terlebih dahulu:**

```powershell
# Jalankan di tiap PC target (atau via GPO)
winrm quickconfig -quiet
Set-Item WSMan:\localhost\Client\TrustedHosts -Value "192.168.1.*" -Force
```

**Jalankan push_all.ps1:**

```powershell
cd C:\Development\go-rmm\deploy

.\push_all.ps1 -User "Administrator" -Pass "passwordPC"
# Atau dengan URL server eksplisit:
.\push_all.ps1 -User "Administrator" -Pass "passwordPC" -Server "ws://192.168.1.10:8080/ws"
```

Script otomatis:
- Copy `agent.exe` ke `C:\LibraryAgent\` di tiap PC
- Tulis `server.txt` berisi URL server
- Daftarkan Task Scheduler (SYSTEM, run at startup)
- Langsung jalankan agent
- Log hasil ke `deploy\deploy_log.txt`

---

## Bagian 4 — Verifikasi

### Dashboard

Buka dari browser mana saja di jaringan: `http://192.168.1.10:8080`

- PC yang sudah terinstall agent harus muncul di tab **PC / Agents** dalam 30 detik.
- Status dot hijau = Online.

### Cek Server di T40

```powershell
# Status task scheduler
Get-ScheduledTask -TaskName "LibraryMonitor" | Select-Object TaskName, State

# Log server
Get-Content C:\LibraryMonitor\logs\server.log -Tail 20
```

### Cek Agent di PC Windows

```powershell
# Status task
Get-ScheduledTask -TaskName "LibraryAgent" | Select-Object TaskName, State

# Log agent
Get-Content C:\LibraryAgent\agent.log -Tail 20
```

---

## Bagian 5 — Update

### Update Server

```powershell
# 1. Build ulang di PC developer
cd C:\Development\go-rmm\server
go build -ldflags="-s -w" -o ..\library-server.exe .

# 2. Stop server di T40
Invoke-Command -ComputerName 192.168.1.10 -ScriptBlock {
    Stop-ScheduledTask -TaskName "LibraryMonitor"
}

# 3. Copy binary baru
Copy-Item .\library-server.exe \\192.168.1.10\C$\LibraryMonitor\library-server.exe

# 4. Start ulang
Invoke-Command -ComputerName 192.168.1.10 -ScriptBlock {
    Start-ScheduledTask -TaskName "LibraryMonitor"
}
```

### Update Agent

```powershell
# 1. Build ulang
cd C:\Development\go-rmm\agent
go build -ldflags="-H windowsgui -s -w" -o ..\deploy\agent.exe .

# 2. Jalankan push_all.ps1 lagi — script overwrite agent lama dan restart otomatis
cd ..\deploy
.\push_all.ps1 -User "Administrator" -Pass "passwordPC"
```

---

## Ringkasan

### Perlu Install Go?

| Mesin | Perlu Go? | Keterangan |
|-------|-----------|------------|
| PC Developer (mesin ini) | **Ya** | Untuk build — sudah terpasang |
| Dell T40 (server) | **Tidak** | Cukup copy `library-server.exe` |
| 60 PC Windows 11 (agent) | **Tidak** | Cukup copy `agent.exe` |

### Di Mana Define IP dan Port?

| Konfigurasi | File | Nilai Default |
|-------------|------|---------------|
| Port server | `config.yaml` → `server.port` | `8080` |
| IP server mendengarkan | `config.yaml` → `server.host` | `0.0.0.0` (semua interface) |
| URL server untuk agent | `deploy\server.txt` | `ws://192.168.1.10:8080/ws` |
| MeshCentral URL | `config.yaml` → `meshcentral.url` | `http://192.168.1.10:4430` |
