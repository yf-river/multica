package upstream

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Config struct {
	ProjectID           string     `json:"project_id"`
	UpstreamURL         string     `json:"upstream_url"`
	UpstreamBranch      string     `json:"upstream_branch"`
	LocalRef            string     `json:"local_ref"`
	RuntimeID           string     `json:"runtime_id"`
	AgentID             string     `json:"agent_id"`
	AutopilotID         string     `json:"autopilot_id"`
	BatchSize           int        `json:"batch_size"`
	ScheduleHour        int        `json:"schedule_hour"`
	SyncState           string     `json:"sync_state"`
	BootstrapBaseSHA    string     `json:"bootstrap_base_sha"`
	BaselineCommittedAt *time.Time `json:"baseline_committed_at"`
	HeadCommittedAt     *time.Time `json:"head_committed_at"`
	PendingCommits      *int       `json:"pending_commits"`
	CursorSHA           string     `json:"cursor_sha"`
	HeadSHA             string     `json:"head_sha"`
	LastRunAt           *time.Time `json:"last_run_at"`
	Error               string     `json:"error"`
	CreatedBy           string     `json:"created_by"`
	// Model 与 ThinkingLevel 存在执行体上，空值表示跟随运行时默认。
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinking_level"`
	// Instructions 是用户改写后的提示词，空值表示跟随内置默认。
	Instructions string `json:"instructions"`
}

type Snapshot struct {
	Config      Config `json:"config"`
	Pending     int    `json:"pending"`
	Adopted     int    `json:"adopted"`
	Ignored     int    `json:"ignored"`
	ActiveRunID string `json:"active_run_id"`
	// NextRunAt 兼容历史响应，手动同步模式下为空。
	NextRunAt *time.Time `json:"next_run_at"`
	// RuntimeOnline 只用于提示；离线不阻止保存设置。
	RuntimeOnline bool `json:"runtime_online"`
	// ExecutorState 为 none（未绑定运行时）、unbound（执行体失去运行时）或 bound。
	ExecutorState string `json:"executor_state"`
	// CanWrite 由 handler 按成员角色注入，只影响页面入口，不代替接口校验。
	CanWrite bool `json:"can_write"`
	LastRun  *Run `json:"last_run"`
}

type Item struct {
	ID                string     `json:"id"`
	RunID             string     `json:"run_id"`
	ClusterKey        string     `json:"cluster_key"`
	Title             string     `json:"title"`
	Summary           string     `json:"summary"`
	Rationale         string     `json:"rationale"`
	Verdict           string     `json:"verdict"`
	Confidence        string     `json:"confidence"`
	Presence          string     `json:"presence"`
	Paths             []string   `json:"paths"`
	CommitCount       int        `json:"commit_count"`
	CommitSHAs        []string   `json:"commit_shas"`
	FirstCommitAt     *time.Time `json:"first_commit_at"`
	LastCommitAt      *time.Time `json:"last_commit_at"`
	Status            string     `json:"status"`
	AdoptedAt         *time.Time `json:"adopted_at"`
	CreatedAt         time.Time  `json:"created_at"`
	AbsorptionIssueID string     `json:"absorption_issue_id,omitempty"`
	AbsorptionStatus  string     `json:"absorption_status"`
	// HasNewCommits 表示已采纳的条目后来又被上游补了提交；只做标记，不自动改状态。
	HasNewCommits bool `json:"has_new_commits"`
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
	if _, err := s.exec(ctx, `INSERT INTO upstream_sync_config(project_id) VALUES(@project)
 ON CONFLICT (project_id) DO NOTHING`); err != nil {
		return nil, err
	}

	var out Snapshot
	out.Config.ProjectID = s.Project()
	var agentID, autopilotID, createdBy *string
	var agentRow, boundRuntime bool
	var runtimeStatus, savedRuntime string
	// 设置页新增的两项存在独立的设置表里，配置表保持升级期的旧结构；
	// 没有设置行时按默认值展示，等同于用户没有改过。
	if err := s.queryRow(ctx, `SELECT c.upstream_url, c.upstream_branch, c.local_ref,
 c.agent_id::text, c.autopilot_id::text, c.batch_size, COALESCE(ss.schedule_hour,9), c.sync_state, c.bootstrap_base_sha,
 c.baseline_committed_at,c.head_committed_at,c.pending_commits,c.cursor_sha, c.head_sha, c.last_run_at, c.error, c.created_by::text, COALESCE(ss.instructions,''),
 COALESCE(a.runtime_id::text,''), COALESCE(a.model,''), COALESCE(a.thinking_level,''),
 (a.id IS NOT NULL), COALESCE(r.status,''), COALESCE(c.runtime_id::text,'')
 FROM upstream_sync_config c
 LEFT JOIN upstream_sync_settings ss ON ss.project_id=@project
 LEFT JOIN agent a ON a.id=c.agent_id
 LEFT JOIN agent_runtime r ON r.id=a.runtime_id
 WHERE c.project_id=@project`).
		Scan(&out.Config.UpstreamURL, &out.Config.UpstreamBranch, &out.Config.LocalRef,
			&agentID, &autopilotID, &out.Config.BatchSize, &out.Config.ScheduleHour, &out.Config.SyncState,
			&out.Config.BootstrapBaseSHA, &out.Config.BaselineCommittedAt, &out.Config.HeadCommittedAt, &out.Config.PendingCommits, &out.Config.CursorSHA, &out.Config.HeadSHA,
			&out.Config.LastRunAt, &out.Config.Error, &createdBy, &out.Config.Instructions,
			&out.Config.RuntimeID, &out.Config.Model, &out.Config.ThinkingLevel,
			&agentRow, &runtimeStatus, &savedRuntime); err != nil {
		return nil, err
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
	boundRuntime = out.Config.RuntimeID != ""
	out.RuntimeOnline = boundRuntime && runtimeStatus == "online"
	// 执行体随运行时一起被删掉时，配置里仍留着 agent_id / runtime_id：
	// 那是"失去运行时、要重新绑定"，不是"从未配置"。
	switch {
	case agentRow && boundRuntime:
		out.ExecutorState = "bound"
	case agentRow || agentID != nil || savedRuntime != "":
		out.ExecutorState = "unbound"
	default:
		out.ExecutorState = "none"
	}
	if err := s.queryRow(ctx, `SELECT
 count(*) FILTER (WHERE status='open'),
 count(*) FILTER (WHERE status='adopted'),
 count(*) FILTER (WHERE status='ignored')
 FROM upstream_sync_item WHERE project_id=@project`).Scan(&out.Pending, &out.Adopted, &out.Ignored); err != nil {
		return nil, err
	}
	// 修改 1. 活动批次的口径与写入一致：租约未过期且领取它的任务仍在执行，任务结束后
	// 页面不再显示一个已经回写不了的批次，也不会因此继续 5 秒轮询。
	err := s.queryRow(ctx, `SELECT id::text FROM upstream_sync_run
 WHERE project_id=@project AND status IN ('preparing','running') AND `+runAlive+` LIMIT 1`).Scan(&out.ActiveRunID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if out.Config.AutopilotID != "" {
		err = s.queryRow(ctx, `SELECT next_run_at FROM autopilot_trigger
 WHERE autopilot_id=$1 AND kind='schedule' LIMIT 1`, out.Config.AutopilotID).Scan(&out.NextRunAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	runs, _, err := s.ListRuns(ctx, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(runs) > 0 {
		out.LastRun = &runs[0]
	}
	return &out, nil
}

func nullableUUID(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// ListItems 返回一页条目与满足筛选条件的总数，页尾判断用 total 而不是"这页是否满"。
func (s *Service) ListItems(ctx context.Context, f ItemFilter) ([]Item, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	// 空值沿用"只看待定"的旧语义；all 表示不按状态过滤。
	if f.Status == "" {
		f.Status = "open"
	}
	if f.Status == "all" {
		f.Status = ""
	}
	if f.Presence == "all" {
		f.Presence = ""
	}
	rows, err := s.query(ctx, `SELECT i.id::text, i.run_id::text, i.cluster_key, i.title, i.summary, i.rationale,
 i.verdict, i.confidence, i.presence, i.paths, i.commit_count, i.commit_shas,
 i.first_commit_at, i.last_commit_at, i.status, i.adopted_at, i.created_at,
 COALESCE(a.issue_id::text,''), COALESCE(a.status,'none'), count(*) OVER(),
 EXISTS(SELECT 1 FROM upstream_sync_commit c WHERE c.item_id=i.id AND i.adopted_at IS NOT NULL AND c.created_at>i.adopted_at)
 FROM upstream_sync_item i
 LEFT JOIN upstream_sync_absorption_task a ON a.item_id=i.id
 WHERE i.project_id=@project AND ($1 = '' OR i.status=$1)
   AND ($2 = '' OR i.verdict=$2)
   AND ($3 = '' OR i.presence=$3)
   AND ($4 = '' OR i.title ILIKE '%'||$4||'%' OR i.summary ILIKE '%'||$4||'%' OR i.cluster_key ILIKE '%'||$4||'%')
 ORDER BY i.created_at DESC, i.id DESC
 LIMIT $5 OFFSET $6`,
		f.Status, f.Verdict, f.Presence, f.Query, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Item, 0, f.Limit)
	total := 0
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.RunID, &it.ClusterKey, &it.Title, &it.Summary, &it.Rationale,
			&it.Verdict, &it.Confidence, &it.Presence, &it.Paths, &it.CommitCount, &it.CommitSHAs,
			&it.FirstCommitAt, &it.LastCommitAt, &it.Status, &it.AdoptedAt, &it.CreatedAt,
			&it.AbsorptionIssueID, &it.AbsorptionStatus, &total, &it.HasNewCommits); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 && f.Offset > 0 {
		// 空页没有窗口计数，用一次计数补齐，保证页尾判断稳定。
		if err := s.queryRow(ctx, `SELECT count(*) FROM upstream_sync_item i
 WHERE i.project_id=@project AND ($1 = '' OR i.status=$1) AND ($2 = '' OR i.verdict=$2) AND ($3 = '' OR i.presence=$3)
   AND ($4 = '' OR i.title ILIKE '%'||$4||'%' OR i.summary ILIKE '%'||$4||'%' OR i.cluster_key ILIKE '%'||$4||'%')`,
			f.Status, f.Verdict, f.Presence, f.Query).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
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
	tag, err := s.exec(ctx, `UPDATE upstream_sync_item SET status=$2,
 adopted_at=$3, adopted_by=$4, updated_at=now()
 WHERE project_id=@project AND id = ANY($1::uuid[])`, ids, status, adoptedAt, adoptedBy)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ListRuns 返回一页同步记录与总条数，页尾判断用 total 而不是"这页是否满 30 条"。
// 总数与条目清单用的是同一套分页语义，最后一页刚好满页时也不会多一次空请求。
func (s *Service) ListRuns(ctx context.Context, limit, offset int) ([]Run, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	var total int
	if err := s.queryRow(ctx, `SELECT count(*) FROM upstream_sync_run WHERE project_id=@project`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.query(ctx, `SELECT id::text, mode, trigger_source, status, base_sha,
 observed_head_sha, cursor_sha, scanned_commits, absorbed_commits, item_count,
 has_more, error, started_at, completed_at, created_at
 FROM upstream_sync_run WHERE project_id=@project ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Run, 0, limit)
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Mode, &r.TriggerSource, &r.Status, &r.BaseSHA,
			&r.ObservedHeadSHA, &r.CursorSHA, &r.ScannedCommits, &r.AbsorbedCommits,
			&r.ItemCount, &r.HasMore, &r.Error, &r.StartedAt, &r.CompletedAt, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
