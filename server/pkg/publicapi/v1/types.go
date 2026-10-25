package publicapiv1

// 本文件职责：定义 v1 契约的请求/响应 DTO——Context（插件启动引导数据）、
// Issue 及其补丁请求、评论、插件 key-value 存储等线格式类型。它们独立于各
// service 层的内部 DTO，保证「服务层字段的演进」不会漏进对外线格式。

// Context 是插件特定的启动引导数据。它放在带版本的契约包里，这样服务层的字段
// 不可能泄漏进线格式。
type Context struct {
	Workspace         ContextWorkspace `json:"workspace"`
	User              *ContextUser     `json:"user,omitempty"`
	Issue             *ContextIssue    `json:"issue,omitempty"`
	Config            map[string]any   `json:"config"`
	GrantedNetDomains []string         `json:"granted_net_domains"`
	Actor             string           `json:"actor"`
}

// ContextWorkspace 是上下文里的工作区概要（id、显示名、slug）。
type ContextWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ContextUser 是上下文里发起请求的用户概要（如有）。
type ContextUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ContextIssue 是上下文里该插件本次关联的 issue 概要（如请求与某个 issue 相关）。
type ContextIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
}

// Issue 刻意镜像 Plugin API v1 已经暴露的字段，但与 handler.IssueResponse 相互
// 独立。这样给 App API 加字段就不可能顺带把公共契约意外撑宽。
type Issue struct {
	ID             string         `json:"id"`
	WorkspaceID    string         `json:"workspace_id"`
	Number         int32          `json:"number"`
	Identifier     string         `json:"identifier"`
	Title          string         `json:"title"`
	Description    *string        `json:"description"`
	Status         string         `json:"status"`
	StatusCategory string         `json:"status_category,omitempty"`
	Priority       string         `json:"priority"`
	AssigneeType   *string        `json:"assignee_type"`
	AssigneeID     *string        `json:"assignee_id"`
	CreatorType    string         `json:"creator_type"`
	CreatorID      string         `json:"creator_id"`
	ParentIssueID  *string        `json:"parent_issue_id"`
	ProjectID      *string        `json:"project_id"`
	Position       float64        `json:"position"`
	Stage          *int32         `json:"stage"`
	StartDate      *string        `json:"start_date"`
	DueDate        *string        `json:"due_date"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
	Revision       int64          `json:"revision"`
	LastActivityAt *string        `json:"last_activity_at"`
	Metadata       map[string]any `json:"metadata"`
	Properties     map[string]any `json:"properties"`
}

// PatchIssueRequest 是 PATCH /issues/{issue_ref} 的可选更新字段。
// ExpectedRevision 用于乐观并发控制：与服务端 revision 不一致时请求被拒。
type PatchIssueRequest struct {
	ExpectedRevision *int64  `json:"expected_revision,omitempty"`
	Title            *string `json:"title,omitempty"`
	Description      *string `json:"description,omitempty"`
}

// Comment 是 issue 下一条评论的线格式。
type Comment struct {
	ID         string `json:"id"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id"`
	Content    string `json:"content"`
	Type       string `json:"type"`
	ParentID   string `json:"parent_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	// DeletedAt is set only on a tombstone: a comment deleted while it still
	// had replies, kept with empty content so those replies keep their parent.
	DeletedAt string `json:"deleted_at,omitempty"`
}

// CommentListResponse 是评论列表接口的响应信封。
type CommentListResponse struct {
	Comments []Comment `json:"comments"`
}

// CreateCommentRequest 是发评论接口的请求体。ParentID 非空表示这是回复某条评论。
type CreateCommentRequest struct {
	Content  string  `json:"content"`
	ParentID *string `json:"parent_id,omitempty"`
}

// StorageKey 描述插件 key-value 存储里的一个键及其元信息。
type StorageKey struct {
	Key       string `json:"key"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt string `json:"updated_at"`
}

// StorageKeyListResponse 是列键接口的响应信封。
type StorageKeyListResponse struct {
	Keys []StorageKey `json:"keys"`
}

// StorageValueResponse 是取值接口的响应信封。
type StorageValueResponse struct {
	Value string `json:"value"`
}

// PutStorageValueRequest 是写值接口的请求体。
type PutStorageValueRequest struct {
	Value string `json:"value"`
}
