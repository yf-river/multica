// byo_install.go：BYO（自带应用）安装的校验与落库——管理员把钉钉 Stream 模式
// 机器人的 AppKey（client id）与 AppSecret（client secret）贴进 Multica 时，
// 先实时验凭据，再把 AppSecret 加密入库。
//
// 与 OAuth 授权码安装不同，BYO 没有「点授权」这一步：用户自建机器人后把两个值
// 直接贴过来。入库前本文件做三件事：1) 实时铸一枚 access_token 证明 AppKey/
// AppSecret 这对凭据真实有效（Stream 机器人的 robotCode == AppKey，无需再查身
// 份）；2) AppSecret 加密落盘；3) 持久化安装记录（每个 agent 一个机器人，
// 回收该 AppKey 的「已死」旧主、对仍在线的旧主回准确的冲突提示，见 install.go）。
package dingtalk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ErrInvalidAppKey / ErrInvalidAppSecret 由 RegisterBYO 在贴来的凭据为空时返回。
// handler 把它们映射成 400，让安装弹窗能给出精确提示而不是一句笼统失败。
var (
	ErrInvalidAppKey    = errors.New("dingtalk: AppKey (client id) is required")
	ErrInvalidAppSecret = errors.New("dingtalk: AppSecret (client secret) is required")
	// ErrCredentialValidation 包装一次「实时铸令牌被拒」——即贴来的 AppKey/
	// AppSecret 不对。这是用户错误（凭据无效），handler 映射成 400；而内部加
	// 密/持久化失败则必须按 500 上报。
	ErrCredentialValidation = errors.New("dingtalk: could not validate credentials")
)

// RegisterBYOParams 是一次 BYO 安装的入参：这个机器人代表哪个 agent、谁在安装、
// 以及用户从自己的钉钉 Stream 模式机器人上贴来的两个凭据。
type RegisterBYOParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	AppKey      string // client id——robotCode 与 access_token 铸造都用它
	AppSecret   string // client secret——铸 access_token 用（静态加密）
}

// RegisterBYO 把用户自带的（BYO）钉钉机器人装到一个 agent 名下。
//
// 流程：用户自建钉钉 Stream 模式机器人后，把 AppKey（client id）+ AppSecret
// （client secret）贴进来。这里没有 OAuth 授权码交换——我们通过现铸一枚
// access_token 来实时验证凭据（能铸出来就证明这对 AppKey/AppSecret 有效），把
// AppSecret 加密落盘，最后持久化安装记录。
//
// 因为每个 BYO 机器人都是独立的钉钉应用（独立 bot 身份），同一个钉钉组织可以并
// 存多个（每个 agent 一个）。存下的 config 把 AppKey 当路由键（config->>'app_id'，
// 对 Stream 机器人等于入站事件的 robotCode）；persistInstall 以 (workspace,
// agent) 为主键落行、回收该 AppKey 的「已死」旧主（已吊销占位行、或所属工作区/
// agent 已被删除的孤儿），让机器人能迁到当前 agent；对仍在线的旧主则用准确的冲突
// 哨兵拒绝。消费所存凭据的 Stream 连接在 dingtalk_channel.go；本方法只负责持久化
// 安装。
func (s *InstallService) RegisterBYO(ctx context.Context, p RegisterBYOParams) (db.ChannelInstallation, error) {
	appKey := strings.TrimSpace(p.AppKey)
	appSecret := strings.TrimSpace(p.AppSecret)
	if appKey == "" {
		return db.ChannelInstallation{}, ErrInvalidAppKey
	}
	if appSecret == "" {
		return db.ChannelInstallation{}, ErrInvalidAppSecret
	}

	// 实时验凭据：能铸出 access_token 就证明这对 AppKey/AppSecret 是真的、已装
	// 好。Stream 机器人的 robotCode 等于 AppKey，所以不需要再做单独的身份查询。
	validationCtx, cancel := context.WithTimeout(ctx, s.validationTimeout)
	defer cancel()
	if _, _, err := fetchAccessToken(validationCtx, s.httpClient, s.apiBase, appKey, appSecret); err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("%w: %v", ErrCredentialValidation, err)
	}

	sealedSecret, err := s.box.Seal([]byte(appSecret))
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encrypt dingtalk app secret: %w", err)
	}
	cfgJSON, err := json.Marshal(installConfig{
		AppID:              appKey,
		RobotCode:          appKey,
		AppSecretEncrypted: base64.StdEncoding.EncodeToString(sealedSecret),
	})
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encode dingtalk installation config: %w", err)
	}

	// 每个 agent 落一条机器人（行以 workspace + agent 为主键）。config 里带的
	// AppKey 供入站路由；persistInstall 会回收该 AppKey 的「已死」旧主让机器人
	// 可迁移，对仍在线的旧主回准确的冲突哨兵。
	return s.persistInstall(ctx, installPersist{
		wsID:        p.WorkspaceID,
		agentID:     p.AgentID,
		installerID: p.InitiatorID,
		appIDKey:    appKey,
		configJSON:  cfgJSON,
	})
}
