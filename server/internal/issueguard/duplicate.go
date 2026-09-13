// Package issueguard 提供创建 issue 前的重复防护：同一工作区/项目/父 issue 下
// 标题（忽略大小写与多余空白）相同的"活跃"issue 只允许存在一个，除非显式放行。
package issueguard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// NormalizeTitle 把标题归一化为可比形式：转小写并把连续空白折叠成单个空格。
func NormalizeTitle(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// DuplicateMessage 生成"存在活跃重复 issue"的用户可见提示文本（含 allow_duplicate
// 放行办法）。错误字符串的一部分，调用方按原样透出。
func DuplicateMessage(identifier, title, status string) string {
	return "Active duplicate issue exists: " + identifier + " " + title + " (status: " + status + "). Set allow_duplicate=true or use --allow-duplicate to create another."
}

// ActiveDuplicateError 表示用户想创建的 issue 与现有活跃 issue 重复，被防护拦下。
type ActiveDuplicateError struct {
	ID         string
	Identifier string
	Title      string
	Status     string
}

// Error 实现 error 接口，返回用户可直接阅读的重复提示。
func (e *ActiveDuplicateError) Error() string {
	return DuplicateMessage(e.Identifier, e.Title, e.Status)
}

// NewActiveDuplicateError 根据命中的 issue 记录构造错误，identifier 形如 "<前缀>-<编号>"。
func NewActiveDuplicateError(issue db.Issue, issuePrefix string) *ActiveDuplicateError {
	return &ActiveDuplicateError{
		ID:         util.UUIDToString(issue.ID),
		Identifier: fmt.Sprintf("%s-%d", issuePrefix, issue.Number),
		Title:      issue.Title,
		Status:     issue.Status,
	}
}

// LockAndFindActiveDuplicate 在创建普通 issue 前调用：先对归一化标题加一把数据库
// advisory 锁（串行化同一标题的并发创建），除非 allowDuplicate 显式放行，再查询
// 现存活跃重复项。返回找到的重复 issue（若有）与是否命中。
func LockAndFindActiveDuplicate(
	ctx context.Context,
	q *db.Queries,
	workspaceID pgtype.UUID,
	projectID pgtype.UUID,
	parentIssueID pgtype.UUID,
	title string,
	allowDuplicate bool,
) (db.Issue, bool, error) {
	normalizedTitle := NormalizeTitle(title)
	if normalizedTitle == "" {
		return db.Issue{}, false, nil
	}
	if err := q.LockIssueDuplicateKey(ctx, lockKey(workspaceID, projectID, parentIssueID, normalizedTitle)); err != nil {
		return db.Issue{}, false, err
	}
	if allowDuplicate {
		return db.Issue{}, false, nil
	}
	// "活跃"= 状态不属于终端类目（done/cancelled 及其展开项）；先算出这些 key。
	terminalStatusKeys, err := issuestatus.ExpandCategories(ctx, q, workspaceID, []string{
		issuestatus.Done,
		issuestatus.Cancelled,
	})
	if err != nil {
		return db.Issue{}, false, err
	}

	duplicate, err := q.FindActiveDuplicateIssue(ctx, db.FindActiveDuplicateIssueParams{
		WorkspaceID:        workspaceID,
		TerminalStatusKeys: terminalStatusKeys,
		ProjectID:          projectID,
		ParentIssueID:      parentIssueID,
		NormalizedTitle:    normalizedTitle,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Issue{}, false, nil
		}
		return db.Issue{}, false, err
	}
	return duplicate, true, nil
}

// LockAndFindRecentAutopilotDuplicate 是 autopilot 专用变体：不要求整条 issue 处于
// 终端状态，而是在给定时间窗内找同一 autopilot 起源、同一 (项目, 归一化标题) 下最近
// 创建的重复项，用于避免自动任务在短时间反复开同一 issue。
func LockAndFindRecentAutopilotDuplicate(
	ctx context.Context,
	q *db.Queries,
	workspaceID pgtype.UUID,
	autopilotID pgtype.UUID,
	projectID pgtype.UUID,
	title string,
	window time.Duration,
) (db.Issue, bool, error) {
	normalizedTitle := NormalizeTitle(title)
	if normalizedTitle == "" || !autopilotID.Valid || window <= 0 {
		return db.Issue{}, false, nil
	}
	if err := q.LockIssueDuplicateKey(ctx, recentAutopilotLockKey(workspaceID, autopilotID, projectID, normalizedTitle)); err != nil {
		return db.Issue{}, false, err
	}
	terminalStatusKeys, err := issuestatus.ExpandCategories(ctx, q, workspaceID, []string{
		issuestatus.Done,
		issuestatus.Cancelled,
	})
	if err != nil {
		return db.Issue{}, false, err
	}

	duplicate, err := q.FindRecentAutopilotDuplicateIssue(ctx, db.FindRecentAutopilotDuplicateIssueParams{
		WorkspaceID:        workspaceID,
		TerminalStatusKeys: terminalStatusKeys,
		OriginID:           autopilotID,
		ProjectID:          projectID,
		NormalizedTitle:    normalizedTitle,
		CreatedAfter:       pgtype.Timestamptz{Time: time.Now().UTC().Add(-window), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Issue{}, false, nil
		}
		return db.Issue{}, false, err
	}
	return duplicate, true, nil
}

// lockKey 构造普通 issue 重复防护的 advisory 锁键（带前缀避免与其他用途键冲突）。
func lockKey(workspaceID, projectID, parentIssueID pgtype.UUID, normalizedTitle string) string {
	return strings.Join([]string{
		"issue-active-duplicate",
		util.UUIDToString(workspaceID),
		util.UUIDToString(projectID),
		util.UUIDToString(parentIssueID),
		normalizedTitle,
	}, "|")
}

// recentAutopilotLockKey 构造 autopilot 近期重复防护的 advisory 锁键。
func recentAutopilotLockKey(workspaceID, autopilotID, projectID pgtype.UUID, normalizedTitle string) string {
	return strings.Join([]string{
		"autopilot-recent-duplicate",
		util.UUIDToString(workspaceID),
		util.UUIDToString(autopilotID),
		util.UUIDToString(projectID),
		normalizedTitle,
	}, "|")
}
