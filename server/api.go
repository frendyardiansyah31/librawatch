package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var allowedUploadExts = map[string]bool{
	".exe": true,
	".msi": true,
	".bat": true,
	".ps1": true,
}

var validAppStatuses = map[string]bool{
	AppStatusPendingReview: true,
	AppStatusAllowed:       true,
	AppStatusBlocked:       true,
	AppStatusIgnored:       true,
}

var validPolicyActions = map[string]bool{
	PolicyActionLog:    true,
	PolicyActionNotify: true,
	PolicyActionBlock:  true,
	PolicyActionDelete: true,
	PolicyActionKill:   true,
}

// wingetIDRe matches valid winget package IDs: Publisher.AppName style.
// Allows letters, digits, dots, dashes, underscores, plus signs.
var wingetIDRe = regexp.MustCompile(`^[A-Za-z0-9][\w.\-+]*$`)

// validateDeployRequest checks that the payload is safe for the given deploy type.
func validateDeployRequest(typ, payload, args string) error {
	switch typ {
	case "exec":
		if len(payload) > 8192 {
			return fmt.Errorf("payload exceeds maximum length of 8192 characters")
		}
		if strings.ContainsRune(payload, 0) {
			return fmt.Errorf("payload contains invalid characters")
		}

	case "winget":
		// Expected format: "winget install --id <ID> ..." or "winget uninstall --id <ID> ..."
		fields := strings.Fields(payload)
		// fields[0]=winget fields[1]=install|uninstall fields[2]=--id fields[3]=<ID>
		if len(fields) < 4 || fields[0] != "winget" ||
			(fields[1] != "install" && fields[1] != "uninstall") ||
			fields[2] != "--id" {
			return fmt.Errorf("invalid winget command format")
		}
		if !wingetIDRe.MatchString(fields[3]) {
			return fmt.Errorf("invalid winget package ID: only letters, digits, dots, dashes, underscores and plus signs allowed")
		}

	case "file_deploy":
		if len(args) > 512 {
			return fmt.Errorf("args exceeds maximum length of 512 characters")
		}
		if strings.ContainsRune(args, 0) {
			return fmt.Errorf("args contains invalid characters")
		}

	case "deepfreeze":
		allowed := map[string]bool{"thaw": true, "freeze": true, "query_df": true}
		if !allowed[payload] {
			return fmt.Errorf("invalid deepfreeze action: must be thaw, freeze, or query_df")
		}

	case "install_ssh":
		// No user-controlled payload.

	default:
		return fmt.Errorf("unknown deploy type: %s", typ)
	}
	return nil
}

func RegisterAPIRoutes(api *gin.RouterGroup, db *DB, hub *Hub, alerter *Alerter, deployer *Deployer, uploadsPath string, maxUploadMB int64) {
	// ── Agents ──────────────────────────────────────────────────────────

	api.GET("/agents", func(c *gin.Context) {
		agents, err := db.GetAllAgents()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, agents)
	})

	api.GET("/agents/:id", func(c *gin.Context) {
		agent, err := db.GetAgentByID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if agent == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "agent not found"})
			return
		}
		c.JSON(http.StatusOK, agent)
	})

	api.GET("/agents/:id/metrics", func(c *gin.Context) {
		metrics, err := db.GetMetrics24h(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if metrics == nil {
			metrics = []Metric{}
		}
		c.JSON(http.StatusOK, metrics)
	})

	api.POST("/agents/:id/kill", func(c *gin.Context) {
		var req struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || (req.PID == 0 && req.Name == "") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "pid or name required"})
			return
		}
		agentID := c.Param("id")
		output, err := hub.KillProcess(agentID, req.PID, req.Name)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		db.InsertAuditLog("kill_process", agentID, fmt.Sprintf("pid=%d name=%s", req.PID, req.Name), c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"output": output})
	})

	api.GET("/agents/:id/processes", func(c *gin.Context) {
		procs, err := db.GetProcesses(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if procs == nil {
			procs = []Process{}
		}
		c.JSON(http.StatusOK, procs)
	})

	api.PATCH("/agents/:id", func(c *gin.Context) {
		var req struct {
			MeshID      *string `json:"mesh_id"`
			DeviceGroup *string `json:"device_group"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		agentID := c.Param("id")
		if req.MeshID != nil {
			if err := db.SetAgentMeshID(agentID, *req.MeshID); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		if req.DeviceGroup != nil {
			if err := db.SetAgentDeviceGroup(agentID, *req.DeviceGroup); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			db.InsertAuditLog("update_device_group", agentID, *req.DeviceGroup, c.ClientIP())
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	api.DELETE("/agents/:id", func(c *gin.Context) {
		agentID := c.Param("id")
		if err := db.DeleteAgent(agentID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		db.InsertAuditLog("delete_agent", agentID, "", c.ClientIP())
		slog.Info("agent deleted", "agent_id", agentID, "ip", c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// ── Alerts ──────────────────────────────────────────────────────────

	api.GET("/alerts", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
		if limit <= 0 || limit > 1000 {
			limit = 100
		}
		alerts, err := db.GetRecentAlerts(limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, alerts)
	})

	// ── Applications / Categories ──────────────────────────────────────

	api.GET("/applications", func(c *gin.Context) {
		status := c.Query("status")
		if status != "" && !validAppStatuses[status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status filter"})
			return
		}
		var categoryID int64
		if v := c.Query("category_id"); v != "" {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid category_id"})
				return
			}
			categoryID = id
		}
		apps, err := db.GetApplications(status, categoryID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, apps)
	})

	api.GET("/applications/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid application id"})
			return
		}
		app, err := db.GetApplicationByID(id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if app == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "application not found"})
			return
		}
		c.JSON(http.StatusOK, app)
	})

	api.PATCH("/applications/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid application id"})
			return
		}
		var req struct {
			Status     string `json:"status"`
			CategoryID *int64 `json:"category_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if !validAppStatuses[req.Status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "status must be one of pending_review, allowed, blocked, ignored"})
			return
		}
		if err := db.UpdateApplicationStatus(id, req.Status, req.CategoryID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		db.InsertAuditLog("update_application", strconv.FormatInt(id, 10), fmt.Sprintf("status=%s", req.Status), c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	api.GET("/categories", func(c *gin.Context) {
		categories, err := db.GetAllCategories()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, categories)
	})

	// ── Events (Phase 2 — Module 7 Event Timeline) ─────────────────────────

	api.GET("/events", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
		events, err := db.GetEvents(c.Query("agent_id"), c.Query("type"), limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, events)
	})

	api.GET("/agents/:id/events", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		events, err := db.GetEvents(c.Param("id"), c.Query("type"), limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, events)
	})

	// ── Policy Rules (Phase 2 — Module 8 Policy Engine) ────────────────────

	api.GET("/policy-rules", func(c *gin.Context) {
		rules, err := db.GetAllPolicyRules()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rules)
	})

	api.POST("/policy-rules", func(c *gin.Context) {
		var rule PolicyRule
		if err := c.ShouldBindJSON(&rule); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if rule.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
			return
		}
		if !validPolicyActions[rule.Action] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "action must be one of log, notify, block, delete, kill"})
			return
		}
		id, err := db.InsertPolicyRule(&rule)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		db.InsertAuditLog("create_policy_rule", strconv.FormatInt(id, 10), rule.Name, c.ClientIP())
		rule.ID = id
		c.JSON(http.StatusOK, rule)
	})

	api.PATCH("/policy-rules/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid rule id"})
			return
		}
		var rule PolicyRule
		if err := c.ShouldBindJSON(&rule); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if rule.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
			return
		}
		if !validPolicyActions[rule.Action] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "action must be one of log, notify, block, delete, kill"})
			return
		}
		if err := db.UpdatePolicyRule(id, &rule); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		db.InsertAuditLog("update_policy_rule", strconv.FormatInt(id, 10), rule.Name, c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	api.DELETE("/policy-rules/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid rule id"})
			return
		}
		if err := db.DeletePolicyRule(id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		db.InsertAuditLog("delete_policy_rule", strconv.FormatInt(id, 10), "", c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// ── Settings ─────────────────────────────────────────────────────────

	api.GET("/settings", func(c *gin.Context) {
		settings, err := db.GetAllSettings()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, settings)
	})

	api.POST("/settings", func(c *gin.Context) {
		var req map[string]interface{}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		for k, v := range req {
			var val string
			switch tv := v.(type) {
			case string:
				val = tv
			case []interface{}:
				b, _ := json.Marshal(tv)
				val = string(b)
			default:
				val = fmt.Sprintf("%v", tv)
			}
			if err := db.SetSetting(k, val); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	api.GET("/audit", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
		logs, err := db.GetAuditLogs(limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if logs == nil {
			logs = []AuditLog{}
		}
		c.JSON(http.StatusOK, logs)
	})

	// ── Health ───────────────────────────────────────────────────────────

	api.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"ok":            true,
			"uptime":        time.Since(serverStart).Round(time.Second).String(),
			"agents_online": hub.OnlineCount(),
		})
	})

	// ── Stats ────────────────────────────────────────────────────────────

	api.GET("/stats", func(c *gin.Context) {
		todayAlerts, _ := db.GetAlertsTodayCount()
		c.JSON(http.StatusOK, gin.H{
			"online":       hub.OnlineCount(),
			"today_alerts": todayAlerts,
		})
	})

	// ── Deploy ───────────────────────────────────────────────────────────

	api.POST("/deploy", func(c *gin.Context) {
		var req struct {
			Type     string   `json:"type"`
			Payload  string   `json:"payload"`
			Args     string   `json:"args"`
			Targets  []string `json:"targets"`
			Priority int      `json:"priority"`
			ExpireAt string   `json:"expire_at"`
			MaxRetry *int     `json:"max_retry"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.Type == "" || len(req.Targets) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "type and targets are required"})
			return
		}
		if req.Type != "install_ssh" && req.Payload == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "payload is required for type " + req.Type})
			return
		}
		if err := validateDeployRequest(req.Type, req.Payload, req.Args); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		var expireAt *time.Time
		if req.ExpireAt != "" {
			t, err := time.Parse(time.RFC3339, req.ExpireAt)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "expire_at must be RFC3339"})
				return
			}
			if t.Before(nowWIB()) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "expire_at must be in the future"})
				return
			}
			expireAt = &t
		}
		maxRetry := deployer.DefaultMaxRetry()
		if req.MaxRetry != nil {
			maxRetry = *req.MaxRetry
		}
		payloadPreview := req.Payload
		if len(payloadPreview) > 80 {
			payloadPreview = payloadPreview[:80] + "…"
		}
		slog.Info("deploy request",
			"source_ip", c.ClientIP(),
			"type", req.Type,
			"payload", payloadPreview,
			"targets", len(req.Targets))
		job, err := deployer.CreateJob(req.Type, req.Payload, req.Args, req.Targets,
			req.Priority, expireAt, maxRetry, "system")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		db.InsertAuditLog("deploy", strings.Join(req.Targets, ","),
			fmt.Sprintf("type=%s payload=%s priority=%d max_retry=%d", req.Type, payloadPreview, req.Priority, maxRetry),
			c.ClientIP())
		c.JSON(http.StatusOK, job)
	})

	api.GET("/deploy", func(c *gin.Context) {
		jobs, err := db.GetAllDeployJobs()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, jobs)
	})

	api.DELETE("/deploy/:id", func(c *gin.Context) {
		jobID := c.Param("id")
		if err := db.CancelDeployJob(jobID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		slog.Info("deploy job cancelled", "job_id", jobID, "source_ip", c.ClientIP())
		db.InsertAuditLog("cancel_deploy", jobID, "", c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	api.GET("/deploy/:id", func(c *gin.Context) {
		job, err := db.GetDeployJobByID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if job == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
			return
		}
		results, err := db.GetDeployResultsByJobID(job.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"job": job, "results": results})
	})

	api.POST("/upload", func(c *gin.Context) {
		file, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if maxUploadMB > 0 && file.Size > maxUploadMB*1024*1024 {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds size limit"})
			return
		}
		filename := filepath.Base(file.Filename)
		if filename == "." || filename == string(filepath.Separator) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
			return
		}
		ext := strings.ToLower(filepath.Ext(filename))
		if !allowedUploadExts[ext] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file type not allowed (.exe .msi .bat .ps1 only)"})
			return
		}
		dest := filepath.Join(uploadsPath, filename)
		if err := c.SaveUploadedFile(file, dest); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		db.InsertAuditLog("upload", filename, fmt.Sprintf("size=%d", file.Size), c.ClientIP())
		c.JSON(http.StatusOK, gin.H{"filename": filename})
	})

	// GET /api/file/:filename is registered publicly in main.go so agents can
	// download files without a dashboard session token. See publicFileHandler.

	// ── Test notifications ────────────────────────────────────────────────

	api.POST("/test/telegram", func(c *gin.Context) {
		if err := alerter.SendTestTelegram(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	api.POST("/test/email", func(c *gin.Context) {
		if err := alerter.SendTestEmail(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// ── Logs ─────────────────────────────────────────────────────────────
	// Full implementation in Milestone 6.

	api.GET("/logs", func(c *gin.Context) {
		lines, _ := strconv.Atoi(c.DefaultQuery("lines", "100"))
		if lines <= 0 || lines > 10000 {
			lines = 100
		}
		output, err := tailFile("./logs/server.log", lines)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"lines": output})
	})

	api.GET("/agents/:id/logs", func(c *gin.Context) {
		lines, _ := strconv.Atoi(c.DefaultQuery("lines", "50"))
		if lines <= 0 || lines > 1000 {
			lines = 50
		}
		output, err := hub.RequestAgentLogs(c.Param("id"), lines)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"lines": output})
	})
}

func tailFile(path string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// publicFileHandler serves uploaded files without requiring a dashboard session
// token, so agents can download files (e.g. agent.exe update) via HTTP.
// Path-traversal protection is still enforced.
func publicFileHandler(uploadsPath string) gin.HandlerFunc {
	return func(c *gin.Context) {
		safe := filepath.Base(filepath.Clean(c.Param("filename")))
		if safe == "." || safe == ".." {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
			return
		}
		dest := filepath.Join(uploadsPath, safe)
		uploadsAbs, _ := filepath.Abs(uploadsPath)
		destAbs, _ := filepath.Abs(dest)
		if !strings.HasPrefix(destAbs, uploadsAbs+string(filepath.Separator)) {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}
		c.File(dest)
	}
}
