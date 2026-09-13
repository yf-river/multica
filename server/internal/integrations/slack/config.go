// config.go：Slack 安装级配置（channel_installation.config 里的 JSON blob）的
// 解析与令牌解密，外加给管理 API 展示用的非机密子集。本文件是包内唯一解读这
// 份 Slack 专属 JSON 的地方，想改配置结构只需动这里。
//
// Slack 集成整体走 BYO（自带应用）模型（MUL-3666）：每个 agent 的 Slack 应用
// 由工作区管理员自建并安装，把机器人令牌（xoxb-）和应用级令牌（xapp-）填进
// Multica。每份 channel_installation 因此各自持有应用级令牌、各自建立独立的
// Socket Mode 连接（连接监督逻辑见 slack_channel.go），同一个 Slack 工作区里
// 可以同时存在多个拥有不同机器人身份的 agent。安装按真实 Slack 应用 id 路由
// （config->>'app_id' == 入站事件的 api_app_id）。
//
// 入站翻译（Events API 负载 -> channel.InboundMessage）在 inbound.go；出站回复
// 路径（chat.postMessage + Markdown->mrkdwn + 线程回复）在 channel.go。设计
// 参照 Hermes Agent 中经过验证的 Slack 适配器。
package slack

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// installConfig 是保存在 channel_installation.config 里的 Slack 安装配置 JSON
// 结构。跨平台通用列仍然平铺在行上，一切 Slack 特有的东西都塞进这个不透明
// blob（这就是文档约定的 config 边界）。
//
// app_id 是真实 Slack 应用 id（从 xapp- 令牌文本里解析出来的那一位）。它是
// 本安装的路由键：通用查询 GetChannelInstallationByAppID（config->>'app_id'）
// 配合 (channel_type, app_id) 唯一索引，把入站事件的 api_app_id 映射回它的
// 安装，这样同一 Slack 工作区里多应用（多 agent）互不混淆。team_id 只留作
// 展示用。
//
// bot_token_encrypted（xoxb-，出站 Web API chat.postMessage 用）和
// app_token_encrypted（xapp-，本安装自己的 Socket Mode 连接用）都以 base64
// 编码的 secretbox 密文存放，绝不落明文（与飞书 app_secret_encrypted 同款
// 做法）。两者都是管理员在 BYO 安装时手动粘贴进来的。
type installConfig struct {
	AppID             string `json:"app_id"`
	TeamID            string `json:"team_id,omitempty"`
	BotUserID         string `json:"bot_user_id,omitempty"`
	BotTokenEncrypted string `json:"bot_token_encrypted"`
	AppTokenEncrypted string `json:"app_token_encrypted,omitempty"`
}

// credentials 是出站发送器实际运行所依赖的「已解密」形态。这里刻意不携带
// 安装所属的身份信息（工作区 / agent / 安装者）：每条消息按需由 Router 的
// InstallationResolver 现查，与飞书适配器的做法一致。
type credentials struct {
	TeamID    string
	BotUserID string
	BotToken  string
}

// Decrypter 把存库的密文还原成明文。生产接线注入基于 secretbox 的实现；
// 测试注入恒等解密器，或者直接给 nil——nil 时把库里存的字节当明文用。
type Decrypter func(ciphertext []byte) (plaintext []byte, err error)

// decodeCredentials 解析每份安装的 config blob 并解密其中令牌，返回出站发送
// 要用的 credentials。整个 Slack 配置 JSON 只在本函数被解读，config 字段格式
// 变更的兼容层也集中在这里。
func decodeCredentials(raw json.RawMessage, decrypt Decrypter) (credentials, error) {
	if len(raw) == 0 {
		return credentials{}, errors.New("slack: empty installation config")
	}
	var cfg installConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return credentials{}, fmt.Errorf("decode slack installation config: %w", err)
	}
	botToken, err := decryptToken(cfg.BotTokenEncrypted, decrypt)
	if err != nil {
		return credentials{}, fmt.Errorf("decrypt bot token: %w", err)
	}
	// team_id 可能为空（老配置没存）；回退成 app_id，保证路由键始终有值。
	teamID := cfg.TeamID
	if teamID == "" {
		teamID = cfg.AppID
	}
	return credentials{
		TeamID:    teamID,
		BotUserID: cfg.BotUserID,
		BotToken:  botToken,
	}, nil
}

// PublicConfig 是安装配置中不含机密的那一子集，可以安全地暴露在管理 API 上
// （加密后的 bot 令牌永远不会出现在这里）。
type PublicConfig struct {
	AppID     string
	TeamID    string
	BotUserID string
}

// DecodePublicConfig 从存储的 config blob 里取出可安全展示的字段。解码失败
// 返回零值 PublicConfig 而不是错误：管理列表即使读不到 config 也仍应渲染出
// 该行的身份列（app_id、工作区等）。
func DecodePublicConfig(raw json.RawMessage) PublicConfig {
	var cfg installConfig
	_ = json.Unmarshal(raw, &cfg)
	teamID := cfg.TeamID
	if teamID == "" {
		teamID = cfg.AppID
	}
	return PublicConfig{AppID: cfg.AppID, TeamID: teamID, BotUserID: cfg.BotUserID}
}

// decryptToken 先把存储的密文做 base64 解码——容忍 PostgreSQL 的
// encode(...,'base64') 在长文本里插入的 MIME 换行——再交给注入的 Decrypter
// 解密。存的是空串就解出空令牌（字段当初没写）；Decrypter 为 nil 时把解码
// 结果直接当明文返回（测试便利）。
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

// stripWhitespace 剥掉字符串里的 ASCII 空白，使「每 64 字符换一行的 MIME
// 版 base64」与不换行版本解码出同样的字节。两套来源并存（PG 回填 vs Go 写入），
// 统一格式才能让两条链路可互换。
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
