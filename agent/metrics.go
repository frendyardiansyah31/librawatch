package main

import (
	"sort"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
)

type ProcessInfo struct {
	PID  int32   `json:"pid"`
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"`
	RAM  float64 `json:"ram"`
}

type MetricsPayload struct {
	Type      string        `json:"type"`
	AgentID   string        `json:"agent_id"`
	Hostname  string        `json:"hostname"`
	IP        string        `json:"ip"`
	OS        string        `json:"os"`
	CPU       float64       `json:"cpu"`
	RAM       float64       `json:"ram"`
	MeshID    string        `json:"mesh_id,omitempty"`
	Processes []ProcessInfo `json:"processes"`
}

// collectMetrics gathers CPU%, RAM%, and the top-50 processes by CPU usage.
// Uses a 500ms CPU sampling window on first call; subsequent calls use elapsed time.
func collectMetrics(agentID, hostname, ip, osName, meshID string) (*MetricsPayload, error) {
	cpuPercents, err := cpu.Percent(500*time.Millisecond, false)
	if err != nil {
		return nil, err
	}
	var totalCPU float64
	if len(cpuPercents) > 0 {
		totalCPU = cpuPercents[0]
	}

	memStat, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}

	procs, _ := process.Processes()
	var list []ProcessInfo
	for _, p := range procs {
		cpuPct, _ := p.CPUPercent()
		ramPct, _ := p.MemoryPercent()
		ramF := float64(ramPct)

		if cpuPct < 0.1 && ramF < 0.5 {
			continue
		}
		name, _ := p.Name()
		if name == "" {
			continue
		}
		list = append(list, ProcessInfo{
			PID:  p.Pid,
			Name: name,
			CPU:  round2(cpuPct),
			RAM:  round2(ramF),
		})
	}

	sort.Slice(list, func(i, j int) bool { return list[i].CPU > list[j].CPU })
	if len(list) > 50 {
		list = list[:50]
	}

	return &MetricsPayload{
		Type:      "metrics",
		AgentID:   agentID,
		Hostname:  hostname,
		IP:        ip,
		OS:        osName,
		CPU:       round2(totalCPU),
		RAM:       round2(memStat.UsedPercent),
		MeshID:    meshID,
		Processes: list,
	}, nil
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
