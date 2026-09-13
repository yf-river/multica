// issue_projection.go
//
// 职责：读取 Issue 领域事件，为"Issue 投影表"加载并校验原始数据。
// 收到 Issue 变更事件后，投影消费者需要拿着事件里的 Issue 快照去主库回查这条
// Issue 的真实状态（还在不在、属于哪个工作区），确认无误后再写投影表。
// 与 comment_projection.go 同理：回查能挡住事件乱序、也能识别"Issue 已在
// 主事务提交后被删除"这类无法补投影的情况。本文件还负责校验事件与库里的
// 工作区归属一致，防止跨工作区串数据。
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// loadIssueProjection 根据一条 Issue 事件加载投影所需数据。
// label 只是用于报错时区分是哪个事件类型（如 "issue-created"），方便排查。
// 返回 (payload, exists, err)：exists=false 表示库里已无此 Issue（被删），
// 调用方应直接完成消费而不是重试。
func loadIssueProjection(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
	label string,
) (issueEventPayload, bool, error) {
	payload, ok := decodeIssueEvent(event)
	if !ok {
		return issueEventPayload{}, false, fmt.Errorf("decode %s projection payload", label)
	}
	if err := validateIssueProjectionScope(event, payload.Issue); err != nil {
		return issueEventPayload{}, false, err
	}
	_, exists, err := getIssueForProjection(ctx, queries, payload.Issue)
	return payload, exists, err
}

// validateIssueProjectionScope 校验事件归属的工作区与事件里 Issue 所属工作区
// 是否一致。事件头可能没带工作区 ID（空串）则跳过校验；一旦两边都填了却对
// 不上，说明消息串台或数据被移动，直接拒绝投影。
func validateIssueProjectionScope(event events.Event, issue eventIssue) error {
	if event.WorkspaceID != "" && event.WorkspaceID != issue.WorkspaceID {
		return fmt.Errorf("event workspace %s does not match issue workspace %s", event.WorkspaceID, issue.WorkspaceID)
	}
	return nil
}

// getIssueForProjection 拿着事件里的 Issue 快照回查主库，返回该 Issue 的
// 当前真实记录。查不到（已被删除）时返回 exists=false 而不是报错——原因见
// 下方行内注释；其他数据库错误才向上抛。
func getIssueForProjection(ctx context.Context, queries *db.Queries, issue eventIssue) (db.Issue, bool, error) {
	issueID, err := util.ParseUUID(issue.ID)
	if err != nil {
		return db.Issue{}, false, fmt.Errorf("projection event has invalid issue ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(issue.WorkspaceID)
	if err != nil {
		return db.Issue{}, false, fmt.Errorf("projection event has invalid workspace ID: %w", err)
	}
	dbIssue, err := queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issueID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The issue was deleted after the primary transaction committed. There
		// is no visible projection left to build, so completing the consumer is
		// correct; retrying an unavoidable foreign-key failure would poison the
		// stream forever.
		//
		// 中文说明：主事务提交后 Issue 才被删，这种场景没有可见投影可建，
		// 老老实实"完成消费"是对的——如果当成错误反复重试，会撞上无法避免的
		// 外键失败，把整条事件流永久毒死。
		return db.Issue{}, false, nil
	} else if err != nil {
		return db.Issue{}, false, fmt.Errorf("load issue before projection: %w", err)
	}
	return dbIssue, true, nil
}
