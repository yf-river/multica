package upstream

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Config struct {
	UpstreamURL      string     `json:"upstream_url"`
	UpstreamBranch   string     `json:"upstream_branch"`
	LocalRef         string     `json:"local_ref"`
	RuntimeID        string     `json:"runtime_id"`
	AgentID          string     `json:"agent_id"`
	AutopilotID      string     `json:"autopilot_id"`
	BatchSize        int        `json:"batch_size"`
	SyncState        string     `json:"sync_state"`
	BootstrapBaseSHA string     `json:"bootstrap_base_sha"`
	CursorSHA        string     `json:"cursor_sha"`
	HeadSHA          string     `json:"head_sha"`
	LastRunAt        *time.Time `json:"last_run_at"`
	Error            string     `json:"error"`
	CreatedBy        string     `json:"created_by"`
}

type Snapshot struct {
	Config      Config `json:"config"`
	Pending     int    `json:"pending"`
	Adopted     int    `json:"adopted"`
	ActiveRunID string `json:"active_run_id"`
	// Instructions 由 handler 注入内置提示词，供页面只读预览。
	Instructions string `json:"instructions"`
}

type Item struct {
	ID            string     `json:"id"`
	RunID         string     `json:"run_id"`
	ClusterKey    string     `json:"cluster_key"`
	Title         string     `json:"title"`
	Summary       string     `json:"summary"`
	Rationale     string     `json:"rationale"`
	Verdict       string     `json:"verdict"`
	Confidence    string     `json:"confidence"`
	Presence      string     `json:"presence"`
	Paths         []string   `json:"paths"`
	CommitCount   int        `json:"commit_count"`
	CommitSHAs    []string   `json:"commit_shas"`
	FirstCommitAt *time.Time `json:"first_commit_at"`
	LastCommitAt  *time.Time `json:"last_commit_at"`
	Status        string     `json:"status"`
	AdoptedAt     *time.Time `json:"adopted_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

type ItemFilter struct {
	Status   string
	Verdict  string
	Presence string
	Query    string
	Limit    int
	Offset   int
}

type Run struct {
	ID              string     `json:"id"`
	Mode            string     `json:"mode"`
	TriggerSource   string     `json:"trigger_source"`
	Status          string     `json:"status"`
	BaseSHA         string     `json:"base_sha"`
	ObservedHeadSHA string     `json:"observed_head_sha"`
	CursorSHA       string     `json:"cursor_sha"`
	ScannedCommits  int        `json:"scanned_commits"`
	AbsorbedCommits int        `json:"absorbed_commits"`
	ItemCount       int        `json:"item_count"`
	HasMore         bool       `json:"has_more"`
	Error           string     `json:"error"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (s *Service) Snapshot(ctx context.Context) (*Snapshot, error) {
	if _, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config(singleton) VALUES(1)
 ON CONFLICT (singleton) DO NOTHING`); err != nil {
		return nil, err
	}

	var out Snapshot
	var runtimeID, agentID, autopilotID, createdBy *string
	if err := s.Pool.QueryRow(ctx, `SELECT upstream_url, upstream_branch, local_ref,
 runtime_id::text, agent_id::text, autopilot_id::text, batch_size, sync_state, bootstrap_base_sha,
 cursor_sha, head_sha, last_run_at, error, created_by::text
 FROM upstream_sync_config WHERE singleton=1`).
		Scan(&out.Config.UpstreamURL, &out.Config.UpstreamBranch, &out.Config.LocalRef,
			&runtimeID, &agentID, &autopilotID, &out.Config.BatchSize, &out.Config.SyncState,
			&out.Config.BootstrapBaseSHA, &out.Config.CursorSHA, &out.Config.HeadSHA,
			&out.Config.LastRunAt, &out.Config.Error, &createdBy); err != nil {
		return nil, err
	}
	if runtimeID != nil {
		out.Config.RuntimeID = *runtimeID
	}
	if agentID != nil {
		out.Config.AgentID = *agentID
	}
	if autopilotID != nil {
		out.Config.AutopilotID = *autopilotID
	}
	if createdBy != nil {
		out.Config.CreatedBy = *createdBy
	}
	if err := s.Pool.QueryRow(ctx, `SELECT
 count(*) FILTER (WHERE status='open'),
 count(*) FILTER (WHERE status='adopted')
 FROM upstream_sync_item`).Scan(&out.Pending, &out.Adopted); err != nil {
		return nil, err
	}
	err := s.Pool.QueryRow(ctx, `SELECT id::text FROM upstream_sync_run
 WHERE status IN ('preparing','running') LIMIT 1`).Scan(&out.ActiveRunID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return &out, nil
}

// UpsertConfig 只接受可以安全变更的字段；游标由 Finish 推进。
func (s *Service) UpsertConfig(ctx context.Context, c Config, patchAgent *string, patchAutopilot *string, actorID string) error {
	if c.BatchSize < 1 || c.BatchSize > 500 {
		return ValidationError{Field: "batch_size", Message: "必须在 1 到 500 之间"}
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO upstream_sync_config
 (singleton, upstream_url, upstream_branch, local_ref, runtime_id, batch_size, created_by)
 VALUES(1,$1,$2,$3,$4,$5,$6)
 ON CONFLICT (singleton) DO UPDATE SET
   upstream_url=EXCLUDED.upstream_url, upstream_branch=EXCLUDED.upstream_branch,
   local_ref=EXCLUDED.local_ref, runtime_id=EXCLUDED.runtime_id,
   batch_size=EXCLUDED.batch_size, updated_at=now()`,
		c.UpstreamURL, c.UpstreamBranch, c.LocalRef, nullableUUID(c.RuntimeID), c.BatchSize, nullableUUID(actorID))
	if err != nil {
		return err
	}
	if patchAgent != nil {
		if _, err = s.Pool.Exec(ctx, `UPDATE upstream_sync_config SET agent_id=$1, updated_at=now() WHERE singleton=1`, nullableUUID(*patchAgent)); err != nil {
			return err
		}
	}
	if patchAutopilot != nil {
		if _, err = s.Pool.Exec(ctx, `UPDATE upstream_sync_config SET autopilot_id=$1, updated_at=now() WHERE singleton=1`, nullableUUID(*patchAutopilot)); err != nil {
			return err
		}
	}
	return nil
}

func nullableUUID(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func (s *Service) ListItems(ctx context.Context, f ItemFilter) ([]Item, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	if f.Status == "" {
		f.Status = "open"
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text, run_id::text, cluster_key, title, summary, rationale,
 verdict, confidence, presence, paths, commit_count, commit_shas,
 first_commit_at, last_commit_at, status, adopted_at, created_at
 FROM upstream_sync_item
 WHERE status=$1
   AND ($2 = '' OR verdict=$2)
   AND ($3 = '' OR presence=$3)
   AND ($4 = '' OR title ILIKE '%'||$4||'%' OR summary ILIKE '%'||$4||'%' OR cluster_key ILIKE '%'||$4||'%')
 ORDER BY created_at DESC, id DESC
 LIMIT $5 OFFSET $6`,
		f.Status, f.Verdict, f.Presence, f.Query, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Item, 0, f.Limit)
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.RunID, &it.ClusterKey, &it.Title, &it.Summary, &it.Rationale,
			&it.Verdict, &it.Confidence, &it.Presence, &it.Paths, &it.CommitCount, &it.CommitSHAs,
			&it.FirstCommitAt, &it.LastCommitAt, &it.Status, &it.AdoptedAt, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkItems 只改标记，不触发任何代码或任务副作用。
func (s *Service) MarkItems(ctx context.Context, ids []string, status, actorID string) (int, error) {
	switch status {
	case "adopted", "ignored", "open":
	default:
		return 0, ValidationError{Field: "status", Message: "只允许 adopted、ignored 或 open"}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	var adoptedAt any
	var adoptedBy any
	if status == "adopted" {
		adoptedAt = time.Now()
		adoptedBy = nullableUUID(actorID)
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE upstream_sync_item SET status=$2,
 adopted_at=$3, adopted_by=$4, updated_at=now()
 WHERE id = ANY($1::uuid[])`, ids, status, adoptedAt, adoptedBy)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Service) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text, mode, trigger_source, status, base_sha,
 observed_head_sha, cursor_sha, scanned_commits, absorbed_commits, item_count,
 has_more, error, started_at, completed_at, created_at
 FROM upstream_sync_run ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Run, 0, limit)
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Mode, &r.TriggerSource, &r.Status, &r.BaseSHA,
			&r.ObservedHeadSHA, &r.CursorSHA, &r.ScannedCommits, &r.AbsorbedCommits,
			&r.ItemCount, &r.HasMore, &r.Error, &r.StartedAt, &r.CompletedAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
