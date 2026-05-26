package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
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

var serverStart = time.Now()

// adminIPWhitelist returns a Gin middleware that allows only requests from
// the given CIDR ranges. Intended for dashboard and API routes only.
func adminIPWhitelist(cidrs []string) gin.HandlerFunc {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		// Accept plain IPs without prefix length (treat as /32 or /128)
		if !strings.Contains(cidr, "/") {
			cidr += "/32"
		}
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			slog.Warn("admin_cidrs: invalid CIDR, skipping", "cidr", cidr, "error", err)
			continue
		}
		nets = append(nets, ipnet)
	}
	return func(c *gin.Context) {
		if len(nets) == 0 {
			c.Next()
			return
		}
		ip := net.ParseIP(c.ClientIP())
		if ip == nil {
			slog.Warn("admin whitelist: cannot parse client IP", "raw", c.ClientIP())
			c.AbortWithStatus(403)
			return
		}
		for _, n := range nets {
			if n.Contains(ip) {
				c.Next()
				return
			}
		}
		slog.Warn("admin whitelist: access denied", "ip", ip)
		c.AbortWithStatus(403)
	}
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
	hub.authToken = cfg.Auth.Token
	if hub.authToken != "" {
		slog.Info("WebSocket auth enabled")
	}

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

	// /ws is NOT behind the admin whitelist — agents from all 60 PCs must connect
	r.GET("/ws", func(c *gin.Context) {
		ServeWS(hub, c.Writer, c.Request)
	})

	// Dashboard and API are behind the admin IP whitelist (if configured)
	var adminMiddleware gin.HandlerFunc
	if len(cfg.Auth.AdminCIDRs) > 0 {
		adminMiddleware = adminIPWhitelist(cfg.Auth.AdminCIDRs)
		slog.Info("admin IP whitelist enabled", "cidrs", cfg.Auth.AdminCIDRs)
	} else {
		adminMiddleware = func(c *gin.Context) { c.Next() }
	}

	r.GET("/", adminMiddleware, func(c *gin.Context) {
		c.File("./dashboard/index.html")
	})
	r.Static("/static", "./dashboard")

	api := r.Group("/api", adminMiddleware)
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
