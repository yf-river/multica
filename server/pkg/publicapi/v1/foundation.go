package publicapiv1

// 本文件职责：定义 v1 契约的「地基」常量与类型——幂等/并发控制的请求头名字、
// 分页上下限，以及认证凭证种类、Actor、风险等级、限流档位、审计状态与操作策略
// 这些被路由清单与鉴权中间件共同引用的抽象。

// HeaderIdempotencyKey 是幂等键请求头名；HeaderIfMatch 用于乐观并发控制；
// MaxIdempotencyBytes 是幂等键长度上限；DefaultPageSize / MaxPageSize 是
// 游标分页的默认与最大每页条数。
const (
	HeaderIdempotencyKey = "Idempotency-Key"
	HeaderIfMatch        = "If-Match"
	MaxIdempotencyBytes  = 255
	DefaultPageSize      = 50
	MaxPageSize          = 200
)

// CredentialKind 标识「给这次请求做鉴权的是哪种信任边界」。
// 资源服务收到的是一个已获授权的 actor，它们不解析 token。
type CredentialKind string

// 四种凭证：用户 OAuth、个人访问令牌、插件安装凭证、单次插件调用凭证。
const (
	CredentialUserOAuth        CredentialKind = "user_oauth"
	CredentialPersonalAccess   CredentialKind = "personal_access_token"
	CredentialPluginInstall    CredentialKind = "plugin_installation"
	CredentialPluginInvocation CredentialKind = "plugin_invocation"
)

// ActorKind 区分发起方是成员还是插件。
type ActorKind string

const (
	ActorMember ActorKind = "member"
	ActorPlugin ActorKind = "plugin"
)

// Actor 是某个鉴权面认证完凭证后，交给共享鉴权/审计/服务层的「与传输无关」
// 身份。
type Actor struct {
	Kind        ActorKind
	SubjectID   string
	WorkspaceID string
	Credential  CredentialKind
}

// RiskLevel 描述某操作的风险档位，供审计与策略分级使用。
type RiskLevel string

const (
	RiskRead         RiskLevel = "read"
	RiskContentWrite RiskLevel = "content_write"
	RiskHigh         RiskLevel = "high"
)

// RateLimitProfile 是限流档位名。插件面比用户默认档位更严（plugin_strict）。
type RateLimitProfile string

const (
	RateLimitUserDefault  RateLimitProfile = "user_default"
	RateLimitPluginStrict RateLimitProfile = "plugin_strict"
)

// AuditStatus 区分「契约里声明了要审计」与「真的实现了审计落点」。
// 一个能力不能仅仅因为在契约台账里列了就宣称已被强制审计。
type AuditStatus string

const (
	AuditNotRequired AuditStatus = "not_required"
	AuditPlanned     AuditStatus = "planned"
	AuditEnforced    AuditStatus = "enforced"
)

// OperationPolicy 在路由契约旁边记录鉴权与可观测性要求。
// 各鉴权面自己的中间件负责强制凭证种类与限流档位；资源层鉴权负责
// workspace 与 scope。
type OperationPolicy struct {
	Credentials []CredentialKind
	Scope       string
	Risk        RiskLevel
	Audit       AuditStatus
	RateLimits  []RateLimitProfile
}

// PageInfo 是游标分页集合通用的响应元数据。游标不透明，
// 且只对「已鉴权的 actor + 本次查询过滤条件」有效。
type PageInfo struct {
	NextCursor string `json:"next_cursor,omitempty"`
}
