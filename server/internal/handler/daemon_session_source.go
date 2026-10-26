package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 会话选择完成后解析原生状态的来源，避免 daemon 按 workdir 猜测另一个任务 home。
func (h *Handler) resolveCodexSessionSource(ctx context.Context, task db.AgentTaskQueue, runtime db.AgentRuntime, response *AgentTaskResponse) error {
	if runtime.Provider != "codex" || response.PriorSessionID == "" {
		return nil
	}
	id, err := h.Queries.GetTaskSessionSource(ctx, db.GetTaskSessionSourceParams{
		TaskID: task.ID, SessionID: response.PriorSessionID, SourceTaskID: task.RerunOfTaskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 旧的 chat_session 指针可能没有任务记录；这不授权扫描其他任务目录。
		return nil
	}
	if err != nil {
		return err
	}
	response.PriorTaskID = uuidToString(id)
	return nil
}
