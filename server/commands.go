package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Canned PowerShell one-liners shared by the Command API and
// server/mcp.go's restart_pc/shutdown_pc tools, so both entry points run the
// exact same command instead of maintaining two copies of the string.
const (
	psRestart  = "Restart-Computer -Force"
	psShutdown = "Stop-Computer -Force"
	psLock     = "rundll32.exe user32.dll,LockWorkStation"
	psLogout   = "shutdown.exe /l"
	psSleep    = "rundll32.exe powrprof.dll,SetSuspendState 0,1,0"
)

// validCommandActions is the full CommandAction enum from docs/openapi.yaml.
var validCommandActions = map[string]bool{
	"restart": true, "shutdown": true, "lock": true, "unlock": true,
	"logout": true, "sleep": true, "wake": true, "kill_process": true,
	"broadcast": true, "popup": true, "deploy": true, "run_command": true,
	"enable_wifi": true, "disable_wifi": true, "enable_lan": true, "disable_lan": true,
	"refresh_policy": true, "collect_inventory": true, "take_screenshot": true,
}

// notImplementedActions are valid CommandAction values with no working
// mechanism yet — these three need new agent-side handlers that don't
// exist — deferred to a later pass.
var notImplementedActions = map[string]bool{
	"refresh_policy": true, "collect_inventory": true, "take_screenshot": true,
}

var (
	commandProcessNameRe       = regexp.MustCompile(`^[A-Za-z0-9_.\-]+\.exe$`)
	commandMessageControlCharRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

var errNoCommandTarget = errors.New("target does not match any agent")

// commandClientError signals that a command was rejected because of bad
// input (as opposed to an internal/DB failure) — handlePostCommand maps it
// to the given HTTP status instead of a 500.
type commandClientError struct {
	Status  int
	Message string
}

func (e *commandClientError) Error() string { return e.Message }

// ─── Request/response shapes (mirrors docs/openapi.yaml's Command* schemas) ─

type CommandRequest struct {
	Target        string                 `json:"target"`
	Action        string                 `json:"action"`
	Parameters    map[string]interface{} `json:"parameters"`
	RequestedBy   string                 `json:"requested_by"`
	CorrelationID string                 `json:"correlation_id"`
}

type CommandResponse struct {
	JobID                  string    `json:"job_id"`
	Status                 string    `json:"status"`
	CreatedAt              time.Time `json:"created_at"`
	EstimatedQueuePosition int       `json:"estimated_queue_position"`
}

type CommandProgress struct {
	Total      int `json:"total"`
	Dispatched int `json:"dispatched"`
	Succeeded  int `json:"succeeded"`
	Failed     int `json:"failed"`
	Pending    int `json:"pending"`
}

type CommandStatus struct {
	JobID         string          `json:"job_id"`
	Target        string          `json:"target"`
	Action        string          `json:"action"`
	Status        string          `json:"status"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	Progress      CommandProgress `json:"progress"`
	Results       []DeployResult  `json:"results"`
	Error         *string         `json:"error"`
}

type CommandSummary struct {
	JobID     string    `json:"job_id"`
	Target    string    `json:"target"`
	Action    string    `json:"action"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// ─── Target resolution ──────────────────────────────────────────────────────

// resolveCommandTargets resolves a CommandRequest.target string to concrete
// agent IDs, checked in order: exact agent ID, hostname (case-insensitive),
// device_group, floor, then the literal "all". See docs/openapi.yaml's
// POST /api/v1/commands description for the same rule.
func resolveCommandTargets(db *DB, target string) ([]string, error) {
	if target == "all" || target == "*" {
		ids, err := db.GetAllAgentIDs()
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, errNoCommandTarget
		}
		return ids, nil
	}

	agents, err := db.GetAllAgents()
	if err != nil {
		return nil, err
	}

	for _, a := range agents {
		if a.ID == target {
			return []string{a.ID}, nil
		}
	}
	for _, a := range agents {
		if strings.EqualFold(a.Hostname, target) {
			return []string{a.ID}, nil
		}
	}
	var byGroup []string
	for _, a := range agents {
		if a.DeviceGroup != "" && a.DeviceGroup == target {
			byGroup = append(byGroup, a.ID)
		}
	}
	if len(byGroup) > 0 {
		return byGroup, nil
	}
	var byFloor []string
	for _, a := range agents {
		if a.Floor != "" && a.Floor == target {
			byFloor = append(byFloor, a.ID)
		}
	}
	if len(byFloor) > 0 {
		return byFloor, nil
	}
	return nil, errNoCommandTarget
}

// ─── Action → job mapping ───────────────────────────────────────────────────

func paramString(params map[string]interface{}, key string) (string, bool) {
	v, ok := params[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// paramInt reads a numeric parameter — encoding/json decodes JSON numbers as
// float64 when the target is map[string]interface{}.
func paramInt(params map[string]interface{}, key string) (int, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// sanitizeExecMessage validates a broadcast/popup message and escapes it for
// safe embedding inside a single-quoted PowerShell string literal (doubling
// each embedded quote), so the message text can never break out of the
// literal into arbitrary PowerShell.
func sanitizeExecMessage(msg string) (string, error) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", fmt.Errorf("parameters.message is required")
	}
	if len(msg) > 500 {
		return "", fmt.Errorf("parameters.message exceeds maximum length of 500 characters")
	}
	if commandMessageControlCharRe.MatchString(msg) {
		return "", fmt.Errorf("parameters.message contains invalid control characters")
	}
	return strings.ReplaceAll(msg, "'", "''"), nil
}

// actionToJob translates every CommandAction that maps onto a plain "exec"
// job into the (jobType, payload, args) triple Deployer.CreateJob expects —
// the same shape POST /api/deploy and server/mcp.go's canned tools already
// use. "deploy" and the network toggles are handled separately by the
// caller since they need per-target logic or a full field passthrough.
func actionToJob(action string, params map[string]interface{}) (jobType, payload, args string, err error) {
	switch action {
	case "restart":
		return "exec", psRestart, "", nil
	case "shutdown":
		return "exec", psShutdown, "", nil
	case "lock":
		return "exec", psLock, "", nil
	case "logout":
		return "exec", psLogout, "", nil
	case "sleep":
		return "exec", psSleep, "", nil
	case "kill_process":
		process, _ := paramString(params, "process")
		if !commandProcessNameRe.MatchString(process) {
			return "", "", "", fmt.Errorf("parameters.process must be a valid .exe process name")
		}
		return "exec", fmt.Sprintf("taskkill /F /IM %s", process), "", nil
	case "broadcast", "popup":
		message, _ := paramString(params, "message")
		escaped, err := sanitizeExecMessage(message)
		if err != nil {
			return "", "", "", err
		}
		return "exec", fmt.Sprintf("msg * /TIME:30 '%s'", escaped), "", nil
	case "run_command":
		command, _ := paramString(params, "command")
		if command == "" {
			return "", "", "", fmt.Errorf("parameters.command is required")
		}
		return "exec", command, "", nil
	default:
		return "", "", "", fmt.Errorf("action %q is not handled by actionToJob", action)
	}
}

// createDeployPassthroughCommand implements action=deploy: a full
// passthrough of parameters.{type,payload,args,priority,expire_at,max_retry}
// into the same Deployer.CreateJob + validateDeployRequest pipeline
// POST /api/deploy itself uses, so "deploy" is a genuine escape hatch rather
// than a re-designed subset of it.
func createDeployPassthroughCommand(deployer *Deployer, params map[string]interface{}, agentIDs []string, requestedBy string) (*DeployJob, error) {
	typ, _ := paramString(params, "type")
	if typ == "" {
		return nil, &commandClientError{http.StatusBadRequest, "parameters.type is required for action=deploy"}
	}
	payload, _ := paramString(params, "payload")
	args, _ := paramString(params, "args")
	if typ != "install_ssh" && payload == "" {
		return nil, &commandClientError{http.StatusBadRequest, "parameters.payload is required for type " + typ}
	}
	if err := validateDeployRequest(typ, payload, args); err != nil {
		return nil, &commandClientError{http.StatusBadRequest, err.Error()}
	}

	priority, _ := paramInt(params, "priority")
	maxRetry := deployer.DefaultMaxRetry()
	if v, ok := paramInt(params, "max_retry"); ok {
		maxRetry = v
	}
	var expireAt *time.Time
	if s, ok := paramString(params, "expire_at"); ok && s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, &commandClientError{http.StatusBadRequest, "parameters.expire_at must be RFC3339"}
		}
		if t.Before(nowWIB()) {
			return nil, &commandClientError{http.StatusBadRequest, "parameters.expire_at must be in the future"}
		}
		expireAt = &t
	}

	return deployer.CreateJob(typ, payload, args, agentIDs, priority, expireAt, maxRetry, requestedBy)
}

// computeNetworkModeTransition returns the new desired_network_mode for one
// of the enable_wifi/disable_wifi/enable_lan/disable_lan actions given the
// agent's current mode. ok=false means applying it would leave the agent
// with no network path at all — the caller must reject rather than apply.
func computeNetworkModeTransition(action, current string) (newMode string, ok bool) {
	switch action {
	case "enable_wifi":
		if current == "ethernet" {
			return "both", true
		}
		return current, true
	case "disable_wifi":
		switch current {
		case "both":
			return "ethernet", true
		case "wifi":
			return "", false
		default:
			return current, true
		}
	case "enable_lan":
		if current == "wifi" {
			return "both", true
		}
		return current, true
	case "disable_lan":
		switch current {
		case "both":
			return "wifi", true
		case "ethernet":
			return "", false
		default:
			return current, true
		}
	default:
		return "", false
	}
}

// createNetworkToggleCommand implements enable_wifi/disable_wifi/
// enable_lan/disable_lan. network_mode is a synchronous per-agent push
// (hub.SetNetworkMode), not a queued job type, and the target mode depends
// on each agent's *current* desired_network_mode — so unlike every other
// action this doesn't go through Deployer.CreateJob/dispatch(). It still
// produces a DeployJob (via the same InsertDeployJob/InsertDeployResult
// methods deploy.go itself uses) purely so the command has a job_id
// GET /api/v1/commands/{job_id} can poll like any other.
//
// Per-agent results never use status "pending"/"running" — those are the
// exact statuses the generic queue (AcquireNextJob) claims work for on an
// agent's reconnect, and dispatch() has no case for job.Type="network_mode"
// (it would forward the action name as Payload, not a resolved mode, to the
// agent's NetworkMode-keyed handler). Using "deferred" for the offline case
// keeps this job type fully outside that machinery, matching what
// POST /api/agents/{id}/network-mode already does today: persist the
// desired state and return immediately, relying on the agent's own
// reconnect-time reconciliation (server/hub.go:314) rather than the deploy
// queue.
// insertDeployResultRow inserts one DeployResult row for a command job built
// outside Deployer.CreateJob (network toggles, wake), logging on failure
// rather than propagating — the job/loop should keep going for the other
// targets even if one insert fails.
func insertDeployResultRow(db *DB, jobID, agentID, status, output string) {
	if err := db.InsertDeployResult(&DeployResult{JobID: jobID, AgentID: agentID, Status: status, Output: output}); err != nil {
		slog.Error("insert deploy result failed", "job_id", jobID, "agent_id", agentID, "error", err)
	}
}

func createNetworkToggleCommand(db *DB, hub *Hub, action string, agentIDs []string, requestedBy string) (*DeployJob, error) {
	if len(agentIDs) == 1 {
		current, err := db.GetAgentDesiredNetworkMode(agentIDs[0])
		if err != nil {
			return nil, err
		}
		if _, ok := computeNetworkModeTransition(action, current); !ok {
			return nil, &commandClientError{http.StatusBadRequest, "this would leave the agent with no network connectivity"}
		}
	}

	targetsJSON, _ := json.Marshal(agentIDs)
	job := &DeployJob{
		ID:        generateJobID(),
		Type:      "network_mode",
		Payload:   action,
		Targets:   string(targetsJSON),
		Status:    "pending",
		CreatedBy: requestedBy,
		CreatedAt: nowWIB(),
	}
	if err := db.InsertDeployJob(job); err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	for _, agentID := range agentIDs {
		current, err := db.GetAgentDesiredNetworkMode(agentID)
		if err != nil {
			slog.Error("get desired network mode failed", "agent_id", agentID, "error", err)
			insertDeployResultRow(db, job.ID, agentID, "failed", err.Error())
			continue
		}
		newMode, ok := computeNetworkModeTransition(action, current)
		if !ok {
			insertDeployResultRow(db, job.ID, agentID, "failed", "would leave agent with no network connectivity")
			continue
		}
		if err := db.SetAgentDesiredNetworkMode(agentID, newMode); err != nil {
			slog.Error("set desired network mode failed", "agent_id", agentID, "error", err)
			insertDeployResultRow(db, job.ID, agentID, "failed", err.Error())
			continue
		}

		result, online, err := hub.SetNetworkMode(agentID, newMode)
		switch {
		case !online:
			insertDeployResultRow(db, job.ID, agentID, "deferred", "agent offline; will reconcile automatically on next connect")
		case err != nil:
			insertDeployResultRow(db, job.ID, agentID, "failed", err.Error())
		default:
			insertDeployResultRow(db, job.ID, agentID, "success", result.Output)
		}
	}

	if err := db.UpdateDeployJobStatus(job.ID, "done"); err != nil {
		slog.Error("update job status failed", "job_id", job.ID, "error", err)
	} else {
		job.Status = "done"
	}
	return job, nil
}

// createWakeCommand implements the wake action (Wake-on-LAN). The target PC
// is, by definition, powered off, so this never goes through the agent
// WebSocket at all — it's a UDP magic-packet broadcast keyed off the
// agent's stored mac_address. Like createNetworkToggleCommand, it builds a
// DeployJob directly (not via Deployer.CreateJob) purely so the command has
// a job_id GET /api/v1/commands/{job_id} can poll, and finalizes it "done"
// immediately since sending is synchronous and there is no ack path — the
// target either received the packet or it didn't, and there's no way to
// tell which from here.
func createWakeCommand(db *DB, agentIDs []string, requestedBy string) (*DeployJob, error) {
	if len(agentIDs) == 1 {
		mac, err := db.GetAgentMacAddress(agentIDs[0])
		if err != nil {
			return nil, err
		}
		if mac == "" {
			return nil, &commandClientError{http.StatusBadRequest, "agent has no known MAC address; cannot send Wake-on-LAN"}
		}
	}

	targetsJSON, _ := json.Marshal(agentIDs)
	job := &DeployJob{
		ID:        generateJobID(),
		Type:      "wake",
		Payload:   "wake",
		Targets:   string(targetsJSON),
		Status:    "pending",
		CreatedBy: requestedBy,
		CreatedAt: nowWIB(),
	}
	if err := db.InsertDeployJob(job); err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	for _, agentID := range agentIDs {
		mac, err := db.GetAgentMacAddress(agentID)
		if err != nil {
			slog.Error("get mac address failed", "agent_id", agentID, "error", err)
			insertDeployResultRow(db, job.ID, agentID, "failed", err.Error())
			continue
		}
		if mac == "" {
			insertDeployResultRow(db, job.ID, agentID, "failed", "agent has no known MAC address")
			continue
		}
		if err := sendMagicPacket(mac); err != nil {
			insertDeployResultRow(db, job.ID, agentID, "failed", err.Error())
			continue
		}
		insertDeployResultRow(db, job.ID, agentID, "success",
			"magic packet sent to "+mac+" (fire-and-forget — no delivery/wake confirmation is possible)")
	}

	if err := db.UpdateDeployJobStatus(job.ID, "done"); err != nil {
		slog.Error("update job status failed", "job_id", job.ID, "error", err)
	} else {
		job.Status = "done"
	}
	return job, nil
}

// ─── HTTP handlers ───────────────────────────────────────────────────────────

func handlePostCommand(db *DB, hub *Hub, deployer *Deployer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req CommandRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.Target == "" || req.Action == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "target and action are required"})
			return
		}
		if !validCommandActions[req.Action] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown action: " + req.Action})
			return
		}
		if req.Action == "unlock" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unlock is not supported: Windows has no safe remote-unlock mechanism without credential injection"})
			return
		}
		if notImplementedActions[req.Action] {
			c.JSON(http.StatusNotImplemented, gin.H{"error": "action " + req.Action + " is not implemented yet"})
			return
		}

		agentIDs, err := resolveCommandTargets(db, req.Target)
		if err != nil {
			if errors.Is(err, errNoCommandTarget) {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		requestedBy := req.RequestedBy
		if requestedBy == "" {
			requestedBy = "api"
		}

		// Advisory queue depth, computed before this command adds its own
		// deploy_results rows — see docs/openapi.yaml's
		// estimated_queue_position description.
		queuePos := 0
		for _, id := range agentIDs {
			if n, err := db.CountPendingResultsForAgent(id); err == nil && n > queuePos {
				queuePos = n
			}
		}

		var job *DeployJob
		switch req.Action {
		case "enable_wifi", "disable_wifi", "enable_lan", "disable_lan":
			job, err = createNetworkToggleCommand(db, hub, req.Action, agentIDs, requestedBy)
		case "deploy":
			job, err = createDeployPassthroughCommand(deployer, req.Parameters, agentIDs, requestedBy)
		case "wake":
			job, err = createWakeCommand(db, agentIDs, requestedBy)
		default:
			var jobType, payload, args string
			jobType, payload, args, err = actionToJob(req.Action, req.Parameters)
			if err == nil {
				if verr := validateDeployRequest(jobType, payload, args); verr != nil {
					err = &commandClientError{http.StatusBadRequest, verr.Error()}
				} else {
					job, err = deployer.CreateJob(jobType, payload, args, agentIDs, 0, nil, deployer.DefaultMaxRetry(), requestedBy)
				}
			}
		}
		if err != nil {
			var clientErr *commandClientError
			if errors.As(err, &clientErr) {
				c.JSON(clientErr.Status, gin.H{"error": clientErr.Message})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if err := db.InsertCommandRequest(job.ID, req.Action, req.Target, req.CorrelationID); err != nil {
			slog.Error("insert command_requests failed", "job_id", job.ID, "error", err)
		}
		db.InsertAuditLog("command", req.Target, fmt.Sprintf("action=%s job_id=%s targets=%d", req.Action, job.ID, len(agentIDs)), c.ClientIP())

		c.JSON(http.StatusOK, CommandResponse{
			JobID:                  job.ID,
			Status:                 job.Status,
			CreatedAt:              job.CreatedAt,
			EstimatedQueuePosition: queuePos,
		})
	}
}

func handleGetCommand(db *DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		jobID := c.Param("job_id")
		job, err := db.GetDeployJobByID(jobID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if job == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "command job not found"})
			return
		}
		meta, err := db.GetCommandRequestByJobID(jobID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if meta == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "command job not found"})
			return
		}
		results, err := db.GetDeployResultsByJobID(jobID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		progress := CommandProgress{Total: len(results)}
		var firstError *string
		for _, r := range results {
			switch r.Status {
			case "pending", "deferred":
				progress.Pending++
			case "running":
				progress.Dispatched++
			case "success", "ok":
				progress.Succeeded++
			default:
				progress.Failed++
				if firstError == nil {
					out := r.Output
					firstError = &out
				}
			}
		}

		c.JSON(http.StatusOK, CommandStatus{
			JobID:         job.ID,
			Target:        meta.Target,
			Action:        meta.Action,
			Status:        job.Status,
			CorrelationID: meta.CorrelationID,
			CreatedAt:     job.CreatedAt,
			Progress:      progress,
			Results:       results,
			Error:         firstError,
		})
	}
}

func handleListCommands(db *DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := db.GetRecentCommandsWithStatus()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		summaries := make([]CommandSummary, 0, len(rows))
		for _, r := range rows {
			summaries = append(summaries, CommandSummary{
				JobID: r.JobID, Target: r.Target, Action: r.Action,
				Status: r.Status, CreatedAt: r.CreatedAt,
			})
		}
		c.JSON(http.StatusOK, summaries)
	}
}

// RegisterCommandRoutes wires the generic Command API (POST /api/v1/commands,
// GET /api/v1/commands, GET /api/v1/commands/{job_id}) onto apiV1 — the same
// group server/main.go already uses for GET /api/v1/clients.
func RegisterCommandRoutes(apiV1 *gin.RouterGroup, db *DB, hub *Hub, deployer *Deployer) {
	apiV1.POST("/commands", handlePostCommand(db, hub, deployer))
	apiV1.GET("/commands", handleListCommands(db))
	apiV1.GET("/commands/:job_id", handleGetCommand(db))
}
