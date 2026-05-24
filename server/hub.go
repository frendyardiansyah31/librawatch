package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 54 * time.Second
	maxMsgSize = 2 * 1024 * 1024 // 2MB
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// IncomingMessage covers all agent→server message types.
type IncomingMessage struct {
	Type      string    `json:"type"`
	AgentID   string    `json:"agent_id"`
	Hostname  string    `json:"hostname"`
	IP        string    `json:"ip"`
	OS        string    `json:"os"`
	CPU       float64   `json:"cpu"`
	RAM       float64   `json:"ram"`
	Processes []Process `json:"processes"`
	// exec_result / log_result fields
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Output string `json:"output"`
}

// OutgoingMessage covers all server→agent message types.
type OutgoingMessage struct {
	Type     string `json:"type"`
	JobID    string `json:"job_id,omitempty"`
	Payload  string `json:"payload,omitempty"`
	Filename string `json:"filename,omitempty"`
	Args     string `json:"args,omitempty"`
	Lines    int    `json:"lines,omitempty"`
}

// ─── Client ────────────────────────────────────────────────────────────────

type Client struct {
	hub       *Hub
	conn      *websocket.Conn
	send      chan []byte
	agentID   string
	closeOnce sync.Once
}

func (c *Client) safeClose() {
	c.closeOnce.Do(func() { close(c.send) })
}

func (c *Client) readPump() {
	defer func() {
		c.hub.removeClient(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMsgSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		c.hub.handleMessage(c, data)
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ─── Hub ───────────────────────────────────────────────────────────────────

type Hub struct {
	mu            sync.RWMutex
	clients       map[string]*Client
	db            *DB
	lastMetricLog sync.Map // agentID → time.Time, throttle log to every 5 min
}

func NewHub(db *DB) *Hub {
	return &Hub{
		clients: make(map[string]*Client),
		db:      db,
	}
}

func (h *Hub) addClient(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.clients[c.agentID]; ok && old != c {
		old.safeClose()
	}
	h.clients[c.agentID] = c
}

func (h *Hub) removeClient(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.agentID != "" && h.clients[c.agentID] == c {
		delete(h.clients, c.agentID)
		if err := h.db.UpdateAgentStatus(c.agentID, "offline"); err != nil {
			slog.Error("update agent offline failed", "agent_id", c.agentID, "error", err)
		}
		slog.Info("agent disconnected", "agent_id", c.agentID)
	}
	c.safeClose()
}

func (h *Hub) SendToAgent(agentID string, msg *OutgoingMessage) bool {
	h.mu.RLock()
	client, ok := h.clients[agentID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	select {
	case client.send <- data:
		return true
	default:
		return false
	}
}

func (h *Hub) IsOnline(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clients[agentID]
	return ok
}

func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) AllOnlineIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.clients))
	for id := range h.clients {
		ids = append(ids, id)
	}
	return ids
}

// ─── Message Handling ──────────────────────────────────────────────────────

func (h *Hub) handleMessage(c *Client, data []byte) {
	var msg IncomingMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Error("ws unmarshal failed", "error", err)
		return
	}

	switch msg.Type {
	case "metrics":
		h.handleMetrics(c, &msg)
	case "exec_result":
		h.handleExecResult(&msg)
	case "log_result":
		h.handleLogResult(c, &msg)
	}
}

func (h *Hub) handleMetrics(c *Client, msg *IncomingMessage) {
	if msg.AgentID == "" {
		return
	}

	// Register client under agentID on first metrics message
	if c.agentID == "" {
		c.agentID = msg.AgentID
		h.addClient(c)
		slog.Info("agent connected", "agent_id", msg.AgentID, "hostname", msg.Hostname, "ip", msg.IP)
	}

	now := nowWIB()
	agent := &Agent{
		ID:        msg.AgentID,
		Hostname:  msg.Hostname,
		IP:        msg.IP,
		OS:        msg.OS,
		LastSeen:  now,
		Status:    "online",
		CreatedAt: now,
	}
	if err := h.db.UpsertAgent(agent); err != nil {
		slog.Error("upsert agent failed", "agent_id", msg.AgentID, "error", err)
		return
	}

	if err := h.db.InsertMetric(msg.AgentID, msg.CPU, msg.RAM); err != nil {
		slog.Error("insert metric failed", "agent_id", msg.AgentID, "error", err)
	}

	if len(msg.Processes) > 0 {
		if err := h.db.UpsertProcesses(msg.AgentID, msg.Processes); err != nil {
			slog.Error("upsert processes failed", "agent_id", msg.AgentID, "error", err)
		}
	}

	// Log only once every 5 minutes per agent
	lastLog, _ := h.lastMetricLog.Load(msg.AgentID)
	if lastLog == nil || now.Sub(lastLog.(time.Time)) >= 5*time.Minute {
		slog.Info("metrics received",
			"agent_id", msg.AgentID,
			"hostname", msg.Hostname,
			"cpu", msg.CPU,
			"ram", msg.RAM)
		h.lastMetricLog.Store(msg.AgentID, now)
	}
}

func (h *Hub) handleExecResult(msg *IncomingMessage) {
	if msg.JobID == "" {
		return
	}
	output := msg.Output
	if len(output) > 4096 {
		output = output[:4096] + "...[truncated]"
	}
	if err := h.db.UpdateDeployResult(msg.JobID, msg.AgentID, msg.Status, output); err != nil {
		slog.Error("update deploy result failed", "job_id", msg.JobID, "error", err)
		return
	}
	slog.Info("exec result received",
		"job_id", msg.JobID,
		"agent_id", msg.AgentID,
		"status", msg.Status)
}

func (h *Hub) handleLogResult(c *Client, msg *IncomingMessage) {
	// Log relay: agent sends back log lines, stored temporarily for API response.
	// Full implementation in Milestone 6.
	_ = c
}

// ─── WebSocket Endpoint ────────────────────────────────────────────────────

func ServeWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
		return
	}

	client := &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}

	go client.writePump()
	go client.readPump()
}
