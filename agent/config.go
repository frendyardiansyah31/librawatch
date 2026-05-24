package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	agentBaseDir  = `C:\LibraryAgent`
	idFile        = `C:\LibraryAgent\id.txt`
	meshIDFile    = `C:\LibraryAgent\mesh_id.txt`
	agentLogFile  = `C:\LibraryAgent\agent.log`
	defaultServer = "ws://192.168.1.10:8080/ws"
)

// getServerURL returns the WebSocket server URL.
// Priority: env var LIBRARY_SERVER_URL > C:\LibraryAgent\server.txt > default.
func getServerURL() string {
	if url := os.Getenv("LIBRARY_SERVER_URL"); url != "" {
		return url
	}
	data, err := os.ReadFile(filepath.Join(agentBaseDir, "server.txt"))
	if err == nil {
		if url := strings.TrimSpace(string(data)); url != "" {
			return url
		}
	}
	return defaultServer
}

// loadOrCreateID reads the agent UUID from disk, generating one if absent.
func loadOrCreateID() (string, error) {
	if err := os.MkdirAll(agentBaseDir, 0755); err != nil {
		return "", fmt.Errorf("create agent dir: %w", err)
	}
	data, err := os.ReadFile(idFile)
	if err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	}
	id := newUUID()
	if err := os.WriteFile(idFile, []byte(id+"\n"), 0644); err != nil {
		return "", fmt.Errorf("save agent id: %w", err)
	}
	return id, nil
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// loadMeshID reads the MeshCentral device ID written by the MeshCentral agent installer.
func loadMeshID() string {
	data, err := os.ReadFile(meshIDFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
