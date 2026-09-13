// 职责定位：构建 comment 相关的持久化事件（outbox envelope）——评论创建、变更（更新/解决/
// 取消解决）与删除，供发布到 issue 流与崩溃后重放。
package handler

import (
	"strconv"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// buildCommentCreatedEvent 是「新评论已创建」的持久化信封。issue 流把回复与 issue 变更按序
// 排给订阅者与 activity 投影。
func buildCommentCreatedEvent(issue db.Issue, comment CommentResponse, actorType, actorID string) events.Event {
	return events.Event{
		Type:        protocol.EventCommentCreated,
		StreamKey:   "issue:" + uuidToString(issue.ID),
		WorkspaceID: uuidToString(issue.WorkspaceID),
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"comment":             comment,
			"issue_title":         issue.Title,
			"issue_assignee_type": textToPtr(issue.AssigneeType),
			"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
			"issue_status":        issue.Status,
			"issue_revision":      comment.IssueRevision,
		},
	}
}

// buildCommentMutationEvent 创建一次评论状态转移（更新/解决/取消解决）的持久化信封。快照有意
// 取自变更查询返回的行：实时客户端可以立即更新，进程重启后 outbox 仍是真源。
func buildCommentMutationEvent(comment db.Comment, eventType, actorType, actorID string, issueRevision int64) events.Event {
	commentID := util.UUIDToString(comment.ID)
	issueID := util.UUIDToString(comment.IssueID)
	payload := map[string]any{
		"comment":    commentToResponse(comment, nil, nil),
		"comment_id": commentID,
		"issue_id":   issueID,
	}
	if issueRevision > 0 {
		payload["issue_revision"] = issueRevision
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "comment:" + eventType + ":" + commentID + ":" + strconv.FormatInt(comment.Revision, 10),
		StreamKey:      "issue:" + issueID,
		WorkspaceID:    util.UUIDToString(comment.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildCommentDeletedEvent 把被删行的身份与 revision 带进事件——提交后该行对消费方已不存在。
func buildCommentDeletedEvent(comment db.Comment, actorType, actorID string, issueRevision int64) events.Event {
	event := buildCommentMutationEvent(comment, protocol.EventCommentDeleted, actorType, actorID, issueRevision)
	event.Payload = map[string]any{
		"comment_id": commentIDString(comment),
		"issue_id":   util.UUIDToString(comment.IssueID),
	}
	if issueRevision > 0 {
		event.Payload.(map[string]any)["issue_revision"] = issueRevision
	}
	return event
}

// commentIDString 是评论 UUID 转字符串的简写。
func commentIDString(comment db.Comment) string { return util.UUIDToString(comment.ID) }
