# Library Monitor — Manual Test Checklist

Run all tests from the project root (`c:\Development\go-rmm`).

---

## M1 — Foundation

- [ ] `cd server && go build ./...` — no errors
- [ ] `./library-server` (or `server/library-server.exe`) starts without error
- [ ] `data/library.db` created automatically on first run
- [ ] `GET http://localhost:8080/api/agents` returns `[]`
- [ ] `GET http://localhost:8080/` returns dashboard HTML
- [ ] Delete `config.yaml`, restart server → file auto-generated with defaults

---

## M2 — Agent Connection

- [ ] Cross-compile agent: `cd agent && GOOS=windows GOARCH=amd64 go build -ldflags="-H windowsgui -s -w" -o agent.exe .`
- [ ] Copy `agent.exe` to a Windows 11 PC, run it → agent appears in dashboard within 30 s
- [ ] `C:\LibraryAgent\id.txt` exists and contains a UUID
- [ ] Kill agent process → logs show retry with backoff (5s → 10s → 20s → max 60s)
- [ ] Restart server while agent running → agent reconnects automatically

---

## M3 — Alert System

**Telegram**
- [ ] Configure bot token + chat ID in Settings → Save
- [ ] Click "Tes Kirim Telegram" → success toast, message received in Telegram

**Email**
- [ ] Configure SMTP settings in Settings → Save
- [ ] Click "Tes Kirim Email" → success toast, email received

**CPU Alert**
- [ ] Simulate high CPU (e.g. run stress tool) → after 3 consecutive readings above threshold → Telegram/email alert sent
- [ ] No duplicate alert within 30-minute cooldown window

**RAM Alert**
- [ ] Same as CPU but for RAM threshold

**Blacklist**
- [ ] Add `notepad.exe` to blacklist in Settings → Save
- [ ] Open Notepad on monitored PC → alert fires within 60 s
- [ ] No duplicate alert within 60-minute cooldown

**Offline / Recovery**
- [ ] Power off monitored PC → offline alert arrives after `offline_after_minutes` (default 5)
- [ ] Power on PC → recovery alert sent within 60 s of reconnect
- [ ] No second offline alert for the same disconnect

**Threshold change**
- [ ] Change CPU threshold from 85 → 50 in Settings → new threshold active immediately (no restart)

---

## M4 — Deploy System

**PowerShell**
- [ ] Select 1 PC in Deploy tab → run `Write-Output 'hello'` → output "hello" visible in job history
- [ ] Run command on offline PC → status shows "pending" → PC comes online → job runs automatically → result visible

**Winget**
- [ ] Install `Notepad++.Notepad++` via winget tab → installs silently → appears in Add/Remove Programs
- [ ] Uninstall `Notepad++.Notepad++` → removed

**File Deploy**
- [ ] Upload a small `.bat` or `.exe` installer → Deploy → runs on target PC

---

## M5 — Dashboard UI

- [ ] Dashboard loads fully with no internet connection (no external CDN requests)
- [ ] Agents table refreshes automatically every 10 seconds
- [ ] Online/Offline/Alerts counters in header update correctly
- [ ] Click agent row → expands with process list and 24h CPU/RAM sparkline
- [ ] MeshCentral link button visible per agent (if mesh_id configured)
- [ ] Deploy: "Semua" button selects all agents; "Online" selects only online; "Reset" deselects
- [ ] Settings: save and reload page → values persist
- [ ] Alerts tab shows last 20 alerts with colour coding
- [ ] Log Server tab shows server log lines
- [ ] Agent log modal opens and shows agent log lines

---

## M6 — Logging

- [ ] `./logs/server.log` created on server start
- [ ] `C:\LibraryAgent\agent.log` created on agent start
- [ ] `GET /api/logs?lines=100` returns JSON `{"lines": "..."}` with last 100 log lines
- [ ] Agent log modal: clicking agent → `GET /api/agents/:id/logs` returns last 50 agent log lines within 15 s
- [ ] `GET /api/agents/:id/logs` for offline agent → returns `{"error": "agent not online"}`
- [ ] Server log rotation: manually truncate to confirm files > 10 MB rotate to `.1`, `.2`, `.3`
- [ ] Agent log rotation: agent.log > 5 MB → rotates to `agent.log.1`

---

## M7 — Deployment Scripts

**install.bat**
- [ ] Run `install.bat` as Administrator on a fresh Windows 11 PC
- [ ] Agent appears in dashboard within 30 s
- [ ] Task Scheduler shows `LibraryAgent` task running as SYSTEM
- [ ] Reboot PC → agent restarts automatically (Task Scheduler ONSTART)

**uninstall.bat**
- [ ] Run `uninstall.bat` as Administrator
- [ ] Agent stops (no longer appears online in dashboard)
- [ ] `LibraryAgent` Task Scheduler entry removed
- [ ] `C:\LibraryAgent\id.txt` still present (ID preserved for re-install)

**push_all.ps1**
- [ ] Populate `deploy/ips.txt` with 5 test PC IPs
- [ ] Enable WinRM on test PCs: `winrm quickconfig` (run once as admin)
- [ ] Run: `.\push_all.ps1 -User "Administrator" -Pass "password"`
- [ ] All 5 agents appear in dashboard within 60 s
- [ ] `deploy_log.txt` created with `[OK]` lines for each IP

**simulate_agents.go**
- [ ] From project root: `go run ./test/ -n 60 -server ws://localhost:8080/ws`
- [ ] 60 simulated agents appear in dashboard
- [ ] After 10 minutes: server process memory < 50 MB (check Task Manager)
- [ ] Ctrl+C stops all simulated agents; they show offline in dashboard after timeout
