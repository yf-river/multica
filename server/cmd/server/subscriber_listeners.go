// subscriber_listeners.go
//
// 职责：以可靠事件消费的方式维护"Issue 订阅者（subscriber）"投影并派生相应
// 事件。订阅者 = 需要收到某 Issue 动态的人/机器人：创建者、负责人、被 @ 的人、
// 评论者等。Issue 创建/更新、评论创建时会触发这里：先把受众算出来、把订阅
// 关系写进库（幂等），再各自派生 subscriber / notification 事件交给下游（如
// 实时推送与站内信）。系统自己的评论（system）不产生订阅者。
package main

import (
	"context"
	"fmt"

	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerDurableAudienceConsumers 注册三类"受众投影"可靠消费者：Issue 创建、
// Issue 更新、评论创建，分别绑定到 issue_audience / comment_audience 两个
// 消费者名下。
func registerDurableAudienceConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventIssueCreated, "issue_audience", consumeIssueCreatedAudience); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventIssueUpdated, "issue_audience", consumeIssueUpdatedAudience); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventCommentCreated, "comment_audience", consumeCommentCreatedAudience)
}

// consumeIssueCreatedAudience 处理 Issue 创建事件：先加载投影所需数据，然后
// 把"创建者、负责人、描述里被 @ 的人"加为订阅者，并给相关方投影创建通知。
// 两部分派生事件拼在一起返回。
func consumeIssueCreatedAudience(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadIssueProjection(ctx, queries, event, "issue-created")
	if err != nil || !exists {
		return nil, err
	}
	subscriberEvents, err := projectIssueCreatedSubscribers(ctx, queries, payload)
	if err != nil {
		return nil, err
	}
	notificationEvents, err := projectIssueCreatedNotifications(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	return append(subscriberEvents, notificationEvents...), nil
}

// consumeIssueUpdatedAudience 处理 Issue 更新事件：按"变更过的字段"增量维护
// 订阅者（换负责人/描述新增 @ 等），并投影更新通知。
func consumeIssueUpdatedAudience(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadIssueProjection(ctx, queries, event, "issue-updated")
	if err != nil || !exists {
		return nil, err
	}
	subscriberEvents, err := projectIssueUpdatedSubscribers(ctx, queries, payload)
	if err != nil {
		return nil, err
	}
	notificationEvents, err := projectIssueUpdatedNotifications(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	return append(subscriberEvents, notificationEvents...), nil
}

// consumeCommentCreatedAudience 处理评论创建事件：系统消息不产生订阅者；
// 普通评论的作者会被加为该 Issue 的订阅者（reason=commenter），再投影通知。
func consumeCommentCreatedAudience(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadCommentProjection(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	if payload.Comment.AuthorType == "system" {
		return nil, nil
	}
	subscriberEvents, err := projectCommentCreatedSubscriber(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	notificationEvents, err := projectCommentCreatedNotifications(ctx, queries, event, payload)
	if err != nil {
		return nil, err
	}
	return append(subscriberEvents, notificationEvents...), nil
}

// subscriberEventAppender 构造一个"把某人加为订阅者"的闭包追加器：闭包内部
// 负责写订阅投影，成功后把派生出的 EventSubscriberAdded 事件追加到 emitted
// 切片里。用闭包包装是为了让各处调用点不用重复处理"写库 + 收集事件"两件事。
func subscriberEventAppender(
	ctx context.Context,
	queries *db.Queries,
	workspaceID string,
	issueID string,
	emitted *[]events.Event,
) func(userType, userID, reason string) error {
	return func(userType, userID, reason string) error {
		created, ok, err := addSubscriberProjection(ctx, queries, workspaceID, issueID, userType, userID, reason)
		if err != nil {
			return err
		}
		if ok {
			*emitted = append(*emitted, created)
		}
		return nil
	}
}

// projectIssueCreatedSubscribers 为新建 Issue 补齐初始订阅者：创建者
// （creator）、负责人（assignee，若与创建者不同），以及描述里被 @ 的每个人。
func projectIssueCreatedSubscribers(ctx context.Context, queries *db.Queries, payload issueEventPayload) ([]events.Event, error) {
	issue := payload.Issue
	emitted := make([]events.Event, 0, 4)
	appendSubscriber := subscriberEventAppender(ctx, queries, issue.WorkspaceID, issue.ID, &emitted)
	if err := appendSubscriber(issue.CreatorType, issue.CreatorID, "creator"); err != nil {
		return nil, err
	}
	if issue.AssigneeType != nil && issue.AssigneeID != nil &&
		(*issue.AssigneeType != issue.CreatorType || *issue.AssigneeID != issue.CreatorID) {
		if err := appendSubscriber(*issue.AssigneeType, *issue.AssigneeID, "assignee"); err != nil {
			return nil, err
		}
	}
	if issue.Description != nil {
		for _, mentioned := range util.ParseMentions(*issue.Description) {
			if err := appendSubscriber(mentioned.Type, mentioned.ID, "mentioned"); err != nil {
				return nil, err
			}
		}
	}
	return emitted, nil
}

// projectIssueUpdatedSubscribers 增量更新更新后的订阅者：只有负责人真的换了
// 才把新负责人加进去；描述变更时，只给"新出现在描述里"的 @ 对象加订阅，
// 已在旧描述里 @ 过的不重复加（防止一改描述就把老被 @ 者再骚扰一遍）。
func projectIssueUpdatedSubscribers(ctx context.Context, queries *db.Queries, payload issueEventPayload) ([]events.Event, error) {
	issue := payload.Issue
	emitted := make([]events.Event, 0, 4)
	appendSubscriber := subscriberEventAppender(ctx, queries, issue.WorkspaceID, issue.ID, &emitted)
	if payload.AssigneeChanged && issue.AssigneeType != nil && issue.AssigneeID != nil {
		if err := appendSubscriber(*issue.AssigneeType, *issue.AssigneeID, "assignee"); err != nil {
			return nil, err
		}
	}
	if payload.DescriptionChanged && issue.Description != nil {
		previous := make(map[string]bool)
		if payload.PrevDescription != nil {
			for _, mentioned := range util.ParseMentions(*payload.PrevDescription) {
				previous[mentioned.Type+":"+mentioned.ID] = true
			}
		}
		for _, mentioned := range util.ParseMentions(*issue.Description) {
			if previous[mentioned.Type+":"+mentioned.ID] {
				continue
			}
			if err := appendSubscriber(mentioned.Type, mentioned.ID, "mentioned"); err != nil {
				return nil, err
			}
		}
	}
	return emitted, nil
}

// projectCommentCreatedSubscriber 把新评论的作者加为订阅者（reason=commenter），
// 让他之后也能收到该 Issue 的动态。作者 ID 为空（无法定位用户）时跳过。
func projectCommentCreatedSubscriber(ctx context.Context, queries *db.Queries, event events.Event, payload commentEventPayload) ([]events.Event, error) {
	comment := payload.Comment
	if comment.AuthorID == "" {
		return nil, nil
	}
	created, ok, err := addSubscriberProjection(ctx, queries, event.WorkspaceID, comment.IssueID, comment.AuthorType, comment.AuthorID, "commenter")
	if err != nil || !ok {
		return nil, err
	}
	return []events.Event{created}, nil
}

// addSubscriberProjection 真正把一条订阅关系写进库（AddIssueSubscriber 幂等，
// inserted=0 说明早已订阅过，不重复派生事件）。只接受 member/agent 两类主体，
// 其他主体类型（如 system）直接忽略；成功写入后返回派生的 EventSubscriberAdded
// 事件。
func addSubscriberProjection(
	ctx context.Context,
	queries *db.Queries,
	workspaceID string,
	issueID string,
	userType string,
	userID string,
	reason string,
) (events.Event, bool, error) {
	if !isMemberOrAgentActorType(userType) {
		return events.Event{}, false, nil
	}
	parsedIssueID, err := util.ParseUUID(issueID)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("subscriber projection has invalid issue ID: %w", err)
	}
	parsedUserID, err := util.ParseUUID(userID)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("subscriber projection has invalid %s ID: %w", userType, err)
	}
	inserted, err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID:  parsedIssueID,
		UserType: userType,
		UserID:   parsedUserID,
		Reason:   reason,
	})
	if err != nil {
		return events.Event{}, false, fmt.Errorf("add %s issue subscriber %s: %w", reason, userID, err)
	}
	if inserted == 0 {
		return events.Event{}, false, nil
	}
	return events.Event{
		Type:        protocol.EventSubscriberAdded,
		WorkspaceID: workspaceID,
		Payload: map[string]any{
			"issue_id":  issueID,
			"user_type": userType,
			"user_id":   userID,
			"reason":    reason,
		},
	}, true, nil
}
