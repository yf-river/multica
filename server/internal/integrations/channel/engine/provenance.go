package engine

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 角色定位：判断"一次聊天任务的结果要不要发到外部渠道"——回复来源（provenance）检查。
// 一条 chat 任务可能由渠道消息触发，也可能由 Web/移动端直接触发；只有前者该把回复（或
// 失败提示）发回 IM 平台。

// ChannelProvenanceQueries 是"回复来源检查"需要的唯一一条查询。*db.Queries 满足它。
type ChannelProvenanceQueries interface {
	TaskHasChannelIngestedMessages(ctx context.Context, taskID pgtype.UUID) (bool, error)
}

// TaskInputIsChannelIngested 判断一条已完成的聊天任务是否**从渠道取输入**——是的话它的
// 回复（或失败提示）属于外部平台。Web/移动端直接任务可以复用一条渠道绑定的会话，但它
// 们的回复留在 Multica 内（MUL-4988）。
//
// 为什么不能只看 chat_input_task_id：被封装的渠道任务和直接任务一样拥有一份输入批次，
// 单看这个字段分不出来。判定靠所拥有批次上那个不可变的 channel_ingested 戳，且按批次
// **属主** id 查——自动重试的克隆任务会继承 chat_input_task_id 但消息仍挂在父任务名下，
// 用属主查能让克隆与父任务得到同样的判定。chat_input_task_id 为 NULL 说明是"输入批次
// 封装（sealing）机制上线前"的渠道任务（直接任务自 MUL-4351 起必然拥有自己的批次，
// 不会走到这里）——保持 #5645 发布的"默认按渠道投递"行为，返回 true。
func TaskInputIsChannelIngested(ctx context.Context, q ChannelProvenanceQueries, task db.AgentTaskQueue) (bool, error) {
	if !task.ChatInputTaskID.Valid {
		return true, nil
	}
	return q.TaskHasChannelIngestedMessages(ctx, task.ChatInputTaskID)
}
