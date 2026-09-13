// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（life_task_context.go）定义"人生认知任务"在 agent_task_queue 里
// 的 claim 载荷结构：用 type=life_cognition 标记这些任务没有 issue/chat/
// autopilot 父级，必须留在受治理的 Life 执行边界内；claim token 与上下文
// 版本号用于围栏每次后续 Life 写入（防租约恢复/删除/竞争 claim）。
package handler

import (
	"encoding/json"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// lifeCognitionContextType 是 Life worker 写入的判别字段。带此上下文的
// 任务没有 issue/chat/autopilot 父级，必须留在受治理的 Life 执行边界内。
const lifeCognitionContextType = "life_cognition"

// lifeCognitionTaskContext 是存放在 agent_task_queue 里的 claim 载荷。
// token 与数值型上下文版本为后续每次 Life 写入围栏（租约恢复、删除、
// 竞争 claim）。
type lifeCognitionTaskContext struct {
	Type           string          `json:"type"`
	JobID          string          `json:"job_id"`
	JobType        string          `json:"job_type"`
	WorkspaceID    string          `json:"workspace_id"`
	UserID         string          `json:"user_id"`
	ClaimToken     string          `json:"claim_token"`
	ContextVersion int64           `json:"context_version_number"`
	Input          json.RawMessage `json:"input"`
}

// lifeCognitionContextForTask 尝试把任务解析为 Life 认知任务上下文：任务须
// 声明了该类型且不带任何 issue/chat/autopilot 父级，否则返回 false。
func lifeCognitionContextForTask(task db.AgentTaskQueue) (lifeCognitionTaskContext, bool) {
	if !lifeCognitionContextDeclared(task) || task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid {
		return lifeCognitionTaskContext{}, false
	}
	var context lifeCognitionTaskContext
	if json.Unmarshal(task.Context, &context) != nil || context.Type != lifeCognitionContextType {
		return lifeCognitionTaskContext{}, false
	}
	return context, true
}

// lifeCognitionContextDeclared 在完整解码前就区分"受治理的 Life 任务"与普通
// 无 issue 任务。已声明但解码失败的生命信封必须在 claim 时就被拒绝，
// 而不能漏到 quick-create 路径拿到仓库工具。
func lifeCognitionContextDeclared(task db.AgentTaskQueue) bool {
	if task.Context == nil {
		return false
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(task.Context, &envelope); err != nil {
		return false
	}
	return envelope.Type == lifeCognitionContextType
}
