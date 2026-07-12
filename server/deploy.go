package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
)

type Deployer struct {
	db  *DB
	hub *Hub
}

func NewDeployer(db *DB, hub *Hub) *Deployer {
	return &Deployer{db: db, hub: hub}
}

// CreateJob creates a deploy job, inserts pending result rows for each target,
// and immediately dispatches to any online agents. Offline agents receive it on reconnect.
func (d *Deployer) CreateJob(jobType, payload, args string, targets []string) (*DeployJob, error) {
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
		CreatedAt: nowWIB(),
	}
	if err := d.db.InsertDeployJob(job); err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	for _, agentID := range agentIDs {
		if err := d.db.InsertDeployResult(&DeployResult{
			JobID:   job.ID,
			AgentID: agentID,
			Status:  "pending",
		}); err != nil {
			slog.Error("insert deploy result", "agent_id", agentID, "error", err)
		}
	}

	onlineCount := 0
	for _, agentID := range agentIDs {
		if d.dispatch(agentID, job) {
			onlineCount++
		}
	}
	if onlineCount == len(agentIDs) {
		if err := d.db.UpdateDeployJobStatus(job.ID, "dispatched"); err == nil {
			job.Status = "dispatched"
		}
	}

	return job, nil
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

// DispatchPending delivers any pending jobs to an agent that just came online.
func (d *Deployer) DispatchPending(agentID string) {
	jobs, err := d.db.GetPendingJobsForAgent(agentID)
	if err != nil {
		slog.Error("get pending jobs", "agent_id", agentID, "error", err)
		return
	}
	for i := range jobs {
		d.dispatch(agentID, &jobs[i])
	}
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
