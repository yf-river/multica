package protocol

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
