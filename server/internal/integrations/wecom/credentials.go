// credentials.go —— 凭证解析默认实现（CredentialsResolver）。
//
// 职责：把安装记录（Installation）里加密存放的智能机器人密钥解成明文，
// 供 WebSocket 订阅帧使用。wecom 包启动时注入一把 secretbox.Box 负责
// 加解密；安装记录本身绝不保存明文，所以任何需要真实密钥的调用方都要
// 经由本文件拿到。
package wecom

import (
	"errors"
	"fmt"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

// CredentialsResolver 负责按次生成带明文密钥的 InstallationCredentials，
// 也就是把 Installation 携带的密文解开。wecom 包启动时注入具体实现，
// 单元测试则注入假实现。
type CredentialsResolver interface {
	Credentials(inst Installation) (InstallationCredentials, error)
}

// SecretboxCredentialsResolver 用一把所有 wecom 安装共用的 secretbox.Box
// 来解密智能机器人密钥。密钥轮换策略与飞书 / Slack 一致：改了环境变量
// MULTICA_WECOM_SECRET_KEY 之后，存量数据都需要重加密迁移后才能继续读。
type SecretboxCredentialsResolver struct {
	Box *secretbox.Box
}

// NewSecretboxCredentialsResolver 校验 Box 非空，避免启动接线时漏配加密盒、
// 导致系统悄悄退化成明文传输。
func NewSecretboxCredentialsResolver(box *secretbox.Box) (*SecretboxCredentialsResolver, error) {
	if box == nil {
		return nil, errors.New("wecom: SecretboxCredentialsResolver requires a non-nil secretbox.Box")
	}
	return &SecretboxCredentialsResolver{Box: box}, nil
}

// Credentials 返回某条 Installation 对应的、带明文的 InstallationCredentials。
// 返回的是值类型，可以放心传递，不会和安装记录里的加密数据块产生别名。
func (r *SecretboxCredentialsResolver) Credentials(inst Installation) (InstallationCredentials, error) {
	if r.Box == nil {
		return InstallationCredentials{}, errors.New("wecom: credentials resolver has no secretbox")
	}
	secret, err := r.Box.Open(inst.SecretEncrypted)
	if err != nil {
		return InstallationCredentials{}, fmt.Errorf("wecom: decrypt secret: %w", err)
	}
	return InstallationCredentials{
		BotID:  inst.BotID,
		Secret: string(secret),
	}, nil
}
