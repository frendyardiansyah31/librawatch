package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

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
		output, err := hub.KillProcess(c.Param("id"), req.PID, req.Name)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
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
			MeshID string `json:"mesh_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := db.SetAgentMeshID(c.Param("id"), req.MeshID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
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

	// ── Health ───────────────────────────────────────────────────────────

	api.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"ok":           true,
			"uptime":       time.Since(serverStart).Round(time.Second).String(),
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
			Type    string   `json:"type"`
			Payload string   `json:"payload"`
			Args    string   `json:"args"`
			Targets []string `json:"targets"`
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
		payloadPreview := req.Payload
		if len(payloadPreview) > 80 {
			payloadPreview = payloadPreview[:80] + "…"
		}
		slog.Info("deploy request",
			"source_ip", c.ClientIP(),
			"type", req.Type,
			"payload", payloadPreview,
			"targets", len(req.Targets))
		job, err := deployer.CreateJob(req.Type, req.Payload, req.Args, req.Targets)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
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
		if err := db.CancelDeployJob(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		slog.Info("deploy job cancelled", "job_id", c.Param("id"), "source_ip", c.ClientIP())
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
		dest := filepath.Join(uploadsPath, filename)
		if err := c.SaveUploadedFile(file, dest); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"filename": filename})
	})

	api.GET("/file/:filename", func(c *gin.Context) {
		c.File(uploadsPath + "/" + c.Param("filename"))
	})

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
