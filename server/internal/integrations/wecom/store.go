// store.go —— wecom 的 ResolverSet 背后那层数据访问适配。它跑在通用化的
// channel_* 表上（migration 124），全部走共享的 sqlc 查询；本文件不写任何
// wecom 专属 SQL。
package wecom

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Store 是 wecom 的解析器与出站路径共用的读写面。它内嵌了 *db.Queries，
// 这样需要通用查询（workspace / member / agent）的调用方无需再加一层包装。
// 所有 wecom 专属的辅助方法都在内部按 channel_type = 'wecom' 限定范围。
type Store struct {
	*db.Queries
}

// NewStore 构造一个绑定到 *db.Queries 的 wecom Store。
func NewStore(q *db.Queries) *Store {
	return &Store{Queries: q}
}

// WithTx 把这个 Store 绑定到某条 pgx.Tx 事务上，让跑入库事务（ingest tx）
// 的调用方可以把 wecom 查询也放进同一事务。
func (s *Store) WithTx(tx pgx.Tx) *Store {
	return &Store{Queries: s.Queries.WithTx(tx)}
}

// GetInstallationByBotID 按智能机器人 id 查安装记录。路由键列就是
// config->>'app_id'，它直接存的就是 BotID（见 encodeInstallConfig），所以
// 直接复用共享的 idx_channel_installation_type_appid 索引即可。
func (s *Store) GetInstallationByBotID(ctx context.Context, botID string) (Installation, error) {
	row, err := s.Queries.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: channelTypeWecom,
		AppID:       botID,
	})
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row)
}

// GetInstallation 按主键查安装记录，并把范围限定在 channel_type = 'wecom'，
// 防止这里误用了飞书的 id 而不自知。
func (s *Store) GetInstallation(ctx context.Context, id pgtype.UUID) (Installation, error) {
	row, err := s.Queries.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          id,
		ChannelType: channelTypeWecom,
	})
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row)
}

// IsWorkspaceMember 在入站时刻重新校验一次成员关系。由于 channel_* 的外键
// 已经去掉（MUL-3515 §4），如果不查一次，过期绑定可能把消息发给已经离开
// 该工作区的用户。
func (s *Store) IsWorkspaceMember(ctx context.Context, workspaceID, userID pgtype.UUID) (bool, error) {
	_, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
