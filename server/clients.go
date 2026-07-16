package main

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ClientSummary is the slim, read-only projection of an agent returned by
// GET /api/v1/clients, for external consumers like the Veyon sync service.
type ClientSummary struct {
	ID           string    `json:"id"`
	Hostname     string    `json:"hostname"`
	IP           string    `json:"ip"`
	MacAddress   string    `json:"mac_address"`
	OS           string    `json:"os"`
	AgentVersion string    `json:"agent_version"`
	Status       string    `json:"status"`
	Floor        string    `json:"floor"`
	LastSeen     time.Time `json:"last_seen"`
}

func (db *DB) GetAllClients() ([]ClientSummary, error) {
	rows, err := db.Query(`
		SELECT id, hostname, ip, mac_address, os, agent_version, status, floor, last_seen
		FROM agents
		ORDER BY hostname ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]ClientSummary, 0)
	for rows.Next() {
		var c ClientSummary
		var lastSeen string
		if err := rows.Scan(
			&c.ID, &c.Hostname, &c.IP, &c.MacAddress, &c.OS,
			&c.AgentVersion, &c.Status, &c.Floor, &lastSeen,
		); err != nil {
			return nil, err
		}
		c.LastSeen = parseDBTime(lastSeen)
		result = append(result, c)
	}
	return result, rows.Err()
}

func handleGetClients(db *DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		clients, err := db.GetAllClients()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    clients,
		})
	}
}
