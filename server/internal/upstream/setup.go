package upstream

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// DailyCron 是每天一次的默认节奏。放在这里而不是让用户填 cron：
// 这个能力的语义就是"每天看一眼上游"，可配置的只有开关与运行时。
const DailyCron = "0 9 * * *"

// SetupExecutor 一次完成"确认运行时 → 确保隐藏 Agent → 确保每日自动化",
// 并把三者写回平台级配置。幂等：重复保存只会更新，不会产生第二条自动化。
func (s *Service) SetupExecutor(ctx context.Context, runtimeID, creatorID, instructions string) (string, string, error) {
	agentID, err := s.EnsureExecutor(ctx, runtimeID, instructions)
	if err != nil {
		return "", "", err
	}

	var wsID string
	if err = s.Pool.QueryRow(ctx, `SELECT workspace_id::text FROM agent_runtime WHERE id=$1`, runtimeID).Scan(&wsID); err != nil {
		return "", "", err
	}

	autopilotID, err := s.ensureDailyAutopilot(ctx, wsID, agentID, creatorID)
	if err != nil {
		return "", "", err
	}
	if err = s.SetExecutor(ctx, runtimeID, agentID, autopilotID); err != nil {
		return "", "", err
	}
	return agentID, autopilotID, nil
}

func (s *Service) ensureDailyAutopilot(ctx context.Context, wsID, agentID, creatorID string) (string, error) {
	// 已有自动化：跟随执行体更新 assignee，并确保触发器仍然启用。
	var existing string
	err := s.Pool.QueryRow(ctx, `SELECT id::text FROM autopilot
 WHERE workspace_id=$1 AND assignee_id=$2 AND status <> 'archived' LIMIT 1`, wsID, agentID).Scan(&existing)
	if err == nil {
		if _, err = s.Pool.Exec(ctx, `UPDATE autopilot SET description=$2, updated_at=now() WHERE id=$1`,
			existing, DailyPrompt); err != nil {
			return "", err
		}
		if _, err = s.Pool.Exec(ctx, `UPDATE autopilot_trigger SET enabled=true, cron_expression=$2,
 timezone='Asia/Shanghai', updated_at=now()
 WHERE autopilot_id=$1 AND kind='schedule'`, existing, DailyCron); err != nil {
			return "", err
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	id := util.UUIDToString(dbid.NewV7())
	if _, err = s.Pool.Exec(ctx, `INSERT INTO autopilot
 (id, workspace_id, title, description, assignee_id, assignee_type, status, execution_mode,
  concurrency_policy, created_by_type, created_by_id)
 VALUES($1,$2,'上游同步（系统）',$3,$4,'agent','active','run_only','skip','member',$5)`,
		id, wsID, DailyPrompt, agentID, creatorID); err != nil {
		return "", err
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO autopilot_trigger
 (id, autopilot_id, kind, enabled, cron_expression, timezone, next_run_at, label)
 VALUES($1,$2,'schedule',true,$3,'Asia/Shanghai', now() + interval '1 day', '每日同步')`,
		util.UUIDToString(dbid.NewV7()), id, DailyCron); err != nil {
		return "", err
	}
	return id, nil
}

// DailyPrompt 是每次定时触发时随任务下发的正文。具体的 git 流程写在
// Agent 的 instructions 里，这里只说明"这次跑什么"。
const DailyPrompt = "执行一批上游同步：先运行 `multica upstream start` 领取范围内的工作，按 instructions 里的流程处理，最后用 `multica upstream finish` 推进游标。若服务端返回 409（另一批次在跑或已追平），直接用 `multica upstream fail --error ...` 结束本次运行，不要重试。"
