package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// leaseSweepInterval is how often the lease sweep looks for overdue/expired
// deploy_results rows — same ticker idiom as alert.go's offlineCheckInterval.
const leaseSweepInterval = 30 * time.Second

type Deployer struct {
	db  *DB
	hub *Hub
}

func NewDeployer(db *DB, hub *Hub) *Deployer {
	return &Deployer{db: db, hub: hub}
}

// CreateJob creates a deploy job, inserts pending result rows for each target,
// and immediately pumps each target's queue. Offline (or already-busy) agents
// receive it once their queue reaches it — on reconnect, on lease-sweep
// requeue, or the moment their current command finishes.
func (d *Deployer) CreateJob(jobType, payload, args string, targets []string,
	priority int, expireAt *time.Time, maxRetry int, createdBy string) (*DeployJob, error) {
	agentIDs, err := d.resolveTargets(targets)
	if err != nil {
		return nil, err
	}
	if len(agentIDs) == 0 {
		return nil, fmt.Errorf("no target agents found")
	}

	targetsJSON, _ := json.Marshal(targets)
	job := &DeployJob{
		ID:        generateJobID(),
		Type:      jobType,
		Payload:   payload,
		Args:      args,
		Targets:   string(targetsJSON),
		Status:    "pending",
		Priority:  priority,
		ExpireAt:  expireAt,
		CreatedBy: createdBy,
		CreatedAt: nowWIB(),
	}
	if err := d.db.InsertDeployJob(job); err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	for _, agentID := range agentIDs {
		if err := d.db.InsertDeployResult(&DeployResult{
			JobID:    job.ID,
			AgentID:  agentID,
			Status:   "pending",
			MaxRetry: maxRetry,
		}); err != nil {
			slog.Error("insert deploy result", "agent_id", agentID, "error", err)
		}
	}

	for _, agentID := range agentIDs {
		d.PumpAgent(agentID)
	}

	// Mirror the original "dispatched once every target has moved off pending"
	// semantics — a target may still be pending here if it's offline, or if
	// PumpAgent claimed a different, higher-priority job for it instead.
	if results, err := d.db.GetDeployResultsByJobID(job.ID); err == nil {
		allDispatched := true
		for _, r := range results {
			if r.Status == "pending" {
				allDispatched = false
				break
			}
		}
		if allDispatched {
			if err := d.db.UpdateDeployJobStatus(job.ID, "dispatched"); err == nil {
				job.Status = "dispatched"
			}
		}
	}

	return job, nil
}

// PumpAgent claims and dispatches the next queued job for agentID, if the
// agent has no job already running. Safe to call from multiple goroutines
// concurrently (reconnect, job completion, lease sweep) — see
// AcquireNextJob's comment for why no additional locking is needed.
func (d *Deployer) PumpAgent(agentID string) bool {
	job, err := d.db.AcquireNextJob(agentID, nowWIB().Add(d.leaseMinutes()))
	if err != nil {
		slog.Error("acquire next job failed", "agent_id", agentID, "error", err)
		return false
	}
	if job == nil {
		return false
	}
	if !d.dispatch(agentID, job) {
		// Agent went offline between claim and send — release the claim with
		// no retry charged, since it never actually received anything.
		if err := d.db.UpdateDeployResult(job.ID, agentID, "pending", "", nil, nil, nil); err != nil {
			slog.Error("release claim failed", "job_id", job.ID, "agent_id", agentID, "error", err)
		}
		return false
	}
	return true
}

// leaseMinutes reads the admin-tunable lease duration from the settings
// table, same pattern as alert.go's offline_after_minutes.
func (d *Deployer) leaseMinutes() time.Duration {
	settings, err := d.db.GetAllSettings()
	if err != nil {
		return 10 * time.Minute
	}
	return time.Duration(parseIntSetting(settings["lease_minutes"], 10)) * time.Minute
}

// DefaultMaxRetry reads the admin-tunable default retry budget from the
// settings table — used by CreateJob callers (the /api/deploy handler, MCP
// tools) that don't specify their own max_retry.
func (d *Deployer) DefaultMaxRetry() int {
	settings, err := d.db.GetAllSettings()
	if err != nil {
		return 3
	}
	return parseIntSetting(settings["default_max_retry"], 3)
}

// StartLeaseSweeper runs indefinitely, requeuing or failing commands whose
// lease has expired and expiring commands that were never dispatched in
// time. Same template as alert.go's StartOfflineChecker.
func (d *Deployer) StartLeaseSweeper() {
	ticker := time.NewTicker(leaseSweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		d.sweepExpiredLeases()
		d.sweepExpiredPending()
	}
}

// sweepExpiredLeases requeues (with a retry charged) or permanently fails
// running commands whose agent hasn't replied before the lease deadline —
// this is what recovers a command after a PC crash, power loss, or agent
// crash mid-execution.
func (d *Deployer) sweepExpiredLeases() {
	expired, err := d.db.GetExpiredLeaseResults(nowWIB())
	if err != nil {
		slog.Error("lease sweep query failed", "error", err)
		return
	}
	for _, r := range expired {
		if r.RetryCount+1 > r.MaxRetry {
			if err := d.db.UpdateDeployResult(r.JobID, r.AgentID, "failed",
				"Lease timeout: retry limit exceeded", nil, nil, nil); err != nil {
				slog.Error("fail expired lease failed", "job_id", r.JobID, "agent_id", r.AgentID, "error", err)
			}
		} else {
			newRetryCount := r.RetryCount + 1
			if err := d.db.UpdateDeployResult(r.JobID, r.AgentID, "pending",
				"Lease timeout: retrying", nil, nil, &newRetryCount); err != nil {
				slog.Error("requeue expired lease failed", "job_id", r.JobID, "agent_id", r.AgentID, "error", err)
			}
			// The agent may still be connected (hung command, dropped WS
			// frame) — retry immediately instead of waiting for a reconnect
			// that may never come.
			d.PumpAgent(r.AgentID)
		}
		if err := d.db.UpdateJobStatus(r.JobID); err != nil {
			slog.Error("update job status failed", "job_id", r.JobID, "error", err)
		}
	}
}

// sweepExpiredPending expires commands that were never dispatched before
// their job's expire_at passed (e.g. a "shutdown" queued for an offline PC
// that never came back online in time).
func (d *Deployer) sweepExpiredPending() {
	rows, err := d.db.GetExpiredPendingResults(nowWIB())
	if err != nil {
		slog.Error("expire sweep query failed", "error", err)
		return
	}
	for _, r := range rows {
		if err := d.db.UpdateDeployResult(r.JobID, r.AgentID, "expired",
			"Expired before execution", nil, nil, nil); err != nil {
			slog.Error("expire pending result failed", "job_id", r.JobID, "agent_id", r.AgentID, "error", err)
		}
		if err := d.db.UpdateJobStatus(r.JobID); err != nil {
			slog.Error("update job status failed", "job_id", r.JobID, "error", err)
		}
	}
}

// dispatch sends a job to a single agent via WebSocket. Returns true if the agent was online.
func (d *Deployer) dispatch(agentID string, job *DeployJob) bool {
	msg := &OutgoingMessage{
		Type:    job.Type,
		JobID:   job.ID,
		Payload: job.Payload,
		Args:    job.Args,
	}
	switch job.Type {
	case "file_deploy":
		msg.Filename = job.Payload // payload is the filename
	case "deepfreeze":
		msg.Action = job.Payload   // thaw / freeze / query_df
		msg.Password = job.Args    // optional DF password
	case "install_ssh":
		msg.Action = "install_ssh"
		msg.Args = job.Args // admin_ip passed via args
	}
	sent := d.hub.SendToAgent(agentID, msg)
	if sent {
		slog.Info("deploy dispatched", "job_id", job.ID, "agent_id", agentID, "type", job.Type)
	}
	return sent
}

// DispatchPending delivers the next queued job to an agent that just came
// online — one at a time, not every pending job at once (see PumpAgent).
func (d *Deployer) DispatchPending(agentID string) {
	d.PumpAgent(agentID)
}

func (d *Deployer) resolveTargets(targets []string) ([]string, error) {
	for _, t := range targets {
		if t == "*" {
			return d.db.GetAllAgentIDs()
		}
	}
	return targets, nil
}

func generateJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
