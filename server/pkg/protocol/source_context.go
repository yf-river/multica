package protocol

// 本文件职责：定义「任务源上下文」（TaskSourceContext）的线类型——任务来自
// 哪个外部系统（如 TAPD）、带哪些只读的快照与外部凭证概要。daemon 用它在任务
// 启动时把不可变的背景信息提供给 agent，而不必让 agent 自己去拉活接口。

// TaskSourceContext 描述一个任务的来源：Provider 是来源系统（如 "tapd"），
// URL 是任务原始链接；各系统专属结构与外部凭证概要分别挂在下面。
type TaskSourceContext struct {
	Provider            string                                   `json:"provider,omitempty"`
	URL                 string                                   `json:"url,omitempty"`
	TAPD                *TAPDTaskSourceContext                   `json:"tapd,omitempty"`
	ExternalCredentials map[string]TaskExternalCredentialContext `json:"external_credentials,omitempty"`
}

// TAPDTaskSourceContext 是 TAPD 来源任务的只读快照（拉取时的字段摘录）。
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

// TaskExternalCredentialContext 描述任务可用的某个外部服务凭证概要——不带机密，
// 只带归属与解析信息，供 agent 与 daemon 判断该用哪个已配凭证去连哪个服务。
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
