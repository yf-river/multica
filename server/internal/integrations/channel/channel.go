package channel

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
)

// 文件角色：全渠道通用契约的两个核心定义——Type（平台判别值）与 Channel 接口
// （每个 IM 集成必须实现的那五个方法）。各平台适配器（feishu、slack、telegram…）
// 把它们的所有平台细节都关在这五个方法后面；引擎/核心只面向这组方法编程，
// 从不接触平台 SDK 或线上格式。Config 与 Factory 是配套的"按安装构建"约定。

// Type 标识一个入站渠道平台——Registry 的键、也是通用 channel_* 表 channel_type 列里
// 持久化的值。用平台的小写 slug（"feishu"、"slack"、"wecom"…）；保持稳定，它是持久数据。
type Type string

const (
	// TypeFeishu 是飞书/Lark 适配器——第一阶段的唯一实现。它同时服务大陆飞书云与
	// Lark 国际云；云（region）是安装级配置，不是独立的 Type。
	TypeFeishu Type = "feishu"
)

// Channel 是每个 IM 集成都必须实现的平台无关契约。适配器把**所有**平台细节藏在这五个
// 方法后面：核心的 supervisor 调 Connect/Disconnect 管理链路、调 Send 发送出站回复、
// 读 Capabilities 决定怎么渲染；它从不直接碰平台 SDK 或线上格式。
//
// 入站消息刻意**不在**这个接口上：Channel 通过构造时建立的接线（adapter 自己持有收消息
// 循环）把规范化后的 InboundMessage 推给核心 router；核心不会轮询 Channel 要消息。
type Channel interface {
	// Type 返回平台判别值。它必须等于注册时的 Type，并且在实例生命周期内保持不变。
	Type() Type

	// Connect 建立平台链路（例如拨出站 WebSocket 长连接，或启动入站 HTTP 监听），
	// 然后**阻塞**运行收消息循环，直到链路结束。连接形态是实现的选择、对核心不可见。
	// 返回值语义：
	//
	//   - ctx 被取消时返回 nil（优雅停机 / 租约丢失）；
	//   - 链路断开且本地无法恢复时返回非 nil 错误——supervisor 把它当作"这次连接失败"
	//     并按指数退避重连。
	//
	// Connect 运行期间，adapter 靠构造时捕获的 InboundHandler（Config.Handler）投递每条
	// 入站消息。Send 可能在连接存活期间被另一个 goroutine 并发调用。实现**必须**容忍
	// 在不同 ctx 上被反复调用 Connect：supervisor 可能 Connect → 返回 → 退避后再 Connect。
	Connect(ctx context.Context) error

	// Disconnect 拆除平台链路并释放其资源。在失败的 Connect 之后调用是安全的，多次调用
	// 也安全；已经断开的 Channel 返回 nil。
	Disconnect(ctx context.Context) error

	// Send 投递一条出站消息，返回平台侧给这条已投递消息的标识。非 nil 错误只留给真正的
	// 投递失败（网络、鉴权、限流）——这些调用方才可能重试。
	Send(ctx context.Context, out OutboundMessage) (SendResult, error)

	// Capabilities 声明这个 Channel 支持什么。它是无副作用、结果稳定的纯声明；调用方读它
	// 选择渲染方式、自行降级（本包不做任何降级——见 Capability 文档）。
	Capabilities() Capability
}

// Config 是 Factory 消费的规范化"按安装"配置。Type 是平台判别值；Raw 是平台自己的
// 凭据/配置 JSON（飞书的 app_id / 加密 app_secret / tenant_key / region，Slack 的
// bot/app token…），原样携带、不为它加字段，这样核心基础永远不会长出"每平台一个字段"。
// 它直接对应 channel_installation 一行的 channel_type 列 + JSONB config
// （MUL-3515 决策 §3）。
type Config struct {
	Type Type
	Raw  json.RawMessage

	// ID 是构建这条 Channel 所依据的 channel_installation.id。构建路径没有安装记录时为
	// 零值（目前代码里没有这种路径，但 Factory 实现应容忍它而不是假定 Valid）。企微用它
	// 把每条连接的 wsSender 放进共享注册表供 OutboundReplier 按 id 查找；飞书和 Slack
	// 目前不读它。
	ID pgtype.UUID

	// Handler 是引擎注入的共享入站入口：构建好的 Channel 靠它把规范化 InboundMessage
	// 送进核心（见 InboundHandler）。Factory 捕获它并在 Channel 的收消息循环里调用它。
	// 当 Channel 仅为出站 Send 而构建（不需要入站投递）时它可以是 nil。
	Handler InboundHandler
}

// Factory 从按安装的 Config 构建一个 Channel。每个适配器在它的 Type 下恰好注册一个
// Factory；Registry 调它来实例化每安装一条的 Channel。Factory 应当校验 Raw，
// 出错就返回错误，而不是交回一个半成品 Channel。
type Factory func(cfg Config) (Channel, error)
