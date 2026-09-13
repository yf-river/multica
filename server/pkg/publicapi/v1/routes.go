// Package publicapiv1 拥有稳定的 Multica 公共 API v1 契约。
//
// 同一份资源契约经由不同的信任面暴露。Plugin API 是第一个消费者：它接受
// 安装/回调 token，并套用 manifest 里声明的 scope。未来的用户/PAT 入口应复用
// 这些路径、DTO 与操作语义，而不是另起一套并行的 handler。
package publicapiv1

import "net/http"

// BasePath 是所有 v1 路由共用的 URL 前缀。
const BasePath = "/v1"

// 以下是 v1 资源路由的稳定路径常量。{issue_ref}/{scope}/{key} 是路径参数，
// 各处 handler 与 OpenAPI 文档都引用这些常量，避免路径与文档各自漂移。
const (
	PathContext       = "/context"
	PathIssue         = "/issues/{issue_ref}"
	PathIssueComments = "/issues/{issue_ref}/comments"
	PathStorageScope  = "/storage/{scope}"
	PathStorageValue  = "/storage/{scope}/{key}"
)

// ContractKind 标记某条操作属于哪一类契约。
type ContractKind string

// 两种契约：shared_resource 是「可经多种凭证类型、按各鉴权面自己的授权」暴露的
// Multica 资源契约；plugin_extension 属于插件安装侧，而非通用 Multica 资源 API。
const (
	// ContractSharedResource 是可以经多种凭证类型、通过各鉴权面自己的授权
	// 暴露的 Multica 资源契约。
	ContractSharedResource ContractKind = "shared_resource"
	// ContractPluginExtension 属于插件安装侧，而非通用 Multica 资源 API。
	ContractPluginExtension ContractKind = "plugin_extension"
)

// Operation 是「目前已实现的 v1 切片」的能力台账。OpenAPI 测试把这个注册表
// 钉在 openapi.yaml 上，让路由、scope 与文档不能各自漂移。
type Operation struct {
	Method   string
	Path     string
	Contract ContractKind
	Policy   OperationPolicy
}

var sharedCredentials = []CredentialKind{
	CredentialUserOAuth,
	CredentialPersonalAccess,
	CredentialPluginInstall,
	CredentialPluginInvocation,
}

var pluginCredentials = []CredentialKind{
	CredentialPluginInstall,
	CredentialPluginInvocation,
}

var sharedRateLimits = []RateLimitProfile{RateLimitUserDefault, RateLimitPluginStrict}
var pluginRateLimits = []RateLimitProfile{RateLimitPluginStrict}

// Operations 是当前 v1 的全部操作清单：方法 + 路径 → 契约种类 + 鉴权/风险/
// 审计/限流策略。routes 注册与权限中间件据此装配。
var Operations = []Operation{
	{Method: http.MethodGet, Path: PathContext, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskRead, Audit: AuditNotRequired, RateLimits: pluginRateLimits}},
	{Method: http.MethodGet, Path: PathIssue, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: sharedCredentials, Scope: "issues:read", Risk: RiskRead, Audit: AuditPlanned, RateLimits: sharedRateLimits}},
	{Method: http.MethodPatch, Path: PathIssue, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: sharedCredentials, Scope: "issues:write", Risk: RiskContentWrite, Audit: AuditPlanned, RateLimits: sharedRateLimits}},
	{Method: http.MethodGet, Path: PathIssueComments, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: sharedCredentials, Scope: "comments:read", Risk: RiskRead, Audit: AuditPlanned, RateLimits: sharedRateLimits}},
	{Method: http.MethodPost, Path: PathIssueComments, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: sharedCredentials, Scope: "comments:write", Risk: RiskContentWrite, Audit: AuditPlanned, RateLimits: sharedRateLimits}},
	{Method: http.MethodGet, Path: PathStorageScope, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskRead, Audit: AuditNotRequired, RateLimits: pluginRateLimits}},
	{Method: http.MethodGet, Path: PathStorageValue, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskRead, Audit: AuditNotRequired, RateLimits: pluginRateLimits}},
	{Method: http.MethodPut, Path: PathStorageValue, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskContentWrite, Audit: AuditPlanned, RateLimits: pluginRateLimits}},
	{Method: http.MethodDelete, Path: PathStorageValue, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskContentWrite, Audit: AuditPlanned, RateLimits: pluginRateLimits}},
}
