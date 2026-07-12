package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kardianos/service"
)

const (
	sendInterval   = 30 * time.Second
	initialBackoff = 5 * time.Second
	maxBackoff     = 60 * time.Second
	agentOS        = "Windows 11 Home"
	agentVersion   = "1.1.0"
	writeWait      = 10 * time.Second
)

var (
	agentLogger   *log.Logger
	hostname      string
	agentIP       string
	meshID        string
	serverBaseURL string
)

// ── Service definition ─────────────────────────────────────────────────────

var svcConfig = &service.Config{
	Name:        "LibraryAgent",
	DisplayName: "Library Monitor Agent",
	Description: "UIII Library Monitor — monitoring agent untuk PC perpustakaan",
	Option: service.KeyValue{
		"StartType":              "automatic",
		"OnFailure":              "restart",
		"OnFailureDelayDuration": "5s",
		"OnFailureResetPeriod":   60,
	},
}

type agentProgram struct {
	agentID   string
	serverURL string
	cancel    context.CancelFunc
	done      chan struct{}
}

func (p *agentProgram) Start(_ service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	startEventWatchers(ctx, p.agentID)
	go func() {
		defer close(p.done)
		connectLoop(ctx, p.agentID, p.serverURL)
	}()
	return nil
}

// startEventWatchers launches the Phase 2 System Policy Enforcement
// detectors (Modules 1–5). They run for the lifetime of ctx independently of
// connection state — sendEvent (agent/events.go) silently drops events while
// disconnected, the same at-most-effort behavior sendMetrics already has, so
// there's no need to gate watcher startup on a live connection.
func startEventWatchers(ctx context.Context, agentID string) {
	go startUSBWatch(ctx, agentID)
	go startDownloadWatch(ctx, agentID)
	go startDesktopWatch(ctx, agentID)
	go startConfigWatch(ctx, agentID)
	go startInstallWatch(ctx, agentID)
}

func (p *agentProgram) Stop(_ service.Service) error {
	logMsg("INFO", "Service stop requested")
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		logMsg("WARN", "Graceful stop timed out")
	}
	return nil
}

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

// ── Entry point ────────────────────────────────────────────────────────────

func main() {
	// Handle service control commands first — no expensive setup needed.
	if len(os.Args) > 1 {
		prg := &agentProgram{}
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

	initLogger()

	var err error
	hostname, err = os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	agentIP = getLocalIP()
	meshID = loadMeshID()

	agentID, err := loadOrCreateID()
	if err != nil {
		logMsg("ERROR", "Failed to get agent ID: %v", err)
		os.Exit(1)
	}

	serverURL := getServerURL()
	serverBaseURL = strings.Replace(serverURL, "ws://", "http://", 1)
	serverBaseURL = strings.Replace(serverBaseURL, "wss://", "https://", 1)
	serverBaseURL = strings.TrimSuffix(serverBaseURL, "/ws")

	if token := loadToken(); token != "" {
		if strings.Contains(serverURL, "?") {
			serverURL = serverURL + "&token=" + token
		} else {
			serverURL = serverURL + "?token=" + token
		}
		logMsg("INFO", "Auth token loaded from token.txt")
	}

	logMsg("INFO", "Agent started, ID: %s, Server: %s, Hostname: %s", agentID, serverURL, hostname)

	prg := &agentProgram{agentID: agentID, serverURL: serverURL}
	svc, err := service.New(prg, svcConfig)
	if err != nil {
		logMsg("ERROR", "Service create: %v", err)
		os.Exit(1)
	}

	// Try to run under the Windows Service Control Manager.
	// If the binary was started directly (not by SCM), svc.Run() returns
	// an error — fall back to running in the foreground so the binary
	// remains useful for debugging / manual testing.
	if err := svc.Run(); err != nil {
		logMsg("INFO", "Not started by SCM, running directly: %v", err)
		ctx, cancel := context.WithCancel(context.Background())

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

		startEventWatchers(ctx, agentID)
		go connectLoop(ctx, agentID, serverURL)

		<-sigCh
		logMsg("INFO", "Shutdown signal, stopping…")
		cancel()
	}
}

func initLogger() {
	_ = os.MkdirAll(agentBaseDir, 0755)
	rw, err := newRotWriter(agentLogFile, 5, 1)
	if err != nil {
		agentLogger = log.New(io.Discard, "", 0)
		return
	}
	agentLogger = log.New(rw, "", 0)
}

func logMsg(level, format string, args ...interface{}) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	agentLogger.Printf(ts+" ["+level+"] "+format, args...)
}

// ── Connection loop ────────────────────────────────────────────────────────

func connectLoop(ctx context.Context, agentID, serverURL string) {
	backoff := initialBackoff
	attempt := 0

	for {
		// Exit immediately if context is cancelled.
		select {
		case <-ctx.Done():
			return
		default:
		}

		attempt++
		conn, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
		if err != nil {
			jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
			wait := backoff + jitter
			logMsg("INFO", "Connect failed (attempt %d): %v, retry in %v", attempt, err, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		logMsg("INFO", "Connected to server")
		backoff = initialBackoff
		attempt = 0

		if err := runSession(ctx, conn, agentID); err != nil {
			if ctx.Err() != nil {
				conn.Close()
				return
			}
			logMsg("ERROR", "Session error: %v", err)
		}
		conn.Close()

		if ctx.Err() != nil {
			return
		}
		logMsg("INFO", "Disconnected, reconnecting in %v", initialBackoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialBackoff):
		}
	}
}

// ── Outbound message queue ──────────────────────────────────────────────────
//
// gorilla/websocket does not allow concurrent writers on one connection.
// Before Phase 2 this was a latent bug — executor.go/deepfreeze.go/ssh.go all
// called conn.WriteMessage directly from their own goroutines — that Phase 2's
// event watchers (usbwatch.go, downloadwatch.go, etc.) would trigger far more
// often. Fixed the same way server/hub.go already solves this exact problem
// on the server side: a single writer goroutine owns the connection, fed by a
// buffered channel every other goroutine pushes onto instead.

var (
	wsSendMu sync.RWMutex
	wsSendCh chan []byte
)

func setSendChannel(ch chan []byte) {
	wsSendMu.Lock()
	wsSendCh = ch
	wsSendMu.Unlock()
}

// wsSend queues data for the writer goroutine. Non-blocking: if the buffer is
// full or there's no active connection, the message is dropped rather than
// stalling the caller — mirrors server/hub.go's SendToAgent, which makes the
// same trade-off.
func wsSend(data []byte) {
	wsSendMu.RLock()
	ch := wsSendCh
	wsSendMu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- data:
	default:
		logMsg("WARN", "ws send buffer full, dropping message")
	}
}

func runSession(ctx context.Context, conn *websocket.Conn, agentID string) error {
	sendCh := make(chan []byte, 256)
	setSendChannel(sendCh)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for data := range sendCh {
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}()
	defer func() {
		setSendChannel(nil)
		close(sendCh)
		<-writerDone
	}()

	if err := sendMetrics(agentID); err != nil {
		return fmt.Errorf("initial metrics: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				done <- err
				return
			}
			handleServerMessage(agentID, data)
		}
	}()

	ticker := time.NewTicker(sendInterval)
	defer ticker.Stop()
	lastMetricLog := time.Time{}

	for {
		select {
		case <-ctx.Done():
			// Graceful close: tell the server we're shutting down. Sent
			// directly (bypassing the channel) since we're tearing down the
			// writer goroutine right after anyway.
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "agent stopping"))
			return nil
		case err := <-done:
			return err
		case <-ticker.C:
			if err := sendMetrics(agentID); err != nil {
				return fmt.Errorf("send metrics: %w", err)
			}
			if time.Since(lastMetricLog) >= 5*time.Minute {
				logMsg("INFO", "Metrics sent: hostname=%s", hostname)
				lastMetricLog = time.Now()
			}
		}
	}
}

func sendMetrics(agentID string) error {
	if current := getLocalIP(); current != "unknown" {
		agentIP = current
	}
	payload, err := collectMetrics(agentID, hostname, agentIP, agentOS, meshID)
	if err != nil {
		logMsg("ERROR", "Collect metrics: %v", err)
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	wsSend(data)
	return nil
}

func handleServerMessage(agentID string, data []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		logMsg("ERROR", "Parse server message: %v", err)
		return
	}
	msgType, _ := msg["type"].(string)
	jobID, _ := msg["job_id"].(string)
	logMsg("INFO", "Command received: type=%s job_id=%s", msgType, jobID)

	switch msgType {
	case "exec":
		go executeCommand(agentID, msg)
	case "file_deploy":
		go deployFile(agentID, msg)
	case "kill_process":
		go handleKillProcess(agentID, msg)
	case "get_logs":
		go sendLogLines(agentID, msg)
	case "deepfreeze":
		go handleDeepFreeze(agentID, msg)
	case "install_ssh":
		go handleInstallSSH(agentID, msg)
	case "delete_file":
		go handleDeleteFile(agentID, msg)
	}
}

func handleKillProcess(agentID string, msg map[string]interface{}) {
	pid := int(floatVal(msg["pid"]))
	name, _ := msg["proc_name"].(string)

	var cmd *exec.Cmd
	if pid > 0 {
		cmd = exec.Command("taskkill", "/F", "/PID", fmt.Sprintf("%d", pid))
	} else {
		cmd = exec.Command("taskkill", "/F", "/IM", name)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, _ := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if output == "" {
		output = fmt.Sprintf("kill PID %d attempted", pid)
	}

	resp := map[string]interface{}{
		"type":     "kill_result",
		"agent_id": agentID,
		"output":   output,
	}
	data, _ := json.Marshal(resp)
	wsSend(data)
	logMsg("INFO", "kill_process pid=%d name=%s output=%s", pid, name, output)
}

func floatVal(v interface{}) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func scanInterfaces() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip.String()
			}
		}
	}
	return ""
}

func getLocalIP() string {
	for i := 0; i < 3; i++ {
		conn, err := net.Dial("udp4", "8.8.8.8:80")
		if err == nil {
			ip := conn.LocalAddr().(*net.UDPAddr).IP.String()
			conn.Close()
			if ip != "" && ip != "<nil>" {
				return ip
			}
		}
		if ip := scanInterfaces(); ip != "" {
			return ip
		}
		if i < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	return "unknown"
}

func sendLogLines(agentID string, msg map[string]interface{}) {
	lines := 50
	if v, ok := msg["lines"].(float64); ok && v > 0 {
		lines = int(v)
	}
	data, err := os.ReadFile(agentLogFile)
	var output string
	if err != nil {
		output = fmt.Sprintf("error reading log: %v", err)
	} else {
		parts := strings.Split(string(data), "\n")
		if len(parts) > lines {
			parts = parts[len(parts)-lines:]
		}
		output = strings.Join(parts, "\n")
	}
	resp, _ := json.Marshal(map[string]interface{}{
		"type":     "log_result",
		"agent_id": agentID,
		"output":   output,
	})
	wsSend(resp)
}

// handleDeleteFile removes a file the Download Policy (Module 2) decided to
// delete. Mirrors handleKillProcess's request/response shape exactly — same
// fire-and-wait pattern the server already uses for kill_process.
func handleDeleteFile(agentID string, msg map[string]interface{}) {
	path, _ := msg["path"].(string)
	var output string
	if path == "" {
		output = "delete_file: path kosong"
	} else if err := os.Remove(path); err != nil {
		output = fmt.Sprintf("gagal hapus %s: %v", path, err)
	} else {
		output = fmt.Sprintf("berhasil hapus %s", path)
	}

	resp, _ := json.Marshal(map[string]interface{}{
		"type":     "delete_file_result",
		"agent_id": agentID,
		"output":   output,
	})
	wsSend(resp)
	logMsg("INFO", "delete_file path=%s output=%s", path, output)
}
