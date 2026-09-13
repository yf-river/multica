// config.go：钉钉安装配置（channel_installation.config JSON blob）的解析与
// AppSecret 解密，外加出站/令牌路径所用凭据的装配。
//
// 钉钉集成的整体形态：与 Slack 一样走 BYO（自带应用）模型——agent 所有者或
// 工作区管理员自建钉钉 Stream 模式机器人，把它的 AppKey（client id）与
// AppSecret（client secret）贴进 Multica。每份 channel_installation 各自持有
// AppSecret、各自建立独立的 Stream 连接（监督逻辑见 dingtalk_channel.go），于是
// 同一个钉钉组织里可以并存多个拥有不同 bot 身份的 agent。
//
// 每个安装的 Stream 连接只投递自己机器人的事件，所以入站信封上会盖上该安装的
// AppKey，resolver 按它路由（config->>'app_id'）。与 Slack 的静态 bot 令牌不同，
// 钉钉出站需要从 AppKey/AppSecret 现铸一个短命 access_token，出站路径像飞书
// 缓存 tenant_access_token 一样缓存它（token.go）。
//
// 维护说明：本包为【社区维护】。维护者名单、支持边界与退役规则发布在
// https://multica.ai/docs/community-maintained
// （apps/docs/content/docs/community-maintained.mdx，四种语言），那里是唯一
// 权威来源——所有权变更记录到那边，别写在这里。改动共享 channel 引擎时，请保持
// 本适配器可编译，并让维护者参与任何影响钉钉可见行为的改动。
package dingtalk

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// TypeDingTalk 是钉钉适配器在 channel 引擎里的类型判别值。刻意定义在这里而不
// 是 channel 核心包：注册新平台不应要求改动核心，Type 值跟随各自适配器
// （镜像 slack.TypeSlack）。
const TypeDingTalk channel.Type = "dingtalk"

// installConfig 是保存在 channel_installation.config 里的钉钉安装 JSON 结构。
// 跨平台通用列平铺在行上，一切钉钉特有字段都塞进这个不透明 blob（即文档约定
// 的 config 边界）。
//
// app_id 存的是 AppKey。对 Stream 模式机器人而言，它等于入站事件的 robotCode：
// 通用查询 GetChannelInstallationByAppID（config->>'app_id'）与 (channel_type,
// app_id) 唯一索引把入站事件的 robotCode 映射回安装，同一钉钉组织里的多个
// 机器人（多个 agent）因此互不混淆。
//
// robot_code 显式存一份，供出站发送 API 使用（oToMessages.batchSend /
// groupMessages.send 都要带它）；它等于 app_id，但单独存储，出站路径就不用假定
// 两者恒等。
//
// app_secret_encrypted 是 base64 编码的 secretbox 密文，绝不落明文（与飞书
// app_secret_encrypted、Slack bot_token_encrypted 同款）。AppKey 本身不是机密，
// 以明文 app_id 存放——与飞书把 app_id 明文放在 app_secret_encrypted 旁边一致。
type installConfig struct {
	AppID              string `json:"app_id"`
	RobotCode          string `json:"robot_code,omitempty"`
	AppSecretEncrypted string `json:"app_secret_encrypted"`
}

// robotCodeOrAppID 返回显式存的 robot_code，没有就回退到 app_id（Stream 模式
// 机器人两者相等；老配置只存了 app_id）。
func (c installConfig) robotCodeOrAppID() string {
	if c.RobotCode != "" {
		return c.RobotCode
	}
	return c.AppID
}

// credentials 是出站发送器与 access_token 缓存实际运行所依赖的「已解密」形态。
// 刻意不携带安装所属身份（工作区 / agent / 安装者）：每条消息按需由 Router 的
// InstallationResolver 现查，与飞书、Slack 适配器一致。
type credentials struct {
	AppKey    string
	RobotCode string
	AppSecret string
}

// Decrypter 把存库密文还原成明文。生产接线注入基于 secretbox 的实现；测试注入
// 恒等解密器，或直接给 nil——nil 时把库里字节当明文用。
type Decrypter func(ciphertext []byte) (plaintext []byte, err error)

// decodeCredentials 解析每份安装的 config blob 并解密其中 AppSecret，给出站与
// 令牌路径使用。整个钉钉配置 JSON 只在本函数被解读。
func decodeCredentials(raw json.RawMessage, decrypt Decrypter) (credentials, error) {
	if len(raw) == 0 {
		return credentials{}, errors.New("dingtalk: empty installation config")
	}
	var cfg installConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return credentials{}, fmt.Errorf("decode dingtalk installation config: %w", err)
	}
	appSecret, err := decryptToken(cfg.AppSecretEncrypted, decrypt)
	if err != nil {
		return credentials{}, fmt.Errorf("decrypt app secret: %w", err)
	}
	return credentials{
		AppKey:    cfg.AppID,
		RobotCode: cfg.robotCodeOrAppID(),
		AppSecret: appSecret,
	}, nil
}

// decryptToken 先把存储的密文做 base64 解码——容忍 PostgreSQL 的
// encode(...,'base64') 在长文本里插入的 MIME 换行——再交给注入的 Decrypter
// 解密。存的是空串就解出空密钥；Decrypter 为 nil 时把解码结果当明文用（测试
// 便利）。
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

// stripWhitespace 剥掉字符串里的 ASCII 空白，使「每 64 字符换一行的 MIME 版
// base64」与不换行版本解码出同样的字节。
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
