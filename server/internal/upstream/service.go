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

// 上游同步是全平台共享的一份台账：同步的是同一个上游仓库，
// 每个工作区各跑一份没有意义。写权限由 handler 按成员角色判定。
var (
	ErrBusy        = errors.New("另一批次正在运行")
	ErrNeedsManual = errors.New("存量同步只能由人工触发")
	ErrRunGone     = errors.New("批次不存在或已结束")
)

type Service struct{ Pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{Pool: pool} }

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

// Start 领一批工作。并发由"同状态只允许一行"的部分唯一索引兜住。
func (s *Service) Start(ctx context.Context, taskID, agentID, triggerSource string) (*StartResult, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var existing StartResult
	err = tx.QueryRow(ctx, `SELECT id::text, mode, base_sha, observed_head_sha, limit_commits, cursor_sha
 FROM upstream_sync_run WHERE task_id=$1`, taskID).
		Scan(&existing.RunID, &existing.Mode, &existing.BaseSHA, &existing.HeadSHA, &existing.Limit, &existing.PreviousCursor)
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_config(singleton) VALUES(1)
 ON CONFLICT (singleton) DO NOTHING`); err != nil {
		return nil, err
	}

	// 回收僵尸批次：租约过期的 run 不该挡住今天的工作。
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET status='interrupted',
 error='租约过期，由新批次接管', updated_at=now()
 WHERE status IN ('preparing','running')
 AND (lease_expires_at IS NULL OR lease_expires_at < now())`); err != nil {
		return nil, err
	}

	var activeID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM upstream_sync_run
 WHERE status IN ('preparing','running') LIMIT 1`).Scan(&activeID)
	if err == nil {
		return nil, fmt.Errorf("%w: %s", ErrBusy, activeID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	var cursor, bootstrapBase, head string
	var batch int
	if err = tx.QueryRow(ctx, `SELECT cursor_sha, bootstrap_base_sha, head_sha, batch_size
 FROM upstream_sync_config WHERE singleton=1`).Scan(&cursor, &bootstrapBase, &head, &batch); err != nil {
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
 (id, mode, trigger_source, task_id, status, base_sha, observed_head_sha, limit_commits, lease_expires_at, started_at)
 VALUES($1,$2,$3,$4,'running',$5,$6,$7, now() + $8::interval, now())`,
		runID, mode, triggerSource, taskID, base, head, batch, DefaultLease.String()); err != nil {
		if strings.Contains(err.Error(), "upstream_sync_run_active") {
			return nil, ErrBusy
		}
		return nil, err
	}

	var absorbed int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM upstream_sync_commit WHERE absorbed`).Scan(&absorbed); err != nil {
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
func (s *Service) AppendReport(ctx context.Context, runID string, rep Report) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM upstream_sync_run WHERE id=$1 FOR UPDATE`, runID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrRunGone
		}
		return 0, err
	}
	if status != "running" {
		return 0, ErrRunGone
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET lease_expires_at = now() + $2::interval, updated_at = now()
 WHERE id=$1`, runID, DefaultLease.String()); err != nil {
		return 0, err
	}

	for _, it := range rep.Items {
		var itemID string
		if err = tx.QueryRow(ctx, `INSERT INTO upstream_sync_item
 (id, run_id, cluster_key, title, summary, rationale, verdict, confidence, presence, paths)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
 ON CONFLICT (cluster_key) DO UPDATE SET
   title=EXCLUDED.title, summary=EXCLUDED.summary, rationale=EXCLUDED.rationale,
   verdict=EXCLUDED.verdict, confidence=EXCLUDED.confidence, presence=EXCLUDED.presence,
   paths=(SELECT COALESCE(array_agg(DISTINCT p), '{}') FROM unnest(upstream_sync_item.paths || EXCLUDED.paths) AS p),
   updated_at=now()
 RETURNING id::text`,
			newID(), runID, it.ClusterKey, it.Title, it.Summary, it.Rationale,
			it.Verdict, it.Confidence, it.Presence, it.Paths).Scan(&itemID); err != nil {
			return 0, err
		}
		for _, sha := range it.CommitSHAs {
			if _, err = tx.Exec(ctx, `UPDATE upstream_sync_commit SET item_id=$2 WHERE commit_sha=$1`, strings.TrimSpace(sha), itemID); err != nil {
				return 0, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE upstream_sync_item i SET
 commit_count=(SELECT count(*) FROM upstream_sync_commit c WHERE c.item_id=i.id),
 commit_shas=(SELECT COALESCE(array_agg(c.commit_sha ORDER BY c.committed_at NULLS LAST), '{}') FROM upstream_sync_commit c WHERE c.item_id=i.id),
 first_commit_at=(SELECT min(c.committed_at) FROM upstream_sync_commit c WHERE c.item_id=i.id),
 last_commit_at=(SELECT max(c.committed_at) FROM upstream_sync_commit c WHERE c.item_id=i.id),
 updated_at=now()
 WHERE i.id=$1`, itemID); err != nil {
			return 0, err
		}
	}

	for _, c := range rep.Commits {
		if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_commit
 (commit_sha, patch_id, run_id, absorbed, absorbed_method, subject, committed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT (commit_sha) DO UPDATE SET
   patch_id=EXCLUDED.patch_id, absorbed=EXCLUDED.absorbed,
   absorbed_method=EXCLUDED.absorbed_method, subject=EXCLUDED.subject,
   committed_at=EXCLUDED.committed_at`,
			strings.TrimSpace(c.SHA), c.PatchID, runID, c.Absorbed, c.AbsorbedMethod, c.Subject, c.CommittedAt); err != nil {
			return 0, err
		}
	}

	var scanned, absorbedCount, itemCount int
	if err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM upstream_sync_commit WHERE run_id=$1),
 (SELECT count(*) FROM upstream_sync_commit WHERE run_id=$1 AND absorbed),
 (SELECT count(*) FROM upstream_sync_item WHERE run_id=$1)`, runID).
		Scan(&scanned, &absorbedCount, &itemCount); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET scanned_commits=$2, absorbed_commits=$3,
 item_count=$4, has_more=(($2 >= limit_commits) OR has_more), updated_at=now()
 WHERE id=$1`, runID, scanned, absorbedCount, itemCount); err != nil {
		return 0, err
	}

	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(rep.Items), nil
}

// Finish 关闭批次并推进游标。游标只存 sha：上游 force-push 后 offset 会失效。
func (s *Service) Finish(ctx context.Context, runID, cursorSHA string, hasMore bool) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var mode, observedHead string
	var scanned, limit int
	if err = tx.QueryRow(ctx, `SELECT mode, observed_head_sha, scanned_commits, limit_commits
 FROM upstream_sync_run WHERE id=$1 AND status='running' FOR UPDATE`, runID).
		Scan(&mode, &observedHead, &scanned, &limit); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunGone
		}
		return err
	}
	if scanned >= limit {
		hasMore = true
	}
	if strings.TrimSpace(cursorSHA) == "" {
		return ValidationError{Field: "cursor_sha", Message: "不能为空"}
	}

	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET status='completed', cursor_sha=$2,
 has_more=$3, completed_at=now(), lease_expires_at=NULL, updated_at=now()
 WHERE id=$1`, runID, cursorSHA, hasMore); err != nil {
		return err
	}

	nextState := "incremental"
	if mode == "bootstrap" && hasMore {
		nextState = "bootstrap"
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET cursor_sha=$1, head_sha=$2,
 sync_state=$3, last_run_at=now(), error='', updated_at=now() WHERE singleton=1`,
		cursorSHA, observedHead, nextState); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Fail(ctx context.Context, runID, message string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE upstream_sync_run SET status='failed', error=$2,
 completed_at=now(), lease_expires_at=NULL, updated_at=now()
 WHERE id=$1 AND status IN ('preparing','running')`, runID, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRunGone
	}
	return nil
}
