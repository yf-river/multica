package upstream

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 批次状态机只允许从这两个状态往前推。
var (
	ErrBusy        = errors.New("另一批次正在运行")
	ErrNeedsManual = errors.New("存量同步只能由人工触发")
	ErrRunGone     = errors.New("批次不存在或已结束")
)

type Service struct{ Pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{Pool: pool} }

// StartResult 是 Agent 领到的一批工作范围。
type StartResult struct {
	RunID           string `json:"run_id"`
	Mode            string `json:"mode"`
	BaseSHA         string `json:"base_sha"`
	HeadSHA         string `json:"head_sha"`
	Limit           int    `json:"limit"`
	PreviousCursor  string `json:"previous_cursor"`
	AlreadyAbsorbed int    `json:"already_absorbed"`
}

func newID() string { return util.UUIDToString(dbid.NewV7()) }

// Start 领一批工作。并发由 (workspace_id) WHERE status IN (...) 的唯一索引兜住：
// 两个 Agent 同时进来，只有一个 INSERT 能成功，另一个读到 ErrBusy。
func (s *Service) Start(ctx context.Context, workspaceID, taskID, agentID, triggerSource string) (*StartResult, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// 同一个 task 重试时返回同一批，避免一次任务开出两个 run。
	var existing StartResult
	err = tx.QueryRow(ctx, `SELECT id::text, mode, base_sha, observed_head_sha, limit_commits, cursor_sha
 FROM upstream_sync_run WHERE workspace_id=$1 AND task_id=$2`, workspaceID, taskID).
		Scan(&existing.RunID, &existing.Mode, &existing.BaseSHA, &existing.HeadSHA, &existing.Limit, &existing.PreviousCursor)
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_config(workspace_id) VALUES($1)
 ON CONFLICT (workspace_id) DO NOTHING`, workspaceID); err != nil {
		return nil, err
	}

	// 回收僵尸批次：租约过期的 run 永远不该挡住今天的工作。没有这一步，
	// 一次 daemon 掉线会让整个功能永久卡死。
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET status='interrupted',
 error='租约过期，由新批次接管', updated_at=now()
 WHERE workspace_id=$1 AND status IN ('preparing','running')
 AND (lease_expires_at IS NULL OR lease_expires_at < now())`, workspaceID); err != nil {
		return nil, err
	}

	var activeID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM upstream_sync_run
 WHERE workspace_id=$1 AND status IN ('preparing','running') LIMIT 1`, workspaceID).Scan(&activeID)
	if err == nil {
		return nil, fmt.Errorf("%w: %s", ErrBusy, activeID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	var cursor, bootstrapBase, head string
	var batch int
	if err = tx.QueryRow(ctx, `SELECT cursor_sha, bootstrap_base_sha, head_sha, batch_size
 FROM upstream_sync_config WHERE workspace_id=$1`, workspaceID).
		Scan(&cursor, &bootstrapBase, &head, &batch); err != nil {
		return nil, err
	}

	mode := "incremental"
	if cursor == "" {
		mode = "bootstrap"
		if triggerSource != "manual" {
			return nil, ErrNeedsManual
		}
	}
	base := cursor
	if base == "" {
		base = bootstrapBase
	}

	runID := newID()
	if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_run
 (id, workspace_id, mode, trigger_source, task_id, status, base_sha, observed_head_sha, limit_commits, lease_expires_at, started_at)
 VALUES($1,$2,$3,$4,$5,'running',$6,$7,$8, now() + $9::interval, now())`,
		runID, workspaceID, mode, triggerSource, taskID, base, head, batch, DefaultLease.String()); err != nil {
		// 唯一索引冲突 = 另一个 Agent 抢到了，语义仍是"忙"。
		if strings.Contains(err.Error(), "upstream_sync_run_active") {
			return nil, ErrBusy
		}
		return nil, err
	}

	var absorbed int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM upstream_sync_commit
 WHERE workspace_id=$1 AND absorbed`, workspaceID).Scan(&absorbed); err != nil {
		return nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &StartResult{
		RunID: runID, Mode: mode, BaseSHA: base, HeadSHA: head,
		Limit: batch, PreviousCursor: cursor, AlreadyAbsorbed: absorbed,
	}, nil
}

// AppendReport 落一批回传。commit 台账是幂等的支点：同一 sha 重复提交只更新，
// 条目数一律从台账重算，不信 Agent 自报。
func (s *Service) AppendReport(ctx context.Context, workspaceID, runID string, rep Report) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM upstream_sync_run
 WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, runID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrRunGone
		}
		return 0, err
	}
	if status != "running" {
		return 0, ErrRunGone
	}
	// 每次回传续租；长批次不会被自己的 10 分钟租约掐断。
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET lease_expires_at = now() + $3::interval, updated_at = now()
 WHERE workspace_id=$1 AND id=$2`, workspaceID, runID, DefaultLease.String()); err != nil {
		return 0, err
	}

	for _, it := range rep.Items {
		var itemID string
		if err = tx.QueryRow(ctx, `INSERT INTO upstream_sync_item
 (id, workspace_id, run_id, cluster_key, title, summary, rationale, verdict, confidence, presence, paths)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
 ON CONFLICT (workspace_id, cluster_key) DO UPDATE SET
   title=EXCLUDED.title, summary=EXCLUDED.summary, rationale=EXCLUDED.rationale,
   verdict=EXCLUDED.verdict, confidence=EXCLUDED.confidence, presence=EXCLUDED.presence,
   paths=(SELECT COALESCE(array_agg(DISTINCT p), '{}') FROM unnest(upstream_sync_item.paths || EXCLUDED.paths) AS p),
   updated_at=now()
 RETURNING id::text`,
			newID(), workspaceID, runID, it.ClusterKey, it.Title, it.Summary, it.Rationale,
			it.Verdict, it.Confidence, it.Presence, it.Paths).Scan(&itemID); err != nil {
			return 0, err
		}
		for _, sha := range it.CommitSHAs {
			if _, err = tx.Exec(ctx, `UPDATE upstream_sync_commit SET item_id=$3
 WHERE workspace_id=$1 AND commit_sha=$2`, workspaceID, sha, itemID); err != nil {
				return 0, err
			}
		}
		// 计数从台账重算，重复回传不会把 commit_count 翻倍。
		if _, err = tx.Exec(ctx, `UPDATE upstream_sync_item i SET
 commit_count=(SELECT count(*) FROM upstream_sync_commit c WHERE c.workspace_id=i.workspace_id AND c.item_id=i.id),
 commit_shas=(SELECT COALESCE(array_agg(c.commit_sha ORDER BY c.committed_at NULLS LAST), '{}') FROM upstream_sync_commit c WHERE c.workspace_id=i.workspace_id AND c.item_id=i.id),
 first_commit_at=(SELECT min(c.committed_at) FROM upstream_sync_commit c WHERE c.workspace_id=i.workspace_id AND c.item_id=i.id),
 last_commit_at=(SELECT max(c.committed_at) FROM upstream_sync_commit c WHERE c.workspace_id=i.workspace_id AND c.item_id=i.id),
 updated_at=now()
 WHERE i.workspace_id=$1 AND i.id=$2`, workspaceID, itemID); err != nil {
			return 0, err
		}
	}

	for _, c := range rep.Commits {
		if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_commit
 (workspace_id, commit_sha, patch_id, run_id, absorbed, absorbed_method, subject, committed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)
 ON CONFLICT (workspace_id, commit_sha) DO UPDATE SET
   patch_id=EXCLUDED.patch_id, absorbed=EXCLUDED.absorbed,
   absorbed_method=EXCLUDED.absorbed_method, subject=EXCLUDED.subject,
   committed_at=EXCLUDED.committed_at`,
			workspaceID, strings.TrimSpace(c.SHA), c.PatchID, runID, c.Absorbed, c.AbsorbedMethod, c.Subject, c.CommittedAt); err != nil {
			return 0, err
		}
	}

	var scanned, absorbedCount, itemCount int
	if err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM upstream_sync_commit WHERE workspace_id=$1 AND run_id=$2),
 (SELECT count(*) FROM upstream_sync_commit WHERE workspace_id=$1 AND run_id=$2 AND absorbed),
 (SELECT count(*) FROM upstream_sync_item WHERE workspace_id=$1 AND run_id=$2)`,
		workspaceID, runID).Scan(&scanned, &absorbedCount, &itemCount); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET scanned_commits=$3, absorbed_commits=$4,
 item_count=$5, has_more=(($3 >= limit_commits) OR has_more), updated_at=now()
 WHERE workspace_id=$1 AND id=$2`, workspaceID, runID, scanned, absorbedCount, itemCount); err != nil {
		return 0, err
	}

	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(rep.Items), nil
}

// Finish 关闭批次并推进游标。游标只存 sha：上游 force-push 后 offset 会失效。
func (s *Service) Finish(ctx context.Context, workspaceID, runID, cursorSHA string, hasMore bool) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var mode, observedHead string
	var scanned, limit int
	if err = tx.QueryRow(ctx, `SELECT mode, observed_head_sha, scanned_commits, limit_commits
 FROM upstream_sync_run WHERE workspace_id=$1 AND id=$2 AND status='running' FOR UPDATE`,
		workspaceID, runID).Scan(&mode, &observedHead, &scanned, &limit); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunGone
		}
		return err
	}
	// 扫满了就一定有下一批，忽略 Agent 的乐观判断。
	if scanned >= limit {
		hasMore = true
	}
	if strings.TrimSpace(cursorSHA) == "" {
		return ValidationError{Field: "cursor_sha", Message: "不能为空"}
	}

	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET status='completed', cursor_sha=$3,
 has_more=$4, completed_at=now(), lease_expires_at=NULL, updated_at=now()
 WHERE workspace_id=$1 AND id=$2`, workspaceID, runID, cursorSHA, hasMore); err != nil {
		return err
	}

	nextState := "incremental"
	if mode == "bootstrap" && hasMore {
		nextState = "bootstrap"
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET cursor_sha=$2, head_sha=$3,
 sync_state=$4, last_run_at=now(), error='', updated_at=now()
 WHERE workspace_id=$1`, workspaceID, cursorSHA, observedHead, nextState); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Fail(ctx context.Context, workspaceID, runID, message string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE upstream_sync_run SET status='failed', error=$3,
 completed_at=now(), lease_expires_at=NULL, updated_at=now()
 WHERE workspace_id=$1 AND id=$2 AND status IN ('preparing','running')`,
		workspaceID, runID, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRunGone
	}
	return nil
}
