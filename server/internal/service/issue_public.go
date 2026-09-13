// issue_public.go —— IssueService 的公开 API 写原语，刻意与传输层/凭据类型无关：
// App、PAT、Plugin 入口各自鉴权后调用同一业务操作。
package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ErrIssueRevisionConflict 表示调用方基于陈旧的 issue revision 做更新。传输层把它映射为稳定的冲突响应。
var ErrIssueRevisionConflict = errors.New("issue revision conflict")

// IssueContentPatch 是首个共享的 Public API 写原语，刻意与传输和凭据无关。
// 字段均为可选指针：只更新被显式提供的字段。
type IssueContentPatch struct {
	Title            *string
	Description      *string
	ExpectedRevision *int64
}

// UpdateContent 只更新首批 Public API 暴露的低风险内容字段（标题/描述，可带乐观锁）。
// 指派、状态、项目与层级变更仍是独立操作，因为它们各自带额外策略与副作用。
// ExpectedRevision 提供而 DB 更新无行时返回 ErrIssueRevisionConflict。
func (s *IssueService) UpdateContent(ctx context.Context, issue db.Issue, patch IssueContentPatch) (db.Issue, error) {
	params := db.UpdateIssueParams{
		ID:            issue.ID,
		AssigneeType:  issue.AssigneeType,
		AssigneeID:    issue.AssigneeID,
		StartDate:     issue.StartDate,
		DueDate:       issue.DueDate,
		ParentIssueID: issue.ParentIssueID,
		ProjectID:     issue.ProjectID,
		Stage:         issue.Stage,
	}
	if patch.ExpectedRevision != nil {
		params.ExpectedRevision = pgtype.Int8{Int64: *patch.ExpectedRevision, Valid: true}
	}
	if patch.Title != nil {
		params.Title = pgtype.Text{String: *patch.Title, Valid: true}
	}
	if patch.Description != nil {
		params.Description = pgtype.Text{String: *patch.Description, Valid: true}
	}

	updated, err := s.Queries.UpdateIssue(ctx, params)
	if patch.ExpectedRevision != nil && errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, ErrIssueRevisionConflict
	}
	return updated, err
}
