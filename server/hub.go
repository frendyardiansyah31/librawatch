package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"library-monitor/shared"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 54 * time.Second
	maxMsgSize = 2 * 1024 * 1024 // 2MB
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// IncomingMessage covers all agent→server message types.
type IncomingMessage struct {
	Type      string    `json:"type"`
	AgentID   string    `json:"agent_id"`
	Hostname  string    `json:"hostname"`
	IP        string    `json:"ip"`
	OS        string    `json:"os"`
	CPU       float64   `json:"cpu"`
	RAM       float64   `json:"ram"`
	Processes []Process `json:"processes"`
	// device profile fields (Phase 1 — sent on every metrics message)
	AgentVersion   string  `json:"agent_version"`
	WindowsVersion string  `json:"windows_version"`
	DiskCapacityGB float64 `json:"disk_capacity_gb"`
	MacAddress     string  `json:"mac_address"`
	// exec_result / log_result fields
	JobID      string `json:"job_id"`
	Attempt    *int   `json:"attempt,omitempty"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
	// network_mode_result field (reuses Status/Output above for the outcome)
	NetworkMode string `json:"network_mode"`
	// event fields (Phase 2 — usb_inserted, download_created, etc; see server/events.go)
	EventType string                 `json:"event_type"`
	Metadata  map[string]interface{} `json:"metadata"`
	// policy_version_check field (Priority 4 — agent's currently-cached policy version)
	PolicyVersion int64 `json:"policy_version"`
	// software_inventory_snapshot field — a full, non-diff report of one
	// agent's currently installed software (see agent/softwareinventory.go);
	// reconciled into the software_inventory table by handleSoftwareInventory.
	SoftwareItems []SoftwareInventoryItem `json:"items,omitempty"`
}

// OutgoingMessage covers all server→agent message types.
type OutgoingMessage struct {
	Type     string `json:"type"`
	JobID    string `json:"job_id,omitempty"`
	Attempt  *int   `json:"attempt,omitempty"`
	Payload  string `json:"payload,omitempty"`
	Filename string `json:"filename,omitempty"`
	Checksum string `json:"checksum,omitempty"` // file_deploy: sha256 hex of the uploaded file
	Args     string `json:"args,omitempty"`
	Lines    int    `json:"lines,omitempty"`
	Action   string `json:"action,omitempty"`    // deepfreeze: thaw/freeze/query_df
	Password string `json:"password,omitempty"`  // deepfreeze: optional DF password
	PID      int    `json:"pid,omitempty"`       // kill_process: target PID
	ProcName string `json:"proc_name,omitempty"` // kill_process: fallback by name; kill_by_identity: exe_name to match
	Path     string `json:"path,omitempty"`      // delete_file: absolute path to remove
	Company  string `json:"company,omitempty"`   // kill_by_identity: expected CompanyName, verified against each matching process's file before it's killed

	NetworkMode string `json:"network_mode,omitempty"` // network_mode: desired ethernet/wifi/both

	// policy_update fields (Priority 4/5) — pushed to every connected agent
	// whenever the policy version bumps, and sent as a direct reply to a
	// stale policy_version_check. DeviceGroup is this specific agent's own
	// current device_group (agents.device_group), included so the agent can
	// evaluate DeviceGroup-scoped rules locally without a second round-trip —
	// it has no other way to know its own assigned group. PolicyApps mirrors
	// GetPolicyRelevantApps (keyed by exe_name) so the agent can evaluate
	// AppStatus/CategoryID-scoped rules locally too — without it, the agent
	// would only know the rules themselves, not which apps are currently
	// blocked/categorized, which is exactly the data Priority 5's realtime
	// enforcement needs for the app_status=blocked case (the original Zoom bug).
	PolicyVersion int64                               `json:"policy_version,omitempty"`
	PolicyRules   []PolicyRule                        `json:"policy_rules,omitempty"`
	PolicyApps    map[string]shared.PolicyRelevantApp `json:"policy_apps,omitempty"`
	DeviceGroup   string                              `json:"device_group,omitempty"`
}

// ─── Client ────────────────────────────────────────────────────────────────

type Client struct {
	hub       *Hub
	conn      *websocket.Conn
	send      chan []byte
	agentID   string
	closeOnce sync.Once
}

func (c *Client) safeClose() {
	c.closeOnce.Do(func() { close(c.send) })
}

func (c *Client) readPump() {
	defer func() {
		c.hub.removeClient(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMsgSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		c.hub.handleMessage(c, data)
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ─── Hub ───────────────────────────────────────────────────────────────────

type Hub struct {
	mu                 sync.RWMutex
	clients            map[string]*Client
	db                 *DB
	alerter            *Alerter
	deployer           *Deployer
	batcher            *MetricsBatcher
	catalog            *Catalog
	policyEngine       *PolicyEngine
	events             *EventRecorder
	authToken          string   // if non-empty, WebSocket clients must provide ?token=
	lastMetricLog      sync.Map // agentID → time.Time, throttle log to every 5 min
	logWaiters         sync.Map // agentID → chan string, pending log relay
	killWaiters        sync.Map // agentID → chan string, pending kill result
	networkModeWaiters sync.Map // agentID → chan networkModeResult, pending network_mode result
}

// networkModeResult is the agent's self-reported outcome of a network-mode
// reconciliation attempt (see agent/network.go).
type networkModeResult struct {
	Mode   string
	Status string
	Output string
}

func NewHub(db *DB) *Hub {
	return &Hub{
		clients: make(map[string]*Client),
		db:      db,
	}
}

func (h *Hub) addClient(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.clients[c.agentID]; ok && old != c {
		old.safeClose()
	}
	h.clients[c.agentID] = c
}

func (h *Hub) removeClient(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.agentID != "" && h.clients[c.agentID] == c {
		delete(h.clients, c.agentID)
		if err := h.db.UpdateAgentStatus(c.agentID, "offline"); err != nil {
			slog.Error("update agent offline failed", "agent_id", c.agentID, "error", err)
		}
		slog.Info("agent disconnected", "agent_id", c.agentID)
	}
	c.safeClose()
}

func (h *Hub) SendToAgent(agentID string, msg *OutgoingMessage) bool {
	h.mu.RLock()
	client, ok := h.clients[agentID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	select {
	case client.send <- data:
		return true
	default:
		return false
	}
}

func (h *Hub) IsOnline(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clients[agentID]
	return ok
}

func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) AllOnlineIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.clients))
	for id := range h.clients {
		ids = append(ids, id)
	}
	return ids
}

// ─── Message Handling ──────────────────────────────────────────────────────

func (h *Hub) handleMessage(c *Client, data []byte) {
	var msg IncomingMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Error("ws unmarshal failed", "error", err)
		return
	}

	switch msg.Type {
	case "metrics":
		h.handleMetrics(c, &msg)
	case "exec_result", "deepfreeze_result":
		h.handleExecResult(&msg)
	case "log_result":
		h.handleLogResult(c, &msg)
	case "kill_result":
		h.handleKillResult(c, &msg)
	case "event":
		h.handleEvent(c, &msg)
	case "proc_start":
		h.handleProcessStart(c, &msg)
	case "delete_file_result":
		slog.Info("delete_file result", "agent_id", msg.AgentID, "output", msg.Output)
	case "network_mode_result":
		h.handleNetworkModeResult(&msg)
	case "policy_version_check":
		h.handlePolicyVersionCheck(c, &msg)
	case "policy_enforcement":
		h.handlePolicyEnforcement(c, &msg)
	case "software_inventory_snapshot":
		h.handleSoftwareInventory(c, &msg)
	}
}

// handleSoftwareInventory reconciles one agent's full software inventory
// snapshot (Software Inventory feature) — a state sync, not an event, so
// unlike handleEvent this never runs through the policy engine and has no
// side effects beyond the software_inventory table itself.
func (h *Hub) handleSoftwareInventory(c *Client, msg *IncomingMessage) {
	agentID := c.agentID
	if agentID == "" {
		agentID = msg.AgentID
	}
	if agentID == "" {
		return
	}
	if err := h.db.ReconcileSoftwareInventory(agentID, msg.SoftwareItems); err != nil {
		slog.Error("software inventory: reconcile failed", "agent_id", agentID, "error", err)
		return
	}
	slog.Info("software inventory: snapshot reconciled", "agent_id", agentID, "items", len(msg.SoftwareItems))
}

// handlePolicyEnforcement records a policy decision the agent already
// evaluated AND enforced locally (Priority 5 — agent/procwatch.go's realtime
// local-kill path, using its cached policy from Priority 4). This is
// audit-only: unlike handleEvent (which runs a fresh EventRecorder.Record →
// PolicyEngine.Evaluate → act pass for discrete OS events), this must NOT
// re-evaluate or re-act — the agent already made and carried out the
// decision before this message was even sent, and EventRecorder.act has no
// PolicyActionKill case, so routing this through it would silently
// overwrite an already-correct "killed" outcome with whatever act() falls
// back to. Just persist exactly what happened.
func (h *Hub) handlePolicyEnforcement(c *Client, msg *IncomingMessage) {
	agentID := c.agentID
	if agentID == "" {
		agentID = msg.AgentID
	}
	if agentID == "" || msg.EventType == "" {
		return
	}
	metadataJSON, err := json.Marshal(msg.Metadata)
	if err != nil {
		slog.Error("policy_enforcement: marshal metadata failed", "agent_id", agentID, "error", err)
		return
	}
	if _, err := h.db.InsertEvent(agentID, msg.EventType, string(metadataJSON), msg.Status); err != nil {
		slog.Error("policy_enforcement: insert event failed", "agent_id", agentID, "error", err)
	}
}

// handlePolicyVersionCheck answers an agent's report of its currently-cached
// policy version (sent on connect, and periodically as the agent's fallback
// safety check — see agent/policycache.go) — replying with a full
// policy_update only if the agent's version is stale, so an agent that's
// already current doesn't get a needless payload. Mirrors
// BroadcastPolicyUpdate's per-agent DeviceGroup lookup.
func (h *Hub) handlePolicyVersionCheck(c *Client, msg *IncomingMessage) {
	agentID := c.agentID
	if agentID == "" {
		agentID = msg.AgentID
	}
	if agentID == "" {
		return
	}

	currentVersion, err := h.db.GetPolicyVersion()
	if err != nil {
		slog.Error("policy_version_check: get version failed", "agent_id", agentID, "error", err)
		return
	}
	if msg.PolicyVersion >= currentVersion {
		return
	}

	if err := h.sendPolicyUpdateTo(agentID, currentVersion); err != nil {
		slog.Error("policy_version_check: send update failed", "agent_id", agentID, "error", err)
	}
}

// BroadcastPolicyUpdate pushes the current policy_rules + version to every
// connected agent. Called after any write that changes what
// PolicyEngine.Evaluate would decide (policy_rules CRUD, applications
// status/category changes) so the agent's local cache (Priority 4) never
// drifts far from the server's source of truth while online. Policy edits
// are rare/cheap, so a broadcast to every client on every edit is
// inexpensive — no batching/coalescing needed.
func (h *Hub) BroadcastPolicyUpdate() {
	version, err := h.db.GetPolicyVersion()
	if err != nil {
		slog.Error("BroadcastPolicyUpdate: get version failed", "error", err)
		return
	}
	for _, agentID := range h.AllOnlineIDs() {
		if err := h.sendPolicyUpdateTo(agentID, version); err != nil {
			slog.Error("BroadcastPolicyUpdate: send failed", "agent_id", agentID, "error", err)
		}
	}
}

// sendPolicyUpdateTo builds and sends one agent's policy_update — rules are
// identical for everyone, but DeviceGroup is looked up per-agent so
// DeviceGroup-scoped rules can be evaluated locally on the agent without a
// second round-trip.
func (h *Hub) sendPolicyUpdateTo(agentID string, version int64) error {
	rules, err := h.db.GetEnabledPolicyRules()
	if err != nil {
		return err
	}
	apps, err := h.db.GetPolicyRelevantApps()
	if err != nil {
		return err
	}
	deviceGroup, _ := h.db.GetAgentDeviceGroup(agentID)

	if !h.SendToAgent(agentID, &OutgoingMessage{
		Type:          "policy_update",
		PolicyVersion: version,
		PolicyRules:   rules,
		PolicyApps:    apps,
		DeviceGroup:   deviceGroup,
	}) {
		return fmt.Errorf("agent not online")
	}
	return nil
}

// handleEvent routes a Phase 2 system-policy event (USB, download, desktop/
// config change, install detection — see agent/events.go and the 5 watcher
// files) to the EventRecorder. Hostname is looked up from the DB rather than
// requiring every event message to carry it, since the agent already
// registers hostname on its first metrics message.
func (h *Hub) handleEvent(c *Client, msg *IncomingMessage) {
	agentID := c.agentID
	if agentID == "" {
		agentID = msg.AgentID
	}
	if agentID == "" || msg.EventType == "" || h.events == nil {
		return
	}
	hostname := agentID
	if agent, err := h.db.GetAgentByID(agentID); err == nil && agent != nil {
		hostname = agent.Hostname
	}
	h.events.Record(agentID, hostname, msg.EventType, msg.Metadata)
}

// handleProcessStart routes a WMI Win32_ProcessStartTrace signal (see
// agent/procwatch.go) straight into the same PolicyEngine.EvaluateProcesses
// path the 30s metrics tick uses (hub.go handleMetrics below), so a process
// that matches a kill rule gets terminated within roughly a second of
// launching instead of waiting for the next tick. Deliberately does not go
// through EventRecorder/the generic "event" type — EventRecorder.act (see
// events.go) has no case for PolicyActionKill, so routing this through it
// would silently swallow kill decisions. Also deliberately does not touch
// UpsertProcesses/catalog.Observe/InsertMetric like handleMetrics does: this
// is a policy-evaluation signal for one process, not a metrics snapshot, and
// must not pollute the processes table or overwrite the agent's real CPU/RAM
// with the zeroes this message carries.
func (h *Hub) handleProcessStart(c *Client, msg *IncomingMessage) {
	agentID := c.agentID
	if agentID == "" {
		agentID = msg.AgentID
	}
	if agentID == "" || len(msg.Processes) == 0 || h.policyEngine == nil {
		return
	}
	hostname := agentID
	if agent, err := h.db.GetAgentByID(agentID); err == nil && agent != nil {
		hostname = agent.Hostname
	}
	h.policyEngine.EvaluateProcesses(agentID, hostname, msg.Processes)
}

func (h *Hub) handleMetrics(c *Client, msg *IncomingMessage) {
	if msg.AgentID == "" {
		return
	}

	// Register client under agentID on first metrics message.
	// Check prior status before UpsertAgent overwrites it to "online".
	if c.agentID == "" {
		wasOffline := false
		if h.alerter != nil {
			existing, _ := h.db.GetAgentByID(msg.AgentID)
			wasOffline = existing != nil && existing.Status == "offline"
		}

		c.agentID = msg.AgentID
		h.addClient(c)
		slog.Info("agent connected", "agent_id", msg.AgentID, "hostname", msg.Hostname, "ip", msg.IP)

		if wasOffline && h.alerter != nil {
			go h.alerter.CheckRecovery(msg.AgentID, msg.Hostname)
		}

		if h.deployer != nil {
			go h.deployer.DispatchPending(msg.AgentID)
		}

		// Push the persisted desired network mode on every fresh connection
		// (first connect, reconnect, or agent restart) — the agent reconciles
		// idempotently, so resending here on every session start is exactly
		// the recovery mechanism needed if a prior reconciliation result was
		// never delivered, with no retry/lease bookkeeping required.
		if mode, err := h.db.GetAgentDesiredNetworkMode(msg.AgentID); err == nil {
			h.SendToAgent(msg.AgentID, &OutgoingMessage{Type: "network_mode", NetworkMode: mode})
		} else {
			slog.Error("get desired network mode failed", "agent_id", msg.AgentID, "error", err)
		}
	}

	now := nowWIB()
	agent := &Agent{
		ID:             msg.AgentID,
		Hostname:       msg.Hostname,
		IP:             msg.IP,
		OS:             msg.OS,
		LastSeen:       now,
		Status:         "online",
		CreatedAt:      now,
		AgentVersion:   msg.AgentVersion,
		WindowsVersion: msg.WindowsVersion,
		DiskCapacityGB: msg.DiskCapacityGB,
		MacAddress:     msg.MacAddress,
	}
	if err := h.db.UpsertAgent(agent); err != nil {
		slog.Error("upsert agent failed", "agent_id", msg.AgentID, "error", err)
		return
	}

	if h.batcher != nil {
		h.batcher.Add(msg.AgentID, msg.CPU, msg.RAM)
	} else {
		if err := h.db.InsertMetric(msg.AgentID, msg.CPU, msg.RAM); err != nil {
			slog.Error("insert metric failed", "agent_id", msg.AgentID, "error", err)
		}
	}

	if len(msg.Processes) > 0 {
		if err := h.db.UpsertProcesses(msg.AgentID, msg.Processes); err != nil {
			slog.Error("upsert processes failed", "agent_id", msg.AgentID, "error", err)
		}
		if h.catalog != nil {
			h.catalog.Observe(msg.AgentID, msg.Processes)
		}
		if h.policyEngine != nil {
			h.policyEngine.EvaluateProcesses(msg.AgentID, msg.Hostname, msg.Processes)
		}
	}

	// Log only once every 5 minutes per agent
	lastLog, _ := h.lastMetricLog.Load(msg.AgentID)
	if lastLog == nil || now.Sub(lastLog.(time.Time)) >= 5*time.Minute {
		slog.Info("metrics received",
			"agent_id", msg.AgentID,
			"hostname", msg.Hostname,
			"cpu", msg.CPU,
			"ram", msg.RAM)
		h.lastMetricLog.Store(msg.AgentID, now)
	}

	if h.alerter != nil {
		h.alerter.CheckMetrics(msg.AgentID, msg.Hostname, msg.CPU, msg.RAM, msg.Processes)
	}
}

func (h *Hub) handleExecResult(msg *IncomingMessage) {
	if msg.JobID == "" {
		return
	}
	output := msg.Output
	if len(output) > 4096 {
		output = output[:4096] + "...[truncated]"
	}
	affected, err := h.db.UpdateDeployResult(msg.JobID, msg.AgentID, msg.Status, output, msg.ExitCode, msg.DurationMS, nil, msg.Attempt)
	if err != nil {
		slog.Error("update deploy result failed", "job_id", msg.JobID, "error", err)
		// A genuine DB error means the result wasn't durably recorded — don't
		// ack, so the agent retries delivery on its next reconnect.
		return
	}

	// The message was received and processed either way — ack now so the
	// agent's local pending-result store stops retransmitting it, even if
	// the row match below turns out to be stale (see next check). Without
	// this, a late ack for an attempt the lease sweeper already superseded
	// would keep being replayed by the agent forever.
	h.SendToAgent(msg.AgentID, &OutgoingMessage{Type: "exec_result_ack", JobID: msg.JobID})

	if affected == 0 {
		slog.Warn("exec_result ignored: stale attempt or cancelled job",
			"job_id", msg.JobID, "agent_id", msg.AgentID, "attempt", msg.Attempt)
		return
	}

	slog.Info("result received",
		"type", msg.Type,
		"job_id", msg.JobID,
		"agent_id", msg.AgentID,
		"status", msg.Status)

	if msg.Status == "error" {
		h.notifyDeployFailure(msg, output)
	}

	if err := h.db.UpdateJobStatus(msg.JobID); err != nil {
		slog.Error("update job status failed", "job_id", msg.JobID, "error", err)
	}

	// This agent's current command just finished — advance its queue right
	// away instead of waiting for the next reconnect.
	if h.deployer != nil {
		go h.deployer.PumpAgent(msg.AgentID)
	}
}

// notifyDeployFailure routes a failed deploy job (exec / winget / file_deploy
// / deepfreeze / install_ssh) through the same alert pipeline already used
// for offline/CPU/blacklisted-app events (Alerter.fire: DB row shown in the
// Alerts tab, plus Telegram/email if configured) — so a failed deploy is
// surfaced proactively instead of only being visible by opening the Deploy
// tab and expanding the job card.
func (h *Hub) notifyDeployFailure(msg *IncomingMessage, output string) {
	if h.alerter == nil {
		return
	}

	hostname := msg.AgentID
	if ag, err := h.db.GetAgentByID(msg.AgentID); err == nil && ag != nil {
		hostname = ag.Hostname
	}

	what := msg.Type
	if job, err := h.db.GetDeployJobByID(msg.JobID); err == nil && job != nil {
		what = job.Type + ": " + job.Payload
	}

	errText := output
	if len(errText) > 300 {
		errText = errText[:300] + "...[truncated]"
	}

	settings, err := h.db.GetAllSettings()
	if err != nil {
		slog.Error("notifyDeployFailure: get settings failed", "job_id", msg.JobID, "error", err)
		return
	}

	message := fmt.Sprintf("⚠️ Deploy gagal di %s: %s\n%s", hostname, what, errText)
	h.alerter.fire(msg.AgentID, "deploy_failed", message, settings)
}

func (h *Hub) handleLogResult(c *Client, msg *IncomingMessage) {
	agentID := c.agentID
	if agentID == "" {
		agentID = msg.AgentID
	}
	if v, ok := h.logWaiters.Load(agentID); ok {
		ch := v.(chan string)
		select {
		case ch <- msg.Output:
		default:
		}
	}
}

func (h *Hub) RequestAgentLogs(agentID string, lines int) (string, error) {
	ch := make(chan string, 1)
	h.logWaiters.Store(agentID, ch)
	defer h.logWaiters.Delete(agentID)

	if !h.SendToAgent(agentID, &OutgoingMessage{Type: "get_logs", Lines: lines}) {
		return "", fmt.Errorf("agent not online")
	}

	select {
	case output := <-ch:
		return output, nil
	case <-time.After(15 * time.Second):
		return "", fmt.Errorf("agent log request timed out")
	}
}

func (h *Hub) handleKillResult(c *Client, msg *IncomingMessage) {
	agentID := c.agentID
	if agentID == "" {
		agentID = msg.AgentID
	}
	if v, ok := h.killWaiters.Load(agentID); ok {
		ch := v.(chan string)
		select {
		case ch <- msg.Output:
		default:
		}
	}
}

func (h *Hub) KillProcess(agentID string, pid int, name string) (string, error) {
	ch := make(chan string, 1)
	h.killWaiters.Store(agentID, ch)
	defer h.killWaiters.Delete(agentID)

	if !h.SendToAgent(agentID, &OutgoingMessage{Type: "kill_process", PID: pid, ProcName: name}) {
		return "", fmt.Errorf("agent not online")
	}

	select {
	case output := <-ch:
		return output, nil
	case <-time.After(10 * time.Second):
		return "", fmt.Errorf("kill request timed out")
	}
}

// KillProcessByIdentity asks the agent to terminate the specific process
// instance identified by pid — verified live against procName/company on
// the agent side before being killed (agent/killidentity.go) — falling back
// to an enumerate-by-name(+company) sweep only if that PID no longer
// matches (already exited, or recycled by an unrelated process in the gap
// between this decision and the agent receiving it). pid is always a live
// value from the current decision (a proc_start event or the current
// metrics tick) — callers must never persist or reuse a PID across
// decisions. Pass pid=0 when it's genuinely unknown, which skips straight to
// the name-based sweep.
//
// Unlike KillProcess(agentID, 0, name) — which maps to a bare
// "taskkill /F /IM name" and would terminate any process with that name,
// regardless of which vendor shipped it — this is for PolicyEngine's
// app_status=blocked kill rule, where exe_name alone isn't a safe-enough
// match. Reuses the same killWaiters wait-for-reply plumbing as
// KillProcess — the agent replies with the same "kill_result" message type
// for both.
func (h *Hub) KillProcessByIdentity(agentID, procName, company string, pid int) (string, error) {
	ch := make(chan string, 1)
	h.killWaiters.Store(agentID, ch)
	defer h.killWaiters.Delete(agentID)

	if !h.SendToAgent(agentID, &OutgoingMessage{Type: "kill_by_identity", ProcName: procName, Company: company, PID: pid}) {
		return "", fmt.Errorf("agent not online")
	}

	select {
	case output := <-ch:
		return output, nil
	case <-time.After(10 * time.Second):
		return "", fmt.Errorf("kill request timed out")
	}
}

// handleNetworkModeResult persists the agent's self-reported reconciliation
// outcome (unlike kill/log results, which are fire-and-forget) and, if an
// admin API call is synchronously waiting on it, delivers it there too.
func (h *Hub) handleNetworkModeResult(msg *IncomingMessage) {
	if msg.AgentID == "" {
		return
	}
	detail := msg.Output
	if len(detail) > 2048 {
		detail = detail[:2048] + "...[truncated]"
	}
	if err := h.db.UpdateAgentNetworkModeResult(msg.AgentID, msg.NetworkMode, msg.Status, detail); err != nil {
		slog.Error("update network mode result failed", "agent_id", msg.AgentID, "error", err)
	}
	if v, ok := h.networkModeWaiters.Load(msg.AgentID); ok {
		select {
		case v.(chan networkModeResult) <- networkModeResult{Mode: msg.NetworkMode, Status: msg.Status, Output: detail}:
		default:
		}
	}
}

// SetNetworkMode pushes a desired network mode to agentID and waits (briefly)
// for its reconciliation result. online=false means the agent wasn't
// connected to receive the push — not an error, since the desired mode is
// persisted by the caller regardless and will be reconciled on next connect.
func (h *Hub) SetNetworkMode(agentID, mode string) (result networkModeResult, online bool, err error) {
	ch := make(chan networkModeResult, 1)
	h.networkModeWaiters.Store(agentID, ch)
	defer h.networkModeWaiters.Delete(agentID)

	if !h.SendToAgent(agentID, &OutgoingMessage{Type: "network_mode", NetworkMode: mode}) {
		return networkModeResult{}, false, nil
	}

	select {
	case r := <-ch:
		return r, true, nil
	case <-time.After(30 * time.Second):
		return networkModeResult{}, true, fmt.Errorf("network mode request timed out")
	}
}

// ─── WebSocket Endpoint ────────────────────────────────────────────────────

func ServeWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	if hub.authToken != "" {
		token := r.URL.Query().Get("token")
		if token != hub.authToken {
			slog.Warn("ws auth rejected", "remote_addr", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
		return
	}

	client := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}

	go client.writePump()
	go client.readPump()
}
