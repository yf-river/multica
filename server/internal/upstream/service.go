package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 上游同步是全平台共享的一份台账：同步的是同一个上游仓库，
// 每个工作区各跑一份没有意义。写权限由 handler 按成员角色判定。
var (
	ErrBusy    = errors.New("另一批次正在运行")
	ErrRunGone = errors.New("批次不存在或已结束")
)

type Service struct{ Pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{Pool: pool} }

type StartResult struct {
	RunID       string            `json:"run_id"`
	Config      Config            `json:"config"`
	Processed   []string          `json:"processed_commits"`
	Assignments map[string]string `json:"existing_clusters"`
	Manifest    *BatchManifest    `json:"manifest,omitempty"`
}

func newID() string { return util.UUIDToString(dbid.NewV7()) }

// Start 在配置行锁下领取批次，返回原配置和已完成提交，不以游标推断覆盖。
func (s *Service) Start(ctx context.Context, taskID, agentID, triggerSource string) (*StartResult, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_config(singleton) VALUES(1) ON CONFLICT(singleton) DO NOTHING`); err != nil {
		return nil, err
	}
	var c Config
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT upstream_url,upstream_branch,local_ref,batch_size,bootstrap_base_sha,head_sha,cursor_sha,sync_state,COALESCE(agent_id::text,'') FROM upstream_sync_config WHERE singleton=1 FOR UPDATE`).Scan(&c.UpstreamURL, &c.UpstreamBranch, &c.LocalRef, &c.BatchSize, &c.BootstrapBaseSHA, &c.HeadSHA, &c.CursorSHA, &c.SyncState, &c.AgentID)
	if err != nil {
		return nil, err
	}
	enabled = c.SyncState != "paused"
	if !enabled || c.AgentID != agentID {
		return nil, ValidationError{Field: "agent_id", Message: "同步已暂停或执行体已变更"}
	}
	var id, status string
	var frozen []byte
	var alive bool
	err = tx.QueryRow(ctx, `SELECT id::text,status,result,COALESCE(lease_expires_at>now(),false) FROM upstream_sync_run WHERE task_id=$1`, taskID).Scan(&id, &status, &frozen, &alive)
	if err == nil {
		if !alive || (status != "preparing" && status != "running") {
			return nil, ErrRunGone
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	} else {
		if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET status='interrupted',error='租约过期',completed_at=now(),updated_at=now() WHERE status IN ('preparing','running') AND lease_expires_at<now()`); err != nil {
			return nil, err
		}
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upstream_sync_run WHERE status IN ('preparing','running'))`).Scan(&active); err != nil {
			return nil, err
		}
		if active {
			return nil, ErrBusy
		}
		id = newID()
		mode := "incremental"
		if c.SyncState != "incremental" {
			mode = "bootstrap"
		}
		if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_run(id,mode,trigger_source,task_id,status,limit_commits,lease_expires_at,started_at) VALUES($1,$2,$3,$4,'preparing',$5,now()+interval '10 minutes',now())`, id, mode, triggerSource, taskID, c.BatchSize); err != nil {
			return nil, err
		}
	}
	processed := []string{}
	rows, err := tx.Query(ctx, `SELECT c.commit_sha FROM upstream_sync_commit c JOIN upstream_sync_run r ON r.id=c.run_id WHERE r.status='completed' ORDER BY c.commit_sha`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sha string
		if err = rows.Scan(&sha); err != nil {
			rows.Close()
			return nil, err
		}
		processed = append(processed, sha)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	assignments := map[string]string{}
	assignedRows, err := tx.Query(ctx, `SELECT c.commit_sha,i.cluster_key FROM upstream_sync_commit c JOIN upstream_sync_item i ON i.id=c.item_id JOIN upstream_sync_run r ON r.id=c.run_id WHERE r.status IN ('failed','interrupted')`)
	if err != nil {
		return nil, err
	}
	for assignedRows.Next() {
		var sha, key string
		if err = assignedRows.Scan(&sha, &key); err != nil {
			assignedRows.Close()
			return nil, err
		}
		assignments[sha] = key
	}
	assignedRows.Close()
	if assignedRows.Err() != nil {
		return nil, assignedRows.Err()
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	result := &StartResult{RunID: id, Config: c, Processed: processed, Assignments: assignments}
	if status == "running" {
		var m BatchManifest
		if err = json.Unmarshal(frozen, &m); err != nil {
			return nil, err
		}
		result.Manifest = &m
	}
	return result, nil
}

func (s *Service) Prepare(ctx context.Context, runID string, m BatchManifest) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var base string
	if err = tx.QueryRow(ctx, `SELECT bootstrap_base_sha FROM upstream_sync_config WHERE singleton=1 FOR UPDATE`).Scan(&base); err != nil {
		return err
	}
	if base != "" && base != m.BaseSHA {
		return ValidationError{Field: "base_sha", Message: "本轮起点与已确认历史范围不符"}
	}

	var status string
	var limit int
	var old []byte
	if err = tx.QueryRow(ctx, `SELECT status,limit_commits,result FROM upstream_sync_run WHERE id=$1 AND lease_expires_at>now() FOR UPDATE`, runID).Scan(&status, &limit, &old); err != nil {
		return ErrRunGone
	}
	if err = ValidateManifest(m, limit); err != nil {
		return err
	}
	raw, _ := json.Marshal(m)
	if status == "running" {
		var prev BatchManifest
		if json.Unmarshal(old, &prev) != nil {
			return ErrRunGone
		}
		a, _ := json.Marshal(prev)
		if string(a) != string(raw) {
			return ValidationError{Field: "manifest", Message: "本轮提交范围已冻结"}
		}
		return nil
	}
	if status != "preparing" {
		return ErrRunGone
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET base_sha=$2,observed_head_sha=$3,result=$4,has_more=$5,status='running',lease_expires_at=now()+interval '10 minutes' WHERE id=$1`, runID, m.BaseSHA, m.HeadSHA, raw, m.HasMore); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET bootstrap_base_sha=CASE WHEN bootstrap_base_sha='' THEN $1 ELSE bootstrap_base_sha END,head_sha=$2,updated_at=now() WHERE singleton=1`, m.BaseSHA, m.HeadSHA); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Heartbeat(ctx context.Context, runID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE upstream_sync_run SET lease_expires_at=now()+interval '10 minutes' WHERE id=$1 AND status IN ('preparing','running') AND lease_expires_at>now()`, runID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrRunGone
	}
	return err
}

// AppendReport 落一批回传。commit 台账是幂等的支点：同一 sha 重复提交只更新，
// 条目数一律从台账重算，不信 Agent 自报。
func (s *Service) AppendReport(ctx context.Context, runID string, rep Report) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if err = ValidateReport(rep); err != nil {
		return 0, err
	}
	var status string
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT status,result FROM upstream_sync_run WHERE id=$1 AND lease_expires_at>now() FOR UPDATE`, runID).Scan(&status, &raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrRunGone
		}
		return 0, err
	}
	if status != "running" {
		return 0, ErrRunGone
	}
	var manifest BatchManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return 0, err
	}
	expected := map[string]CommitRef{}
	for _, c := range manifest.Commits {
		expected[c.SHA] = c
	}
	for _, c := range rep.Commits {
		original, ok := expected[c.SHA]
		if !ok || original.Absorbed != c.Absorbed || original.AbsorbedMethod != c.AbsorbedMethod || original.PatchID != c.PatchID {
			return 0, ValidationError{Field: "commits", Message: "回传与本轮冻结清单不符"}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET lease_expires_at = now() + $2::interval, updated_at = now()
 WHERE id=$1`, runID, DefaultLease.String()); err != nil {
		return 0, err
	}

	for _, c := range rep.Commits {
		if _, err = tx.Exec(ctx, `INSERT INTO upstream_sync_commit
 (commit_sha, patch_id, run_id, absorbed, absorbed_method, subject, committed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT (commit_sha) DO UPDATE SET
   patch_id=EXCLUDED.patch_id, absorbed=EXCLUDED.absorbed,
   absorbed_method=EXCLUDED.absorbed_method, subject=EXCLUDED.subject,
   committed_at=EXCLUDED.committed_at,run_id=EXCLUDED.run_id
 WHERE upstream_sync_commit.run_id=EXCLUDED.run_id OR EXISTS(SELECT 1 FROM upstream_sync_run r WHERE r.id=upstream_sync_commit.run_id AND r.status IN ('failed','interrupted'))`,
			strings.TrimSpace(c.SHA), c.PatchID, runID, c.Absorbed, c.AbsorbedMethod, c.Subject, c.CommittedAt); err != nil {
			return 0, err
		}
	}

	for _, it := range rep.Items {
		var itemID string
		if err = tx.QueryRow(ctx, `INSERT INTO upstream_sync_item
 (id, run_id, cluster_key, title, summary, rationale, verdict, confidence, presence, paths)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,COALESCE($10::text[],'{}'))
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
			if tag, updateErr := tx.Exec(ctx, `UPDATE upstream_sync_commit SET item_id=$2 WHERE commit_sha=$1 AND run_id=$3 AND (item_id IS NULL OR item_id=$2)`, strings.TrimSpace(sha), itemID, runID); updateErr != nil {
				return 0, updateErr
			} else if tag.RowsAffected() != 1 {
				return 0, ValidationError{Field: "commit_shas", Message: "提交不属于本批或已归入其他条目"}
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

	var scanned, absorbedCount, itemCount int
	if err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM upstream_sync_commit WHERE run_id=$1),
 (SELECT count(*) FROM upstream_sync_commit WHERE run_id=$1 AND absorbed),
 (SELECT count(DISTINCT item_id) FROM upstream_sync_commit WHERE run_id=$1)`, runID).
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
	var singleton int
	if err = tx.QueryRow(ctx, `SELECT singleton FROM upstream_sync_config WHERE singleton=1 FOR UPDATE`).Scan(&singleton); err != nil {
		return err
	}

	var raw []byte
	var mode string
	if err = tx.QueryRow(ctx, `SELECT result,mode FROM upstream_sync_run WHERE id=$1 AND status='running' AND lease_expires_at>now() FOR UPDATE`, runID).Scan(&raw, &mode); err != nil {
		return ErrRunGone
	}
	var m BatchManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	var count, unassigned int
	if err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE NOT absorbed AND item_id IS NULL) FROM upstream_sync_commit WHERE run_id=$1`, runID).Scan(&count, &unassigned); err != nil {
		return err
	}
	expectedCursor := m.HeadSHA
	if len(m.Commits) > 0 {
		expectedCursor = m.Commits[len(m.Commits)-1].SHA
	}
	if cursorSHA != "" && cursorSHA != expectedCursor {
		return ValidationError{Field: "cursor_sha", Message: "只能推进到本批完整处理的位置"}
	}
	if count != len(m.Commits) || unassigned > 0 {
		return ValidationError{Field: "commits", Message: "本批尚有未回传或未归组的提交"}
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_run SET status='completed',cursor_sha=$2,has_more=$3,completed_at=now(),lease_expires_at=NULL,updated_at=now() WHERE id=$1`, runID, expectedCursor, m.HasMore); err != nil {
		return err
	}
	state := "incremental"
	if m.HasMore {
		state = "bootstrap"
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET bootstrap_base_sha=CASE WHEN bootstrap_base_sha='' THEN $4 ELSE bootstrap_base_sha END,cursor_sha=$1,head_sha=$2,sync_state=$3,last_run_at=now(),error='',updated_at=now() WHERE singleton=1`, expectedCursor, m.HeadSHA, state, m.BaseSHA); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Fail(ctx context.Context, runID, message string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var singleton int
	if err = tx.QueryRow(ctx, `SELECT singleton FROM upstream_sync_config WHERE singleton=1 FOR UPDATE`).Scan(&singleton); err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `UPDATE upstream_sync_run SET status='failed',error=$2,completed_at=now(),lease_expires_at=NULL,updated_at=now() WHERE id=$1 AND status IN ('preparing','running')`, runID, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRunGone
	}
	if _, err = tx.Exec(ctx, `UPDATE upstream_sync_config SET error=$1,updated_at=now() WHERE singleton=1`, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
