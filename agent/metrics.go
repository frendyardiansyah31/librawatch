package main

import (
	"sort"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
)

// maxProcessesReported is a defensive cap only — not a "top by activity"
// filter. Every running process is reported regardless of CPU/RAM usage (an
// idle blacklisted app must still be visible to the alerting/catalog
// pipeline); this just guards against a pathological process explosion.
const maxProcessesReported = 500

type ProcessInfo struct {
	PID  int32   `json:"pid"`
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"`
	RAM  float64 `json:"ram"`

	// Optional metadata, populated only the first time this session that we
	// see this executable path (see appmeta.go). Empty on repeat sightings.
	Path           string `json:"path,omitempty"`
	ProductName    string `json:"product_name,omitempty"`
	Company        string `json:"company,omitempty"`
	Description    string `json:"description,omitempty"`
	ProductVersion string `json:"product_version,omitempty"`
	Size           int64  `json:"size,omitempty"`
	FileCreatedAt  string `json:"file_created_at,omitempty"`
	FileModifiedAt string `json:"file_modified_at,omitempty"`
}

type MetricsPayload struct {
	Type           string        `json:"type"`
	AgentID        string        `json:"agent_id"`
	Hostname       string        `json:"hostname"`
	IP             string        `json:"ip"`
	OS             string        `json:"os"`
	CPU            float64       `json:"cpu"`
	RAM            float64       `json:"ram"`
	MeshID         string        `json:"mesh_id,omitempty"`
	Processes      []ProcessInfo `json:"processes"`
	AgentVersion   string        `json:"agent_version,omitempty"`
	WindowsVersion string        `json:"windows_version,omitempty"`
	DiskCapacityGB float64       `json:"disk_capacity_gb,omitempty"`
}

// collectMetrics gathers CPU%, RAM%, and every running process. Uses a 500ms
// CPU sampling window on first call; subsequent calls use elapsed time.
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
		name, _ := p.Name()
		if name == "" {
			continue
		}
		cpuPct, _ := p.CPUPercent()
		ramPct, _ := p.MemoryPercent()

		info := ProcessInfo{
			PID:  p.Pid,
			Name: name,
			CPU:  round2(cpuPct),
			RAM:  round2(float64(ramPct)),
		}
		if path, err := p.Exe(); err == nil && path != "" {
			info.Path = path
			if meta := extractMetadataIfNew(path); meta != nil {
				info.ProductName = meta.ProductName
				info.Company = meta.Company
				info.Description = meta.Description
				info.ProductVersion = meta.ProductVersion
				info.Size = meta.Size
				info.FileCreatedAt = meta.FileCreatedAt.Format(time.RFC3339)
				info.FileModifiedAt = meta.FileModifiedAt.Format(time.RFC3339)
			}
		}
		list = append(list, info)
	}

	sort.Slice(list, func(i, j int) bool { return list[i].CPU > list[j].CPU })
	if len(list) > maxProcessesReported {
		list = list[:maxProcessesReported]
	}

	return &MetricsPayload{
		Type:           "metrics",
		AgentID:        agentID,
		Hostname:       hostname,
		IP:             ip,
		OS:             osName,
		CPU:            round2(totalCPU),
		RAM:            round2(memStat.UsedPercent),
		MeshID:         meshID,
		Processes:      list,
		AgentVersion:   agentVersion,
		WindowsVersion: getWindowsVersion(),
		DiskCapacityGB: getDiskCapacityGB(),
	}, nil
}

func getWindowsVersion() string {
	info, err := host.Info()
	if err != nil {
		return ""
	}
	if info.PlatformVersion != "" {
		return info.Platform + " " + info.PlatformVersion
	}
	return info.Platform
}

func getDiskCapacityGB() float64 {
	usage, err := disk.Usage(`C:\`)
	if err != nil {
		return 0
	}
	return round2(float64(usage.Total) / (1024 * 1024 * 1024))
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
