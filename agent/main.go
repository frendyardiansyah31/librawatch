package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	sendInterval   = 30 * time.Second
	initialBackoff = 5 * time.Second
	maxBackoff     = 60 * time.Second
	agentOS        = "Windows 11 Home"
)

var (
	agentLogger   *log.Logger
	hostname      string
	agentIP       string
	meshID        string
	serverBaseURL string // HTTP base URL derived from WebSocket URL, used for file downloads
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
	// Derive base HTTP URL before appending token query param
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

	connectLoop(agentID, serverURL)
}

func initLogger() {
	_ = os.MkdirAll(agentBaseDir, 0755)
	// Agent runs as windowsgui (no console). Writing to os.Stdout fails silently
	// under Task Scheduler SYSTEM, which causes io.MultiWriter to skip the file
	// writer. Log to file only.
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

func connectLoop(agentID, serverURL string) {
	backoff := initialBackoff
	attempt := 0

	for {
		attempt++
		conn, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
		if err != nil {
			logMsg("INFO", "Connect failed (attempt %d): %v, retry in %v", attempt, err, backoff)
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		logMsg("INFO", "Connected to server")
		backoff = initialBackoff
		attempt = 0

		if err := runSession(conn, agentID); err != nil {
			logMsg("ERROR", "Session error: %v", err)
		}
		conn.Close()
		logMsg("INFO", "Disconnected, reconnecting in %v", initialBackoff)
		time.Sleep(initialBackoff)
	}
}

func runSession(conn *websocket.Conn, agentID string) error {
	// Send metrics immediately on connect
	if err := sendMetrics(conn, agentID); err != nil {
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
			handleServerMessage(conn, agentID, data)
		}
	}()

	ticker := time.NewTicker(sendInterval)
	defer ticker.Stop()
	lastMetricLog := time.Time{}

	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			if err := sendMetrics(conn, agentID); err != nil {
				return fmt.Errorf("send metrics: %w", err)
			}
			if time.Since(lastMetricLog) >= 5*time.Minute {
				logMsg("INFO", "Metrics sent: hostname=%s", hostname)
				lastMetricLog = time.Now()
			}
		}
	}
}

func sendMetrics(conn *websocket.Conn, agentID string) error {
	payload, err := collectMetrics(agentID, hostname, agentIP, agentOS, meshID)
	if err != nil {
		logMsg("ERROR", "Collect metrics: %v", err)
		return nil // don't disconnect on transient collection error
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func handleServerMessage(conn *websocket.Conn, agentID string, data []byte) {
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
		go executeCommand(conn, agentID, msg)
	case "file_deploy":
		go deployFile(conn, agentID, msg)
	case "get_logs":
		go sendLogLines(conn, agentID, msg)
	case "deepfreeze":
		go handleDeepFreeze(conn, agentID, msg)
	case "install_ssh":
		go handleInstallSSH(conn, agentID, msg)
	}
}

func getLocalIP() string {
	// UDP dial: no packet sent, but OS resolves the routing table and returns
	// the local address that would be used for outbound traffic (default route).
	// This correctly handles multiple NICs, Ethernet 1/2/3, WiFi, etc.
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		if ip := conn.LocalAddr().(*net.UDPAddr).IP.String(); ip != "" {
			return ip
		}
	}

	// Fallback: scan interfaces, skip loopback and link-local (169.254.x.x)
	ifaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
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
	return "unknown"
}

func sendLogLines(conn *websocket.Conn, agentID string, msg map[string]interface{}) {
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
	conn.WriteMessage(websocket.TextMessage, resp)
}
