package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var wibLoc *time.Location

func init() {
	var err error
	wibLoc, err = time.LoadLocation("Asia/Jakarta")
	if err != nil {
		wibLoc = time.FixedZone("WIB", 7*60*60)
	}
}

func nowWIB() time.Time {
	return time.Now().In(wibLoc)
}

func fmtTime(t time.Time) string {
	return t.In(wibLoc).Format("2006-01-02 15:04:05")
}

func parseDBTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, wibLoc)
	if err == nil {
		return t
	}
	t, err = time.ParseInLocation(time.RFC3339, s, wibLoc)
	if err == nil {
		return t
	}
	return time.Time{}
}

// ─── Data Types ────────────────────────────────────────────────────────────

type Agent struct {
	ID        string    `json:"id"`
	Hostname  string    `json:"hostname"`
	IP        string    `json:"ip"`
	OS        string    `json:"os"`
	LastSeen  time.Time `json:"last_seen"`
	MeshID    string    `json:"mesh_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentWithMetrics struct {
	Agent
	CPU        float64 `json:"cpu"`
	RAM        float64 `json:"ram"`
	TopProcess string  `json:"top_process"`
}

type Metric struct {
	ID         int64     `json:"id"`
	AgentID    string    `json:"agent_id"`
	CPU        float64   `json:"cpu"`
	RAM        float64   `json:"ram"`
	RecordedAt time.Time `json:"recorded_at"`
}

type Process struct {
	Name string  `json:"name"`
	PID  int     `json:"pid"`
	CPU  float64 `json:"cpu"`
	RAM  float64 `json:"ram"`
}

type Alert struct {
	ID      int64     `json:"id"`
	AgentID string    `json:"agent_id"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sent_at"`
}

type DeployJob struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Payload   string    `json:"payload"`
	Args      string    `json:"args"`
	Targets   string    `json:"targets"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type DeployResult struct {
	ID         int64      `json:"id"`
	JobID      string     `json:"job_id"`
	AgentID    string     `json:"agent_id"`
	Status     string     `json:"status"`
	Output     string     `json:"output"`
	ExecutedAt *time.Time `json:"executed_at"`
}

// ─── DB Init ───────────────────────────────────────────────────────────────

type DB struct {
	*sql.DB
}

func initDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db := &DB{raw}
	if err := db.configure(); err != nil {
		return nil, fmt.Errorf("configure db: %w", err)
	}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate db: %w", err)
	}
	return db, nil
}

func (db *DB) configure() error {
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return nil
}

func (db *DB) migrate() error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS agents (
		id         TEXT PRIMARY KEY,
		hostname   TEXT NOT NULL,
		ip         TEXT NOT NULL,
		os         TEXT NOT NULL DEFAULT '',
		last_seen  TEXT NOT NULL,
		mesh_id    TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT 'offline',
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS metrics (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id    TEXT NOT NULL,
		cpu         REAL NOT NULL,
		ram         REAL NOT NULL,
		recorded_at TEXT NOT NULL,
		FOREIGN KEY (agent_id) REFERENCES agents(id)
	);
	CREATE INDEX IF NOT EXISTS idx_metrics_agent_time
		ON metrics(agent_id, recorded_at);

	CREATE TABLE IF NOT EXISTS processes (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id    TEXT NOT NULL,
		name        TEXT NOT NULL,
		pid         INTEGER NOT NULL,
		cpu         REAL NOT NULL,
		ram         REAL NOT NULL,
		recorded_at TEXT NOT NULL,
		FOREIGN KEY (agent_id) REFERENCES agents(id)
	);
	CREATE INDEX IF NOT EXISTS idx_processes_agent_time
		ON processes(agent_id, recorded_at);

	CREATE TABLE IF NOT EXISTS alerts (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id TEXT NOT NULL,
		type     TEXT NOT NULL,
		message  TEXT NOT NULL,
		sent_at  TEXT NOT NULL,
		FOREIGN KEY (agent_id) REFERENCES agents(id)
	);
	CREATE INDEX IF NOT EXISTS idx_alerts_agent_type
		ON alerts(agent_id, type, sent_at);

	CREATE TABLE IF NOT EXISTS deploy_jobs (
		id         TEXT PRIMARY KEY,
		type       TEXT NOT NULL,
		payload    TEXT NOT NULL,
		args       TEXT NOT NULL DEFAULT '',
		targets    TEXT NOT NULL,
		status     TEXT NOT NULL DEFAULT 'pending',
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS deploy_results (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id      TEXT NOT NULL,
		agent_id    TEXT NOT NULL,
		status      TEXT NOT NULL DEFAULT 'pending',
		output      TEXT NOT NULL DEFAULT '',
		executed_at TEXT,
		FOREIGN KEY (job_id)    REFERENCES deploy_jobs(id),
		FOREIGN KEY (agent_id) REFERENCES agents(id)
	);
	CREATE INDEX IF NOT EXISTS idx_deploy_results_job
		ON deploy_results(job_id, agent_id);

	CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`)
	return err
}

// ─── Agent Queries ─────────────────────────────────────────────────────────

func (db *DB) UpsertAgent(a *Agent) error {
	_, err := db.Exec(`
		INSERT INTO agents (id, hostname, ip, os, last_seen, mesh_id, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname  = excluded.hostname,
			ip        = excluded.ip,
			os        = excluded.os,
			last_seen = excluded.last_seen,
			status    = excluded.status
	`, a.ID, a.Hostname, a.IP, a.OS,
		fmtTime(a.LastSeen), a.MeshID, a.Status, fmtTime(a.CreatedAt))
	return err
}

func (db *DB) GetAllAgents() ([]AgentWithMetrics, error) {
	rows, err := db.Query(`
		SELECT
			a.id, a.hostname, a.ip, a.os, a.last_seen, a.mesh_id, a.status, a.created_at,
			COALESCE(lm.cpu, 0.0),
			COALESCE(lm.ram, 0.0),
			COALESCE(tp.name, '')
		FROM agents a
		LEFT JOIN (
			SELECT agent_id, cpu, ram,
			       ROW_NUMBER() OVER (PARTITION BY agent_id ORDER BY recorded_at DESC) AS rn
			FROM metrics
		) lm ON a.id = lm.agent_id AND lm.rn = 1
		LEFT JOIN (
			SELECT agent_id, name,
			       ROW_NUMBER() OVER (PARTITION BY agent_id ORDER BY recorded_at DESC, cpu DESC) AS rn
			FROM processes
		) tp ON a.id = tp.agent_id AND tp.rn = 1
		ORDER BY a.hostname ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]AgentWithMetrics, 0)
	for rows.Next() {
		var a AgentWithMetrics
		var lastSeen, createdAt string
		if err := rows.Scan(
			&a.ID, &a.Hostname, &a.IP, &a.OS,
			&lastSeen, &a.MeshID, &a.Status, &createdAt,
			&a.CPU, &a.RAM, &a.TopProcess,
		); err != nil {
			return nil, err
		}
		a.LastSeen = parseDBTime(lastSeen)
		a.CreatedAt = parseDBTime(createdAt)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (db *DB) GetAgentByID(id string) (*AgentWithMetrics, error) {
	var a AgentWithMetrics
	var lastSeen, createdAt string
	err := db.QueryRow(
		`SELECT id, hostname, ip, os, last_seen, mesh_id, status, created_at
		 FROM agents WHERE id = ?`, id,
	).Scan(&a.ID, &a.Hostname, &a.IP, &a.OS, &lastSeen, &a.MeshID, &a.Status, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.LastSeen = parseDBTime(lastSeen)
	a.CreatedAt = parseDBTime(createdAt)

	var cpu, ram float64
	if err := db.QueryRow(
		`SELECT cpu, ram FROM metrics WHERE agent_id = ? ORDER BY recorded_at DESC LIMIT 1`, id,
	).Scan(&cpu, &ram); err == nil {
		a.CPU = cpu
		a.RAM = ram
	}

	var topName string
	if err := db.QueryRow(
		`SELECT name FROM processes WHERE agent_id = ? ORDER BY recorded_at DESC, cpu DESC LIMIT 1`, id,
	).Scan(&topName); err == nil {
		a.TopProcess = topName
	}

	return &a, nil
}

func (db *DB) UpdateAgentStatus(id, status string) error {
	_, err := db.Exec(
		`UPDATE agents SET status = ?, last_seen = ? WHERE id = ?`,
		status, fmtTime(nowWIB()), id,
	)
	return err
}

func (db *DB) SetAgentMeshID(id, meshID string) error {
	_, err := db.Exec(`UPDATE agents SET mesh_id = ? WHERE id = ?`, meshID, id)
	return err
}

func (db *DB) GetAllAgentIDs() ([]string, error) {
	rows, err := db.Query(`SELECT id FROM agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ─── Metrics Queries ───────────────────────────────────────────────────────

func (db *DB) InsertMetric(agentID string, cpu, ram float64) error {
	_, err := db.Exec(
		`INSERT INTO metrics (agent_id, cpu, ram, recorded_at) VALUES (?, ?, ?, ?)`,
		agentID, cpu, ram, fmtTime(nowWIB()),
	)
	return err
}

func (db *DB) GetMetrics24h(agentID string) ([]Metric, error) {
	cutoff := fmtTime(nowWIB().Add(-24 * time.Hour))
	rows, err := db.Query(`
		SELECT id, agent_id, cpu, ram, recorded_at
		FROM metrics
		WHERE agent_id = ? AND recorded_at >= ?
		ORDER BY recorded_at ASC
	`, agentID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Metric
	for rows.Next() {
		var m Metric
		var recAt string
		if err := rows.Scan(&m.ID, &m.AgentID, &m.CPU, &m.RAM, &recAt); err != nil {
			return nil, err
		}
		m.RecordedAt = parseDBTime(recAt)
		result = append(result, m)
	}
	return result, rows.Err()
}

func (db *DB) PurgeOldMetrics() error {
	cutoff := fmtTime(nowWIB().Add(-24 * time.Hour))
	_, err := db.Exec(`DELETE FROM metrics WHERE recorded_at < ?`, cutoff)
	return err
}

// ─── Process Queries ───────────────────────────────────────────────────────

func (db *DB) UpsertProcesses(agentID string, procs []Process) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM processes WHERE agent_id = ?`, agentID); err != nil {
		return err
	}

	now := fmtTime(nowWIB())
	for _, p := range procs {
		if _, err := tx.Exec(
			`INSERT INTO processes (agent_id, name, pid, cpu, ram, recorded_at) VALUES (?, ?, ?, ?, ?, ?)`,
			agentID, p.Name, p.PID, p.CPU, p.RAM, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) GetProcesses(agentID string) ([]Process, error) {
	rows, err := db.Query(`
		SELECT name, pid, cpu, ram FROM processes WHERE agent_id = ? ORDER BY cpu DESC
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Process
	for rows.Next() {
		var p Process
		if err := rows.Scan(&p.Name, &p.PID, &p.CPU, &p.RAM); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// ─── Alert Queries ─────────────────────────────────────────────────────────

func (db *DB) InsertAlert(agentID, alertType, message string) error {
	_, err := db.Exec(
		`INSERT INTO alerts (agent_id, type, message, sent_at) VALUES (?, ?, ?, ?)`,
		agentID, alertType, message, fmtTime(nowWIB()),
	)
	return err
}

func (db *DB) GetRecentAlerts(limit int) ([]Alert, error) {
	rows, err := db.Query(`
		SELECT id, agent_id, type, message, sent_at
		FROM alerts ORDER BY sent_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Alert, 0)
	for rows.Next() {
		var a Alert
		var sentAt string
		if err := rows.Scan(&a.ID, &a.AgentID, &a.Type, &a.Message, &sentAt); err != nil {
			return nil, err
		}
		a.SentAt = parseDBTime(sentAt)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (db *DB) GetAlertsTodayCount() (int, error) {
	today := nowWIB().Format("2006-01-02") + " 00:00:00"
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE sent_at >= ?`, today).Scan(&count)
	return count, err
}

func (db *DB) GetLastAlert(agentID, alertType string) (*Alert, error) {
	var a Alert
	var sentAt string
	err := db.QueryRow(`
		SELECT id, agent_id, type, message, sent_at FROM alerts
		WHERE agent_id = ? AND type = ?
		ORDER BY sent_at DESC LIMIT 1
	`, agentID, alertType).Scan(&a.ID, &a.AgentID, &a.Type, &a.Message, &sentAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.SentAt = parseDBTime(sentAt)
	return &a, nil
}

func (db *DB) GetLastAlertForApp(agentID, appName string) (*Alert, error) {
	var a Alert
	var sentAt string
	err := db.QueryRow(`
		SELECT id, agent_id, type, message, sent_at FROM alerts
		WHERE agent_id = ? AND type = 'blacklisted_app' AND message LIKE ?
		ORDER BY sent_at DESC LIMIT 1
	`, agentID, "%"+appName+"%").Scan(&a.ID, &a.AgentID, &a.Type, &a.Message, &sentAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.SentAt = parseDBTime(sentAt)
	return &a, nil
}

// ─── Deploy Queries ────────────────────────────────────────────────────────

func (db *DB) InsertDeployJob(job *DeployJob) error {
	_, err := db.Exec(`
		INSERT INTO deploy_jobs (id, type, payload, args, targets, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, job.ID, job.Type, job.Payload, job.Args, job.Targets, job.Status, fmtTime(job.CreatedAt))
	return err
}

func (db *DB) GetAllDeployJobs() ([]DeployJob, error) {
	rows, err := db.Query(`
		SELECT id, type, payload, args, targets, status, created_at
		FROM deploy_jobs ORDER BY created_at DESC LIMIT 100
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]DeployJob, 0)
	for rows.Next() {
		var j DeployJob
		var createdAt string
		if err := rows.Scan(&j.ID, &j.Type, &j.Payload, &j.Args, &j.Targets, &j.Status, &createdAt); err != nil {
			return nil, err
		}
		j.CreatedAt = parseDBTime(createdAt)
		result = append(result, j)
	}
	return result, rows.Err()
}

func (db *DB) GetDeployJobByID(id string) (*DeployJob, error) {
	var j DeployJob
	var createdAt string
	err := db.QueryRow(`
		SELECT id, type, payload, args, targets, status, created_at
		FROM deploy_jobs WHERE id = ?
	`, id).Scan(&j.ID, &j.Type, &j.Payload, &j.Args, &j.Targets, &j.Status, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.CreatedAt = parseDBTime(createdAt)
	return &j, nil
}

func (db *DB) UpdateDeployJobStatus(id, status string) error {
	_, err := db.Exec(`UPDATE deploy_jobs SET status = ? WHERE id = ?`, status, id)
	return err
}

func (db *DB) InsertDeployResult(r *DeployResult) error {
	_, err := db.Exec(`
		INSERT INTO deploy_results (job_id, agent_id, status, output) VALUES (?, ?, ?, ?)
	`, r.JobID, r.AgentID, r.Status, r.Output)
	return err
}

func (db *DB) GetDeployResultsByJobID(jobID string) ([]DeployResult, error) {
	rows, err := db.Query(`
		SELECT id, job_id, agent_id, status, output, executed_at
		FROM deploy_results WHERE job_id = ? ORDER BY id ASC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DeployResult
	for rows.Next() {
		var r DeployResult
		var exAt sql.NullString
		if err := rows.Scan(&r.ID, &r.JobID, &r.AgentID, &r.Status, &r.Output, &exAt); err != nil {
			return nil, err
		}
		if exAt.Valid {
			t := parseDBTime(exAt.String)
			r.ExecutedAt = &t
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (db *DB) UpdateDeployResult(jobID, agentID, status, output string) error {
	_, err := db.Exec(`
		UPDATE deploy_results SET status = ?, output = ?, executed_at = ?
		WHERE job_id = ? AND agent_id = ?
	`, status, output, fmtTime(nowWIB()), jobID, agentID)
	return err
}

func (db *DB) GetPendingJobsForAgent(agentID string) ([]DeployJob, error) {
	rows, err := db.Query(`
		SELECT j.id, j.type, j.payload, j.args, j.targets, j.status, j.created_at
		FROM deploy_jobs j
		JOIN deploy_results r ON j.id = r.job_id
		WHERE r.agent_id = ? AND r.status = 'pending'
		ORDER BY j.created_at ASC
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DeployJob
	for rows.Next() {
		var j DeployJob
		var createdAt string
		if err := rows.Scan(&j.ID, &j.Type, &j.Payload, &j.Args, &j.Targets, &j.Status, &createdAt); err != nil {
			return nil, err
		}
		j.CreatedAt = parseDBTime(createdAt)
		result = append(result, j)
	}
	return result, rows.Err()
}

// ─── Settings Queries ──────────────────────────────────────────────────────

func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

func (db *DB) GetSetting(key string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (db *DB) GetAllSettings() (map[string]string, error) {
	rows, err := db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		settings[k] = v
	}
	return settings, rows.Err()
}

func (db *DB) InitDefaultSettings(cfg *Config) error {
	blacklistJSON, _ := json.Marshal(cfg.Alerts.Blacklist)

	defaults := map[string]string{
		"cpu_threshold":         fmt.Sprintf("%.0f", cfg.Alerts.CPUThreshold),
		"ram_threshold":         fmt.Sprintf("%.0f", cfg.Alerts.RAMThreshold),
		"offline_after_minutes": fmt.Sprintf("%d", cfg.Alerts.OfflineAfterMinutes),
		"blacklist":             string(blacklistJSON),
		"telegram_token":        cfg.Telegram.Token,
		"telegram_chat_id":      cfg.Telegram.ChatID,
		"smtp_host":             cfg.Email.SMTPHost,
		"smtp_port":             fmt.Sprintf("%d", cfg.Email.SMTPPort),
		"smtp_user":             cfg.Email.SMTPUser,
		"smtp_pass":             cfg.Email.SMTPPass,
		"smtp_to":               cfg.Email.SMTPTo,
		"mesh_url":              cfg.MeshCentral.URL,
	}

	for k, v := range defaults {
		existing, err := db.GetSetting(k)
		if err != nil {
			return err
		}
		if existing == "" {
			if err := db.SetSetting(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}
