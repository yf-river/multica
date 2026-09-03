package handler

import (
	"encoding/json"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// lifeCognitionContextType is the discriminator written by the Life worker.
// A task with this context has no issue/chat/autopilot parent and must stay
// inside the governed Life execution boundary.
const lifeCognitionContextType = "life_cognition"

// lifeCognitionTaskContext is the claim payload stored in agent_task_queue.
// The token and numeric context version fence every later Life write against
// lease recovery, deletion, and a competing claim.
type lifeCognitionTaskContext struct {
	Type           string          `json:"type"`
	JobID          string          `json:"job_id"`
	JobType        string          `json:"job_type"`
	WorkspaceID    string          `json:"workspace_id"`
	UserID         string          `json:"user_id"`
	ClaimToken     string          `json:"claim_token"`
	ContextVersion int64           `json:"context_version_number"`
	Input          json.RawMessage `json:"input"`
}

func lifeCognitionContextForTask(task db.AgentTaskQueue) (lifeCognitionTaskContext, bool) {
	if !lifeCognitionContextDeclared(task) || task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid {
		return lifeCognitionTaskContext{}, false
	}
	var context lifeCognitionTaskContext
	if json.Unmarshal(task.Context, &context) != nil || context.Type != lifeCognitionContextType {
		return lifeCognitionTaskContext{}, false
	}
	return context, true
}

// lifeCognitionContextDeclared distinguishes a governed task from an ordinary
// issue-less task before the full context is decoded. A declared-but-invalid
// Life envelope must be rejected at claim time instead of falling through to
// quick-create and receiving repository tools.
func lifeCognitionContextDeclared(task db.AgentTaskQueue) bool {
	if task.Context == nil {
		return false
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(task.Context, &envelope); err != nil {
		return false
	}
	return envelope.Type == lifeCognitionContextType
}
