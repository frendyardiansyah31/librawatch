package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func main() {
	cfg, err := loadConfig("config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	db, err := initDB(cfg.Database.Path)
	if err != nil {
		slog.Error("database init failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := os.MkdirAll(cfg.Uploads.Path, 0755); err != nil {
		slog.Error("uploads dir creation failed", "error", err)
		os.Exit(1)
	}

	if err := db.InitDefaultSettings(cfg); err != nil {
		slog.Error("settings init failed", "error", err)
		os.Exit(1)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/", func(c *gin.Context) {
		c.File("./dashboard/index.html")
	})
	r.Static("/static", "./dashboard")

	api := r.Group("/api")
	{
		api.GET("/agents", func(c *gin.Context) {
			agents, err := db.GetAllAgents()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, agents)
		})
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	slog.Info("Library Monitor started", "address", addr)
	if err := r.Run(addr); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
