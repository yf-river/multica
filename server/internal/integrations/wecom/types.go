// Package wecom 是「企业微信智能机器人（智能机器人 / aibot）」接入多通道
// 入站引擎的适配器（MUL-3620）。它和飞书、Slack 一样接入同一个
// engine.Router / ResolverSet。
//
// 与「内部客服号」那种走 HTTP 回调的方式不同，智能机器人走的是客户端主动
// 建立的 WebSocket 长连接：wss://openws.work.weixin.qq.com。每条安装记录
// 保存一对 (bot_id, secret)；WS 握手完成后客户端先发 aibot_subscribe 订阅，
// 之后收 aibot_msg_callback 事件，并通过同一条连接发 aibot_send_msg /
// aibot_respond_msg / aibot_upload_media_* 等指令。整个过程不需要对外暴露
// 回调 URL。
//
// 一条安装 = 一个机器人 = 一条 WebSocket。企业微信限定每个机器人同一时刻只
// 允许一条活跃连接；第二条连接会把第一条挤下线（disconnected_event）。
// 这一「单活跃连接」约束恰好和 engine.Supervisor 的 WS 租约对齐，所以跨进程
// 的多副本「每条安装至多一条活跃连接」这一不变量，不需要 wecom 特有代码就能
// 成立。
//
// 维护说明：本包属于 COMMUNITY-MAINTAINED（社区维护）。维护者名单、支持边界
// 和下线规则发布在 https://multica.ai/docs/community-maintained
// （apps/docs/content/docs/community-maintained.mdx，四个语言版本）。该页面
// 是唯一事实来源 —— 所有权变更请改那里，不要改这里。要动共享的通道路由引擎？
// 请保证本适配器仍然能编译，并且任何会影响 WeCom 可见行为的改动都要拉上本包
// 维护者一起评审。
//
// 入站方向能处理：文本、企业微信返回的语音转写文本、图片、文件、视频以及图文
// 混排（media_ingest.go 负责下载并解密回调里指向的资源）；遇到读不动的类型，
// 至少会给一条简短的回执。
//
// 出站方向有个限制：文件送达结果无法回传给当初产生该文件的 agent。
// `multica attachment upload` 在对象进存储并绑定到回复那一刻就返回了，真正往
// 群里发送要等 EventChatDone —— 那时本轮对话已经结束。因此被丢弃、被企微拒收、
// 或随连接丢失的投递，只能当面告诉群里的人（见 outbound_media.go），无法告诉
// 已经退场的 agent。把这种结果再接回下一轮对话，是单独的一块工作。
//
// 已知且有意为之的限制：出站投递要求后端只有「单副本」部署，因为唯一的发送
// 路径是进程内的 WebSocket（sendersRegistry），而 EventChatDone 也在进程内的
// events.Bus 上分发。详见 SELF_HOSTING.md。
package wecom

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TypeWecom 是企业微信智能机器人适配器的通道判别值（channel discriminator）。
// 它定义在本包、和各 wecom 特有类型放一起（而不是放 package channel），这样
// 不编译 wecom 的构建就不必连带改 channel 包；Slack 也用同样的套路，见
// TypeSlack。
const TypeWecom channel.Type = "wecom"

// channelTypeWecom 是持久化在 channel_installation 表里的字符串形态。
const channelTypeWecom = string(TypeWecom)

// InstallationStatus 与 channel_installation.status 列的取值一一对应。
type InstallationStatus string

// 安装状态的取值：
//   - InstallationActive 表示安装记录当前可用，连接与收发都走正常流程。
//   - InstallationRevoked 表示安装已被撤销，对应记录不应再被使用。
const (
	InstallationActive  InstallationStatus = "active"
	InstallationRevoked InstallationStatus = "revoked"
)

// Installation 是 channel_installation 表里一条企业微信智能机器人记录解码后
// 的内存形态。SecretEncrypted 是 secretbox 密封过的密文，绝不是明文；需要
// 明文的调用方一律走 Credentials 解析器。
type Installation struct {
	ID              pgtype.UUID
	WorkspaceID     pgtype.UUID
	AgentID         pgtype.UUID
	InstallerUserID pgtype.UUID
	Status          InstallationStatus

	// BotID 是智能机器人在企业微信管理后台创建时分配的标识。它既是
	// aibot_subscribe 订阅帧里出示的鉴权身份，也是我们持久化在
	// config->>'app_id' 里的路由键——GetChannelInstallationByAppID 靠它把
	// 入站事件归到对应安装记录上。
	BotID string

	// SecretEncrypted 是被密封过的长连接密钥。它和回调模式机器人用的
	// token / EncodingAESKey 是两回事（我们不走回调模式）。密钥轮换通过
	// 重新安装完成。
	SecretEncrypted []byte

	// BotDisplayName 是机器人在群里显示的名字。企业微信的群 @ 是以纯文本
	// 形式进来的——"@Multica Bot /new 重新分析"，整个 payload 里没有任何
	// 结构化的 @ 列表，所以只有正确识别 @ 在哪里结束，含空格的名字才不会
	// 把后面的命令一起吞掉。
	//
	// 可选项。为空时退回到 stripLeadingMentions 里的空白启发式规则，该规则
	// 对单词型名字是正确的，也是所有存量安装当前的默认行为。
	BotDisplayName string
}

// InstallationCredentials 是 WebSocket 订阅帧需要的那种带明文的视图。
// 它由凭证解析器在每次建立连接时生成，这样明文密钥就不会常驻在
// 持久的 Installation 对象上。
type InstallationCredentials struct {
	BotID  string
	Secret string
}

// installConfig 是企业微信智能机器人这类 channel_installation 行里
// config（JSONB）落盘时的形态。这里刻意让 `app_id == bot_id`，这样通用的
// idx_channel_installation_type_appid 索引和 GetChannelInstallationByAppID
// 查询都可以直接复用，不用为 wecom 单独造轮子。
type installConfig struct {
	AppID           string `json:"app_id"`
	BotID           string `json:"bot_id"`
	SecretEncrypted []byte `json:"secret_encrypted"`

	// BotDisplayName 是机器人在会话里显示的名字，仅用于识别它自己的 @ 提及
	// （见 ws_frame.go）。可选项；存量行都没有这个字段。缺省时走空白启发式，
	// 即本适配器在该字段出现之前就一直用的行为。
	BotDisplayName string `json:"bot_display_name,omitempty"`
}

// encodeInstallConfig 把 Installation 中需要落库的字段序列化成
// channel_installation.config 里存的 JSONB 数据。
func encodeInstallConfig(inst Installation) ([]byte, error) {
	if inst.BotID == "" {
		return nil, errors.New("wecom: bot_id is required")
	}
	return json.Marshal(installConfig{
		AppID:           inst.BotID,
		BotID:           inst.BotID,
		SecretEncrypted: inst.SecretEncrypted,
		BotDisplayName:  inst.BotDisplayName,
	})
}

// installationFromRow 从一条 channel_installation 行填充出一个 Installation。
// 这里信任行里的 ChannelType 已正确（调用方都已按 channel_type = 'wecom'
// 限定过查询范围），所以不再重复校验。
func installationFromRow(row db.ChannelInstallation) (Installation, error) {
	var cfg installConfig
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			return Installation{}, fmt.Errorf("wecom: decode installation config: %w", err)
		}
	}
	return Installation{
		ID:              row.ID,
		WorkspaceID:     row.WorkspaceID,
		AgentID:         row.AgentID,
		InstallerUserID: row.InstallerUserID,
		Status:          InstallationStatus(row.Status),
		BotID:           cfg.BotID,
		SecretEncrypted: cfg.SecretEncrypted,
		BotDisplayName:  cfg.BotDisplayName,
	}, nil
}
