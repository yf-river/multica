package protocol

import "encoding/json"

// TaskRepository identifies a repository available to a claimed task.
type TaskRepository struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// TaskProjectResource carries project-scoped context to the daemon. ResourceRef
// is type-specific JSON and remains opaque except to consumers that understand
// the corresponding ResourceType.
type TaskProjectResource struct {
	ID           string          `json:"id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        string          `json:"label,omitempty"`
}

// TaskIssueExecutionSpace requests one daemon-managed worktree for all tasks
// belonging to an issue.
type TaskIssueExecutionSpace struct {
	Enabled        bool   `json:"enabled"`
	IssueID        string `json:"issue_id"`
	PrimaryRepoURL string `json:"primary_repo_url"`
	Ref            string `json:"ref,omitempty"`
}

// TaskAgent is the agent execution configuration returned by task claim.
type TaskAgent struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Instructions  string            `json:"instructions"`
	Skills        []TaskSkill       `json:"skills,omitempty"`
	CustomEnv     map[string]string `json:"custom_env,omitempty"`
	CustomArgs    []string          `json:"custom_args,omitempty"`
	McpConfig     json.RawMessage   `json:"mcp_config,omitempty"`
	Model         string            `json:"model,omitempty"`
	ThinkingLevel string            `json:"thinking_level,omitempty"`
	RuntimeConfig json.RawMessage   `json:"runtime_config,omitempty"`
}

// TaskSkill is a skill and its supporting files delivered with a claimed task.
type TaskSkill struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Content     string          `json:"content"`
	Files       []TaskSkillFile `json:"files,omitempty"`
}

type TaskSkillFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// TaskUsage is token usage reported by a daemon for one provider/model pair.
type TaskUsage struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

// TaskMessage is one live execution message reported by a daemon.
type TaskMessage struct {
	Seq     int            `json:"seq"`
	Type    string         `json:"type"`
	Tool    string         `json:"tool,omitempty"`
	Content string         `json:"content,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Output  string         `json:"output,omitempty"`
}

type DaemonTaskProgressRequest struct {
	Summary string `json:"summary"`
	Step    int    `json:"step"`
	Total   int    `json:"total"`
}

type DaemonTaskMessagesRequest struct {
	Messages []TaskMessage `json:"messages"`
}

type DaemonTaskCompleteRequest struct {
	Output     string `json:"output"`
	BranchName string `json:"branch_name,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	WorkDir    string `json:"work_dir,omitempty"`
}

type DaemonTaskUsageRequest struct {
	Usage []TaskUsage `json:"usage"`
}

type DaemonTaskFailureRequest struct {
	Error         string `json:"error"`
	SessionID     string `json:"session_id,omitempty"`
	WorkDir       string `json:"work_dir,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

type DaemonTaskSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

type DaemonRuntimeRegistration struct {
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Version   string          `json:"version"`
	Status    string          `json:"status"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	ProfileID string          `json:"profile_id,omitempty"`
}

type DaemonRegisterRequest struct {
	WorkspaceID string                      `json:"workspace_id"`
	DaemonID    string                      `json:"daemon_id"`
	DeviceName  string                      `json:"device_name"`
	CLIVersion  string                      `json:"cli_version"`
	LaunchedBy  string                      `json:"launched_by"`
	Runtimes    []DaemonRuntimeRegistration `json:"runtimes"`
}

// WorkspaceResponse is the current workspace representation returned by the
// workspace API and consumed by the daemon's workspace sync.
type WorkspaceResponse struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Slug        string         `json:"slug"`
	Description *string        `json:"description"`
	Context     *string        `json:"context"`
	Settings    map[string]any `json:"settings"`
	Repos       []any          `json:"repos"`
	IssuePrefix string         `json:"issue_prefix"`
	AvatarURL   *string        `json:"avatar_url"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

type PersonalAccessTokenRenewalResponse struct {
	ExpiresAt string `json:"expires_at"`
	Renewed   bool   `json:"renewed"`
}

type RuntimeProfileResponse struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	DisplayName    string   `json:"display_name"`
	ProtocolFamily string   `json:"protocol_family"`
	CommandName    string   `json:"command_name"`
	Description    *string  `json:"description"`
	FixedArgs      []string `json:"fixed_args"`
	CreatedBy      *string  `json:"created_by"`
	Enabled        bool     `json:"enabled"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

type RuntimeProfilesResponse struct {
	WorkspaceID     string                   `json:"workspace_id"`
	RuntimeProfiles []RuntimeProfileResponse `json:"runtime_profiles"`
}

type DaemonWorkspaceReposResponse struct {
	WorkspaceID  string           `json:"workspace_id"`
	Repos        []TaskRepository `json:"repos"`
	ReposVersion string           `json:"repos_version"`
	Settings     json.RawMessage  `json:"settings,omitempty"`
}

// TaskSourceContext is the source and credential context carried by the task
// claim response from the server to the daemon.
type TaskSourceContext struct {
	Provider            string                                   `json:"provider,omitempty"`
	URL                 string                                   `json:"url,omitempty"`
	TAPD                *TAPDTaskSourceContext                   `json:"tapd,omitempty"`
	ExternalCredentials map[string]TaskExternalCredentialContext `json:"external_credentials,omitempty"`
}

type TAPDTaskSourceContext struct {
	WorkspaceID   string `json:"workspace_id,omitempty"`
	ResourceType  string `json:"resource_type,omitempty"`
	ResourceID    string `json:"resource_id,omitempty"`
	FetchProvider string `json:"fetch_provider,omitempty"`
	FetchStatus   string `json:"fetch_status,omitempty"`
	FetchError    string `json:"fetch_error,omitempty"`
	Title         string `json:"title,omitempty"`
	Summary       string `json:"summary,omitempty"`
	BodyExcerpt   string `json:"body_excerpt,omitempty"`
	Version       string `json:"version,omitempty"`
}

type TaskExternalCredentialContext struct {
	Provider      string `json:"provider"`
	Scope         string `json:"scope"`
	Inheritance   string `json:"inheritance"`
	UserID        string `json:"user_id,omitempty"`
	ProfileID     string `json:"profile_id,omitempty"`
	ProfileName   string `json:"profile_name,omitempty"`
	ProfileStatus string `json:"profile_status,omitempty"`
	MCPServer     string `json:"mcp_server,omitempty"`
	Configured    bool   `json:"configured"`
}

// ChatAttachmentMeta identifies an attachment that an agent can download with
// the task-scoped Multica credential. Stable IDs avoid exposing expiring CDN
// URLs as the task contract.
type ChatAttachmentMeta struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
}
