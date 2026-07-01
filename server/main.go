package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kardianos/service"
	"golang.org/x/crypto/bcrypt"
)

// ── Log rotation ───────────────────────────────────────────────────────────

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

// ── Service definition ─────────────────────────────────────────────────────

var svcConfig = &service.Config{
	Name:        "LibraryMonitor",
	DisplayName: "Library Monitor Server",
	Description: "UIII Library Monitor — server monitoring untuk 60 PC perpustakaan",
	Option: service.KeyValue{
		"StartType":              "automatic",
		"OnFailure":              "restart",
		"OnFailureDelayDuration": "5s",
		"OnFailureResetPeriod":   60,
	},
}

type serverProgram struct {
	srv *http.Server
}

func (p *serverProgram) Start(_ service.Service) error {
	go func() {
		if err := p.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()
	return nil
}

func (p *serverProgram) Stop(_ service.Service) error {
	slog.Info("Service stop requested, shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return p.srv.Shutdown(ctx)
}

// ── Admin IP whitelist middleware ──────────────────────────────────────────

func adminIPWhitelist(cidrs []string) gin.HandlerFunc {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
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

// ── Entry point ────────────────────────────────────────────────────────────

func main() {
	// When running as a Windows Service the working directory is typically
	// C:\Windows\System32. Change to the executable's directory so all
	// relative paths in config.yaml (data/, logs/, dashboard/) work correctly.
	if exePath, err := os.Executable(); err == nil {
		_ = os.Chdir(filepath.Dir(exePath))
	}

	// Handle subcommands first — skip expensive startup.
	if len(os.Args) > 1 {
		if os.Args[1] == "hash-password" {
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: library-server hash-password <plaintext>")
				os.Exit(1)
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(os.Args[2]), bcrypt.DefaultCost)
			if err != nil {
				fmt.Fprintf(os.Stderr, "hash error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(string(hash))
			return
		}
		prg := &serverProgram{}
		svc, err := service.New(prg, svcConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "service create: %v\n", err)
			os.Exit(1)
		}
		if err := service.Control(svc, os.Args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "service %s: %v\n", os.Args[1], err)
			os.Exit(1)
		}
		return
	}

	// ── Logging setup ──────────────────────────────────────────────────────
	if err := os.MkdirAll("./logs", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "logs dir creation failed: %v\n", err)
	}
	logOut := io.Writer(os.Stdout)
	if rw, err := newRotWriter("./logs/server.log", 10, 3); err == nil {
		logOut = io.MultiWriter(os.Stdout, rw)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logOut, nil)))

	// ── Config & DB ────────────────────────────────────────────────────────
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

	// ── Auth / Hub / Alerter / Deployer ───────────────────────────────────
	authMgr := NewAuthManager(cfg.Auth.AdminUsername, cfg.Auth.AdminPassword)
	if cfg.Auth.AdminUsername != "" {
		slog.Info("admin auth enabled", "username", cfg.Auth.AdminUsername)
	}

	hub := NewHub(db)
	hub.batcher = NewMetricsBatcher(db)
	hub.authToken = cfg.Auth.Token
	if hub.authToken != "" {
		slog.Info("WebSocket auth enabled")
	}

	alerter := NewAlerter(db)
	hub.alerter = alerter
	go alerter.StartOfflineChecker()

	deployer := NewDeployer(db, hub)
	hub.deployer = deployer

	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := db.PurgeOldMetrics(); err != nil {
				slog.Error("metrics purge failed", "error", err)
			}
		}
	}()

	// ── Router ─────────────────────────────────────────────────────────────
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// /ws is NOT behind the admin whitelist — all 60 agent PCs must connect.
	r.GET("/ws", func(c *gin.Context) {
		ServeWS(hub, c.Writer, c.Request)
	})

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

	loginLimiter := NewLoginRateLimiter(rateLimitMaxAttempts, rateLimitWindow)

	// /api/login is public — must be registered before the protected group.
	r.POST("/api/login", handleLogin(authMgr, db, loginLimiter))

	api := r.Group("/api", adminMiddleware, authMgr.Middleware())
	api.POST("/logout", handleLogout(authMgr, db))
	RegisterAPIRoutes(api, db, hub, alerter, deployer, cfg.Uploads.Path, cfg.Uploads.MaxSizeMB)

	// ── HTTP server ────────────────────────────────────────────────────────
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	prg := &serverProgram{srv: srv}
	svc, err := service.New(prg, svcConfig)
	if err != nil {
		slog.Error("service create", "error", err)
		os.Exit(1)
	}

	slog.Info("Library Monitor started", "address", addr)

	// Try to run under the Windows Service Control Manager.
	// Falls back to foreground run when started directly (dev/debug).
	if err := svc.Run(); err != nil {
		slog.Info("Not started by SCM, running directly", "reason", err)

		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("server error", "error", err)
				os.Exit(1)
			}
		}()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh

		slog.Info("Shutdown signal, stopping…")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}
