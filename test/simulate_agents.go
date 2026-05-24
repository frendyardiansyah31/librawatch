package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	flagN      = flag.Int("n", 60, "number of agents to simulate")
	flagServer = flag.String("server", "ws://localhost:8080/ws", "WebSocket server URL")
)

type process struct {
	Name  string  `json:"name"`
	PID   int     `json:"pid"`
	CPU   float64 `json:"cpu"`
	RAMMB float64 `json:"ram_mb"`
}

var procPool = []string{
	"chrome.exe", "explorer.exe", "svchost.exe", "winword.exe",
	"teams.exe", "outlook.exe", "notepad.exe", "msedge.exe",
}

func main() {
	flag.Parse()
	fmt.Printf("Starting %d simulated agents → %s\n", *flagN, *flagServer)

	var wg sync.WaitGroup
	for i := 0; i < *flagN; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Stagger connections 50ms apart to avoid thundering herd
			time.Sleep(time.Duration(i*50) * time.Millisecond)
			runAgent(i)
		}(i)
	}
	wg.Wait()
}

func runAgent(i int) {
	agentID := fmt.Sprintf("sim-%04d", i)
	hostname := fmt.Sprintf("PC-SIM-%04d", i)
	ip := fmt.Sprintf("192.168.1.%d", 100+i%150)

	backoff := 5 * time.Second
	for {
		conn, _, err := websocket.DefaultDialer.Dial(*flagServer, nil)
		if err != nil {
			fmt.Printf("[%s] connect error: %v — retry in %v\n", agentID, err, backoff)
			time.Sleep(backoff)
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Second
		fmt.Printf("[%s] connected as %s (%s)\n", agentID, hostname, ip)
		runSession(conn, agentID, hostname, ip)
		conn.Close()
		time.Sleep(5 * time.Second)
	}
}

func runSession(conn *websocket.Conn, agentID, hostname, ip string) {
	// Drain incoming messages (exec commands etc.) in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	if err := sendMetrics(conn, agentID, hostname, ip); err != nil {
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := sendMetrics(conn, agentID, hostname, ip); err != nil {
				return
			}
		}
	}
}

func sendMetrics(conn *websocket.Conn, agentID, hostname, ip string) error {
	cpu := rand.Float64()*40 + 5  // 5–45%
	ram := rand.Float64()*40 + 30 // 30–70%

	procs := make([]process, 5)
	for i := range procs {
		procs[i] = process{
			Name:  procPool[rand.Intn(len(procPool))],
			PID:   1000 + rand.Intn(60000),
			CPU:   rand.Float64() * 15,
			RAMMB: rand.Float64() * 600,
		}
	}

	msg := map[string]interface{}{
		"type":      "metrics",
		"agent_id":  agentID,
		"hostname":  hostname,
		"ip":        ip,
		"os":        "Windows 11 Home (simulated)",
		"cpu":       cpu,
		"ram":       ram,
		"processes": procs,
	}
	data, _ := json.Marshal(msg)
	return conn.WriteMessage(websocket.TextMessage, data)
}
