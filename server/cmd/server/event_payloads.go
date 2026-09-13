// event_payloads.go
//
// 职责：定义领域事件在持久化/重放时的"线上形状"（wire format）以及配套解码器。
// 事件负载要跨异步持久化边界：可能是进程内直接发布，也可能是从 JSON 重放，
// 所以消费者一律按这里的 struct（字段上带 json tag）去解，而不是断言 handler
// 层的具体 Go 类型——这样两种来源的语义完全一致。文件里的 *Changed 布尔位
// 用来描述"这次变更动了哪些字段"，投影与通知逻辑靠它们决定要不要干活。
package main

import (
	"encoding/json"

	"github.com/multica-ai/multica/server/internal/events"
)

// issueEventPayload 是 Issue 变更事件的负载：包含 Issue 快照本体、一组
// "哪些字段在这次变更里动了"的标志位，以及变更前各字段的旧值（Prev*）。
// 旧值只有在对应 Changed 位为真时才有意义。
type issueEventPayload struct {
	Issue eventIssue `json:"issue"`

	// 以下 *Changed 标志位：true 表示这次更新动了对应字段，供投影/通知做
	// 精确触发（例如只有状态变了才同步 autopilot 运行）。
	AssigneeChanged    bool `json:"assignee_changed"`
	StatusChanged      bool `json:"status_changed"`
	PriorityChanged    bool `json:"priority_changed"`
	StartDateChanged   bool `json:"start_date_changed"`
	DueDateChanged     bool `json:"due_date_changed"`
	DescriptionChanged bool `json:"description_changed"`
	TitleChanged       bool `json:"title_changed"`

	// Prev* 系列记录变更前的旧值；指针类型是因为"可空"与"未提供"要区分开。
	PrevTitle        string  `json:"prev_title"`
	PrevAssigneeType *string `json:"prev_assignee_type"`
	PrevAssigneeID   *string `json:"prev_assignee_id"`
	PrevStatus       string  `json:"prev_status"`
	PrevPriority     string  `json:"prev_priority"`
	PrevStartDate    *string `json:"prev_start_date"`
	PrevDueDate      *string `json:"prev_due_date"`
	PrevDescription  *string `json:"prev_description"`
}

// eventIssue 是事件内嵌的 Issue 快照（都是字符串形式的 ID/状态等），
// 由发布方在源事务内序列化，避免消费者再回查主表才知道"发生了什么"。
type eventIssue struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	Title         string  `json:"title"`
	Description   *string `json:"description"`
	Status        string  `json:"status"`
	Priority      string  `json:"priority"`
	AssigneeType  *string `json:"assignee_type"`
	AssigneeID    *string `json:"assignee_id"`
	CreatorType   string  `json:"creator_type"`
	CreatorID     string  `json:"creator_id"`
	StartDate     *string `json:"start_date"`
	DueDate       *string `json:"due_date"`
	ParentIssueID *string `json:"parent_issue_id"`
}

// commentEventPayload 是评论变更事件的负载：评论快照 + 所属 Issue 的标题/
// 负责人/状态等摘要，让通知逻辑不用再回查 Issue 就能拼出文案。
type commentEventPayload struct {
	Comment           eventComment `json:"comment"`
	IssueTitle        string       `json:"issue_title"`
	IssueAssigneeType *string      `json:"issue_assignee_type"`
	IssueAssigneeID   *string      `json:"issue_assignee_id"`
	IssueStatus       string       `json:"issue_status"`
}

// eventComment 是事件内嵌的评论快照。
type eventComment struct {
	ID         string `json:"id"`
	IssueID    string `json:"issue_id"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id"`
	Content    string `json:"content"`
}

// taskEventPayload 是任务状态事件的负载，描述任务当前走到了哪个状态、挂在
// 哪个 Issue/会话/Agent 下。QuickActionsPending 表示还有快捷操作待消费。
type taskEventPayload struct {
	TaskID              string `json:"task_id"`
	IssueID             string `json:"issue_id"`
	ChatSessionID       string `json:"chat_session_id"`
	AgentID             string `json:"agent_id"`
	Status              string `json:"status"`
	QuickActionsPending bool   `json:"quick_actions_pending"`
}

// activityCreatedEventPayload 是"活动已创建"事件的负载，携带要写进活动流的
// 那一条 entry。
type activityCreatedEventPayload struct {
	IssueID string        `json:"issue_id"`
	Entry   activityEntry `json:"entry"`
}

// activityEntry 是活动流里单条条目的最小身份描述（写库时按 ID 去重/定位）。
type activityEntry struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Action string `json:"action"`
}

// issueReactionEventPayload 是"Issue 被加反应"事件的负载。
type issueReactionEventPayload struct {
	Reaction eventIssueReaction `json:"reaction"`
}

// eventIssueReaction 描述一条加到 Issue 上的反应：谁（actor）用哪个 emoji
// 点的赞。
type eventIssueReaction struct {
	ID        string `json:"id"`
	IssueID   string `json:"issue_id"`
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	Emoji     string `json:"emoji"`
}

// commentReactionEventPayload 是"评论被加反应"事件的负载。
type commentReactionEventPayload struct {
	Reaction eventCommentReaction `json:"reaction"`
}

// eventCommentReaction 描述一条加到评论上的反应。
type eventCommentReaction struct {
	ID        string `json:"id"`
	CommentID string `json:"comment_id"`
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	Emoji     string `json:"emoji"`
}

// decodeEventPayload 把事件负载（可能是任意 Go 类型，也可能已经是 JSON 串）
// 先 JSON 序列化再反序列化到目标类型 T，实现"任意来源 -> 统一线上形状"。
// 任何一步失败都返回 ok=false，调用方应把它当作不可解负载拒收。
func decodeEventPayload[T any](event events.Event) (T, bool) {
	var payload T
	raw, err := json.Marshal(event.Payload)
	if err != nil {
		return payload, false
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, false
	}
	return payload, true
}

// decodeIssueEvent 解码 Issue 事件负载，并要求快照里带非空 Issue ID
// （空 ID 说明负载不完整，视为解码失败）。
func decodeIssueEvent(event events.Event) (issueEventPayload, bool) {
	payload, ok := decodeEventPayload[issueEventPayload](event)
	return payload, ok && payload.Issue.ID != ""
}

// decodeCommentEvent 解码评论事件负载，要求评论 ID 与其所属 Issue ID 都非空。
func decodeCommentEvent(event events.Event) (commentEventPayload, bool) {
	payload, ok := decodeEventPayload[commentEventPayload](event)
	return payload, ok && payload.Comment.ID != "" && payload.Comment.IssueID != ""
}

// decodeIssueReactionEvent 解码 Issue 反应事件负载，要求反应的关键字段
// （ID/IssueID/actor/emoji）齐全。
func decodeIssueReactionEvent(event events.Event) (issueReactionEventPayload, bool) {
	payload, ok := decodeEventPayload[issueReactionEventPayload](event)
	reaction := payload.Reaction
	return payload, ok && reaction.ID != "" && reaction.IssueID != "" && reaction.ActorID != "" && reaction.Emoji != ""
}

// decodeCommentReactionEvent 解码评论反应事件负载，要求关键字段齐全。
func decodeCommentReactionEvent(event events.Event) (commentReactionEventPayload, bool) {
	payload, ok := decodeEventPayload[commentReactionEventPayload](event)
	reaction := payload.Reaction
	return payload, ok && reaction.ID != "" && reaction.CommentID != "" && reaction.ActorID != "" && reaction.Emoji != ""
}

// decodeActivityCreatedEvent 解码活动创建事件负载，并校验这确实是一条
// type == "activity" 的活动条目（防止别的事件类型混进活动流）。
func decodeActivityCreatedEvent(event events.Event) (activityCreatedEventPayload, bool) {
	payload, ok := decodeEventPayload[activityCreatedEventPayload](event)
	return payload, ok && payload.IssueID != "" && payload.Entry.ID != "" &&
		payload.Entry.Type == "activity" && payload.Entry.Action != ""
}
