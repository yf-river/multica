// Package telegram 是 Telegram 对接 channel-agnostic 引擎的适配器。采用与 Slack 相同的
// BYO（自带机器人）模式：工作区管理员通过 @BotFather 创建机器人，把 bot token 粘贴进
// Multica，安装记录以机器人的数字 id（token 里冒号前的数字段）为键。
//
// 收消息：每个安装跑一条 getUpdates 长轮询循环（telegram_channel.go）——Telegram 不提供
// WebSocket，长轮询是唯一不需要公网 HTTPS 回调和端口转发的接入方式，与飞书/Slack
// "由 engine.Supervisor 监督的、每个安装一条持久连接"的形态对齐。
// 发消息：agent 的流式回复通过限速的 editMessageText 逐段刷新（outbound.go）；判断类
// 回复（绑定引导、离线提示）在 replier.go。
//
// 维护说明：本包由社区维护。维护者名单、支持边界与停用规则发布在
// https://multica.ai/docs/community-maintained
// （apps/docs/content/docs/community-maintained.mdx，四种语言版本）。该页面是唯一事实源——
// 所有权变更请改那里而不是这里。改动共享的 channel 引擎时请保持本适配器可编译，涉及任何
// 用户可见行为的变更需同步社区维护者。
package telegram

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// 文件角色：安装配置的形状定义与解析。Telegram 的每一条安装记录对应 channel_installation
// 表的一行，其 config 字段存一份 JSON——本文件是唯一解释这份 JSON 的地方：解开加密的
// bot token、抽出作为路由键的 bot 数字 id、产出对外展示用的非敏感子集（PublicConfig）。
// 其他文件（install.go 校验时、engine 建渠道时、管理 API 展示时）都经由这里出入配置。

// TypeTelegram 是 Telegram 适配器的渠道判别值。定义在这里（而非 channel 核心包里），
// 这样"注册一个平台"永远不需要改核心代码——与 TypeSlack 的设计一致。
const TypeTelegram channel.Type = "telegram"

// installConfig 是存在 channel_installation.config 里的 Telegram 安装配置 JSON 结构。
//
// app_id 存机器人的数字 id（字符串形式，从 token 的 "<数字id>:<密钥>" 前缀解析出来）。
// 它填充通用路由槽位 (channel_type, config->>'app_id')；收消息其实不需要按事件做路由查找
// （每条轮询循环只服务一个安装），但唯一索引仍然保证"一个 bot 在全部工作区里只映射一个
// agent"。
//
// bot_token_encrypted 是 base64 编码的 secretbox 密文，绝不存明文——与 Slack 的
// bot_token_encrypted、飞书的 app_secret_encrypted 一致。
type installConfig struct {
	AppID             string `json:"app_id"`
	BotUsername       string `json:"bot_username,omitempty"`
	BotTokenEncrypted string `json:"bot_token_encrypted"`
}

// credentials 是解码并解密后、传输层真正使用的凭据形态。安装的归属身份（工作区/agent/
// 安装者）刻意不放在这里：它像飞书、Slack 一样由 Router 按消息逐条解析。
type credentials struct {
	BotID       string
	BotUsername string
	BotToken    string
}

// Decrypter 把存储的密文转成明文。装配时注入基于 secretbox 的实现；测试注入 nil
// （此时存储的字节被当作明文直接使用）。
type Decrypter func(ciphertext []byte) (plaintext []byte, err error)

// decodeCredentials 解析单条安装的配置 JSON 并解密其中存储的 bot token。
// 它是唯一"解释 Telegram 配置 JSON"的地方，其余代码都通过它取凭据。
func decodeCredentials(raw json.RawMessage, decrypt Decrypter) (credentials, error) {
	if len(raw) == 0 {
		return credentials{}, errors.New("telegram: empty installation config")
	}
	var cfg installConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return credentials{}, fmt.Errorf("decode telegram installation config: %w", err)
	}
	token, err := decryptToken(cfg.BotTokenEncrypted, decrypt)
	if err != nil {
		return credentials{}, fmt.Errorf("decrypt bot token: %w", err)
	}
	return credentials{
		BotID:       cfg.AppID,
		BotUsername: cfg.BotUsername,
		BotToken:    token,
	}, nil
}

// PublicConfig 是安装配置里不含秘密的子集，可以安全地暴露给管理 API
// （加密后的 bot token 永不包含在内）。
type PublicConfig struct {
	BotID       string
	BotUsername string
}

// DecodePublicConfig 从存储的配置 JSON 里抽出可展示字段。解析失败返回零值 PublicConfig
// 而非错误——这样管理列表哪怕遇到一条坏配置，仍然能渲染出该行（只是字段为空）。
func DecodePublicConfig(raw json.RawMessage) PublicConfig {
	var cfg installConfig
	_ = json.Unmarshal(raw, &cfg)
	return PublicConfig{BotID: cfg.AppID, BotUsername: cfg.BotUsername}
}

// decryptToken 先把存储的密文做 base64 解码（容忍 MIME 换行包裹），再交给注入的
// Decrypter 解密。与 Slack 同名辅助函数的行为一致。decrypt 为 nil 时跳过解密，
// 密文原样当明文返回（仅测试场景）。
func decryptToken(enc string, decrypt Decrypter) (string, error) {
	if enc == "" {
		return "", nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(stripWhitespace(enc))
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	if decrypt == nil {
		return string(ciphertext), nil
	}
	plaintext, err := decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// stripWhitespace 去掉字符串里所有空白字符。为什么需要它：bot token 在数据库里可能经过
// MIME 换行折叠或人工粘贴时折行，而 base64 标准解码不允许出现空白，先剔除再解码。
func stripWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseBotID 从 bot token 里抽出机器人的数字 id。BotFather 发的 token 形如
// "<数字id>:<密钥>"；id 部分是稳定的每 bot 身份，用作安装路由键（config->>'app_id'）。
func parseBotID(token string) (string, error) {
	id, secret, ok := strings.Cut(strings.TrimSpace(token), ":")
	if !ok || id == "" || secret == "" {
		return "", ErrInvalidBotToken
	}
	// id 段必须是纯数字（BotFather 签发的 token 前缀就是纯数字的 bot id），
	// 逐字符校验，任何非数字字符都判为无效 token。
	for _, r := range id {
		if r < '0' || r > '9' {
			return "", ErrInvalidBotToken
		}
	}
	return id, nil
}
