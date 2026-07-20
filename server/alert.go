package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gopkg.in/gomail.v2"
)

const (
	cpuRAMCooldown       = 30 * time.Minute
	blacklistCooldown    = 60 * time.Minute
	offlineCheckInterval = 2 * time.Minute
	consecutiveRequired  = 3
)

type Alerter struct {
	db          *DB
	hub         *Hub
	consecutive sync.Map // "agentID:alertType" → int
}

func NewAlerter(db *DB, hub *Hub) *Alerter {
	return &Alerter{db: db, hub: hub}
}

func (a *Alerter) getConsecutive(agentID, alertType string) int {
	v, ok := a.consecutive.Load(agentID + ":" + alertType)
	if !ok {
		return 0
	}
	return v.(int)
}

func (a *Alerter) setConsecutive(agentID, alertType string, n int) {
	a.consecutive.Store(agentID+":"+alertType, n)
}

// CheckMetrics evaluates CPU, RAM and blacklist thresholds.
// Called synchronously for every metrics message (DB reads are fast; network calls are goroutines).
func (a *Alerter) CheckMetrics(agentID, hostname string, cpu, ram float64, procs []Process) {
	settings, err := a.db.GetAllSettings()
	if err != nil {
		slog.Error("alert: get settings", "error", err)
		return
	}

	cpuThreshold := parseFloatSetting(settings["cpu_threshold"], 85)
	ramThreshold := parseFloatSetting(settings["ram_threshold"], 85)

	// CPU consecutive threshold check
	if cpu > cpuThreshold {
		n := a.getConsecutive(agentID, "cpu") + 1
		a.setConsecutive(agentID, "cpu", n)
		if n >= consecutiveRequired {
			last, _ := a.db.GetLastAlert(agentID, "cpu_high")
			if last == nil || nowWIB().Sub(last.SentAt) >= cpuRAMCooldown {
				msg := fmt.Sprintf("⚠️ CPU tinggi di %s: %.1f%% (threshold %.0f%%) — %s",
					hostname, cpu, cpuThreshold, nowWIB().Format("15:04 WIB"))
				a.fire(agentID, "cpu_high", msg, settings)
				a.setConsecutive(agentID, "cpu", 0)
			}
		}
	} else {
		a.setConsecutive(agentID, "cpu", 0)
	}

	// RAM consecutive threshold check
	if ram > ramThreshold {
		n := a.getConsecutive(agentID, "ram") + 1
		a.setConsecutive(agentID, "ram", n)
		if n >= consecutiveRequired {
			last, _ := a.db.GetLastAlert(agentID, "ram_high")
			if last == nil || nowWIB().Sub(last.SentAt) >= cpuRAMCooldown {
				msg := fmt.Sprintf("⚠️ RAM tinggi di %s: %.1f%% (threshold %.0f%%) — %s",
					hostname, ram, ramThreshold, nowWIB().Format("15:04 WIB"))
				a.fire(agentID, "ram_high", msg, settings)
				a.setConsecutive(agentID, "ram", 0)
			}
		}
	} else {
		a.setConsecutive(agentID, "ram", 0)
	}

	// Blacklist process check (60-min cooldown per app per agent)
	blacklist := parseBlacklistSetting(settings["blacklist"])
	autoKill := parseBoolSetting(settings["auto_kill_enabled"])
	for _, proc := range procs {
		for _, bl := range blacklist {
			if strings.EqualFold(proc.Name, bl) {
				last, _ := a.db.GetLastAlertForApp(agentID, proc.Name)
				if last == nil || nowWIB().Sub(last.SentAt) >= blacklistCooldown {
					msg := fmt.Sprintf("🚫 Aplikasi terlarang terdeteksi di %s: %s — %s",
						hostname, proc.Name, nowWIB().Format("15:04 WIB"))
					a.fire(agentID, "blacklisted_app", msg, settings)
				}
				if autoKill {
					go a.autoKill(agentID, hostname, proc.Name)
				}
				break
			}
		}
	}
}

// autoKill runs when auto_kill_enabled is on: it kills the blacklisted
// process on the agent (same mechanism as the dashboard's kill button) and
// records the outcome in audit_logs, regardless of success or failure, so
// it's traceable independently of the notification cooldown above.
func (a *Alerter) autoKill(agentID, hostname, procName string) {
	output, err := a.hub.KillProcess(agentID, 0, procName)
	if err != nil {
		slog.Warn("auto-kill failed", "agent_id", agentID, "hostname", hostname, "process", procName, "error", err)
		a.db.InsertAuditLog("auto_kill_failed", agentID, fmt.Sprintf("name=%s error=%s", procName, err.Error()), "system")
		return
	}
	slog.Info("auto-kill succeeded", "agent_id", agentID, "hostname", hostname, "process", procName, "output", output)
	a.db.InsertAuditLog("auto_kill", agentID, fmt.Sprintf("name=%s output=%s", procName, output), "system")
}

// CheckRecovery fires a recovery alert when an agent reconnects after being offline.
func (a *Alerter) CheckRecovery(agentID, hostname string) {
	settings, err := a.db.GetAllSettings()
	if err != nil {
		return
	}
	msg := fmt.Sprintf("✅ PC kembali online: %s — %s",
		hostname, nowWIB().Format("15:04 WIB"))
	a.fire(agentID, "recovery", msg, settings)
}

// FireTamperAlert fires a peripheral-tamper alert (mouse/keyboard unplugged)
// through the same fire() pipeline CPU/RAM/offline/recovery alerts already
// use — alerts table insert, Telegram, email — instead of a second
// notification path.
func (a *Alerter) FireTamperAlert(agentID, hostname, deviceName, deviceClass string) {
	settings, err := a.db.GetAllSettings()
	if err != nil {
		slog.Error("alert: get settings for tamper alert", "error", err)
		return
	}
	label := "Perangkat"
	switch deviceClass {
	case "keyboard":
		label = "Keyboard"
	case "pointing_device":
		label = "Mouse"
	}
	msg := fmt.Sprintf("🚨 %s terlepas di %s: %s — %s",
		label, hostname, deviceName, nowWIB().Format("15:04 WIB"))
	a.fire(agentID, "peripheral_removed", msg, settings)
}

// StartOfflineChecker runs indefinitely, checking every 2 minutes for agents
// that haven't been seen for longer than offline_after_minutes.
func (a *Alerter) StartOfflineChecker() {
	ticker := time.NewTicker(offlineCheckInterval)
	defer ticker.Stop()
	for range ticker.C {
		a.checkOfflineAgents()
	}
}

func (a *Alerter) checkOfflineAgents() {
	settings, err := a.db.GetAllSettings()
	if err != nil {
		return
	}
	offlineMinutes := parseIntSetting(settings["offline_after_minutes"], 5)
	cutoff := nowWIB().Add(-time.Duration(offlineMinutes) * time.Minute)

	agents, err := a.db.GetAllAgents()
	if err != nil {
		slog.Error("alert: get agents for offline check", "error", err)
		return
	}

	for _, ag := range agents {
		if ag.Status != "offline" || !ag.LastSeen.Before(cutoff) {
			continue
		}
		// Don't re-alert for the same disconnect: skip if last offline alert was after last_seen.
		last, _ := a.db.GetLastAlert(ag.ID, "offline")
		if last != nil && last.SentAt.After(ag.LastSeen) {
			continue
		}
		msg := fmt.Sprintf("🔴 PC offline: %s — sejak %s",
			ag.Hostname, ag.LastSeen.Format("15:04 WIB"))
		a.fire(ag.ID, "offline", msg, settings)
	}
}

func (a *Alerter) fire(agentID, alertType, message string, settings map[string]string) {
	if err := a.db.InsertAlert(agentID, alertType, message); err != nil {
		slog.Error("alert: insert failed", "error", err)
		return
	}
	slog.Info("alert fired", "type", alertType, "agent_id", agentID)
	go a.sendTelegram(message, settings["telegram_token"], settings["telegram_chat_id"])
	go a.sendEmail(message, settings)
}

// NotifyEvent sends a Phase 2 system-policy notification (USB, blocked
// execution, software install, desktop/config change, ...) through the
// exact same Telegram/email plumbing fire() already uses above — reusing
// sendTelegram/sendEmail instead of building a second notification path
// (Module 9). Unlike fire(), it does NOT touch the alerts table or any
// cooldown/consecutive-count state: those stay CPU/RAM/blacklist/offline-
// recovery only, exactly as they were before Phase 2. Callers (server/
// events.go, server/policy.go) are responsible for their own dedup —
// Phase 2 events aren't threshold-based, so the cooldown model above
// doesn't apply to them.
func (a *Alerter) NotifyEvent(message string) {
	settings, err := a.db.GetAllSettings()
	if err != nil {
		slog.Error("notify event: get settings", "error", err)
		return
	}
	go a.sendTelegram(message, settings["telegram_token"], settings["telegram_chat_id"])
	go a.sendEmail(message, settings)
}

func (a *Alerter) sendTelegram(message, token, chatID string) {
	if token == "" || chatID == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    message,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.telegram.org/bot"+token+"/sendMessage",
		bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("telegram: send failed", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		slog.Warn("telegram: non-200 response", "status", resp.StatusCode)
	}
}

func buildDialer(settings map[string]string) (*gomail.Dialer, string, string, bool) {
	host := settings["smtp_host"]
	to := settings["smtp_to"]
	user := settings["smtp_user"]
	pass := settings["smtp_pass"]
	port := parseIntSetting(settings["smtp_port"], 587)
	if host == "" || to == "" || user == "" {
		return nil, "", "", false
	}
	d := gomail.NewDialer(host, port, user, pass)
	switch settings["smtp_tls"] {
	case "ssl":
		d.SSL = true
	default: // "starttls" or empty
		d.SSL = false
	}
	return d, user, to, true
}

func (a *Alerter) sendEmail(message string, settings map[string]string) {
	d, from, to, ok := buildDialer(settings)
	if !ok {
		return
	}
	m := gomail.NewMessage()
	m.SetHeader("From", from)
	m.SetHeader("To", to)
	m.SetHeader("Subject", "Library Monitor Alert")
	m.SetBody("text/plain", message)
	if err := d.DialAndSend(m); err != nil {
		slog.Error("email: send failed", "error", err)
	}
}

// SendTestTelegram sends a test message using current DB settings and returns any error.
func (a *Alerter) SendTestTelegram() error {
	settings, err := a.db.GetAllSettings()
	if err != nil {
		return err
	}
	token := settings["telegram_token"]
	chatID := settings["telegram_chat_id"]
	if token == "" || chatID == "" {
		return fmt.Errorf("Telegram belum dikonfigurasi (token/chat_id kosong)")
	}
	body, _ := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    "✅ Library Monitor UIII: Tes notifikasi Telegram berhasil",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.telegram.org/bot"+token+"/sendMessage",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Telegram merespons dengan status %d", resp.StatusCode)
	}
	return nil
}

// SendTestEmail sends a test email using current DB settings and returns any error.
func (a *Alerter) SendTestEmail() error {
	settings, err := a.db.GetAllSettings()
	if err != nil {
		return err
	}
	d, from, to, ok := buildDialer(settings)
	if !ok {
		return fmt.Errorf("Email belum dikonfigurasi (smtp_host/smtp_to/smtp_user kosong)")
	}
	m := gomail.NewMessage()
	m.SetHeader("From", from)
	m.SetHeader("To", to)
	m.SetHeader("Subject", "Library Monitor UIII — Tes Email")
	m.SetBody("text/plain", "✅ Library Monitor UIII: Tes notifikasi email berhasil")
	return d.DialAndSend(m)
}

func parseBoolSetting(s string) bool {
	return s == "true" || s == "1"
}

func parseFloatSetting(s string, def float64) float64 {
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil || v == 0 {
		return def
	}
	return v
}

func parseIntSetting(s string, def int) int {
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil || v == 0 {
		return def
	}
	return v
}

func parseBlacklistSetting(s string) []string {
	if s == "" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(s), &list); err != nil {
		return nil
	}
	return list
}
