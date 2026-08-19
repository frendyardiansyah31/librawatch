package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"library-monitor/shared"
)

// ─── Data types ─────────────────────────────────────────────────────────────

// SoftwareInventoryItem is one entry within an agent's
// software_inventory_snapshot message — see agent/softwareinventory.go's
// buildAndSendSnapshot for the sender side.
type SoftwareInventoryItem struct {
	DisplayName            string `json:"display_name"`
	Publisher              string `json:"publisher"`
	DisplayVersion         string `json:"display_version"`
	InstallDate            string `json:"install_date"`
	WindowsInstaller       bool   `json:"windows_installer"`
	ProductCode            string `json:"product_code"`
	UninstallString        string `json:"uninstall_string"`
	QuietUninstallString   string `json:"quiet_uninstall_string"`
	Hive                   string `json:"hive"`
	Arch                   string `json:"arch"`
	Scope                  string `json:"scope"`
	WingetID               string `json:"winget_id"`
	WingetAvailableVersion string `json:"winget_available_version"`
}

func (item SoftwareInventoryItem) identityKey() string {
	return shared.SoftwareIdentity{
		WindowsInstaller: item.WindowsInstaller,
		ProductCode:      item.ProductCode,
		DisplayName:      item.DisplayName,
		Publisher:        item.Publisher,
	}.Key()
}

// SoftwareInventoryRow is one software_inventory table row, joined with the
// owning agent's hostname, plus the uninstall-tier classification and
// update status computed at read time (not stored) — see classifyUninstall
// and computeUpdateStatus below, the single source of truth for both what
// the dashboard displays and what an uninstall dispatch actually runs.
type SoftwareInventoryRow struct {
	AgentID                string `json:"agent_id"`
	Hostname               string `json:"hostname"`
	IdentityKey            string `json:"identity_key"`
	DisplayName            string `json:"display_name"`
	Publisher              string `json:"publisher"`
	DisplayVersion         string `json:"display_version"`
	InstallDate            string `json:"install_date"`
	WindowsInstaller       bool   `json:"windows_installer"`
	ProductCode            string `json:"product_code"`
	UninstallString        string `json:"uninstall_string"`
	QuietUninstallString   string `json:"quiet_uninstall_string"`
	Hive                   string `json:"hive"`
	Arch                   string `json:"arch"`
	Scope                  string `json:"scope"`
	WingetID               string `json:"winget_id"`
	WingetAvailableVersion string `json:"winget_available_version"`
	FirstSeen              string `json:"first_seen"`
	LastSeen               string `json:"last_seen"`

	UpdateStatus       string `json:"update_status"`
	UninstallSupported bool   `json:"uninstall_supported"`
	UninstallTier      string `json:"uninstall_tier"`
	UninstallReason    string `json:"uninstall_reason,omitempty"`
}

// SoftwareAggregateRow is one fleet-wide GET /api/v1/software row — the same
// logical software (by identity key) collapsed across every endpoint that
// reports it.
type SoftwareAggregateRow struct {
	IdentityKey           string `json:"identity_key"`
	Name                  string `json:"name"`
	Vendor                string `json:"vendor"`
	Version               string `json:"version"` // an exact version, or "varies"
	VersionVaries         bool   `json:"version_varies"`
	NewestUpdate          string `json:"newest_update"`
	InstallType           string `json:"install_type"`  // "msi" | "exe" | "mixed"
	UpdateStatus          string `json:"update_status"` // "up_to_date" | "update_available" | "unknown"
	Platform              string `json:"platform"`      // "x64" | "x86" | "mixed"
	EndpointCount         int    `json:"endpoint_count"`
	AnyUninstallSupported bool   `json:"any_uninstall_supported"`
}

// ─── Reconciliation ─────────────────────────────────────────────────────────

// ReconcileSoftwareInventory folds one agent's full software_inventory_snapshot
// into the software_inventory table: every reported item is upserted, and
// anything previously stored for this agent_id but absent from this snapshot
// is hard-deleted — full reconciliation, scoped strictly to this one agent,
// every time. first_seen is set only on insert (it's left out of the ON
// CONFLICT SET list, so it survives untouched across updates).
func (db *DB) ReconcileSoftwareInventory(agentID string, items []SoftwareInventoryItem) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	now := fmtTime(nowWIB())
	keys := make([]string, 0, len(items))

	for _, item := range items {
		key := item.identityKey()
		keys = append(keys, key)
		_, err := tx.Exec(`
			INSERT INTO software_inventory (
				agent_id, identity_key, display_name, publisher, display_version,
				install_date, windows_installer, product_code, uninstall_string,
				quiet_uninstall_string, hive, arch, scope, winget_id,
				winget_available_version, first_seen, last_seen
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(agent_id, identity_key) DO UPDATE SET
				display_name             = excluded.display_name,
				publisher                = excluded.publisher,
				display_version          = excluded.display_version,
				install_date             = excluded.install_date,
				windows_installer        = excluded.windows_installer,
				product_code             = excluded.product_code,
				uninstall_string         = excluded.uninstall_string,
				quiet_uninstall_string   = excluded.quiet_uninstall_string,
				hive                     = excluded.hive,
				arch                     = excluded.arch,
				scope                    = excluded.scope,
				winget_id                = excluded.winget_id,
				winget_available_version = excluded.winget_available_version,
				last_seen                = excluded.last_seen
		`, agentID, key, item.DisplayName, item.Publisher, item.DisplayVersion,
			item.InstallDate, item.WindowsInstaller, item.ProductCode, item.UninstallString,
			item.QuietUninstallString, item.Hive, item.Arch, item.Scope, item.WingetID,
			item.WingetAvailableVersion, now, now)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("upsert software_inventory: %w", err)
		}
	}

	delQuery := `DELETE FROM software_inventory WHERE agent_id = ?`
	args := []interface{}{agentID}
	if len(keys) > 0 {
		placeholders := make([]string, len(keys))
		for i, k := range keys {
			placeholders[i] = "?"
			args = append(args, k)
		}
		delQuery += " AND identity_key NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	if _, err := tx.Exec(delQuery, args...); err != nil {
		tx.Rollback()
		return fmt.Errorf("reconcile delete software_inventory: %w", err)
	}

	return tx.Commit()
}

// ─── Queries ─────────────────────────────────────────────────────────────

type softwareRowFilter struct {
	search      string
	identityKey string
	agentID     string
}

// querySoftwareInventoryRows is the single query path every software
// endpoint builds on — search-filtered, joined to agents for hostname, with
// per-row update status and uninstall classification computed uniformly so
// the aggregated list, the per-software endpoint drill-down, and the
// uninstall-dispatch handler can never disagree about what's supported.
func (db *DB) querySoftwareInventoryRows(f softwareRowFilter) ([]SoftwareInventoryRow, error) {
	query := `
		SELECT si.agent_id, a.hostname, si.identity_key, si.display_name, si.publisher,
		       si.display_version, si.install_date, si.windows_installer, si.product_code,
		       si.uninstall_string, si.quiet_uninstall_string, si.hive, si.arch, si.scope,
		       si.winget_id, si.winget_available_version, si.first_seen, si.last_seen
		FROM software_inventory si
		JOIN agents a ON a.id = si.agent_id
		WHERE 1=1
	`
	args := []interface{}{}
	if f.search != "" {
		query += ` AND (si.display_name LIKE ? OR si.publisher LIKE ?)`
		like := "%" + f.search + "%"
		args = append(args, like, like)
	}
	if f.identityKey != "" {
		query += ` AND si.identity_key = ?`
		args = append(args, f.identityKey)
	}
	if f.agentID != "" {
		query += ` AND si.agent_id = ?`
		args = append(args, f.agentID)
	}
	query += ` ORDER BY si.identity_key, a.hostname`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SoftwareInventoryRow, 0)
	for rows.Next() {
		var r SoftwareInventoryRow
		if err := rows.Scan(&r.AgentID, &r.Hostname, &r.IdentityKey, &r.DisplayName, &r.Publisher,
			&r.DisplayVersion, &r.InstallDate, &r.WindowsInstaller, &r.ProductCode,
			&r.UninstallString, &r.QuietUninstallString, &r.Hive, &r.Arch, &r.Scope,
			&r.WingetID, &r.WingetAvailableVersion, &r.FirstSeen, &r.LastSeen); err != nil {
			return nil, err
		}
		r.UpdateStatus = computeUpdateStatus(&r)
		tier, _, _, _, ok, reason := classifyUninstall(&r)
		r.UninstallTier = tier
		r.UninstallSupported = ok
		r.UninstallReason = reason
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSoftwareInventoryAggregated is the GET /api/v1/software query — every
// software_inventory row, grouped by identity key into one fleet-wide row
// per logical software.
func (db *DB) GetSoftwareInventoryAggregated(search, installType, updateStatus, platform string) ([]SoftwareAggregateRow, error) {
	rows, err := db.querySoftwareInventoryRows(softwareRowFilter{search: search})
	if err != nil {
		return nil, err
	}
	return aggregateSoftwareRows(rows, installType, updateStatus, platform), nil
}

// GetSoftwareInventoryByIdentityKey is the per-software endpoint
// drill-down: every endpoint currently reporting this identity key.
func (db *DB) GetSoftwareInventoryByIdentityKey(identityKey string) ([]SoftwareInventoryRow, error) {
	return db.querySoftwareInventoryRows(softwareRowFilter{identityKey: identityKey})
}

// GetSoftwareInventoryRowForUninstall is the exact single-row lookup the
// uninstall dispatch handler uses to source trusted, agent-reported
// metadata — never anything supplied by the frontend. nil, nil means no
// such row (agent+identity_key combination not currently installed).
func (db *DB) GetSoftwareInventoryRowForUninstall(agentID, identityKey string) (*SoftwareInventoryRow, error) {
	rows, err := db.querySoftwareInventoryRows(softwareRowFilter{agentID: agentID, identityKey: identityKey})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// ─── Aggregation ─────────────────────────────────────────────────────────

func aggregateSoftwareRows(rows []SoftwareInventoryRow, installTypeFilter, updateStatusFilter, platformFilter string) []SoftwareAggregateRow {
	groups := make(map[string][]SoftwareInventoryRow)
	order := make([]string, 0)
	for _, r := range rows {
		if _, exists := groups[r.IdentityKey]; !exists {
			order = append(order, r.IdentityKey)
		}
		groups[r.IdentityKey] = append(groups[r.IdentityKey], r)
	}

	out := make([]SoftwareAggregateRow, 0, len(order))
	for _, key := range order {
		agg := aggregateOneSoftwareGroup(key, groups[key])
		if installTypeFilter != "" && agg.InstallType != installTypeFilter {
			continue
		}
		if updateStatusFilter != "" && agg.UpdateStatus != updateStatusFilter {
			continue
		}
		if platformFilter != "" && agg.Platform != platformFilter {
			continue
		}
		out = append(out, agg)
	}
	return out
}

// aggregateOneSoftwareGroup collapses every endpoint's row for one identity
// key into a single fleet-wide summary row. Disagreement across endpoints
// (different installed version, install type, or platform) is surfaced as
// "varies"/"mixed" rather than silently picking one arbitrarily.
// UpdateStatus intentionally favors visibility over strict agreement: if
// any endpoint needs an update, the aggregate row reports
// "update_available" so an admin scanning the list doesn't have to drill
// into every row to notice one machine is behind.
func aggregateOneSoftwareGroup(key string, rows []SoftwareInventoryRow) SoftwareAggregateRow {
	agg := SoftwareAggregateRow{IdentityKey: key, EndpointCount: len(rows)}
	if len(rows) == 0 {
		return agg
	}
	agg.Name = rows[0].DisplayName
	agg.Vendor = rows[0].Publisher

	versions := map[string]bool{}
	installTypes := map[string]bool{}
	platforms := map[string]bool{}
	statuses := map[string]bool{}
	newest := ""

	for _, r := range rows {
		versions[r.DisplayVersion] = true
		if r.WindowsInstaller {
			installTypes["msi"] = true
		} else {
			installTypes["exe"] = true
		}
		if r.Arch != "" {
			platforms[r.Arch] = true
		}
		statuses[r.UpdateStatus] = true
		if r.WingetAvailableVersion != "" {
			if newest == "" {
				newest = r.WingetAvailableVersion
			} else if cmp, ok := shared.CompareVersions(newest, r.WingetAvailableVersion); ok && cmp < 0 {
				newest = r.WingetAvailableVersion
			}
		}
		if r.UninstallSupported {
			agg.AnyUninstallSupported = true
		}
	}

	if len(versions) == 1 {
		for v := range versions {
			agg.Version = v
		}
	} else {
		agg.Version = "varies"
		agg.VersionVaries = true
	}

	agg.InstallType = collapseStringSet(installTypes)
	agg.Platform = collapseStringSet(platforms)
	agg.NewestUpdate = newest

	switch {
	case statuses["update_available"]:
		agg.UpdateStatus = "update_available"
	case len(statuses) == 1 && statuses["up_to_date"]:
		agg.UpdateStatus = "up_to_date"
	default:
		agg.UpdateStatus = "unknown"
	}

	return agg
}

func collapseStringSet(set map[string]bool) string {
	if len(set) == 0 {
		return ""
	}
	if len(set) == 1 {
		for v := range set {
			return v
		}
	}
	return "mixed"
}

// ─── Update status ─────────────────────────────────────────────────────────

// computeUpdateStatus classifies one row's update status from winget
// enrichment data alone — never from raw string inequality (see
// shared.CompareVersions). A total absence of enrichment (no winget_id, or
// winget never having matched this entry) maps to "unknown", never an error
// and never a reason to hide the row.
func computeUpdateStatus(row *SoftwareInventoryRow) string {
	if row.WingetID == "" || row.WingetAvailableVersion == "" {
		return "unknown"
	}
	cmp, ok := shared.CompareVersions(row.DisplayVersion, row.WingetAvailableVersion)
	if !ok {
		return "unknown"
	}
	if cmp < 0 {
		return "update_available"
	}
	// cmp == 0 (equal), or cmp > 0 (installed ahead of what winget
	// reports, e.g. a pre-release/beta channel install) — either way
	// there's nothing to update *to*.
	return "up_to_date"
}

// ─── Uninstall tier classification ─────────────────────────────────────────

// classifyUninstall is the single source of truth for both what the
// dashboard displays as "supported" and what an uninstall dispatch actually
// runs — see server/api.go's POST /api/v1/software/uninstall handler.
// payload/args are built entirely from row (server/agent-reported,
// DB-stored state) — never from anything the frontend supplies.
//
// Tier A (MSI): row.ProductCode normalizes to a valid GUID and
// row.WindowsInstaller is set -> jobType "msiexec_uninstall", payload is
// the GUID alone; the agent builds the full msiexec invocation itself as a
// structured argv (see agent/softwareinventory.go's executeMsiexecUninstall).
//
// Tier B (quiet uninstall): row.QuietUninstallString passes
// shared.ParseQuietUninstallCommand -> jobType "quiet_uninstall", payload is
// the exact QuietUninstallString as last reported, args is the identity key
// (used by the agent to re-look-up its own last-known entry for a
// self-consistency check before executing). A QuietUninstallString that
// fails structural validation is never offered as Tier B — it falls
// through to Tier C rather than ever being classified "supported" and then
// failing at execution time.
//
// Tier C (winget fallback): row.WingetID matches wingetIDRe -> jobType
// "winget", payload reuses the existing validateDeployRequest("winget", ...)
// shape verbatim.
//
// Tier D (unsupported): none of the above -> ok=false with a human-readable
// reason for the UI.
func classifyUninstall(row *SoftwareInventoryRow) (tier, jobType, payload, args string, ok bool, reason string) {
	if row.WindowsInstaller {
		if guid, valid := shared.NormalizeProductCode(row.ProductCode); valid {
			return "A", "msiexec_uninstall", guid, "", true, ""
		}
	}
	if row.QuietUninstallString != "" {
		if _, _, parseOK := shared.ParseQuietUninstallCommand(row.QuietUninstallString); parseOK {
			return "B", "quiet_uninstall", row.QuietUninstallString, row.IdentityKey, true, ""
		}
	}
	if row.WingetID != "" && wingetIDRe.MatchString(row.WingetID) {
		wingetPayload := fmt.Sprintf("winget uninstall --id %s --silent --accept-source-agreements", row.WingetID)
		return "C", "winget", wingetPayload, "", true, ""
	}
	return "D", "", "", "", false,
		"No MSI ProductCode, no safely-parseable QuietUninstallString, and no recognized winget package ID — this software has no known silent uninstall mechanism"
}

// ─── Routes ─────────────────────────────────────────────────────────────

// RegisterSoftwareRoutes wires the Software Inventory feature's endpoints
// onto apiV1 — GET /software (aggregated fleet view), GET
// /software/:identity_key/endpoints (per-software drill-down), POST
// /software/uninstall (dispatch). Mirrors RegisterCommandRoutes's
// registration style (commands.go).
func RegisterSoftwareRoutes(apiV1 *gin.RouterGroup, db *DB, deployer *Deployer) {
	apiV1.GET("/software", handleGetSoftwareInventory(db))
	apiV1.GET("/software/:identity_key/endpoints", handleGetSoftwareEndpoints(db))
	apiV1.POST("/software/uninstall", handlePostSoftwareUninstall(db, deployer))
}

// handleGetSoftwareInventory serves
// GET /api/v1/software?search=&install_type=&update_status=&platform= — a
// plain JSON array, matching handleGetComputers's no-envelope convention
// (the closest existing /api/v1 sibling).
func handleGetSoftwareInventory(db *DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := db.GetSoftwareInventoryAggregated(
			c.Query("search"), c.Query("install_type"), c.Query("update_status"), c.Query("platform"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rows)
	}
}

// handleGetSoftwareEndpoints serves
// GET /api/v1/software/:identity_key/endpoints — the per-software
// drill-down. Empty array (not 404) when nothing matches, consistent with
// GetComputers never 404ing on filters.
func handleGetSoftwareEndpoints(db *DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := db.GetSoftwareInventoryByIdentityKey(c.Param("identity_key"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rows)
	}
}

type softwareUninstallRequest struct {
	IdentityKey string   `json:"identity_key"`
	AgentIDs    []string `json:"agent_ids"`
}

type softwareUninstallJobResult struct {
	AgentID string `json:"agent_id"`
	JobID   string `json:"job_id"`
	Tier    string `json:"tier"`
}

type softwareUninstallSkip struct {
	AgentID string `json:"agent_id"`
	Reason  string `json:"reason"`
}

// handlePostSoftwareUninstall serves POST /api/v1/software/uninstall. The
// frontend supplies only a software identity and target endpoints — never a
// command. For each target agent, the actual uninstall mechanism is
// resolved server-side from that agent's own stored inventory row via
// classifyUninstall, then dispatched through the existing deploy job queue
// (Deployer.CreateJob) — one job per (agent, tier), since different
// endpoints can have different install metadata for "the same" software, so
// the normal one-payload-for-every-target deploy shape doesn't fit here.
func handlePostSoftwareUninstall(db *DB, deployer *Deployer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req softwareUninstallRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.IdentityKey == "" || len(req.AgentIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "identity_key and agent_ids are required"})
			return
		}

		jobs := make([]softwareUninstallJobResult, 0, len(req.AgentIDs))
		skipped := make([]softwareUninstallSkip, 0)

		for _, agentID := range req.AgentIDs {
			row, err := db.GetSoftwareInventoryRowForUninstall(agentID, req.IdentityKey)
			if err != nil {
				slog.Error("software uninstall: lookup failed", "agent_id", agentID, "identity_key", req.IdentityKey, "error", err)
				skipped = append(skipped, softwareUninstallSkip{AgentID: agentID, Reason: "lookup failed"})
				continue
			}
			if row == nil {
				skipped = append(skipped, softwareUninstallSkip{AgentID: agentID, Reason: "software not currently installed on this endpoint"})
				continue
			}
			tier, jobType, payload, args, ok, reason := classifyUninstall(row)
			if !ok {
				skipped = append(skipped, softwareUninstallSkip{AgentID: agentID, Reason: reason})
				continue
			}
			// Defense in depth: re-validate the server-built payload through
			// the same gate every other deploy job type goes through, even
			// though classifyUninstall already checked it.
			if err := validateDeployRequest(jobType, payload, args); err != nil {
				slog.Error("software uninstall: server-built payload failed validation", "agent_id", agentID, "job_type", jobType, "error", err)
				skipped = append(skipped, softwareUninstallSkip{AgentID: agentID, Reason: "internal validation error"})
				continue
			}
			job, err := deployer.CreateJob(jobType, payload, args, []string{agentID}, 0, nil, deployer.DefaultMaxRetry(), "admin")
			if err != nil {
				slog.Error("software uninstall: create job failed", "agent_id", agentID, "error", err)
				skipped = append(skipped, softwareUninstallSkip{AgentID: agentID, Reason: "job dispatch failed"})
				continue
			}
			jobs = append(jobs, softwareUninstallJobResult{AgentID: agentID, JobID: job.ID, Tier: tier})
		}

		if len(jobs) > 0 {
			jobIDs := make([]string, len(jobs))
			for i, j := range jobs {
				jobIDs[i] = j.JobID
			}
			db.InsertAuditLog("uninstall_software", strings.Join(req.AgentIDs, ","),
				fmt.Sprintf("identity_key=%s job_ids=%s", req.IdentityKey, strings.Join(jobIDs, ",")),
				c.ClientIP())
		}

		c.JSON(http.StatusOK, gin.H{"jobs": jobs, "skipped": skipped})
	}
}
