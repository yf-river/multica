package protocol

// TaskContextEnvironment is the task-scoped environment persisted by the
// daemon and consumed by the CLI inside an agent worktree.
type TaskContextEnvironment struct {
	Token        string `json:"token,omitempty"`
	ServerURL    string `json:"server_url,omitempty"`
	DaemonPort   string `json:"daemon_port,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	TaskSlot     string `json:"task_slot,omitempty"`
	AutopilotRun string `json:"autopilot_run_id,omitempty"`
	AutopilotID  string `json:"autopilot_id,omitempty"`
	QuickCreate  string `json:"quick_create_task_id,omitempty"`
}

// RuntimeLocalSkillSummary is one locally discovered skill reported by a
// daemon to the server.
type RuntimeLocalSkillSummary struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SourcePath  string `json:"source_path"`
	Provider    string `json:"provider"`
	FileCount   int    `json:"file_count"`
}
