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
	consecutive sync.Map // "agentID:alertType" → int
}

func NewAlerter(db *DB) *Alerter {
	return &Alerter{db: db}
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
	for _, proc := range procs {
		for _, bl := range blacklist {
			if strings.EqualFold(proc.Name, bl) {
				last, _ := a.db.GetLastAlertForApp(agentID, proc.Name)
				if last == nil || nowWIB().Sub(last.SentAt) >= blacklistCooldown {
					msg := fmt.Sprintf("🚫 Aplikasi terlarang terdeteksi di %s: %s — %s",
						hostname, proc.Name, nowWIB().Format("15:04 WIB"))
					a.fire(agentID, "blacklisted_app", msg, settings)
				}
				break
			}
		}
	}
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

func (a *Alerter) sendEmail(message string, settings map[string]string) {
	host := settings["smtp_host"]
	to := settings["smtp_to"]
	user := settings["smtp_user"]
	pass := settings["smtp_pass"]
	port := parseIntSetting(settings["smtp_port"], 587)
	if host == "" || to == "" || user == "" {
		return
	}
	m := gomail.NewMessage()
	m.SetHeader("From", user)
	m.SetHeader("To", to)
	m.SetHeader("Subject", "Library Monitor Alert")
	m.SetBody("text/plain", message)
	d := gomail.NewDialer(host, port, user, pass)
	if err := d.DialAndSend(m); err != nil {
		slog.Error("email: send failed", "error", err)
	}
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
