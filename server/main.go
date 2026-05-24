package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

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

	hub := NewHub(db)

	// Purge metrics older than 24h every hour
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := db.PurgeOldMetrics(); err != nil {
				slog.Error("metrics purge failed", "error", err)
			}
		}
	}()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/", func(c *gin.Context) {
		c.File("./dashboard/index.html")
	})
	r.Static("/static", "./dashboard")

	r.GET("/ws", func(c *gin.Context) {
		ServeWS(hub, c.Writer, c.Request)
	})

	api := r.Group("/api")
	RegisterAPIRoutes(api, db, hub, cfg.Uploads.Path)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	slog.Info("Library Monitor started", "address", addr)

	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
