// squad_no_action.go —— 群组长 no_action 评估辅助：判断某任务是否已记录过群组长 no_action 评估，
// 供完成路径决定是否抑制多余的兜底评论。
package service

import (
	"context"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// HasSquadLeaderNoActionEvaluationForTask 判断这条确切任务是否已记录过群组长 no_action 评估；
// 参数不完整（q/任务/issue/agent 任一无效）时返回 false（不抑制）。
func HasSquadLeaderNoActionEvaluationForTask(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (bool, error) {
	if q == nil || !task.ID.Valid || !task.IssueID.Valid || !task.AgentID.Valid {
		return false, nil
	}
	return q.HasSquadLeaderNoActionEvaluationForTask(ctx, db.HasSquadLeaderNoActionEvaluationForTaskParams{
		IssueID: task.IssueID,
		AgentID: task.AgentID,
		TaskID:  util.UUIDToString(task.ID),
	})
}
