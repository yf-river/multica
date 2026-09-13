// types.go：飞书接入的领域类型与常量定义——open_id、chat_id、聊天类型、
// 安装状态、地域（云）、丢弃原因、绑定令牌 TTL 等。统一用命名类型包一层
// 裸 string，防止跨渠道传参时拿错 id。
package lark

import "time"

// OpenID 是 Lark 用户在“某个安装”维度下的标识。同一个 App 的不同安装会给
// 同一个真人不同的 open_id；跨安装合并身份需要 union_id（Phase 2）。用命名
// 类型而非裸 string，使调用方不可能把 Multica 用户 UUID 误当成 Lark open_id
// 传进来。
type OpenID string

// ChatID 标识一个飞书会话（单聊 p2p 或群聊）。一个 ChatID 通过
// lark_chat_session_binding 对应到一个 Multica chat_session。
type ChatID string

// ChatType 区分单聊（与 Bot 的一对一私聊）与群聊。DB 列
// lark_chat_session_binding.lark_chat_type 也约束为同样的两个取值。
type ChatType string

const (
	// ChatTypeP2P 表示与 Bot 的单聊会话。
	ChatTypeP2P ChatType = "p2p"
	// ChatTypeGroup 表示群聊会话。
	ChatTypeGroup ChatType = "group"
)

// InstallationStatus 镜像 lark_installation.status 的 CHECK 约束。
// revoked 的安装不再接收任何事件：其 WebSocket 会被拆除，入站事件带审计行丢弃。
type InstallationStatus string

const (
	// InstallationActive 表示安装处于激活可用状态。
	InstallationActive InstallationStatus = "active"
	// InstallationRevoked 表示安装已撤销，事件不再被处理。
	InstallationRevoked InstallationStatus = "revoked"
)

// Region 标识某条安装落在 Lark 开放平台的哪一朵云上。飞书（中国大陆，
// open.feishu.cn / accounts.feishu.cn）与 Lark（国际版，open.larksuite.com /
// accounts.larksuite.com）是各自独立的云、主机不同；单个 Multica 部署同时服务
// 两者，方式是每次按该值解析出对应主机，而不是用部署级环境变量。镜像
// lark_installation.region 的 CHECK 约束（migration 116）——两边要保持同步。
type Region string

const (
	// RegionFeishu 表示中国大陆飞书云。
	RegionFeishu Region = "feishu"
	// RegionLark 表示国际版 Lark 云。
	RegionLark Region = "lark"
)

// larkInternationalOpenBaseURL 是 Lark 国际版云的开放平台主机。中国大陆飞书
// 对应 defaultLarkBaseURL（"https://open.feishu.cn"，定义在 http_client.go），
// 它同时兼任 WS 长连接引导主机（/callback/ws/endpoint 的 POST 也发往同一个
// 开放平台主机）。
const larkInternationalOpenBaseURL = "https://open.larksuite.com"

// OpenPlatformBaseURL 把地域映射成对应的开放平台主机——既是 REST API
// （http_client.go）的基础 URL，也是 WebSocket /callback/ws/endpoint 引导
// （ws_endpoint.go）的基础 URL。地域未设置或未知时回退到飞书（中国大陆），
// 这也是所有未带地域的早期安装行默认携带的值。
func (r Region) OpenPlatformBaseURL() string {
	if r == RegionLark {
		return larkInternationalOpenBaseURL
	}
	return defaultLarkBaseURL
}

// RegionOrDefault 把存储的 region 字符串（源自 lark_installation.region 列）
// 归一化为 Region；空值或无法识别的值默认按飞书处理，这样坏数据行永远不会
// 解析出空主机（或触发违反 CHECK 的写入）。导出是因为 router 的 WS 凭据
// provider（package main）需要从原始行还原出凭据。
func RegionOrDefault(s string) Region {
	if Region(s) == RegionLark {
		return RegionLark
	}
	return RegionFeishu
}

// DropReason 枚举入站管线写入 lark_inbound_audit.drop_reason 的丢弃原因类别。
// DB 列是开放 TEXT，新增原因无需迁移；调用方应尽量复用这些常量，以保持
// 看板/查询口径一致。
//
// 所有 drop_reason 都“不带消息正文”记录——见 MUL-2671 §4.7（丢弃审计策略）。
type DropReason string

const (
	// DropReasonUnboundUser —— 发送者的 open_id 在本安装下没有对应的
	// lark_user_binding 行。Bot 回复绑定卡片；消息本身不入库。
	DropReasonUnboundUser DropReason = "unbound_user"

	// DropReasonNonWorkspaceMember —— 发送者能解析成 Multica 用户，但该用户
	// 不是本安装所属 workspace 的成员。Bot 回复“不在该空间”提示；消息本身不入库。
	DropReasonNonWorkspaceMember DropReason = "non_workspace_member"

	// DropReasonNotAddressedInGroup —— 消息发在群聊里，但没有 @ Bot，也不是
	// 对 Bot 卡片的回复。群聊只摄入明确发给 Bot 的消息。
	DropReasonNotAddressedInGroup DropReason = "not_addressed_in_group"

	// DropReasonDuplicate —— message_id 已在 lark_inbound_message_dedup 中。
	// WebSocket 重连会重放事件，这正是幂等路径。
	DropReasonDuplicate DropReason = "duplicate"

	// DropReasonRevokedInstallation —— installation.status='revoked'。
	// WS 连接此时本应已关闭；此原因兜住拆除期间仍落在管线里的在途事件。
	DropReasonRevokedInstallation DropReason = "revoked_installation"

	// DropReasonInvalidEvent —— 载荷没通过 schema 校验（缺少必填字段、
	// 事件类型与本 hook 不符等）。
	DropReasonInvalidEvent DropReason = "invalid_event"
)

// BindingTokenTTL 限制成员绑定令牌的有效期上限。DB 层 channel_binding_token
// 的 CHECK（expires_at <= created_at + INTERVAL '15 minutes'）施加同样的上限，
// 配置错误的调用方或手工插入的 SQL 行也无法突破。产品值若调整，两处要同步改。
const BindingTokenTTL = 15 * time.Minute
