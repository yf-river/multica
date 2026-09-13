// reaction_projection.go
//
// 职责：以可靠事件消费的方式，为"被加了表情反应"的 Issue / 评论作者生成站内
// 通知。收到 ReactionAdded 类事件后，先校验操作者身份与事件一致、再回查目标
// 资源（Issue 或评论，以及评论所属 Issue）仍在，然后给被 @ 的/被赞的作者
// 通过 notifyDirect 发一条"某人给你的内容加了 :emoji:"的通知。评论反应还要
// 顺带判空兜底：评论或 Issue 已被删就安静跳过，不报错重试。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerDurableReactionConsumers 把"反应通知"可靠消费者注册进 dispatcher，
// 分别覆盖 Issue 反应（EventIssueReactionAdded）与评论反应（EventReactionAdded）
// 两类事件，消费者名都是 reaction_notification。
func registerDurableReactionConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventIssueReactionAdded, "reaction_notification", consumeIssueReactionNotification); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventReactionAdded, "reaction_notification", consumeCommentReactionNotification)
}

// consumeIssueReactionNotification 处理"Issue 被加反应"事件：校验操作者身份，
// 确认 Issue 存在后，给 Issue 的创建者发一条 reaction_added 通知。
func consumeIssueReactionNotification(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeIssueReactionEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode issue reaction notification payload")
	}
	reaction := payload.Reaction
	if err := validateReactionActor(event, reaction.ActorType, reaction.ActorID); err != nil {
		return nil, err
	}
	issue, exists, err := getIssueForProjection(ctx, queries, eventIssue{
		ID:          reaction.IssueID,
		WorkspaceID: event.WorkspaceID,
	})
	if err != nil || !exists {
		return nil, err
	}
	details, _ := json.Marshal(map[string]string{"emoji": reaction.Emoji})
	collector := newNotificationEventCollector()
	if err := notifyDirect(
		ctx,
		queries,
		collector.bus,
		issue.CreatorType,
		util.UUIDToString(issue.CreatorID),
		event.WorkspaceID,
		event,
		reaction.IssueID,
		issue.Status,
		"reaction_added",
		"info",
		issue.Title,
		details,
	); err != nil {
		return nil, fmt.Errorf("notify issue reaction recipient: %w", err)
	}
	return collector.events, nil
}

// consumeCommentReactionNotification 处理"评论被加反应"事件：校验操作者身份，
// 回查评论及其所属 Issue 都还在（被删则静默跳过），再给评论作者发一条
// reaction_added 通知，其中带上评论 ID 方便跳转定位。
func consumeCommentReactionNotification(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeCommentReactionEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode comment reaction notification payload")
	}
	reaction := payload.Reaction
	if err := validateReactionActor(event, reaction.ActorType, reaction.ActorID); err != nil {
		return nil, err
	}
	commentID, err := util.ParseUUID(reaction.CommentID)
	if err != nil {
		return nil, fmt.Errorf("reaction event has invalid comment ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(event.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("reaction event has invalid workspace ID: %w", err)
	}
	comment, err := queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          commentID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load comment before reaction projection: %w", err)
	}
	issue, err := queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          comment.IssueID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load issue before comment reaction projection: %w", err)
	}
	details, _ := json.Marshal(map[string]string{
		"emoji":      reaction.Emoji,
		"comment_id": reaction.CommentID,
	})
	collector := newNotificationEventCollector()
	if err := notifyDirect(
		ctx,
		queries,
		collector.bus,
		comment.AuthorType,
		util.UUIDToString(comment.AuthorID),
		event.WorkspaceID,
		event,
		util.UUIDToString(issue.ID),
		issue.Status,
		"reaction_added",
		"info",
		issue.Title,
		details,
	); err != nil {
		return nil, fmt.Errorf("notify comment reaction recipient: %w", err)
	}
	return collector.events, nil
}

// validateReactionActor 校验反应事件里的操作者（actor）与事件头声明的
// ActorType/ActorID 一致，并确认 actor ID 是合法 UUID——防止伪造或串台
// 的事件把"谁点了赞"记错人。
func validateReactionActor(event events.Event, actorType, actorID string) error {
	if actorType != event.ActorType || actorID != event.ActorID {
		return fmt.Errorf("reaction event actor mismatch")
	}
	if _, err := util.ParseUUID(actorID); err != nil {
		return fmt.Errorf("reaction event has invalid actor ID: %w", err)
	}
	return nil
}
