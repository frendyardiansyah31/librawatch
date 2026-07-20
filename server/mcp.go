package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPPC struct {
	Hostname string    `json:"hostname"`
	IP       string    `json:"ip"`
	LastSeen time.Time `json:"last_seen"`
}

type OnlinePCsOutput struct {
	Count int     `json:"count"`
	PCs   []MCPPC `json:"pcs"`
}

type PCActionInput struct {
	Hostname string `json:"hostname" jsonschema:"hostname of the PC, e.g. PC-LIB-05"`
}

type PCActionOutput struct {
	Hostname string `json:"hostname"`
	JobID    string `json:"job_id"`
	Status   string `json:"status"` // "dispatched" (PC online, sent now) or "pending" (PC offline, runs on reconnect)
}

type DeepFreezeStatusOutput struct {
	Hostname string `json:"hostname"`
	Status   string `json:"status"`           // "frozen", "thawed", "offline", "error", or "unknown"
	Detail   string `json:"detail,omitempty"` // raw agent output, populated for "error"/"unknown"
}

type KillProcessInput struct {
	Hostname    string `json:"hostname" jsonschema:"hostname of the PC, e.g. PC-LIB-05"`
	ProcessName string `json:"process_name" jsonschema:"process image name to kill, e.g. chrome.exe"`
}

type KillProcessOutput struct {
	Hostname    string `json:"hostname"`
	ProcessName string `json:"process_name"`
	Output      string `json:"output"` // raw taskkill output from the agent
}

// NewMCPHandler builds an MCP server exposing get_online_pcs, restart_pc,
// shutdown_pc, freeze_pc, thaw_pc, check_deepfreeze_status, and kill_process,
// and returns it as an http.Handler. Tool handlers call the existing
// Hub/DB/Deployer directly — no HTTP round-trip, no duplicated logic.
func NewMCPHandler(hub *Hub, db *DB, deployer *Deployer, deepFreezePassword string) http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "librarywatch", Version: "0.1.0"}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_online_pcs",
		Description: "Returns every computer that is currently connected.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, OnlinePCsOutput, error) {
		online := hub.AllOnlineIDs()
		onlineSet := make(map[string]bool, len(online))
		for _, id := range online {
			onlineSet[id] = true
		}

		agents, err := db.GetAllAgents()
		if err != nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
			}, OnlinePCsOutput{}, nil
		}

		pcs := make([]MCPPC, 0)
		for _, a := range agents {
			if onlineSet[a.ID] {
				pcs = append(pcs, MCPPC{Hostname: a.Hostname, IP: a.IP, LastSeen: a.LastSeen})
			}
		}

		return nil, OnlinePCsOutput{Count: len(pcs), PCs: pcs}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "restart_pc",
		Description: "Restart a specific PC by hostname.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PCActionInput) (*mcp.CallToolResult, PCActionOutput, error) {
		return pcActionResult(dispatchPCJob(db, deployer, in.Hostname, "exec", psRestart, "", "restart_pc"))
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "shutdown_pc",
		Description: "Shut down a specific PC by hostname.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PCActionInput) (*mcp.CallToolResult, PCActionOutput, error) {
		return pcActionResult(dispatchPCJob(db, deployer, in.Hostname, "exec", psShutdown, "", "shutdown_pc"))
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "freeze_pc",
		Description: "Set Deep Freeze to frozen on next reboot for a specific PC (DFC.exe /BOOTFROZEN) — the PC reverts all changes on every restart.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PCActionInput) (*mcp.CallToolResult, PCActionOutput, error) {
		if deepFreezePassword == "" {
			return toolError[PCActionOutput](fmt.Errorf("deepfreeze.password is not configured in config.yaml"))
		}
		return pcActionResult(dispatchPCJob(db, deployer, in.Hostname, "deepfreeze", "freeze", deepFreezePassword, "freeze_pc"))
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "thaw_pc",
		Description: "Set Deep Freeze to thawed on next reboot for a specific PC (DFC.exe /BOOTTHAWED) — the PC keeps changes across restarts.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PCActionInput) (*mcp.CallToolResult, PCActionOutput, error) {
		if deepFreezePassword == "" {
			return toolError[PCActionOutput](fmt.Errorf("deepfreeze.password is not configured in config.yaml"))
		}
		return pcActionResult(dispatchPCJob(db, deployer, in.Hostname, "deepfreeze", "thaw", deepFreezePassword, "thaw_pc"))
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "check_deepfreeze_status",
		Description: "Check whether a PC's Deep Freeze is currently FROZEN or THAWED (DFC.exe get /ISFROZEN). The PC must be online.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in PCActionInput) (*mcp.CallToolResult, DeepFreezeStatusOutput, error) {
		out, err := queryDeepFreezeStatus(ctx, db, deployer, in.Hostname)
		if err != nil {
			return toolError[DeepFreezeStatusOutput](err)
		}
		return nil, *out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kill_process",
		Description: "Kill a running process by name on a specific PC (e.g. chrome.exe). The PC must be online; waits up to 10s for confirmation.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in KillProcessInput) (*mcp.CallToolResult, KillProcessOutput, error) {
		if in.ProcessName == "" {
			return toolError[KillProcessOutput](fmt.Errorf("process_name is required"))
		}
		agent, err := resolveAgentByHostname(db, in.Hostname)
		if err != nil {
			return toolError[KillProcessOutput](err)
		}
		output, err := hub.KillProcess(agent.ID, 0, in.ProcessName)
		if err != nil {
			return toolError[KillProcessOutput](err)
		}
		db.InsertAuditLog("kill_process", agent.ID, fmt.Sprintf("name=%s via=mcp", in.ProcessName), "mcp")
		return nil, KillProcessOutput{Hostname: agent.Hostname, ProcessName: in.ProcessName, Output: output}, nil
	})

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
}

// toolError builds the (result, output, error) triple for a failed tool call.
func toolError[Out any](err error) (*mcp.CallToolResult, Out, error) {
	var zero Out
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}, zero, nil
}

// pcActionResult adapts dispatchPCJob's (output, error) return into the
// (result, output, error) triple ToolHandlerFor expects.
func pcActionResult(out *PCActionOutput, err error) (*mcp.CallToolResult, PCActionOutput, error) {
	if err != nil {
		return toolError[PCActionOutput](err)
	}
	return nil, *out, nil
}

// resolveAgentByHostname finds an agent by hostname, case-insensitively.
func resolveAgentByHostname(db *DB, hostname string) (*AgentWithMetrics, error) {
	agents, err := db.GetAllAgents()
	if err != nil {
		return nil, err
	}
	for i := range agents {
		if strings.EqualFold(agents[i].Hostname, hostname) {
			return &agents[i], nil
		}
	}
	return nil, fmt.Errorf("PC %q not found", hostname)
}

// dispatchPCJob resolves hostname to an agent, then reuses the existing
// deploy pipeline (the same one the dashboard's deploy panel uses) to send a
// fixed, non-user-controlled job to it — either an "exec" PowerShell command
// or a "deepfreeze" action (payload="freeze"/"thaw", args=password).
func dispatchPCJob(db *DB, deployer *Deployer, hostname, jobType, payload, args, auditAction string) (*PCActionOutput, error) {
	agent, err := resolveAgentByHostname(db, hostname)
	if err != nil {
		return nil, err
	}

	job, err := deployer.CreateJob(jobType, payload, args, []string{agent.ID},
		0, nil, deployer.DefaultMaxRetry(), "mcp")
	if err != nil {
		return nil, err
	}

	db.InsertAuditLog(auditAction, agent.ID, fmt.Sprintf("hostname=%s via=mcp", agent.Hostname), "mcp")

	return &PCActionOutput{Hostname: agent.Hostname, JobID: job.ID, Status: job.Status}, nil
}

// queryDeepFreezeStatus dispatches a "deepfreeze"/"query_df" job (read-only,
// no password needed) and, if the PC is online, polls deploy_results for the
// agent's reply. This is the one MCP tool that needs a synchronous answer
// rather than fire-and-forget, so it briefly waits on the existing async
// deploy-result pipeline instead of adding a second, parallel result path.
func queryDeepFreezeStatus(ctx context.Context, db *DB, deployer *Deployer, hostname string) (*DeepFreezeStatusOutput, error) {
	agent, err := resolveAgentByHostname(db, hostname)
	if err != nil {
		return nil, err
	}

	job, err := deployer.CreateJob("deepfreeze", "query_df", "", []string{agent.ID},
		0, nil, deployer.DefaultMaxRetry(), "mcp")
	if err != nil {
		return nil, err
	}

	if job.Status != "dispatched" {
		return &DeepFreezeStatusOutput{
			Hostname: agent.Hostname,
			Status:   "offline",
			Detail:   "PC is offline, cannot check Deep Freeze status right now",
		}, nil
	}

	const pollInterval = 300 * time.Millisecond
	const pollTimeout = 8 * time.Second
	deadline := time.Now().Add(pollTimeout)

	for time.Now().Before(deadline) {
		results, err := db.GetDeployResultsByJobID(job.ID)
		if err == nil {
			for _, r := range results {
				if r.AgentID == agent.ID && !isPendingLikeStatus(r.Status) {
					return parseDeepFreezeResult(agent.Hostname, r), nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return &DeepFreezeStatusOutput{Hostname: agent.Hostname, Status: "unknown", Detail: "request cancelled"}, nil
		case <-time.After(pollInterval):
		}
	}

	return &DeepFreezeStatusOutput{
		Hostname: agent.Hostname,
		Status:   "unknown",
		Detail:   "PC did not respond in time",
	}, nil
}

func parseDeepFreezeResult(hostname string, r DeployResult) *DeepFreezeStatusOutput {
	if r.Status != "ok" {
		return &DeepFreezeStatusOutput{Hostname: hostname, Status: "error", Detail: r.Output}
	}
	switch strings.ToUpper(strings.TrimSpace(r.Output)) {
	case "FROZEN":
		return &DeepFreezeStatusOutput{Hostname: hostname, Status: "frozen"}
	case "THAWED":
		return &DeepFreezeStatusOutput{Hostname: hostname, Status: "thawed"}
	default:
		return &DeepFreezeStatusOutput{Hostname: hostname, Status: "unknown", Detail: r.Output}
	}
}
