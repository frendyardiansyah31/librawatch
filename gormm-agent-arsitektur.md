# Agent Golang — Arsitektur & Panduan Pengembangan

> ⚠️ **Sesuaikan dengan project** — Dokumen ini adalah panduan umum. Sesuaikan nama package, struktur folder, format pesan WebSocket, dan konfigurasi dengan project Golang yang sudah ada.

---

## Daftar Isi

1. [Arsitektur Final](#arsitektur-final)
2. [Prompt Vibe Coding](#prompt-vibe-coding)
3. [Milestone Pengembangan](#milestone-pengembangan)
4. [Keamanan (Security)](#keamanan-security)
5. [Instalasi](#instalasi)
6. [Cara Testing](#cara-testing)

---

## Arsitektur Final

### Gambaran Umum

```
[Dashboard Web Admin]  ← Browser admin
        ↕ WebSocket
[Server Admin - Gin]   ← PC/Server admin (1 unit)
        ↕ WebSocket (60 koneksi simultan)
[60 PC Agent Golang]   ← Berjalan sebagai Windows Service
   ├── /ws             → WebSocket endpoint utama
   ├── Thaw/Freeze     → Kontrol Deep Freeze via DFCmd.exe
   ├── Install SSH     → Buka OpenSSH untuk Ansible
   ├── Execute Script  → Jalankan PowerShell/CMD
   ├── Deploy App      → Install .exe/.msi dari file share
   └── Heartbeat       → Status real-time ke admin
```

### Alur Kerja Lengkap

```
1. Agent aktif di 60 PC (Windows Service, auto-start)
            ↓
2. Agent konek WebSocket ke Server Admin
            ↓
3. Admin buka Dashboard → lihat status semua PC real-time
            ↓
4. Admin klik "Thaw All" → broadcast ke 60 PC paralel
            ↓
5. Agent jalankan DFCmd.exe /BOOTTHAWED
            ↓
6. PC restart dalam kondisi thawed
            ↓
7. Admin deploy: SSH / aplikasi / script via agent
            ↓
8. Selesai → Admin klik "Freeze All" → PC kembali frozen
```

### Struktur Folder (Sesuaikan dengan project)

```
project/
├── cmd/
│   ├── agent/          → entry point agent (di PC perpus)
│   └── server/         → entry point server admin
├── internal/
│   ├── agent/
│   │   ├── handler.go  → handler pesan WebSocket
│   │   ├── deepfreeze.go → integrasi DFCmd.exe
│   │   ├── ssh.go      → install/config OpenSSH
│   │   └── executor.go → jalankan script/command
│   ├── server/
│   │   ├── hub.go      → manage 60 koneksi WebSocket
│   │   ├── broadcast.go → kirim pesan ke semua/sebagian PC
│   │   └── dashboard.go → API untuk dashboard web
│   └── shared/
│       └── message.go  → struct pesan WebSocket (shared)
├── web/                → dashboard HTML/JS admin
├── config/
│   └── config.yaml     → konfigurasi (token, port, dll)
└── scripts/
    └── install-service.ps1 → install agent sebagai Windows Service
```

### Format Pesan WebSocket (Sesuaikan dengan project)

```go
// shared/message.go
// ⚠️ Sesuaikan dengan struct pesan yang sudah ada di project

type Message struct {
    Type    string          `json:"type"`    // jenis perintah
    Token   string          `json:"token"`   // auth token
    Payload string          `json:"payload"` // data/script
    Meta    map[string]string `json:"meta"` // info tambahan
}

// Tipe pesan yang tersedia:
// Agent menerima:
//   "thaw"         → thaw Deep Freeze
//   "freeze"       → freeze Deep Freeze
//   "query_df"     → cek status Deep Freeze
//   "install_ssh"  → install & aktifkan OpenSSH
//   "execute"      → jalankan PowerShell script
//   "install_app"  → install aplikasi dari path
//   "reboot"       → restart PC
//   "status"       → minta info PC

// Agent mengirim:
//   "heartbeat"    → ping rutin (tiap 30 detik)
//   "log"          → output real-time dari command
//   "success"      → perintah berhasil
//   "error"        → perintah gagal
//   "pc_info"      → info PC (OS, RAM, IP, status DF)
```

---

## Prompt Vibe Coding

> Copy-paste prompt berikut ke AI coding assistant. Sesuaikan bagian dalam kurung `[ ]`.

### Prompt 1 — Handler Deep Freeze

```
Saya punya project Golang menggunakan Gin dan Gorilla WebSocket
sebagai agent yang berjalan di Windows.

Tambahkan handler baru untuk mengontrol Deep Freeze via DFCmd.exe.
- Path DFCmd.exe: "C:\Program Files\Faronics\Deep Freeze 8\DFCmd.exe"
- [Sesuaikan dengan project] Format pesan WebSocket yang sudah ada: [tempel struct Message yang ada]
- [Sesuaikan dengan project] Cara register handler yang sudah dipakai: [tempel contoh handler yang ada]

Buat fungsi:
1. handleThaw(conn, password) → jalankan /BOOTTHAWED
2. handleFreeze(conn, password) → jalankan /BOOTFROZEN
3. handleQueryDF(conn, password) → jalankan /QUERY dan return status
4. Stream output real-time ke WebSocket
5. Handle error jika DFCmd.exe tidak ditemukan

Jangan ubah struktur yang sudah ada.
```

### Prompt 2 — Install OpenSSH

```
Di project Golang agent saya yang sudah ada, tambahkan handler
untuk menginstall dan mengkonfigurasi OpenSSH Server di Windows 11.

[Sesuaikan dengan project] Gunakan format pesan dan pattern handler yang sudah ada.

Yang perlu dilakukan:
1. Cek apakah OpenSSH sudah terinstall
2. Install via: Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
3. Start service sshd dan set Automatic
4. Tambahkan firewall rule: hanya izinkan dari IP [IP_ADMIN]
5. Stream setiap langkah ke WebSocket sebagai "log" message
6. Return "ssh_ready" jika sukses

Jangan ubah struktur yang sudah ada.
```

### Prompt 3 — Hub Server (Kelola 60 Koneksi)

```
Di project Golang server admin saya yang menggunakan Gin dan WebSocket,
buat Hub untuk mengelola koneksi dari 60 PC agent secara simultan.

[Sesuaikan dengan project] Sesuaikan dengan struktur yang sudah ada.

Fitur yang dibutuhkan:
1. Register/unregister PC saat connect/disconnect
2. Simpan metadata tiap PC: nama, IP, status Deep Freeze, last seen
3. Broadcast pesan ke semua PC sekaligus
4. Kirim pesan ke PC tertentu by IP atau nama
5. Deteksi PC offline jika tidak ada heartbeat > 60 detik
6. Thread-safe menggunakan mutex atau channel

Jangan ubah struktur yang sudah ada.
```

### Prompt 4 — Dashboard API

```
Tambahkan REST API endpoint di Gin server untuk dashboard web admin.
[Sesuaikan dengan project] Gunakan router dan middleware yang sudah ada.

Endpoint yang dibutuhkan:
GET  /api/pcs              → list semua PC + status
POST /api/pcs/thaw-all     → thaw semua PC
POST /api/pcs/freeze-all   → freeze semua PC
POST /api/pcs/:ip/execute  → kirim script ke 1 PC
POST /api/pcs/broadcast    → kirim perintah ke semua PC
GET  /api/pcs/:ip/status   → status detail 1 PC

Gunakan middleware auth token yang sudah ada jika tersedia.
Jangan ubah struktur yang sudah ada.
```

---

## Milestone Pengembangan

### Milestone 1 — Stabilkan Koneksi WebSocket (Minggu 1)

- [ ] Pastikan agent auto-reconnect jika koneksi putus
- [ ] Tambah heartbeat setiap 30 detik dari agent ke server
- [ ] Server tandai PC offline jika heartbeat > 5 menit tidak datang
- [ ] Test dengan 5 PC dulu sebelum 60 PC

### Milestone 2 — Integrasi Deep Freeze (Minggu 1-2)

- [ ] Tambah handler `thaw`, `freeze`, `query_df`
- [ ] Test DFCmd.exe manual dulu di 1 PC
- [ ] Catat path DFCmd.exe yang benar di semua PC
- [ ] Test broadcast thaw ke semua PC
- [ ] Verifikasi status setelah thaw/freeze

### Milestone 3 — Install SSH via Agent (Minggu 2)

- [ ] Tambah handler `install_ssh`
- [ ] Test di 1 PC dulu
- [ ] Pastikan firewall rule terpasang dengan benar
- [ ] Test koneksi Ansible dari PC admin ke 1 PC target
- [ ] Broadcast install SSH ke semua PC
- [ ] Test Ansible ke semua 60 PC

### Milestone 4 — Dashboard Admin (Minggu 2-3)

- [ ] Buat REST API endpoint
- [ ] Buat halaman HTML sederhana (bisa pakai Tailwind CDN)
- [ ] Tampilkan status semua PC real-time
- [ ] Tombol thaw/freeze all
- [ ] Log output perintah real-time per PC
- [ ] Indikator online/offline per PC

### Milestone 5 — Deploy Aplikasi & Script (Minggu 3-4)

- [ ] Setup file share di PC admin (folder shared di LAN)
- [ ] Tambah handler `install_app` (jalankan .exe/.msi)
- [ ] Tambah handler `execute` untuk PowerShell script
- [ ] Test deploy aplikasi ke 1 PC
- [ ] Test deploy massal ke 60 PC
- [ ] Tambah rollback jika gagal

### Milestone 6 — Hardening & Produksi (Minggu 4)

- [ ] Implementasi semua item security (lihat bagian Security)
- [ ] Install agent sebagai Windows Service
- [ ] Test skenario: PC mati mendadak, koneksi putus, dll
- [ ] Dokumentasi runbook untuk operator perpus
- [ ] Monitoring log terpusat

---

## Keamanan (Security)

### Level Risiko Saat Ini

> Agent yang bisa eksekusi PowerShell = akses penuh ke PC.
> Harus diamankan dengan benar sebelum produksi.

### 1. Autentikasi Token

```go
// ⚠️ Sesuaikan dengan project — gunakan mekanisme auth yang sudah ada jika ada

// Gunakan token panjang dan random, bukan string sederhana
// Generate sekali: openssl rand -hex 32
const AgentToken = "ganti-dengan-token-64-karakter-random-yang-kuat"

func validateToken(token string) bool {
    // Gunakan constant time comparison untuk hindari timing attack
    return subtle.ConstantTimeCompare(
        []byte(token),
        []byte(AgentToken),
    ) == 1
}
```

### 2. Whitelist IP Admin

```go
// Hanya terima koneksi dari IP admin
// ⚠️ Sesuaikan dengan IP admin di jaringan perpus

var allowedIPs = []string{
    "10.5.39.86", // IP PC admin — sesuaikan
}

func ipWhitelistMiddleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        clientIP := c.ClientIP()
        allowed := false
        for _, ip := range allowedIPs {
            if clientIP == ip {
                allowed = true
                break
            }
        }
        if !allowed {
            c.AbortWithStatus(403)
            return
        }
        c.Next()
    }
}
```

### 3. Command Whitelist (Penting!)

```go
// Jangan izinkan sembarang script — whitelist perintah yang boleh
// ⚠️ Sesuaikan dengan kebutuhan operasional perpus

var allowedCommands = map[string]string{
    "install_ssh":    "Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0",
    "start_ssh":      "Start-Service sshd",
    "check_status":   "Get-Service sshd | Select-Object Status",
    "get_pc_info":    "Get-ComputerInfo | Select-Object CsName,OsName,TotalPhysicalMemory",
    // tambah sesuai kebutuhan
}

// Jangan gunakan payload script bebas di produksi
// Gunakan key → lookup command yang sudah diizinkan
```

### 4. TLS/HTTPS untuk WebSocket (WSS)

```go
// Gunakan WSS bukan WS untuk enkripsi di LAN
// Generate self-signed cert untuk LAN:
// openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 365 -nodes

router.RunTLS(":8765", "cert.pem", "key.pem")

// Client connect ke: wss://192.168.1.10:8765/ws
// Bukan: ws://192.168.1.10:8765/ws
```

### 5. Rate Limiting

```go
// Batasi jumlah perintah per menit untuk hindari abuse
// ⚠️ Sesuaikan dengan project — tambah di middleware yang sudah ada

import "golang.org/x/time/rate"

var limiter = rate.NewLimiter(rate.Every(time.Minute), 30) // max 30 cmd/menit

func rateLimitCheck(conn *websocket.Conn) bool {
    if !limiter.Allow() {
        conn.WriteJSON(Message{Type: "error", Payload: "rate limit exceeded"})
        return false
    }
    return true
}
```

### 6. Audit Log

```go
// Catat semua perintah yang masuk
// ⚠️ Sesuaikan dengan sistem logging yang sudah ada di project

type AuditLog struct {
    Timestamp time.Time `json:"timestamp"`
    SourceIP  string    `json:"source_ip"`
    PCName    string    `json:"pc_name"`
    Command   string    `json:"command"`
    Success   bool      `json:"success"`
}

func logAudit(entry AuditLog) {
    // Simpan ke file log atau database
    data, _ := json.Marshal(entry)
    log.Println(string(data))
}
```

### Checklist Security Sebelum Produksi

- [ ] Token diganti dari default ke random 64 karakter
- [ ] Whitelist IP hanya IP admin yang benar
- [ ] WebSocket menggunakan WSS (TLS)
- [ ] Command whitelist aktif (tidak ada free execute di produksi)
- [ ] Rate limiting aktif
- [ ] Audit log aktif dan disimpan
- [ ] Port agent (misal 8765) diblokir dari VLAN pengunjung
- [ ] Password Deep Freeze tidak di-hardcode (pakai env variable atau config file terenkripsi)

---

## Instalasi

### A. Build Agent (di PC developer)

```bash
# Build untuk Windows 64-bit
GOOS=windows GOARCH=amd64 go build -o agent.exe ./cmd/agent

# Build server admin
go build -o server ./cmd/server
```

### B. Konfigurasi Agent

```yaml
# config/config.yaml
# ⚠️ Sesuaikan semua nilai dengan environment perpus

agent:
  server_url: "wss://192.168.1.10:8765/ws" # IP server admin
  token: "ganti-dengan-token-yang-kuat"
  reconnect_interval: 10 # detik
  heartbeat_interval: 30 # detik

deepfreeze:
  dfcmd_path: "C:\\Program Files\\Faronics\\Deep Freeze 8\\DFCmd.exe"
  # ⚠️ Jangan simpan password di sini — kirim dari server saat dibutuhkan

ssh:
  admin_ip: "192.168.1.10" # IP yang diizinkan akses SSH
  port: 22
```

### C. Install Agent sebagai Windows Service

```powershell
# scripts/install-service.ps1
# Jalankan as Administrator di tiap PC
# ⚠️ Sesuaikan path dengan lokasi agent.exe

$serviceName = "PerpusAgent"
$agentPath = "C:\PerpusAgent\agent.exe"

# Buat folder
New-Item -ItemType Directory -Force -Path "C:\PerpusAgent"

# Copy agent dan config
Copy-Item "agent.exe" "C:\PerpusAgent\"
Copy-Item "config.yaml" "C:\PerpusAgent\"

# Install sebagai service
New-Service -Name $serviceName `
            -BinaryPathName $agentPath `
            -DisplayName "Perpus Agent" `
            -StartupType Automatic `
            -Description "Agent monitoring dan deployment perpustakaan"

# Start service
Start-Service -Name $serviceName

Write-Host "✅ Agent berhasil diinstall sebagai Windows Service"
```

### D. Deploy Agent ke 60 PC via USB/Flashdisk

```
Isi flashdisk:
├── agent.exe
├── config.yaml
└── install-service.ps1

Di tiap PC:
1. Tancap flashdisk
2. Buka PowerShell as Administrator
3. cd D:\  (atau drive flashdisk)
4. .\install-service.ps1
5. Selesai (~2 menit per PC)
```

### E. Jalankan Server Admin

```bash
# Di PC admin
./server

# Atau dengan env variable untuk keamanan
AGENT_TOKEN=xxx DFCMD_PASSWORD=xxx ./server
```

---

## Cara Testing

### Test 1 — Koneksi WebSocket Dasar

```bash
# Install wscat untuk testing WebSocket
npm install -g wscat

# Connect ke agent di 1 PC test
wscat -c ws://192.168.1.101:8765/ws

# Kirim heartbeat manual
{"type":"status","token":"token-test"}

# Expected response:
# {"type":"pc_info","payload":{"name":"PC-01","os":"Windows 11",...}}
```

### Test 2 — Status Deep Freeze

```bash
# Query status Deep Freeze di 1 PC
# Kirim via wscat atau dashboard
{"type":"query_df","token":"token-test"}

# Expected response:
# {"type":"success","payload":"Boot Thawed"}
# atau
# {"type":"success","payload":"Boot Frozen"}
```

### Test 3 — Thaw & Freeze (1 PC dulu!)

```
⚠️ Test di 1 PC yang tidak sedang dipakai pengunjung

1. Pastikan PC dalam kondisi Frozen
2. Kirim: {"type":"thaw","token":"xxx"}
3. PC akan restart
4. Login setelah restart → cek taskbar Deep Freeze (warna berbeda)
5. Kirim: {"type":"freeze","token":"xxx"}
6. PC restart lagi → kembali Frozen
```

### Test 4 — Install SSH (1 PC)

```powershell
# Setelah kirim perintah install_ssh via agent,
# verifikasi di PC target:

# Cek service SSH
Get-Service sshd

# Cek port 22 aktif
netstat -an | findstr :22

# Test koneksi SSH dari PC admin
ssh administrator@192.168.1.101

# Cek firewall rule terpasang
Get-NetFirewallRule -Name "SSH-AdminOnly"
```

### Test 5 — Broadcast ke 5 PC (Stress Test Kecil)

```python
# test_broadcast.py — jalankan dari PC admin
# ⚠️ Sesuaikan IP dan token

import asyncio
import websockets
import json

async def send_command(ip, command):
    try:
        async with websockets.connect(f"ws://{ip}:8765/ws") as ws:
            await ws.send(json.dumps({
                "type": command,
                "token": "token-test"
            }))
            response = await asyncio.wait_for(ws.recv(), timeout=30)
            print(f"✅ {ip}: {response[:100]}")
    except Exception as e:
        print(f"❌ {ip}: {e}")

# Test ke 5 PC dulu
test_pcs = [
    "192.168.1.101",
    "192.168.1.102",
    "192.168.1.103",
    "192.168.1.104",
    "192.168.1.105",
]

async def main():
    tasks = [send_command(ip, "status") for ip in test_pcs]
    await asyncio.gather(*tasks)

asyncio.run(main())
```

### Test 6 — Simulasi PC Mati Mendadak

```
1. Jalankan agent di PC test
2. Cabut kabel LAN / matikan WiFi
3. Pantau di server admin → PC harus masuk status "offline" dalam < 60 detik
4. Colok kembali LAN
5. Agent harus auto-reconnect tanpa perlu restart manual
```

### Test 7 — Load Test 60 PC Simultan

```go
// loadtest/main.go
// Simulasi 60 agent connect ke server sekaligus
// Jalankan di PC admin untuk test kapasitas server

package main

import (
    "fmt"
    "sync"
    "github.com/gorilla/websocket"
)

func simulateAgent(id int, wg *sync.WaitGroup) {
    defer wg.Done()

    conn, _, err := websocket.DefaultDialer.Dial(
        "ws://localhost:8765/ws", nil,
    )
    if err != nil {
        fmt.Printf("❌ Agent %d gagal connect: %v\n", id, err)
        return
    }
    defer conn.Close()

    fmt.Printf("✅ Agent %d connected\n", id)
    // Tunggu 30 detik sambil kirim heartbeat
    // ...
}

func main() {
    var wg sync.WaitGroup
    for i := 1; i <= 60; i++ {
        wg.Add(1)
        go simulateAgent(i, &wg)
    }
    wg.Wait()
    fmt.Println("Load test selesai")
}
```

### Checklist Testing Sebelum Produksi

- [ ] Test koneksi WebSocket dari 1 PC berhasil
- [ ] Test thaw/freeze di 1 PC berhasil
- [ ] Test install SSH di 1 PC berhasil
- [ ] Test Ansible masuk via SSH ke 1 PC berhasil
- [ ] Test broadcast ke 5 PC berhasil
- [ ] Test auto-reconnect saat koneksi putus
- [ ] Test PC offline terdeteksi dalam < 60 detik
- [ ] Test load 60 koneksi simultan di server
- [ ] Test semua endpoint API dashboard
- [ ] Test keseluruhan alur: thaw → deploy → freeze di 5 PC

---

## Catatan Akhir

> ⚠️ **Sesuaikan dengan project** — Semua kode di dokumen ini adalah template/panduan.
> Sesuaikan dengan struktur, naming convention, format pesan, dan pattern yang sudah ada di project Golang kamu sebelum diimplementasi.

> 🔐 **Keamanan dulu** — Jangan deploy ke 60 PC sebelum semua checklist security selesai.

> 🧪 **Test bertahap** — Selalu test di 1 PC → 5 PC → 60 PC. Jangan langsung broadcast ke semua.
