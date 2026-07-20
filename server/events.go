package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// EventRecorder is the ingestion point for Module 7 (Event Timeline): every
// USB/download/desktop/config/install event an agent reports comes through
// Record, gets stored, evaluated by the Policy Engine, and — for install
// events — folded into the existing Application Catalog (Module 5) via the
// same UpsertApplication family Phase 1 already built.
type EventRecorder struct {
	db     *DB
	hub    *Hub
	policy *PolicyEngine
}

func NewEventRecorder(db *DB, hub *Hub, policy *PolicyEngine) *EventRecorder {
	return &EventRecorder{db: db, hub: hub, policy: policy}
}

// Record stores an incoming agent event, evaluates it against policy, acts
// on the decision (notify/delete — block and kill aren't applicable to
// discrete OS events the same way they are to running processes; kill only
// makes sense for Module 6, delete only for Module 2 downloads), and — for
// install/update events — integrates with the Application Catalog.
func (e *EventRecorder) Record(agentID, hostname, eventType string, metadata map[string]interface{}) {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		slog.Error("event: marshal metadata failed", "type", eventType, "error", err)
		return
	}

	ctx := PolicyContext{AgentID: agentID, EventType: eventType}
	if group, err := e.db.GetAgentDeviceGroup(agentID); err == nil {
		ctx.DeviceGroup = group
	}
	if ext, ok := metadata["extension"].(string); ok {
		ctx.FileExtension = ext
	}
	if loc, ok := metadata["location"].(string); ok {
		ctx.ExecutionLocation = loc
	} else if eventType == "usb_inserted" || eventType == "usb_removed" {
		ctx.ExecutionLocation = "usb"
	}

	decision := e.policy.Evaluate(ctx)
	finalAction := e.act(agentID, hostname, eventType, metadata, decision)

	if _, err := e.db.InsertEvent(agentID, eventType, string(metadataJSON), finalAction); err != nil {
		slog.Error("event: insert failed", "type", eventType, "error", err)
	}

	if eventType == "software_installed" || eventType == "software_updated" {
		e.integrateCatalog(agentID, metadata)
	}

	if eventType == "peripheral_removed" && e.hub.alerter != nil {
		deviceName, _ := metadata["device_name"].(string)
		deviceClass, _ := metadata["device_class"].(string)
		e.hub.alerter.FireTamperAlert(agentID, hostname, deviceName, deviceClass)
	}

	slog.Info("event recorded", "agent_id", agentID, "type", eventType, "action", finalAction)
}

// act executes what the policy decided and returns the action that should be
// persisted on the event row (which may differ from the requested action if
// execution failed, mirroring policy.go's actOnExecution).
func (e *EventRecorder) act(agentID, hostname, eventType string, metadata map[string]interface{}, decision PolicyDecision) string {
	switch decision.Action {
	case PolicyActionNotify:
		e.policy.notify(formatEventMessage(hostname, eventType, metadata))
		return EventActionNotify

	case PolicyActionDelete:
		if eventType != "download_created" {
			return EventActionLog // delete only applies to Module 2 events
		}
		path, _ := metadata["path"].(string)
		if path == "" || !e.hub.SendToAgent(agentID, &OutgoingMessage{Type: "delete_file", Path: path}) {
			slog.Warn("event: delete_file dispatch failed", "agent_id", agentID, "path", path)
			return EventActionLog
		}
		e.policy.notify(fmt.Sprintf("🗑️ File dihapus otomatis di %s: %s", hostname, path))
		return EventActionDeleted

	case PolicyActionBlock:
		// USB "block" is recorded only (brief: "do NOT disable USB devices
		// yet") — same for desktop/config "block", which has no agent-side
		// enforcement hook this phase.
		e.policy.notify(formatEventMessage(hostname, eventType, metadata))
		return EventActionBlocked

	default:
		return EventActionLog
	}
}

func formatEventMessage(hostname, eventType string, metadata map[string]interface{}) string {
	label := map[string]string{
		"usb_inserted":         "🔌 USB terpasang",
		"usb_removed":          "🔌 USB dilepas",
		"peripheral_connected": "🖱️ Perangkat terpasang",
		"peripheral_removed":   "🖱️ Perangkat terlepas",
		"download_created":     "⬇️ File baru di folder terpantau",
		"wallpaper_changed":    "🖼️ Wallpaper diubah",
		"theme_changed":        "🎨 Tema diubah",
		"config_changed":       "⚙️ Konfigurasi Windows berubah",
		"software_installed":   "📦 Software terinstall",
		"software_removed":     "📦 Software dihapus",
		"software_updated":     "📦 Software diperbarui",
	}[eventType]
	if label == "" {
		label = eventType
	}
	detail, _ := json.Marshal(metadata)
	return fmt.Sprintf("%s — %s\n%s", label, hostname, string(detail))
}

// integrateCatalog folds a software_installed/software_updated event into
// the existing Application Catalog (Phase 1's applications table) via
// UpsertApplicationByProduct — see server/db.go for why installs use a
// different lookup than process-detected apps (no exe_name available from
// the Uninstall registry).
func (e *EventRecorder) integrateCatalog(agentID string, metadata map[string]interface{}) {
	productName, _ := metadata["display_name"].(string)
	company, _ := metadata["publisher"].(string)
	version, _ := metadata["version"].(string)
	if productName == "" {
		return
	}
	meta := &AppMetadata{ProductVersion: version}
	if _, err := e.db.UpsertApplicationByProduct(productName, company, meta); err != nil {
		slog.Error("event: catalog integration failed", "agent_id", agentID, "product", productName, "error", err)
	}
}
