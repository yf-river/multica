// chat_quick_actions.go —— “追问建议”的解析与落盘侧实现：
// 把模型返回的原始输出解析成最多三个“建议下一步”按钮（兼容代码围栏、裸数组等
// 常见偏离），做清洗/去重/截断/单主按钮约束，再把结果写回该轮助手消息并广播
// chat:quick_actions（含占位符化解）。另含残留 fence 语法的防御性剥离。
// 生成部分在 chat_quick_actions_generate.go。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	chatQuickActionsFence    = "```quick-actions\n"
	chatQuickActionMaxCount  = 3
	chatQuickActionLabelMax  = 80
	chatQuickActionPromptMax = 500
)

// parseChatQuickActionsOutput 把生成趟的原始输出解析成清洗后的动作列表。生成时要求
// 输出 {"actions":[...]}，但这个解析器故意很宽容——模型裹代码围栏、丢掉外壳、或先
// 说句话再给 JSON 都常有，严格解析只会悄悄丢掉好建议。任何解析不了的都降级为“无
// 建议”；这段输出永远不会进对话记录，所以没什么可泄露的。
func parseChatQuickActionsOutput(raw string) []protocol.ChatQuickAction {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// 尝试顺序从“严格”一路放到“凑合”：先试要求的对象形态（或裸数组）、再取代码
	// 围栏内部、最后扫最外层括号区间。括号扫描放最后，是因为前置的散文里本身可能
	// 含括号（如 "here's [my] take: [...]"），先扫会把切片切错位。
	for _, candidate := range []string{raw, insideCodeFence(raw)} {
		if actions, ok := unmarshalChatQuickActions(candidate); ok {
			return actions
		}
	}
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil
	}
	actions, _ := unmarshalChatQuickActions(raw[start : end+1])
	return actions
}

// insideCodeFence 返回 raw 中第一段被围栏包住的代码内容（容忍语言标记），没有完整
// 围栏时返回空串。
func insideCodeFence(raw string) string {
	open := strings.Index(raw, "```")
	if open < 0 {
		return ""
	}
	rest := raw[open+3:]
	if nl := strings.Index(rest, "\n"); nl >= 0 {
		rest = rest[nl+1:] // 去掉围栏开头的语言标记行
	} else {
		return ""
	}
	closing := strings.Index(rest, "```")
	if closing < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:closing])
}

func unmarshalChatQuickActions(raw string) ([]protocol.ChatQuickAction, bool) {
	if raw == "" {
		return nil, false
	}
	// 要求输出 {"actions":[...]} 是因为 response_format=json_object 需要顶层是对象。
	// 但裸数组也照收：下面的括号兜底会从散文里抠一个出来，模型丢掉了外壳也不该让
	// 用户损失建议。
	var wrapper struct {
		Actions []protocol.ChatQuickAction `json:"actions"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err == nil && wrapper.Actions != nil {
		return sanitizeChatQuickActions(wrapper.Actions), true
	}
	var candidates []protocol.ChatQuickAction
	if err := json.Unmarshal([]byte(raw), &candidates); err != nil {
		return nil, false
	}
	return sanitizeChatQuickActions(candidates), true
}

// splitChatQuickActions 把回复末尾一段保留的 quick-actions 围栏从可见正文里拆出来。
// 只要识别到收尾脚注就一定剥掉——哪怕里面的 JSON 是坏的——这样私有控制语法永远
// 不会漏进 Chat。回复中间的围栏只是普通可见的 markdown，保持原样。
//
// 建议现在在服务端生成了，所以这里不再是建议的来源——但这段剥离逻辑仍保留作防御：
// 在该功能下线前创建的 provider 会话里，历史中带着这种脚注语法，agent 还会继续发
// 一段时间；不剥的话，原始围栏就会落进存档的对话记录。
func splitChatQuickActions(output string) (string, []protocol.ChatQuickAction) {
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	trimmed := strings.TrimRight(normalized, " \t\n")
	if !strings.HasSuffix(trimmed, "\n```") {
		return output, nil
	}

	withoutClose := strings.TrimSuffix(trimmed, "\n```")
	marker := "\n" + chatQuickActionsFence
	start := strings.LastIndex(withoutClose, marker)
	visible := ""
	raw := ""
	switch {
	case start >= 0:
		visible = withoutClose[:start]
		raw = withoutClose[start+len(marker):]
	case strings.HasPrefix(withoutClose, chatQuickActionsFence):
		raw = strings.TrimPrefix(withoutClose, chatQuickActionsFence)
	default:
		return output, nil
	}
	// 在整条回复结束前就闭合的 quick-actions 围栏是普通可见的 markdown——哪怕后面
	// 碰巧有一段无关代码围栏收尾。没有这层保护，上面那脚最终的收尾围栏就会和回复
	// 中间的开头围栏配对，静默截掉那段示例之后的所有内容。
	if strings.Contains(raw, "\n```") {
		return output, nil
	}

	visible = strings.TrimRight(visible, " \t\n")
	var candidates []protocol.ChatQuickAction
	if err := json.Unmarshal([]byte(raw), &candidates); err != nil {
		return visible, nil
	}
	return visible, sanitizeChatQuickActions(candidates)
}

// sanitizeChatQuickActions 无论候选来自哪条通道，都对 agent 给的候选执行服务端
// 契约：最多三个动作、标签规范化且非空、标签不区分大小写去重、prompt 缺省为标签、
// 按 rune 安全截断、并且恰好一个 primary。
func sanitizeChatQuickActions(candidates []protocol.ChatQuickAction) []protocol.ChatQuickAction {
	actions := make([]protocol.ChatQuickAction, 0, min(len(candidates), chatQuickActionMaxCount))
	seen := make(map[string]struct{}, chatQuickActionMaxCount)
	primarySeen := false
	for _, candidate := range candidates {
		if len(actions) == chatQuickActionMaxCount {
			break
		}
		label := strings.Join(strings.Fields(candidate.Label), " ")
		if label == "" {
			continue
		}
		label = truncateChatQuickAction(label, chatQuickActionLabelMax)
		key := strings.ToLower(label)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}

		prompt := strings.TrimSpace(candidate.Prompt)
		if prompt == "" {
			prompt = label
		}
		prompt = truncateChatQuickAction(prompt, chatQuickActionPromptMax)
		primary := candidate.Primary && !primarySeen
		primarySeen = primarySeen || primary
		actions = append(actions, protocol.ChatQuickAction{
			Label:   label,
			Prompt:  prompt,
			Primary: primary,
		})
	}
	// 若没有一个候选声明 primary，兜底把第一个标成主按钮。
	if len(actions) > 0 && !primarySeen {
		actions[0].Primary = true
	}
	return actions
}

func truncateChatQuickAction(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

// chatQuickActionsEligible 判断一轮完成的聊天是否值得跑一次建议生成——也因此决定
// chat:done 广播要不要告诉客户端“等着收 chat:quick_actions 的补充”。
//
// 决定权在服务端自己手里：建议现在在服务端生成，daemon 报什么都不能授予或剥夺它。
func (s *TaskService) chatQuickActionsEligible(ctx context.Context, task db.AgentTaskQueue, msg *db.ChatMessage) bool {
	return s.chatQuickActionsEligibleWithQueries(ctx, s.Queries, task, msg)
}

func (s *TaskService) chatQuickActionsEligibleWithQueries(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue, msg *db.ChatMessage) bool {
	if s.QuickActions == nil || !s.QuickActions.Enabled() {
		return false
	}
	if !task.ChatSessionID.Valid {
		return false
	}
	// 只有普通回复能孕育建议：no_response 结果没有可依的素材，只带附件的回复没有
	// 可供锚定的正文。
	if msg == nil || msg.MessageKind != protocol.ChatMessageKindMessage {
		return false
	}
	if strings.TrimSpace(msg.Content) == "" {
		return false
	}
	// 挂在渠道（Slack / Lark）上的会话没有按钮展示面。判断标准与
	// writeChatCompletionOutcome 用的是同一个：这轮“自有输入批次”上不可变的
	// channel_ingested 印记。owner 为 NULL 表示这是 agent 发起的开场轮，那种轮在
	// Chat 里会正常渲染。
	if task.ChatInputTaskID.Valid {
		if queries == nil {
			return false
		}
		channelIngested, err := queries.TaskHasChannelIngestedMessages(ctx, task.ChatInputTaskID)
		if err != nil || channelIngested {
			return false
		}
	}
	return true
}

// SupplementChatQuickActions 把一次生成趟的原始输出挂到已完成这轮的助手消息上，并
// 广播 chat:quick_actions。语义是尽力而为：即使结果解析不了或为空，也照样广播这行
// 当前（通常为空）的动作，让 pending 占位符能化解。从未写过助手行的轮
// （no_response、渠道空投递）静默返回——那种情况下没有客户端在等，因为 pending
// 标记只为已写入的普通消息立过。
//
// failed 把这次广播标记成“失败化解”（一次显式刷新、但重新生成失败了）：按钮保持
// 原样，客户端显示“刷新失败”提示，而不是把它们当成新生成的结果。
func (s *TaskService) SupplementChatQuickActions(ctx context.Context, task db.AgentTaskQueue, raw string, failed bool) error {
	if !task.ChatSessionID.Valid {
		return nil
	}
	actions := parseChatQuickActionsOutput(raw)
	for i := range actions {
		actions[i].Label = redact.Text(actions[i].Label)
		actions[i].Prompt = redact.Text(actions[i].Prompt)
	}

	if s.TxStarter == nil {
		// 没有事务器（无 TxStarter 的装配路径）：直接在连接池上写并广播。
		msg, err := s.applyChatQuickActions(ctx, s.Queries, task, actions)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if msg == nil {
			return nil
		}
		workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
		if workspaceID != "" {
			s.Bus.Publish(chatQuickActionsEvent(task, workspaceID, msg, failed))
		}
		return nil
	}

	// 有事务器：在同一事务里写助手行 + 落 durable 事件，提交后再广播，避免“事件
	// 发了但写入丢了/写入了但事件丢了”的割裂。
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin chat quick actions transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.Queries.WithTx(tx)
	msg, err := s.applyChatQuickActions(ctx, qtx, task, actions)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if msg == nil {
		return nil
	}
	workspaceID, err := resolveTaskWorkspaceWithQueries(ctx, qtx, task)
	if err != nil || workspaceID == "" {
		if err == nil {
			err = errors.New("task has no workspace")
		}
		return fmt.Errorf("resolve chat quick actions workspace: %w", err)
	}
	persisted, err := recordDurableEventTx(ctx, qtx, chatQuickActionsEvent(task, workspaceID, msg, failed))
	if err != nil {
		return fmt.Errorf("record chat quick actions event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit chat quick actions transaction: %w", err)
	}
	publishCommittedEvent(s.Bus, persisted)
	return nil
}

// applyChatQuickActions 把解析好的动作写回“这轮助手消息”这一行：有动作就 UPDATE
// 该行的 quick_actions；没动作（空结果）就按任务取一次助手行。两种情况都返回该行，
// 供调用方拼广播事件。
func (s *TaskService) applyChatQuickActions(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, actions []protocol.ChatQuickAction) (*db.ChatMessage, error) {
	var (
		msg db.ChatMessage
		err error
	)
	if len(actions) > 0 {
		encoded, marshalErr := json.Marshal(actions)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal chat quick actions: %w", marshalErr)
		}
		msg, err = q.SetChatMessageQuickActionsByTask(ctx, db.SetChatMessageQuickActionsByTaskParams{TaskID: task.ID, QuickActions: encoded})
	} else {
		msg, err = q.GetChatMessageByTaskAssistant(ctx, task.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("attach chat quick actions: %w", err)
	}
	return &msg, nil
}

// chatQuickActionsEvent 把“这轮动作已就绪/化解”包装成事件。幂等键含事件体摘要：
// 对同一轮内容完全相同的两次广播会去重成一条。
func chatQuickActionsEvent(task db.AgentTaskQueue, workspaceID string, msg *db.ChatMessage, failed bool) events.Event {
	payload := protocol.ChatQuickActionsPayload{
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		TaskID:        util.UUIDToString(task.ID),
		MessageID:     util.UUIDToString(msg.ID),
		QuickActions:  []protocol.ChatQuickAction{},
		Failed:        failed,
	}
	_ = json.Unmarshal(msg.QuickActions, &payload.QuickActions)
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return events.Event{
		Type:           protocol.EventChatQuickActions,
		IdempotencyKey: fmt.Sprintf("chat-quick-actions:%s:%x", util.UUIDToString(task.ID), digest[:]),
		StreamKey:      "chat:" + util.UUIDToString(task.ChatSessionID),
		WorkspaceID:    workspaceID,
		ActorType:      "system",
		TaskID:         util.UUIDToString(task.ID),
		ChatSessionID:  util.UUIDToString(task.ChatSessionID),
		Payload:        payload,
	}
}
