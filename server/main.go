package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type rotWriter struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	maxBack int
	file    *os.File
	size    int64
}

func newRotWriter(path string, maxSizeMB int64, maxBackup int) (*rotWriter, error) {
	w := &rotWriter{path: path, maxSize: maxSizeMB * 1024 * 1024, maxBack: maxBackup}
	return w, w.open()
}

func (w *rotWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file, w.size = f, fi.Size()
	return nil
}

func (w *rotWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	if w.file == nil {
		return 0, fmt.Errorf("log file unavailable")
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotWriter) rotate() {
	w.file.Close()
	w.file = nil
	for i := w.maxBack; i >= 1; i-- {
		src := w.path
		if i > 1 {
			src = fmt.Sprintf("%s.%d", w.path, i-1)
		}
		os.Rename(src, fmt.Sprintf("%s.%d", w.path, i))
	}
	_ = w.open()
}

func main() {
	if err := os.MkdirAll("./logs", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "logs dir creation failed: %v\n", err)
	}
	logOut := io.Writer(os.Stdout)
	if rw, err := newRotWriter("./logs/server.log", 10, 3); err == nil {
		logOut = io.MultiWriter(os.Stdout, rw)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logOut, nil)))

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

	alerter := NewAlerter(db)
	hub.alerter = alerter
	go alerter.StartOfflineChecker()

	deployer := NewDeployer(db, hub)
	hub.deployer = deployer

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
	RegisterAPIRoutes(api, db, hub, alerter, deployer, cfg.Uploads.Path, cfg.Uploads.MaxSizeMB)

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
