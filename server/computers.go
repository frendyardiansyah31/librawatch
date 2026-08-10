package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Computer is the minimal read-only projection of an agent exposed to
// external integrations (e.g. a Veyon sync worker) that only need machine
// identity + physical location — a narrower field set than ClientSummary
// (server/clients.go), which is a different existing consumer.
type Computer struct {
	Hostname   string `json:"hostname"`
	IPAddress  string `json:"ip_address"`
	MacAddress string `json:"mac_address"`
	Floor      string `json:"floor"`
}

// GetComputers lists agents as Computer projections, optionally filtered by
// exact floor and/or hostname match, ordered by hostname ascending. Empty
// filter values are ignored (match everything), same convention as
// GetApplications's status/category_id filters.
func (db *DB) GetComputers(floor, hostname string) ([]Computer, error) {
	query := `SELECT hostname, ip, mac_address, floor FROM agents WHERE 1=1`
	args := []interface{}{}
	if floor != "" {
		query += ` AND floor = ?`
		args = append(args, floor)
	}
	if hostname != "" {
		query += ` AND hostname = ?`
		args = append(args, hostname)
	}
	query += ` ORDER BY hostname ASC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]Computer, 0)
	for rows.Next() {
		var c Computer
		if err := rows.Scan(&c.Hostname, &c.IPAddress, &c.MacAddress, &c.Floor); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// handleGetComputers serves GET /api/v1/computers?floor=&hostname= — a
// plain JSON array (no {success,data} envelope), per the external
// integration contract this endpoint was added for.
func handleGetComputers(db *DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		computers, err := db.GetComputers(c.Query("floor"), c.Query("hostname"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, computers)
	}
}
