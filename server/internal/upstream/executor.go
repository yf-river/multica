package upstream

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// ExecutorSystemKeyPrefix 把隐藏执行体按运行时区分：运行时被删除时，
// system agent 会被连带硬删，因此每次使用前都按这个 key 重新确认，而不是长期缓存 id。
const ExecutorSystemKeyPrefix = "upstream_sync:"

// EnsureExecutor 返回绑定到该运行时的隐藏 Agent，没有就建一个。
// kind='system' 让它不出现在智能体列表与任何选择器里——用户只在这个菜单里
// 选运行时，不需要知道执行体的存在。
func (s *Service) EnsureExecutor(ctx context.Context, runtimeID, instructions string) (string, error) {
	if strings.TrimSpace(runtimeID) == "" {
		return "", ValidationError{Field: "runtime_id", Message: "请选择一个运行时"}
	}

	var wsID, runtimeMode string
	if err := s.Pool.QueryRow(ctx, `SELECT workspace_id::text, runtime_mode
 FROM agent_runtime WHERE id=$1`, runtimeID).Scan(&wsID, &runtimeMode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ValidationError{Field: "runtime_id", Message: "运行时不存在"}
		}
		return "", err
	}

	systemKey := ExecutorSystemKeyPrefix + runtimeID
	var existing string
	err := s.Pool.QueryRow(ctx, `SELECT id::text FROM agent
 WHERE workspace_id=$1 AND runtime_id=$2 AND system_key=$3 AND kind='system' LIMIT 1`,
		wsID, runtimeID, systemKey).Scan(&existing)
	if err == nil {
		// 提示词随版本更新；每次确认时刷新，避免旧执行体一直跑旧说明。
		if _, err = s.Pool.Exec(ctx, `UPDATE agent SET instructions=$2, updated_at=now() WHERE id=$1`, existing, instructions); err != nil {
			return "", err
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	if strings.TrimSpace(instructions) == "" {
		return "", fmt.Errorf("executor instructions are empty")
	}
	var created string
	if err = s.Pool.QueryRow(ctx, `INSERT INTO agent
 (id, workspace_id, name, description, avatar_url, runtime_mode, runtime_id, instructions,
  visibility, permission_mode, max_concurrent_tasks, kind, system_key, status)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,'private','private',1,'system',$9,'offline')
 RETURNING id::text`,
		util.UUIDToString(dbid.NewV7()), wsID, "上游观察员", "上游同步的隐藏执行体（系统任务，不在智能体列表展示）",
		"🛰", runtimeMode, runtimeID, instructions, systemKey).Scan(&created); err != nil {
		return "", err
	}
	return created, nil
}

// SetExecutor 把执行体与运行时写进平台级配置。
func (s *Service) SetExecutor(ctx context.Context, runtimeID, agentID, autopilotID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE upstream_sync_config
 SET runtime_id=$1, agent_id=$2, autopilot_id=$3, updated_at=now() WHERE singleton=1`,
		nullableUUID(runtimeID), nullableUUID(agentID), nullableUUID(autopilotID))
	return err
}
