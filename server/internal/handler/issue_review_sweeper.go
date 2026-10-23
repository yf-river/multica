// 职责定位：「审核中」的审查补扫。状态后处理入口已经覆盖正常路径，这里按数据库状态
// 找「停在审核中却没有活动审查」的任务补派发，兜住交付入口遗漏、审查入队失败和进程重启
// 丢掉的那一次派发，同时用显式跳过条件保护人工停止与已通过的结果。
package handler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/review"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// 1. 60 秒宽限避开正常入口的派发与补扫抢跑；只认 queued/dispatched 会漏掉正在跑
	// 的审查，补扫就会重复派发，所以活动态要覆盖到 running 和等待工作目录。
	reviewSweepGrace     = 60 * time.Second
	reviewSweepBatch     = 50
	reviewActiveStatuses = "('queued','dispatched','running','waiting_local_directory')"
)

// reviewSweepStats 是一轮补扫的观测数据：核对、补派、跳过各多少。
type reviewSweepStats struct {
	Scanned    int
	Dispatched int
	Skipped    int
}

// reviewSweepCandidate 是判断一条「审核中」任务是否需要补派审查所需的全部输入。
type reviewSweepCandidate struct {
	HasRow       bool
	State        string
	ReviewedHead string
	CurrentHead  string
	Passed       bool
	ActiveRun    bool
	RanThisRound bool
	ReviewAge    time.Duration
	StatusAge    time.Duration
}

// 2. skipReason 返回不补派的原因；空串表示应该补派。判定集中在这里，补扫与测试共用同一
// 套边界，避免「补扫另写一份规则」再次和正常路径分叉。没有关联 PR 时解析不出提交号，
// 「打回后未改动」只能交给轮次上限兜底，不能凭猜测再烧一轮。
func (c reviewSweepCandidate) skipReason() string {
	switch {
	case c.StatusAge < reviewSweepGrace:
		return "任务刚变化"
	case c.ActiveRun:
		return "已有活动审查运行"
	case c.HasRow && c.State == review.StateStopped:
		return "自动返工已停止，等待人工处理"
	case c.HasRow && c.State == review.StatePassed && c.Passed && (c.CurrentHead == "" || c.ReviewedHead == c.CurrentHead):
		return "已通过当前候选"
	case c.HasRow && c.State == review.StateRejected && c.ReviewedHead != "" && c.ReviewedHead == c.CurrentHead:
		return "打回后没有新提交"
	case c.HasRow && c.State == review.StateDispatched && c.RanThisRound:
		// 修改 1. 这一轮审查确实执行过（有审查运行）但没交结论就结束了。没有 PR 时拿不到新
		// 提交，重派只会把同一轮再跑一次，轮次还不增长，等于无限重审；交给人判断。
		return "本轮审查已执行但未提交结论，等待人工处理"
	case c.HasRow && c.State == review.StateDispatched && c.ReviewAge < reviewSweepGrace:
		return "刚派发，等待运行"
	}
	return ""
}

// 3. SweepIssueReviews 扫描所有开启了审查的工作区，给「停在审核中、没有活动审查」的
// 任务补派一轮。重复派发由 startReviewRound 的轮次判定与入队去重兜底，这里只负责选出候选。
func (h *Handler) SweepIssueReviews(ctx context.Context) reviewSweepStats {
	var stats reviewSweepStats
	rows, err := h.DB.Query(ctx, `SELECT workspace_id FROM review_config WHERE enabled AND agent_id IS NOT NULL`)
	if err != nil {
		slog.Warn("review sweep: list workspaces failed", "error", err)
		return stats
	}
	workspaces := make([]pgtype.UUID, 0, 8)
	for rows.Next() {
		var ws pgtype.UUID
		if err := rows.Scan(&ws); err != nil {
			slog.Warn("review sweep: read workspace failed", "error", err)
			break
		}
		workspaces = append(workspaces, ws)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("review sweep: list workspaces failed", "error", err)
		return stats
	}

	for _, ws := range workspaces {
		cfg, configured, err := h.loadReviewConfig(ctx, ws)
		if err != nil || !configured || !cfg.Enabled || !cfg.AgentID.Valid || !cfg.RuntimeID.Valid {
			continue
		}
		// 自定义状态按类别解析：所有映射到「审核中」的状态键都要参与补扫。
		keys, err := issuestatus.ExpandCategories(ctx, h.Queries, ws, []string{issuestatus.InReview})
		if err != nil {
			slog.Warn("review sweep: expand statuses failed", "workspace_id", uuidToString(ws), "error", err)
			continue
		}
		issueIDs, err := h.sweepReviewCandidates(ctx, ws, keys)
		if err != nil {
			slog.Warn("review sweep: list candidates failed", "workspace_id", uuidToString(ws), "error", err)
			continue
		}
		for _, issueID := range issueIDs {
			issue, err := h.Queries.GetIssue(ctx, issueID)
			if err != nil {
				slog.Warn("review sweep: load issue failed", "issue_id", uuidToString(issueID), "error", err)
				continue
			}
			if issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status) != issuestatus.InReview {
				continue
			}
			stats.Scanned++
			reason, err := h.reviewSweepSkipReason(ctx, issue, cfg)
			if err != nil {
				// 读不到状态时按「本轮不动」处理：补扫是兜底，不能靠猜再起一轮审查。
				slog.Warn("review sweep: inspect candidate failed", "issue_id", uuidToString(issue.ID), "error", err)
				stats.Skipped++
				continue
			}
			if reason != "" {
				stats.Skipped++
				continue
			}
			h.startReviewRound(ctx, issue)
			stats.Dispatched++
		}
	}
	return stats
}

// 4. sweepReviewCandidates 取该工作区里停在审核中、且已经过了宽限期的任务，最旧的优先。
func (h *Handler) sweepReviewCandidates(ctx context.Context, ws pgtype.UUID, keys []string) ([]pgtype.UUID, error) {
	rows, err := h.DB.Query(ctx, `SELECT id FROM issue
		WHERE workspace_id=$1 AND status = ANY($2)
		  AND updated_at <= now() - make_interval(secs => $3)
		ORDER BY updated_at ASC
		LIMIT $4`, ws, keys, reviewSweepGrace.Seconds(), reviewSweepBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]pgtype.UUID, 0, 8)
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// 5. reviewSweepSkipReason 汇总一条候选的判断输入并给出跳过原因。
func (h *Handler) reviewSweepSkipReason(ctx context.Context, issue db.Issue, cfg reviewConfig) (string, error) {
	candidate := reviewSweepCandidate{
		CurrentHead: h.TaskService.ResolveIssueReviewSHA(ctx, issue.ID),
		StatusAge:   time.Since(issue.UpdatedAt.Time),
	}
	active, err := h.hasActiveReviewTask(ctx, issue.ID, cfg.AgentID)
	if err != nil {
		return "", err
	}
	candidate.ActiveRun = active
	if passed, ok := parseIssueMetadata(issue.Metadata)[reviewPassedKey].(bool); ok {
		candidate.Passed = passed
	}

	var lastRound int
	var reviewedAt pgtype.Timestamptz
	err = h.DB.QueryRow(ctx, `SELECT round,state,head_sha,updated_at FROM issue_review
		WHERE issue_id=$1 AND workspace_id=$2`, issue.ID, issue.WorkspaceID).Scan(
		&lastRound, &candidate.State, &candidate.ReviewedHead, &reviewedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err == nil {
		candidate.HasRow = true
		candidate.ReviewAge = time.Since(reviewedAt.Time)
		if candidate.State == review.StateDispatched {
			ran, runErr := h.hasReviewRunSince(ctx, issue.ID, cfg.AgentID, reviewedAt)
			if runErr != nil {
				return "", runErr
			}
			candidate.RanThisRound = ran
		}
	}
	return candidate.skipReason(), nil
}

// 6. hasActiveReviewTask 判断审查 Agent 在该任务上是否还有活动执行。范围包含 running 与
// waiting_local_directory：这两种状态已经不在 queued/dispatched 里，但审查仍在进行。
func (h *Handler) hasActiveReviewTask(ctx context.Context, issueID, agentID pgtype.UUID) (bool, error) {
	var active bool
	err := h.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_queue
		WHERE issue_id=$1 AND agent_id=$2 AND status IN `+reviewActiveStatuses+`)`, issueID, agentID).Scan(&active)
	return active, err
}

// 修改 2. hasReviewRunSince 判断本轮派发之后是否已经建过审查运行：已经跑过却没有结论的
// 那一轮不再补派，避免同一轮在无 PR 时反复重跑。
func (h *Handler) hasReviewRunSince(ctx context.Context, issueID, agentID pgtype.UUID, since pgtype.Timestamptz) (bool, error) {
	var ran bool
	err := h.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_queue
		WHERE issue_id=$1 AND agent_id=$2 AND created_at >= $3::timestamptz - interval '1 millisecond')`, issueID, agentID, since).Scan(&ran)
	return ran, err
}
