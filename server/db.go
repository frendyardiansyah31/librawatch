package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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
	ID             string    `json:"id"`
	Hostname       string    `json:"hostname"`
	IP             string    `json:"ip"`
	OS             string    `json:"os"`
	LastSeen       time.Time `json:"last_seen"`
	MeshID         string    `json:"mesh_id"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	AgentVersion   string    `json:"agent_version"`
	WindowsVersion string    `json:"windows_version"`
	DiskCapacityGB float64   `json:"disk_capacity_gb"`
	DeviceGroup    string    `json:"device_group"`
	MacAddress     string    `json:"mac_address"`
	Floor          string    `json:"floor"`

	// Desired-state network mode reconciliation (see agent/network.go).
	// DesiredNetworkMode is admin-set and drives agent behavior; the rest
	// reflect the agent's last self-reported reconciliation outcome.
	DesiredNetworkMode   string    `json:"desired_network_mode"`
	CurrentNetworkMode   string    `json:"current_network_mode"`
	NetworkModeStatus    string    `json:"network_mode_status"`
	NetworkModeDetail    string    `json:"network_mode_detail"`
	NetworkModeUpdatedAt time.Time `json:"network_mode_updated_at"`
}

type AgentWithMetrics struct {
	Agent
	CPU                    float64 `json:"cpu"`
	RAM                    float64 `json:"ram"`
	TopProcess             string  `json:"top_process"`
	InstalledSoftwareCount int     `json:"installed_software_count"`
	RunningProcessCount    int     `json:"running_process_count"`
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

	// Optional metadata, populated by the agent only the first time it sees a
	// given executable path in its session (see agent/appmeta.go). Empty on
	// most messages — the catalog upsert (see catalog.go) treats empty
	// metadata fields as "no update" rather than blanking existing data.
	Path           string `json:"path,omitempty"`
	ProductName    string `json:"product_name,omitempty"`
	Company        string `json:"company,omitempty"`
	Description    string `json:"description,omitempty"`
	ProductVersion string `json:"product_version,omitempty"`
	Size           int64  `json:"size,omitempty"`
	FileCreatedAt  string `json:"file_created_at,omitempty"`
	FileModifiedAt string `json:"file_modified_at,omitempty"`
}

type Category struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ApplicationStatus values, validated in server/api.go.
const (
	AppStatusPendingReview = "pending_review"
	AppStatusAllowed       = "allowed"
	AppStatusBlocked       = "blocked"
	AppStatusIgnored       = "ignored"
)

type Application struct {
	ID             int64     `json:"id"`
	ExeName        string    `json:"exe_name"`
	Company        string    `json:"company"`
	ProductName    string    `json:"product_name"`
	Description    string    `json:"description"`
	ProductVersion string    `json:"product_version"`
	CategoryID     *int64    `json:"category_id"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ApplicationWithStats is the row shape for GET /api/applications.
type ApplicationWithStats struct {
	Application
	CategoryName    string    `json:"category_name"`
	DeviceCount     int       `json:"device_count"`
	TotalExecutions int       `json:"total_executions"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
}

type AppSighting struct {
	AgentID        string    `json:"agent_id"`
	Hostname       string    `json:"hostname"`
	ApplicationID  int64     `json:"application_id"`
	Path           string    `json:"path"`
	Size           int64     `json:"size"`
	FileCreatedAt  string    `json:"file_created_at"`
	FileModifiedAt string    `json:"file_modified_at"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	ExecCount      int       `json:"exec_count"`
}

// ApplicationDetail is the response shape for GET /api/applications/:id.
type ApplicationDetail struct {
	ApplicationWithStats
	Sightings []AppSighting `json:"sightings"`
}

// AppMetadata carries the optional per-executable facts the agent extracts
// once per unique path. Nil means "no metadata available this cycle" — the
// catalog upsert leaves any previously recorded fields untouched.
type AppMetadata struct {
	ProductName    string
	Company        string
	Description    string
	ProductVersion string
	Size           int64
	FileCreatedAt  time.Time
	FileModifiedAt time.Time
}

// Event action values, set by the Policy Engine (server/policy.go) once it
// evaluates an event or an executing process against policy_rules.
const (
	EventActionLog     = "log"
	EventActionNotify  = "notify"
	EventActionBlocked = "blocked"
	EventActionDeleted = "deleted"
	EventActionKilled  = "killed"
)

// Event is a single System Policy Enforcement occurrence (USB, download,
// desktop/config change, install, blocked execution, ...). Separate from the
// existing Alert table on purpose — Alert stays 100% CPU/RAM/blacklist/
// offline-recovery, unmodified from Phase 1.
type Event struct {
	ID        int64     `json:"id"`
	AgentID   string    `json:"agent_id"`
	Hostname  string    `json:"hostname,omitempty"`
	Type      string    `json:"type"`
	Metadata  string    `json:"metadata"`
	Action    string    `json:"action"`
	CreatedAt time.Time `json:"created_at"`
}

// PolicyRuleAction values, validated in server/api.go.
const (
	PolicyActionLog    = "log"
	PolicyActionNotify = "notify"
	PolicyActionBlock  = "block"
	PolicyActionDelete = "delete"
	PolicyActionKill   = "kill"
)

// PolicyRule is one data-driven rule the Policy Engine (server/policy.go)
// matches against. Empty string fields mean "any" for that dimension.
type PolicyRule struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	EventType         string    `json:"event_type"`
	CategoryID        *int64    `json:"category_id"`
	FileExtension     string    `json:"file_extension"`
	ExecutionLocation string    `json:"execution_location"`
	DeviceGroup       string    `json:"device_group"`
	Action            string    `json:"action"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
}

type Alert struct {
	ID      int64     `json:"id"`
	AgentID string    `json:"agent_id"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sent_at"`
}

type DeployJob struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Payload   string     `json:"payload"`
	Args      string     `json:"args"`
	Targets   string     `json:"targets"`
	Status    string     `json:"status"`
	Priority  int        `json:"priority"`
	ExpireAt  *time.Time `json:"expire_at,omitempty"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
}

type DeployResult struct {
	ID         int64      `json:"id"`
	JobID      string     `json:"job_id"`
	AgentID    string     `json:"agent_id"`
	Status     string     `json:"status"`
	Output     string     `json:"output"`
	ExecutedAt *time.Time `json:"executed_at"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	RetryCount int        `json:"retry_count"`
	MaxRetry   int        `json:"max_retry"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	DurationMS *int64     `json:"duration_ms,omitempty"`
}

// isPendingLikeStatus reports whether a deploy_results row is still "in
// flight" — i.e. hasn't reached a terminal state yet. Every call site that
// used to check `status != "pending"` to mean "the agent replied" must use
// this instead now that "running" also means "not done yet" (server/mcp.go's
// queryDeepFreezeStatus and dashboard/app.js's startJobPoller both do).
func isPendingLikeStatus(status string) bool {
	return status == "pending" || status == "running"
}

type AuditLog struct {
	ID     int64  `json:"id"`
	Ts     string `json:"ts"`
	Action string `json:"action"`
	Target string `json:"target"`
	Detail string `json:"detail"`
	IP     string `json:"ip"`
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
	// SQLite allows only one writer at a time. A single connection serializes
	// all access and ensures busy_timeout pragma applies to every operation.
	raw.SetMaxOpenConns(1)

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
		"PRAGMA cache_size=-64000", // 64 MB page cache
		"PRAGMA temp_store=MEMORY",
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

	CREATE TABLE IF NOT EXISTS audit_logs (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		ts     TEXT NOT NULL,
		action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		ip     TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(ts);

	CREATE TABLE IF NOT EXISTS categories (
		id   INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE
	);

	CREATE TABLE IF NOT EXISTS applications (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		exe_name         TEXT NOT NULL,
		company          TEXT NOT NULL DEFAULT '',
		product_name     TEXT NOT NULL DEFAULT '',
		description      TEXT NOT NULL DEFAULT '',
		product_version  TEXT NOT NULL DEFAULT '',
		category_id      INTEGER REFERENCES categories(id),
		status           TEXT NOT NULL DEFAULT 'pending_review',
		sha256           TEXT,
		signature_status TEXT,
		created_at       TEXT NOT NULL,
		updated_at       TEXT NOT NULL
	);
	-- Partial index: install-detected rows (Module 5) leave exe_name blank
	-- since the Uninstall registry never exposes it, and dedup for those goes
	-- through UpsertApplicationByProduct's explicit (product_name, company)
	-- lookup instead — a plain unique index on (exe_name, company) would
	-- wrongly collide multiple different products from the same company that
	-- both have exe_name=''.
	CREATE UNIQUE INDEX IF NOT EXISTS idx_applications_identity ON applications(exe_name, company) WHERE exe_name <> '';
	CREATE INDEX IF NOT EXISTS idx_applications_status ON applications(status);

	CREATE TABLE IF NOT EXISTS app_sightings (
		agent_id          TEXT NOT NULL REFERENCES agents(id),
		application_id    INTEGER NOT NULL REFERENCES applications(id),
		path              TEXT NOT NULL,
		size              INTEGER NOT NULL DEFAULT 0,
		file_created_at   TEXT NOT NULL DEFAULT '',
		file_modified_at  TEXT NOT NULL DEFAULT '',
		first_seen        TEXT NOT NULL,
		last_seen         TEXT NOT NULL,
		exec_count        INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (agent_id, application_id, path)
	);
	CREATE INDEX IF NOT EXISTS idx_sightings_app ON app_sightings(application_id);

	CREATE TABLE IF NOT EXISTS events (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id   TEXT NOT NULL REFERENCES agents(id),
		type       TEXT NOT NULL,
		metadata   TEXT NOT NULL DEFAULT '{}',
		action     TEXT NOT NULL DEFAULT 'log',
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_events_agent_time ON events(agent_id, created_at);
	CREATE INDEX IF NOT EXISTS idx_events_type       ON events(type, created_at);

	CREATE TABLE IF NOT EXISTS policy_rules (
		id                 INTEGER PRIMARY KEY AUTOINCREMENT,
		name               TEXT NOT NULL,
		event_type         TEXT NOT NULL DEFAULT '',
		category_id        INTEGER REFERENCES categories(id),
		file_extension     TEXT NOT NULL DEFAULT '',
		execution_location TEXT NOT NULL DEFAULT '',
		device_group       TEXT NOT NULL DEFAULT '',
		action             TEXT NOT NULL DEFAULT 'log',
		enabled            INTEGER NOT NULL DEFAULT 1,
		created_at         TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_policy_rules_enabled ON policy_rules(enabled);
	`)
	if err != nil {
		return err
	}

	for _, col := range []struct{ name, decl string }{
		{"agent_version", "TEXT NOT NULL DEFAULT ''"},
		{"windows_version", "TEXT NOT NULL DEFAULT ''"},
		{"disk_capacity_gb", "REAL NOT NULL DEFAULT 0"},
		{"device_group", "TEXT NOT NULL DEFAULT ''"},
		{"desired_network_mode", "TEXT NOT NULL DEFAULT 'both'"},
		{"current_network_mode", "TEXT NOT NULL DEFAULT ''"},
		{"network_mode_status", "TEXT NOT NULL DEFAULT ''"},
		{"network_mode_detail", "TEXT NOT NULL DEFAULT ''"},
		{"network_mode_updated_at", "TEXT NOT NULL DEFAULT ''"},
		{"mac_address", "TEXT NOT NULL DEFAULT ''"},
		{"floor", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := db.addColumnIfMissing("agents", col.name, col.decl); err != nil {
			return fmt.Errorf("add column agents.%s: %w", col.name, err)
		}
	}

	for _, col := range []struct{ name, decl string }{
		{"priority", "INTEGER NOT NULL DEFAULT 0"},
		{"expire_at", "TEXT"},
		{"created_by", "TEXT NOT NULL DEFAULT 'system'"},
	} {
		if err := db.addColumnIfMissing("deploy_jobs", col.name, col.decl); err != nil {
			return fmt.Errorf("add column deploy_jobs.%s: %w", col.name, err)
		}
	}

	for _, col := range []struct{ name, decl string }{
		{"lease_until", "TEXT"},
		{"retry_count", "INTEGER NOT NULL DEFAULT 0"},
		{"max_retry", "INTEGER NOT NULL DEFAULT 0"},
		{"exit_code", "INTEGER"},
		{"duration_ms", "INTEGER"},
	} {
		if err := db.addColumnIfMissing("deploy_results", col.name, col.decl); err != nil {
			return fmt.Errorf("add column deploy_results.%s: %w", col.name, err)
		}
	}

	return nil
}

// addColumnIfMissing runs ALTER TABLE ... ADD COLUMN only if the column
// doesn't already exist, so it's safe to call on every startup — the same
// idempotent-migration philosophy as the CREATE TABLE IF NOT EXISTS blocks
// above, just for retrofitting a table that already exists.
func (db *DB) addColumnIfMissing(table, column, decl string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

// ─── Agent Queries ─────────────────────────────────────────────────────────

func (db *DB) DeleteAgent(id string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM metrics       WHERE agent_id = ?`,
		`DELETE FROM processes     WHERE agent_id = ?`,
		`DELETE FROM alerts        WHERE agent_id = ?`,
		`DELETE FROM deploy_results WHERE agent_id = ?`,
		`DELETE FROM agents        WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) UpsertAgent(a *Agent) error {
	_, err := db.Exec(`
		INSERT INTO agents (id, hostname, ip, os, last_seen, mesh_id, status, created_at, agent_version, windows_version, disk_capacity_gb, mac_address)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname         = excluded.hostname,
			ip               = excluded.ip,
			os               = excluded.os,
			last_seen        = excluded.last_seen,
			status           = excluded.status,
			agent_version    = CASE WHEN excluded.agent_version   <> '' THEN excluded.agent_version   ELSE agents.agent_version   END,
			windows_version  = CASE WHEN excluded.windows_version <> '' THEN excluded.windows_version ELSE agents.windows_version END,
			disk_capacity_gb = CASE WHEN excluded.disk_capacity_gb <> 0 THEN excluded.disk_capacity_gb ELSE agents.disk_capacity_gb END,
			mac_address      = CASE WHEN excluded.mac_address     <> '' THEN excluded.mac_address     ELSE agents.mac_address     END
	`, a.ID, a.Hostname, a.IP, a.OS,
		fmtTime(a.LastSeen), a.MeshID, a.Status, fmtTime(a.CreatedAt),
		a.AgentVersion, a.WindowsVersion, a.DiskCapacityGB, a.MacAddress)
	return err
}

func (db *DB) GetAllAgents() ([]AgentWithMetrics, error) {
	rows, err := db.Query(`
		SELECT
			a.id, a.hostname, a.ip, a.os, a.last_seen, a.mesh_id, a.status, a.created_at,
			a.agent_version, a.windows_version, a.disk_capacity_gb, a.device_group,
			a.desired_network_mode, a.current_network_mode, a.network_mode_status,
			a.network_mode_detail, a.network_mode_updated_at,
			a.mac_address, a.floor,
			COALESCE((SELECT cpu  FROM metrics   WHERE agent_id = a.id ORDER BY recorded_at DESC LIMIT 1), 0.0),
			COALESCE((SELECT ram  FROM metrics   WHERE agent_id = a.id ORDER BY recorded_at DESC LIMIT 1), 0.0),
			COALESCE((SELECT name FROM processes WHERE agent_id = a.id ORDER BY recorded_at DESC, cpu DESC LIMIT 1), ''),
			(SELECT COUNT(DISTINCT application_id) FROM app_sightings WHERE agent_id = a.id),
			(SELECT COUNT(*) FROM processes WHERE agent_id = a.id)
		FROM agents a
		ORDER BY a.hostname ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]AgentWithMetrics, 0)
	for rows.Next() {
		var a AgentWithMetrics
		var lastSeen, createdAt, networkModeUpdatedAt string
		if err := rows.Scan(
			&a.ID, &a.Hostname, &a.IP, &a.OS,
			&lastSeen, &a.MeshID, &a.Status, &createdAt,
			&a.AgentVersion, &a.WindowsVersion, &a.DiskCapacityGB, &a.DeviceGroup,
			&a.DesiredNetworkMode, &a.CurrentNetworkMode, &a.NetworkModeStatus,
			&a.NetworkModeDetail, &networkModeUpdatedAt,
			&a.MacAddress, &a.Floor,
			&a.CPU, &a.RAM, &a.TopProcess,
			&a.InstalledSoftwareCount, &a.RunningProcessCount,
		); err != nil {
			return nil, err
		}
		a.LastSeen = parseDBTime(lastSeen)
		a.CreatedAt = parseDBTime(createdAt)
		a.NetworkModeUpdatedAt = parseDBTime(networkModeUpdatedAt)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (db *DB) GetAgentByID(id string) (*AgentWithMetrics, error) {
	var a AgentWithMetrics
	var lastSeen, createdAt, networkModeUpdatedAt string
	err := db.QueryRow(
		`SELECT id, hostname, ip, os, last_seen, mesh_id, status, created_at, agent_version, windows_version, disk_capacity_gb, device_group,
		 	desired_network_mode, current_network_mode, network_mode_status, network_mode_detail, network_mode_updated_at,
		 	mac_address, floor
		 FROM agents WHERE id = ?`, id,
	).Scan(&a.ID, &a.Hostname, &a.IP, &a.OS, &lastSeen, &a.MeshID, &a.Status, &createdAt,
		&a.AgentVersion, &a.WindowsVersion, &a.DiskCapacityGB, &a.DeviceGroup,
		&a.DesiredNetworkMode, &a.CurrentNetworkMode, &a.NetworkModeStatus,
		&a.NetworkModeDetail, &networkModeUpdatedAt,
		&a.MacAddress, &a.Floor)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.LastSeen = parseDBTime(lastSeen)
	a.CreatedAt = parseDBTime(createdAt)
	a.NetworkModeUpdatedAt = parseDBTime(networkModeUpdatedAt)

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

	db.QueryRow(`SELECT COUNT(DISTINCT application_id) FROM app_sightings WHERE agent_id = ?`, id).
		Scan(&a.InstalledSoftwareCount)
	db.QueryRow(`SELECT COUNT(*) FROM processes WHERE agent_id = ?`, id).
		Scan(&a.RunningProcessCount)

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

func (db *DB) SetAgentDeviceGroup(id, group string) error {
	_, err := db.Exec(`UPDATE agents SET device_group = ? WHERE id = ?`, group, id)
	return err
}

func (db *DB) SetAgentFloor(id, floor string) error {
	_, err := db.Exec(`UPDATE agents SET floor = ? WHERE id = ?`, floor, id)
	return err
}

// GetAgentDeviceGroup is a lightweight lookup (no metrics/process subqueries,
// unlike GetAgentByID) for the Policy Engine, called once per metrics cycle
// per agent rather than the heavier full-agent query.
func (db *DB) GetAgentDeviceGroup(id string) (string, error) {
	var group string
	err := db.QueryRow(`SELECT device_group FROM agents WHERE id = ?`, id).Scan(&group)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return group, err
}

func (db *DB) SetAgentDesiredNetworkMode(id, mode string) error {
	_, err := db.Exec(`UPDATE agents SET desired_network_mode = ? WHERE id = ?`, mode, id)
	return err
}

// GetAgentDesiredNetworkMode defaults to "both" (no restriction) when the
// agent row can't be found, mirroring the column's own DB default.
func (db *DB) GetAgentDesiredNetworkMode(id string) (string, error) {
	var mode string
	err := db.QueryRow(`SELECT desired_network_mode FROM agents WHERE id = ?`, id).Scan(&mode)
	if err == sql.ErrNoRows {
		return "both", nil
	}
	return mode, err
}

// UpdateAgentNetworkModeResult records the agent's self-reported outcome of
// its last network-mode reconciliation attempt (see agent/network.go).
func (db *DB) UpdateAgentNetworkModeResult(id, mode, status, detail string) error {
	_, err := db.Exec(
		`UPDATE agents SET current_network_mode = ?, network_mode_status = ?, network_mode_detail = ?, network_mode_updated_at = ? WHERE id = ?`,
		mode, status, detail, fmtTime(nowWIB()), id,
	)
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
	cutoff := fmtTime(nowWIB().Add(-7 * 24 * time.Hour))
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
	var expireAt interface{}
	if job.ExpireAt != nil {
		expireAt = fmtTime(*job.ExpireAt)
	}
	_, err := db.Exec(`
		INSERT INTO deploy_jobs (id, type, payload, args, targets, status, priority, expire_at, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.ID, job.Type, job.Payload, job.Args, job.Targets, job.Status, job.Priority, expireAt, job.CreatedBy, fmtTime(job.CreatedAt))
	return err
}

func (db *DB) GetAllDeployJobs() ([]DeployJob, error) {
	rows, err := db.Query(`
		SELECT id, type, payload, args, targets, status, priority, expire_at, created_by, created_at
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
		var expireAt sql.NullString
		if err := rows.Scan(&j.ID, &j.Type, &j.Payload, &j.Args, &j.Targets, &j.Status,
			&j.Priority, &expireAt, &j.CreatedBy, &createdAt); err != nil {
			return nil, err
		}
		j.CreatedAt = parseDBTime(createdAt)
		if expireAt.Valid {
			t := parseDBTime(expireAt.String)
			j.ExpireAt = &t
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

func (db *DB) GetDeployJobByID(id string) (*DeployJob, error) {
	var j DeployJob
	var createdAt string
	var expireAt sql.NullString
	err := db.QueryRow(`
		SELECT id, type, payload, args, targets, status, priority, expire_at, created_by, created_at
		FROM deploy_jobs WHERE id = ?
	`, id).Scan(&j.ID, &j.Type, &j.Payload, &j.Args, &j.Targets, &j.Status,
		&j.Priority, &expireAt, &j.CreatedBy, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.CreatedAt = parseDBTime(createdAt)
	if expireAt.Valid {
		t := parseDBTime(expireAt.String)
		j.ExpireAt = &t
	}
	return &j, nil
}

func (db *DB) UpdateDeployJobStatus(id, status string) error {
	_, err := db.Exec(`UPDATE deploy_jobs SET status = ? WHERE id = ?`, status, id)
	return err
}

func (db *DB) CancelDeployJob(id string) error {
	_, err := db.Exec(`UPDATE deploy_jobs SET status = 'cancelled' WHERE id = ?`, id)
	if err != nil {
		return err
	}
	_, err = db.Exec(`
		UPDATE deploy_results SET status = 'cancelled', output = 'Job dibatalkan oleh admin', lease_until = NULL
		WHERE job_id = ? AND status IN ('pending', 'running')
	`, id)
	return err
}

func (db *DB) InsertDeployResult(r *DeployResult) error {
	_, err := db.Exec(`
		INSERT INTO deploy_results (job_id, agent_id, status, output, max_retry) VALUES (?, ?, ?, ?, ?)
	`, r.JobID, r.AgentID, r.Status, r.Output, r.MaxRetry)
	return err
}

func (db *DB) GetDeployResultsByJobID(jobID string) ([]DeployResult, error) {
	rows, err := db.Query(`
		SELECT id, job_id, agent_id, status, output, executed_at,
			lease_until, retry_count, max_retry, exit_code, duration_ms
		FROM deploy_results WHERE job_id = ? ORDER BY id ASC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DeployResult
	for rows.Next() {
		if r, err := scanDeployResult(rows); err != nil {
			return nil, err
		} else {
			result = append(result, r)
		}
	}
	return result, rows.Err()
}

// scanDeployResult scans one row shaped like GetDeployResultsByJobID's SELECT
// — shared so AcquireNextJob's paths and any future listing query don't
// duplicate the same nullable-column handling.
func scanDeployResult(rows *sql.Rows) (DeployResult, error) {
	var r DeployResult
	var exAt, leaseUntil sql.NullString
	var exitCode sql.NullInt64
	var durationMS sql.NullInt64
	if err := rows.Scan(&r.ID, &r.JobID, &r.AgentID, &r.Status, &r.Output, &exAt,
		&leaseUntil, &r.RetryCount, &r.MaxRetry, &exitCode, &durationMS); err != nil {
		return r, err
	}
	if exAt.Valid {
		t := parseDBTime(exAt.String)
		r.ExecutedAt = &t
	}
	if leaseUntil.Valid {
		t := parseDBTime(leaseUntil.String)
		r.LeaseUntil = &t
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		r.ExitCode = &v
	}
	if durationMS.Valid {
		r.DurationMS = &durationMS.Int64
	}
	return r, nil
}

// UpdateDeployResult applies a status change plus whatever result fields are
// known. retryCount == nil leaves retry_count untouched. lease_until is
// always cleared — every transition here either finishes the row or sends it
// back to a fresh pending state, so a stale lease never lingers. Guarded so a
// late real agent reply can never overwrite an admin cancellation.
//
// expectedAttempt fences an agent's ack against the retry_count that was
// current when the job was dispatched to it: nil skips the check (used by
// server-internal transitions — dispatch release, lease-sweep requeue/fail,
// pending-expiry — which originate a new attempt rather than acking one).
// A non-nil value that no longer matches the row's current retry_count means
// the lease sweeper has already requeued this job as a new attempt since the
// agent picked it up — most likely because the ack for THIS attempt was lost
// (e.g. the command disabled the network adapter carrying the ack) and the
// row was reset before the late ack arrived. Rejecting it here (0 rows
// affected) stops that stale ack from clobbering the newer attempt's state.
// Returns the number of rows the UPDATE actually matched, so callers can
// distinguish "applied" from "ignored as stale".
func (db *DB) UpdateDeployResult(jobID, agentID, status, output string, exitCode *int, durationMS *int64, retryCount *int, expectedAttempt *int) (int64, error) {
	var executedAt interface{}
	if !isPendingLikeStatus(status) {
		executedAt = fmtTime(nowWIB())
	}
	res, err := db.Exec(`
		UPDATE deploy_results SET
			status = ?, output = ?,
			exit_code = COALESCE(?, exit_code),
			duration_ms = COALESCE(?, duration_ms),
			retry_count = COALESCE(?, retry_count),
			lease_until = NULL,
			executed_at = COALESCE(?, executed_at)
		WHERE job_id = ? AND agent_id = ? AND status <> 'cancelled'
			AND (? IS NULL OR retry_count = ?)
	`, status, output, exitCode, durationMS, retryCount, executedAt, jobID, agentID, expectedAttempt, expectedAttempt)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateJobStatus marks a job "done" once none of its per-agent results are
// still pending-like (mirrors the same check today's handleExecResult used
// to do inline) — shared by both the completion path and the lease sweep.
func (db *DB) UpdateJobStatus(jobID string) error {
	results, err := db.GetDeployResultsByJobID(jobID)
	if err != nil {
		return err
	}
	for _, r := range results {
		if isPendingLikeStatus(r.Status) {
			return nil
		}
	}
	return db.UpdateDeployJobStatus(jobID, "done")
}

// AcquireNextJob atomically claims the highest-priority, oldest pending job
// for agentID and marks its result row 'running' with a lease deadline, all
// inside one transaction — the single pooled SQLite connection
// (SetMaxOpenConns(1) in initDB) means this transaction blocks every other
// goroutine's DB call for its duration, so two concurrent callers (agent
// reconnect, job completion, lease sweep) can never both claim a job for the
// same computer. Returns (nil, 0, nil) if the agent already has a running
// job, or has nothing pending.
//
// Also returns the claimed row's retry_count, which the caller threads
// through to the agent as an attempt fencing token (see UpdateDeployResult)
// so a later ack can be matched against the exact attempt it was dispatched
// for.
func (db *DB) AcquireNextJob(agentID string, leaseUntil time.Time) (*DeployJob, int, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	var inFlight int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM deploy_results WHERE agent_id = ? AND status = 'running'`, agentID,
	).Scan(&inFlight); err != nil {
		return nil, 0, err
	}
	if inFlight > 0 {
		return nil, 0, nil
	}

	var jobID string
	var retryCount int
	err = tx.QueryRow(`
		SELECT j.id, r.retry_count FROM deploy_jobs j JOIN deploy_results r ON j.id = r.job_id
		WHERE r.agent_id = ? AND r.status = 'pending'
		ORDER BY j.priority DESC, j.created_at ASC LIMIT 1
	`, agentID).Scan(&jobID, &retryCount)
	if err == sql.ErrNoRows {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}

	if _, err := tx.Exec(
		`UPDATE deploy_results SET status = 'running', lease_until = ? WHERE job_id = ? AND agent_id = ?`,
		fmtTime(leaseUntil), jobID, agentID,
	); err != nil {
		return nil, 0, err
	}

	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	job, err := db.GetDeployJobByID(jobID)
	return job, retryCount, err
}

// GetExpiredLeaseResults returns every 'running' result whose lease has
// passed — candidates for the lease sweep to requeue or fail.
func (db *DB) GetExpiredLeaseResults(now time.Time) ([]DeployResult, error) {
	rows, err := db.Query(`
		SELECT id, job_id, agent_id, status, output, executed_at,
			lease_until, retry_count, max_retry, exit_code, duration_ms
		FROM deploy_results
		WHERE status = 'running' AND lease_until IS NOT NULL AND lease_until < ?
	`, fmtTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DeployResult
	for rows.Next() {
		if r, err := scanDeployResult(rows); err != nil {
			return nil, err
		} else {
			result = append(result, r)
		}
	}
	return result, rows.Err()
}

// GetExpiredPendingResults returns every still-'pending' result whose job's
// expire_at has passed without ever being dispatched.
func (db *DB) GetExpiredPendingResults(now time.Time) ([]DeployResult, error) {
	rows, err := db.Query(`
		SELECT r.id, r.job_id, r.agent_id, r.status, r.output, r.executed_at,
			r.lease_until, r.retry_count, r.max_retry, r.exit_code, r.duration_ms
		FROM deploy_results r
		JOIN deploy_jobs j ON j.id = r.job_id
		WHERE r.status = 'pending' AND j.expire_at IS NOT NULL AND j.expire_at < ?
	`, fmtTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DeployResult
	for rows.Next() {
		if r, err := scanDeployResult(rows); err != nil {
			return nil, err
		} else {
			result = append(result, r)
		}
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
		"auto_kill_enabled":     "false",
		"telegram_token":        cfg.Telegram.Token,
		"telegram_chat_id":      cfg.Telegram.ChatID,
		"smtp_host":             cfg.Email.SMTPHost,
		"smtp_port":             fmt.Sprintf("%d", cfg.Email.SMTPPort),
		"smtp_tls":              "starttls",
		"smtp_user":             cfg.Email.SMTPUser,
		"smtp_pass":             cfg.Email.SMTPPass,
		"smtp_to":               cfg.Email.SMTPTo,
		"mesh_url":              cfg.MeshCentral.URL,
		"lease_minutes":         fmt.Sprintf("%d", cfg.Deploy.LeaseMinutes),
		"default_max_retry":     fmt.Sprintf("%d", cfg.Deploy.DefaultMaxRetry),
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

// ─── Audit Log Queries ─────────────────────────────────────────────────────

func (db *DB) InsertAuditLog(action, target, detail, ip string) {
	_, err := db.Exec(
		`INSERT INTO audit_logs (ts, action, target, detail, ip) VALUES (?, ?, ?, ?, ?)`,
		fmtTime(nowWIB()), action, target, detail, ip,
	)
	if err != nil {
		slog.Error("audit log insert failed", "action", action, "error", err)
	}
}

func (db *DB) GetAuditLogs(limit int) ([]AuditLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := db.Query(
		`SELECT id, ts, action, target, detail, ip FROM audit_logs ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AuditLog
	for rows.Next() {
		var a AuditLog
		if err := rows.Scan(&a.ID, &a.Ts, &a.Action, &a.Target, &a.Detail, &a.IP); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// ─── Category Queries ──────────────────────────────────────────────────────

var defaultCategories = []string{
	"Browser", "Office", "Academic", "Programming", "Graphic Design",
	"Multimedia", "Games", "Remote Access", "Utilities", "System",
}

// InitDefaultCategories seeds the category list on first run. Idempotent —
// safe to call on every startup, same pattern as InitDefaultSettings.
func (db *DB) InitDefaultCategories() error {
	for _, name := range defaultCategories {
		if _, err := db.Exec(`INSERT OR IGNORE INTO categories (name) VALUES (?)`, name); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) GetAllCategories() ([]Category, error) {
	rows, err := db.Query(`SELECT id, name FROM categories ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Category, 0)
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// ─── Application Queries ───────────────────────────────────────────────────

// UpsertApplication finds or creates the catalog row identified by
// (exeName, company). Metadata fields are only written when non-empty, so a
// later sighting that arrives without metadata (the common case — the agent
// only extracts it once per path per session) never blanks out previously
// recorded data. Returns the application ID.
func (db *DB) UpsertApplication(exeName, company string, meta *AppMetadata) (int64, error) {
	now := fmtTime(nowWIB())

	var productName, description, productVersion string
	if meta != nil {
		productName, description, productVersion = meta.ProductName, meta.Description, meta.ProductVersion
	}

	// The conflict target must restate idx_applications_identity's partial
	// predicate (WHERE exe_name <> '') verbatim — SQLite requires an exact
	// match to resolve ON CONFLICT against a partial unique index, otherwise
	// every insert here errors with "does not match any PRIMARY KEY or
	// UNIQUE constraint" (caught via live testing against a real agent,
	// which sends real exe names on every call to this function).
	_, err := db.Exec(`
		INSERT INTO applications (exe_name, company, product_name, description, product_version, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(exe_name, company) WHERE exe_name <> '' DO UPDATE SET
			product_name    = CASE WHEN excluded.product_name    <> '' THEN excluded.product_name    ELSE applications.product_name    END,
			description     = CASE WHEN excluded.description     <> '' THEN excluded.description     ELSE applications.description     END,
			product_version = CASE WHEN excluded.product_version <> '' THEN excluded.product_version ELSE applications.product_version END,
			updated_at      = excluded.updated_at
	`, exeName, company, productName, description, productVersion, AppStatusPendingReview, now, now)
	if err != nil {
		return 0, err
	}

	var id int64
	err = db.QueryRow(`SELECT id FROM applications WHERE exe_name = ? AND company = ?`, exeName, company).Scan(&id)
	return id, err
}

// UpsertApplicationByProduct is the Install Detection (Module 5) counterpart
// to UpsertApplication: the Windows Uninstall registry only exposes
// DisplayName/Publisher, never the primary executable name, so it can't use
// the (exe_name, company) identity key. It looks up an existing catalog row
// by (product_name, company) case-insensitively first — merging into an
// install-detected row from an earlier install of the same product — and
// only creates a new row (with exe_name left blank) if none exists. This is
// a best-effort merge: a process-detected row for the same app (keyed by its
// real exe_name) is not automatically unified with an install-detected row
// for it — cross-source identity matching by product name alone is out of
// scope for Phase 2 (documented limitation, not a bug).
func (db *DB) UpsertApplicationByProduct(productName, company string, meta *AppMetadata) (int64, error) {
	now := fmtTime(nowWIB())
	var description, productVersion string
	if meta != nil {
		description, productVersion = meta.Description, meta.ProductVersion
	}

	var id int64
	err := db.QueryRow(
		`SELECT id FROM applications WHERE product_name = ? COLLATE NOCASE AND company = ? COLLATE NOCASE`,
		productName, company,
	).Scan(&id)
	if err == nil {
		_, err = db.Exec(`
			UPDATE applications SET
				description     = CASE WHEN ? <> '' THEN ? ELSE description     END,
				product_version = CASE WHEN ? <> '' THEN ? ELSE product_version END,
				updated_at      = ?
			WHERE id = ?
		`, description, description, productVersion, productVersion, now, id)
		return id, err
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	res, err := db.Exec(`
		INSERT INTO applications (exe_name, company, product_name, description, product_version, status, created_at, updated_at)
		VALUES ('', ?, ?, ?, ?, ?, ?, ?)
	`, company, productName, description, productVersion, AppStatusPendingReview, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetApplications lists the catalog, optionally filtered by status and/or category.
func (db *DB) GetApplications(status string, categoryID int64) ([]ApplicationWithStats, error) {
	query := `
		SELECT
			a.id, a.exe_name, a.company, a.product_name, a.description, a.product_version,
			a.category_id, a.status, a.created_at, a.updated_at,
			COALESCE(c.name, ''),
			COALESCE((SELECT COUNT(DISTINCT agent_id) FROM app_sightings WHERE application_id = a.id), 0),
			COALESCE((SELECT SUM(exec_count) FROM app_sightings WHERE application_id = a.id), 0),
			COALESCE((SELECT MIN(first_seen) FROM app_sightings WHERE application_id = a.id), ''),
			COALESCE((SELECT MAX(last_seen) FROM app_sightings WHERE application_id = a.id), '')
		FROM applications a
		LEFT JOIN categories c ON c.id = a.category_id
		WHERE 1=1`
	args := []interface{}{}
	if status != "" {
		query += ` AND a.status = ?`
		args = append(args, status)
	}
	if categoryID != 0 {
		query += ` AND a.category_id = ?`
		args = append(args, categoryID)
	}
	query += ` ORDER BY a.updated_at DESC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]ApplicationWithStats, 0)
	for rows.Next() {
		var app ApplicationWithStats
		var categoryID sql.NullInt64
		var createdAt, updatedAt, firstSeen, lastSeen string
		if err := rows.Scan(
			&app.ID, &app.ExeName, &app.Company, &app.ProductName, &app.Description, &app.ProductVersion,
			&categoryID, &app.Status, &createdAt, &updatedAt,
			&app.CategoryName, &app.DeviceCount, &app.TotalExecutions, &firstSeen, &lastSeen,
		); err != nil {
			return nil, err
		}
		if categoryID.Valid {
			app.CategoryID = &categoryID.Int64
		}
		app.CreatedAt = parseDBTime(createdAt)
		app.UpdatedAt = parseDBTime(updatedAt)
		app.FirstSeen = parseDBTime(firstSeen)
		app.LastSeen = parseDBTime(lastSeen)
		result = append(result, app)
	}
	return result, rows.Err()
}

func (db *DB) GetApplicationByID(id int64) (*ApplicationDetail, error) {
	apps, err := db.GetApplications("", 0)
	if err != nil {
		return nil, err
	}
	var found *ApplicationWithStats
	for i := range apps {
		if apps[i].ID == id {
			found = &apps[i]
			break
		}
	}
	if found == nil {
		return nil, nil
	}

	rows, err := db.Query(`
		SELECT s.agent_id, COALESCE(ag.hostname, ''), s.application_id, s.path, s.size,
		       s.file_created_at, s.file_modified_at, s.first_seen, s.last_seen, s.exec_count
		FROM app_sightings s
		LEFT JOIN agents ag ON ag.id = s.agent_id
		WHERE s.application_id = ?
		ORDER BY s.last_seen DESC
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sightings := make([]AppSighting, 0)
	for rows.Next() {
		var s AppSighting
		var firstSeen, lastSeen string
		if err := rows.Scan(
			&s.AgentID, &s.Hostname, &s.ApplicationID, &s.Path, &s.Size,
			&s.FileCreatedAt, &s.FileModifiedAt, &firstSeen, &lastSeen, &s.ExecCount,
		); err != nil {
			return nil, err
		}
		s.FirstSeen = parseDBTime(firstSeen)
		s.LastSeen = parseDBTime(lastSeen)
		sightings = append(sightings, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &ApplicationDetail{ApplicationWithStats: *found, Sightings: sightings}, nil
}

func (db *DB) UpdateApplicationStatus(id int64, status string, categoryID *int64) error {
	_, err := db.Exec(
		`UPDATE applications SET status = ?, category_id = ?, updated_at = ? WHERE id = ?`,
		status, categoryID, fmtTime(nowWIB()), id,
	)
	return err
}

// ─── App Sighting Queries ──────────────────────────────────────────────────

// GetSightingApplicationID looks up which application a (agentID, path) pair
// was already resolved to by an earlier sighting. Used by catalog.go to
// avoid re-deriving identity from possibly-empty metadata on every cycle —
// once a path has been linked to an application, later sightings of the
// same path (even ones the agent didn't attach fresh metadata to — e.g. a
// second chrome.exe process sharing chrome.exe's already-cached path) reuse
// that link instead of risking a second, company-less catalog row.
func (db *DB) GetSightingApplicationID(agentID, path string) (int64, bool, error) {
	var appID int64
	err := db.QueryRow(
		`SELECT application_id FROM app_sightings WHERE agent_id = ? AND path = ?`,
		agentID, path,
	).Scan(&appID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return appID, true, nil
}

// UpsertSighting records that applicationID was seen running at path on
// agentID. size/createdAt/modifiedAt are only written on insert or when the
// caller has fresh metadata (size > 0) — repeat sightings without metadata
// just bump last_seen and exec_count.
func (db *DB) UpsertSighting(agentID string, applicationID int64, path string, size int64, fileCreatedAt, fileModifiedAt time.Time) error {
	now := fmtTime(nowWIB())
	var createdStr, modifiedStr string
	if !fileCreatedAt.IsZero() {
		createdStr = fmtTime(fileCreatedAt)
	}
	if !fileModifiedAt.IsZero() {
		modifiedStr = fmtTime(fileModifiedAt)
	}

	_, err := db.Exec(`
		INSERT INTO app_sightings (agent_id, application_id, path, size, file_created_at, file_modified_at, first_seen, last_seen, exec_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(agent_id, application_id, path) DO UPDATE SET
			last_seen         = excluded.last_seen,
			exec_count        = app_sightings.exec_count + 1,
			size              = CASE WHEN excluded.size > 0 THEN excluded.size ELSE app_sightings.size END,
			file_created_at   = CASE WHEN excluded.file_created_at  <> '' THEN excluded.file_created_at  ELSE app_sightings.file_created_at  END,
			file_modified_at  = CASE WHEN excluded.file_modified_at <> '' THEN excluded.file_modified_at ELSE app_sightings.file_modified_at END
	`, agentID, applicationID, path, size, createdStr, modifiedStr, now, now)
	return err
}

// ─── Event Queries ─────────────────────────────────────────────────────────

func (db *DB) InsertEvent(agentID, eventType, metadataJSON, action string) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO events (agent_id, type, metadata, action, created_at) VALUES (?, ?, ?, ?, ?)`,
		agentID, eventType, metadataJSON, action, fmtTime(nowWIB()),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetEvents lists events across all agents, optionally filtered by agentID and/or type.
func (db *DB) GetEvents(agentID, eventType string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `
		SELECT e.id, e.agent_id, COALESCE(ag.hostname, ''), e.type, e.metadata, e.action, e.created_at
		FROM events e
		LEFT JOIN agents ag ON ag.id = e.agent_id
		WHERE 1=1`
	args := []interface{}{}
	if agentID != "" {
		query += ` AND e.agent_id = ?`
		args = append(args, agentID)
	}
	if eventType != "" {
		query += ` AND e.type = ?`
		args = append(args, eventType)
	}
	query += ` ORDER BY e.created_at DESC, e.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]Event, 0)
	for rows.Next() {
		var e Event
		var createdAt string
		if err := rows.Scan(&e.ID, &e.AgentID, &e.Hostname, &e.Type, &e.Metadata, &e.Action, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt = parseDBTime(createdAt)
		result = append(result, e)
	}
	return result, rows.Err()
}

// ─── Policy Rule Queries ────────────────────────────────────────────────────

func (db *DB) GetAllPolicyRules() ([]PolicyRule, error) {
	rows, err := db.Query(`
		SELECT id, name, event_type, category_id, file_extension, execution_location, device_group, action, enabled, created_at
		FROM policy_rules ORDER BY id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]PolicyRule, 0)
	for rows.Next() {
		var r PolicyRule
		var categoryID sql.NullInt64
		var enabled int
		var createdAt string
		if err := rows.Scan(&r.ID, &r.Name, &r.EventType, &categoryID, &r.FileExtension,
			&r.ExecutionLocation, &r.DeviceGroup, &r.Action, &enabled, &createdAt); err != nil {
			return nil, err
		}
		if categoryID.Valid {
			r.CategoryID = &categoryID.Int64
		}
		r.Enabled = enabled != 0
		r.CreatedAt = parseDBTime(createdAt)
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetEnabledPolicyRules returns only enabled rules — what the Policy Engine
// evaluates against. Kept separate from GetAllPolicyRules (which the
// dashboard's rule-management panel uses) so the hot path never scans
// disabled rows.
func (db *DB) GetEnabledPolicyRules() ([]PolicyRule, error) {
	all, err := db.GetAllPolicyRules()
	if err != nil {
		return nil, err
	}
	enabled := make([]PolicyRule, 0, len(all))
	for _, r := range all {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	return enabled, nil
}

func (db *DB) InsertPolicyRule(r *PolicyRule) (int64, error) {
	res, err := db.Exec(`
		INSERT INTO policy_rules (name, event_type, category_id, file_extension, execution_location, device_group, action, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.Name, r.EventType, r.CategoryID, r.FileExtension, r.ExecutionLocation, r.DeviceGroup, r.Action, boolToInt(r.Enabled), fmtTime(nowWIB()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) UpdatePolicyRule(id int64, r *PolicyRule) error {
	_, err := db.Exec(`
		UPDATE policy_rules SET
			name = ?, event_type = ?, category_id = ?, file_extension = ?,
			execution_location = ?, device_group = ?, action = ?, enabled = ?
		WHERE id = ?
	`, r.Name, r.EventType, r.CategoryID, r.FileExtension, r.ExecutionLocation, r.DeviceGroup, r.Action, boolToInt(r.Enabled), id)
	return err
}

func (db *DB) DeletePolicyRule(id int64) error {
	_, err := db.Exec(`DELETE FROM policy_rules WHERE id = ?`, id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
