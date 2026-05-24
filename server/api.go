package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func RegisterAPIRoutes(api *gin.RouterGroup, db *DB, hub *Hub, uploadsPath string) {
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

	// ── Stats ────────────────────────────────────────────────────────────

	api.GET("/stats", func(c *gin.Context) {
		todayAlerts, _ := db.GetAlertsTodayCount()
		c.JSON(http.StatusOK, gin.H{
			"online":       hub.OnlineCount(),
			"today_alerts": todayAlerts,
		})
	})

	// ── Deploy ───────────────────────────────────────────────────────────
	// Full implementation in Milestone 4; stubs return 501 until then.

	api.POST("/deploy", func(c *gin.Context) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "deploy available in Milestone 4"})
	})

	api.GET("/deploy", func(c *gin.Context) {
		jobs, err := db.GetAllDeployJobs()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, jobs)
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
		c.JSON(http.StatusNotImplemented, gin.H{"error": "upload available in Milestone 4"})
	})

	api.GET("/file/:filename", func(c *gin.Context) {
		c.File(uploadsPath + "/" + c.Param("filename"))
	})

	// ── Logs ─────────────────────────────────────────────────────────────
	// Full implementation in Milestone 6.

	api.GET("/logs", func(c *gin.Context) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "logs available in Milestone 6"})
	})

	api.GET("/agents/:id/logs", func(c *gin.Context) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "agent logs available in Milestone 6"})
	})
}
