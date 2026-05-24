package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
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
	agentLogger *log.Logger
	hostname    string
	agentIP     string
	meshID      string
)

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
	logMsg("INFO", "Agent started, ID: %s, Server: %s, Hostname: %s", agentID, serverURL, hostname)

	connectLoop(agentID, serverURL)
}

func initLogger() {
	_ = os.MkdirAll(agentBaseDir, 0755)
	f, err := os.OpenFile(agentLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		agentLogger = log.New(os.Stdout, "", 0)
		return
	}
	agentLogger = log.New(io.MultiWriter(os.Stdout, f), "", 0)
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
		// Milestone 4: executeCommand(conn, agentID, msg)
	case "file_deploy":
		// Milestone 4: deployFile(conn, agentID, msg)
	case "get_logs":
		// Milestone 6: sendLogLines(conn, agentID, msg)
	}
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "unknown"
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "unknown"
}
