package main

import (
	"log/slog"
)

// Catalog is the application-detection pipeline:
//
//	Process Detected → Metadata Extraction (agent-side, see agent/appmeta.go)
//	                  → Application Lookup (UpsertApplication)
//	                  → Category Lookup    (category_id on the applications row,
//	                                        nil until an admin assigns one)
//	                  → Policy Decision / Action — unchanged in Phase 1. Alerter
//	                    still drives kill/alert purely off the settings.blacklist
//	                    text list; a future PolicyEngine would plug in here,
//	                    reading Application.Status instead.
//
// Catalog.Observe runs alongside (not instead of) the existing
// db.UpsertProcesses + Alerter.CheckMetrics calls in hub.go — it only
// populates the catalog/sightings tables and never affects alerting.
type Catalog struct {
	db *DB
}

func NewCatalog(db *DB) *Catalog {
	return &Catalog{db: db}
}

// Observe records every process in procs against the application catalog.
// Many PIDs share one executable path (e.g. multiple chrome.exe / svchost.exe
// instances) so it dedupes by path within the batch before upserting.
func (c *Catalog) Observe(agentID string, procs []Process) {
	seen := make(map[string]bool, len(procs))
	for _, p := range procs {
		if p.Path == "" || seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		c.observeOne(agentID, p)
	}
}

// observeOne resolves p.Path to an application ID and records a sighting.
//
// Identity (exe_name + company) only needs to be resolved once per
// (agent, path): the agent only attaches metadata (including Company) the
// first time it sees a path each session (agent/appmeta.go), so on every
// later cycle — and for every other PID that happens to share that same
// path, e.g. a second chrome.exe process — p.Company arrives empty. Calling
// UpsertApplication with an empty company on those cycles would look up (or
// create) the identity (p.Name, "") instead of the real
// (p.Name, "Google LLC") row, fragmenting one app into two catalog rows.
// Checking app_sightings first avoids that: once a path is linked to an
// application, every later sighting of that exact path reuses the link
// directly instead of re-deriving identity from data that may no longer be
// present in this cycle's message.
func (c *Catalog) observeOne(agentID string, p Process) {
	appID, found, err := c.db.GetSightingApplicationID(agentID, p.Path)
	if err != nil {
		slog.Error("catalog: lookup sighting failed", "agent_id", agentID, "path", p.Path, "error", err)
		return
	}

	if !found {
		var meta *AppMetadata
		if p.ProductName != "" || p.Company != "" || p.Description != "" || p.ProductVersion != "" {
			meta = &AppMetadata{
				ProductName:    p.ProductName,
				Company:        p.Company,
				Description:    p.Description,
				ProductVersion: p.ProductVersion,
			}
		}
		appID, err = c.db.UpsertApplication(p.Name, p.Company, meta)
		if err != nil {
			slog.Error("catalog: upsert application failed", "agent_id", agentID, "exe", p.Name, "error", err)
			return
		}
	}

	fileCreated := parseDBTime(p.FileCreatedAt)
	fileModified := parseDBTime(p.FileModifiedAt)
	if err := c.db.UpsertSighting(agentID, appID, p.Path, p.Size, fileCreated, fileModified); err != nil {
		slog.Error("catalog: upsert sighting failed", "agent_id", agentID, "path", p.Path, "error", err)
	}
}
