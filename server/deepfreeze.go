package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// deepFreezeActions maps a caller-facing verb (used by both the REST endpoint
// POST /api/agents/:id/deepfreeze and the MCP tools) to the deploy-job payload
// the agent's handleDeepFreeze understands. "status" is a read-only check that
// needs no password.
var deepFreezeActions = map[string]string{
	"freeze": "freeze",
	"thaw":   "thaw",
	"status": "query_df",
}

// validDeepFreezeJobActions is the set of payloads dispatchDeepFreeze will
// actually send — mirrors validateDeployRequest's "deepfreeze" case in api.go.
var validDeepFreezeJobActions = map[string]bool{
	"freeze":   true,
	"thaw":     true,
	"query_df": true,
}

// dispatchDeepFreeze queues a "deepfreeze" job for one agent through the same
// deploy pipeline the dashboard and MCP tools use — no second queue. jobAction
// must be one of freeze/thaw/query_df; password is passed as the job args and
// travels to the agent as msg.Password (empty for query_df, which needs none).
func dispatchDeepFreeze(db *DB, deployer *Deployer, agentID, jobAction, password, createdBy string) (*DeployJob, error) {
	if !validDeepFreezeJobActions[jobAction] {
		return nil, fmt.Errorf("invalid deepfreeze action: must be freeze, thaw, or query_df")
	}
	return deployer.CreateJob("deepfreeze", jobAction, password, []string{agentID},
		0, nil, deployer.DefaultMaxRetry(), createdBy)
}

const (
	deepFreezePollInterval = 300 * time.Millisecond
	deepFreezePollTimeout  = 8 * time.Second
)

// pollDeepFreezeResult briefly waits on the async deploy-result pipeline for
// the agent's reply to a just-dispatched "deepfreeze" job, so a caller that
// wants a synchronous answer (status check) doesn't need a second result path.
// Returns:
//   - "frozen" / "thawed"          — agent answered
//   - "error"   + detail           — agent reported a failure (detail = its output)
//   - "unknown" + detail           — agent answered with something unrecognised,
//     or the request context was cancelled
//   - "pending"                    — no answer within deepFreezePollTimeout
func pollDeepFreezeResult(ctx context.Context, db *DB, jobID, agentID string) (status, detail string) {
	deadline := time.Now().Add(deepFreezePollTimeout)
	for time.Now().Before(deadline) {
		if results, err := db.GetDeployResultsByJobID(jobID); err == nil {
			for _, r := range results {
				if r.AgentID == agentID && !isPendingLikeStatus(r.Status) {
					return parseDeepFreezeOutput(r.Status, r.Output)
				}
			}
		}
		select {
		case <-ctx.Done():
			return "unknown", "request cancelled"
		case <-time.After(deepFreezePollInterval):
		}
	}
	return "pending", ""
}

// parseDeepFreezeOutput turns a deploy_results (status, output) pair from a
// "query_df" job into a (status, detail) verdict. resultStatus is the agent's
// own status field ("ok" on success); output is "FROZEN"/"THAWED" for a good
// query, or an error string otherwise.
func parseDeepFreezeOutput(resultStatus, output string) (status, detail string) {
	if resultStatus != "ok" {
		return "error", output
	}
	switch strings.ToUpper(strings.TrimSpace(output)) {
	case "FROZEN":
		return "frozen", ""
	case "THAWED":
		return "thawed", ""
	default:
		return "unknown", output
	}
}
