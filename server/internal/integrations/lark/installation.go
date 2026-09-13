// installation.go：飞书安装（Installation）的增删改查服务——负责 app_secret
// 的落库加密与 workspace 范围查询。
package lark

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// InstallationParams 是 RegistrationService 在设备流扫码安装成功后拼装出来的
// 输入形态。凭据在这里以明文提供——加密发生在 InstallationService.Upsert
// 内部，通过注入的 *secretbox.Box 完成，调用方永远看不到（也因此不会泄漏）
// 最终落进 DB 的密文。
type InstallationParams struct {
	WorkspaceID     pgtype.UUID
	AgentID         pgtype.UUID
	AppID           string
	AppSecret       string // 明文；在服务边界加密
	TenantKey       string // 可选，"" 视为 NULL
	BotOpenID       string
	InstallerUserID pgtype.UUID
	Region          Region // 哪朵云（feishu/lark）；空默认 feishu
}

// InstallationService 创建、刷新与撤销每个 agent 的飞书安装。它独占 app_secret
// 的落库静态加密，使任何调用方（包括测试 fixture）都不会误插入明文凭据——写
// lark_installation 的唯一入口就是这里。
type InstallationService struct {
	queries *ChannelStore
	box     *secretbox.Box
}

// NewInstallationService 把服务绑定到 queries 句柄和一把用于静态加密的
// secretbox 上。box 必须非 nil；即使测试或 dev 配置也拒绝退回明文存储——那
// 正是 §4.4 要求要防的回归。
func NewInstallationService(queries *db.Queries, box *secretbox.Box) (*InstallationService, error) {
	if box == nil {
		return nil, errors.New("lark: InstallationService requires a non-nil secretbox.Box")
	}
	return &InstallationService{queries: NewChannelStore(queries), box: box}, nil
}

// Upsert 新建安装，或就地刷新已有安装（按 (workspace_id, agent_id) UNIQUE
// 匹配）。重装会把状态重置回 'active'，但【不】碰 WS 租约——那是 hub 的事，
// 不是我们的。返回的行是写后状态；加密 secret 列仅为完整性而带，调用方不应
// 再把它记日志或另存。
func (s *InstallationService) Upsert(ctx context.Context, p InstallationParams) (Installation, error) {
	if err := validateInstallationParams(p); err != nil {
		return Installation{}, err
	}
	sealed, err := s.box.Seal([]byte(p.AppSecret))
	if err != nil {
		return Installation{}, fmt.Errorf("encrypt app_secret: %w", err)
	}
	return s.queries.UpsertLarkInstallation(ctx, UpsertInstallationParams{
		WorkspaceID:        p.WorkspaceID,
		AgentID:            p.AgentID,
		AppID:              p.AppID,
		AppSecretEncrypted: sealed,
		TenantKey:          textOrNull(p.TenantKey),
		BotOpenID:          p.BotOpenID,
		InstallerUserID:    p.InstallerUserID,
		Region:             string(RegionOrDefault(string(p.Region))),
	})
}

// Revoke 把状态翻成 'revoked'，让 WS hub 在下次清扫时拆掉连接，dispatcher 也
// 会丢弃任何在途事件。行被保留（不做 DELETE）以维持可查询的审计历史；之后经
// Upsert 重装会把状态原子地翻回 'active'。
func (s *InstallationService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.queries.SetLarkInstallationStatus(ctx, SetInstallationStatusParams{
		ID:     id,
		Status: string(InstallationRevoked),
	})
}

// DecryptAppSecret 返回给定安装行的明文 app_secret。WS hub 需要代表某个安装
// 向 Lark API 鉴权时使用；不要用在做只读展示的路径上。明文绝不能经任何 HTTP
// 响应往返出去。
func (s *InstallationService) DecryptAppSecret(inst Installation) (string, error) {
	plain, err := s.box.Open(inst.AppSecretEncrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt app_secret: %w", err)
	}
	return string(plain), nil
}

// GetInWorkspace 是限定 workspace 范围的查询辅助。内部调用方（Dispatcher）因为
// 事件负载只带 app_id，直接走 GetLarkInstallationByAppID；HTTP 侧调用方总是
// 知道 workspace，应使用本方法——这样伪造的、属于别的 workspace 的
// installation_id 会返回 NotFound，而不是泄漏存在性。
func (s *InstallationService) GetInWorkspace(ctx context.Context, id, workspaceID pgtype.UUID) (Installation, error) {
	row, err := s.queries.GetLarkInstallationInWorkspace(ctx, GetInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Installation{}, ErrInstallationNotFound
		}
		return Installation{}, err
	}
	return row, nil
}

// ListByWorkspace 返回该 workspace 下全部安装（active 与 revoked 都有），旧的
// 在前。状态列让 UI 能区分“已接线”和“已拆除但留档审计”。
func (s *InstallationService) ListByWorkspace(ctx context.Context, workspaceID pgtype.UUID) ([]Installation, error) {
	return s.queries.ListLarkInstallationsByWorkspace(ctx, workspaceID)
}

// ErrInstallationNotFound 表示“该 workspace 内没有匹配的行”——HTTP 层据此
// 返回 404。与裸 pgx.ErrNoRows 区分，让 handler 不必 import pgx。
var ErrInstallationNotFound = errors.New("lark installation not found")

// validateInstallationParams 校验安装参数必填项是否齐全。
func validateInstallationParams(p InstallationParams) error {
	switch {
	case !p.WorkspaceID.Valid:
		return errors.New("workspace_id is required")
	case !p.AgentID.Valid:
		return errors.New("agent_id is required")
	case !p.InstallerUserID.Valid:
		return errors.New("installer_user_id is required")
	case p.AppID == "":
		return errors.New("app_id is required")
	case p.AppSecret == "":
		return errors.New("app_secret is required")
	case p.BotOpenID == "":
		return errors.New("bot_open_id is required")
	}
	return nil
}

// textOrNull 把空字符串映射成 NULL pgtype.Text，非空则返回 Valid 值。
func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
