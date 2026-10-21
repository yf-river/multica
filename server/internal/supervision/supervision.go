// Package supervision 管理工作区审查台账、材料覆盖与监督执行。
package supervision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const RulesVersion = "supervision-v6"

type Service struct {
	Pool      *pgxpool.Pool
	Tasks     *service.TaskService
	Authorize func(context.Context, string, string, string) bool
}

func New(pool *pgxpool.Pool, tasks *service.TaskService) *Service {
	return &Service{Pool: pool, Tasks: tasks}
}

type Evidence struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	RunID string `json:"run_id,omitempty"`
}
type Material struct {
	Evidence
	Generation   int64  `json:"generation"`
	Offset       int    `json:"offset"`
	End          int    `json:"end"`
	Length       int    `json:"length"`
	Text         string `json:"text,omitempty"`
	Note         bool   `json:"supervisor_note,omitempty"`
	ContextOnly  bool   `json:"context_only,omitempty"`
	UnreadOffset int    `json:"unread_offset,omitempty"`
}
type Finding struct {
	ID             string     `json:"id,omitempty"`
	Code           string     `json:"code"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Impact         string     `json:"impact"`
	Recommendation string     `json:"recommendation"`
	Confidence     string     `json:"confidence"`
	Evidence       []Evidence `json:"evidence"`
}
type Resolution struct {
	ID       string     `json:"id"`
	Reason   string     `json:"reason"`
	Evidence []Evidence `json:"evidence"`
}
type Result struct {
	Summary     string       `json:"summary"`
	Efficiency  string       `json:"efficiency"`
	Correctness string       `json:"correctness"`
	Gaps        []string     `json:"gaps"`
	Findings    []Finding    `json:"findings"`
	Resolutions []Resolution `json:"resolutions"`
	Covered     []Evidence   `json:"covered"`
}

func uuid(s string) pgtype.UUID { u, _ := util.ParseUUID(s); return u }

// Tick 仅安排有待办的工作区；每个工作区的配置行与任务唯一索引共同限制并发派发。
func (s *Service) Tick(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT workspace_id::text FROM supervision_config ORDER BY workspace_id`)
	if err != nil {
		return err
	}
	var workspaces []string
	for rows.Next() {
		var ws string
		if err = rows.Scan(&ws); err != nil {
			rows.Close()
			return err
		}
		workspaces = append(workspaces, ws)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ws := range workspaces {
		if err = s.reconcile(ctx, ws); err != nil {
			s.setError(ctx, ws, err)
			continue
		}
		var enabled bool
		if err = s.Pool.QueryRow(ctx, `SELECT enabled FROM supervision_config WHERE workspace_id=$1`, ws).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			continue
		}
		if err = s.inventory(ctx, ws); err != nil {
			s.setError(ctx, ws, err)
			continue
		}
		_, err = s.Pool.Exec(ctx, `UPDATE supervision_subject s SET reviewed_version=desired_version,summary='暂无执行记录，尚无法评价执行质量。'
		  WHERE workspace_id=$1 AND last_checked_at IS NULL AND NOT EXISTS(SELECT 1 FROM agent_task_queue t WHERE t.issue_id=s.issue_id)`, ws)
		if err != nil {
			s.setError(ctx, ws, err)
			continue
		}
		var capacity int
		if err = s.Pool.QueryRow(ctx, `SELECT COALESCE(a.max_concurrent_tasks,1) FROM supervision_config c LEFT JOIN agent a ON a.id=c.agent_id WHERE c.workspace_id=$1`, ws).Scan(&capacity); err != nil {
			return err
		}
		for i := 0; i < capacity; i++ {
			if err = s.dispatch(ctx, ws); err != nil {
				s.setError(ctx, ws, err)
				break
			}
		}
	}
	return nil
}
func (s *Service) setError(ctx context.Context, ws string, err error) {
	_, _ = s.Pool.Exec(ctx, `UPDATE supervision_config SET error=$2,last_scan_at=now() WHERE workspace_id=$1`, ws, err.Error())
}

// inventory 每轮有界补齐历史引用。已存在引用不变，在线写入由事务触发器登记。
func (s *Service) inventory(ctx context.Context, ws string) error {
	var recent bool
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(last_inventory_at>now()-make_interval(secs=>interval_seconds),false) FROM supervision_config WHERE workspace_id=$1`, ws).Scan(&recent); err != nil {
		return err
	}
	if recent {
		_, err := s.Pool.Exec(ctx, `UPDATE supervision_config SET last_scan_at=now() WHERE workspace_id=$1`, ws)
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO supervision_subject(workspace_id,issue_id)
      SELECT workspace_id,id FROM issue i WHERE workspace_id=$1 AND COALESCE(origin_type,'')<>'health_report'
      AND NOT EXISTS(SELECT 1 FROM supervision_subject s WHERE s.workspace_id=$1 AND s.issue_id=i.id)
      ORDER BY id LIMIT 200 ON CONFLICT DO NOTHING`, ws); err != nil {
		return err
	}
	var inserted int
	err = tx.QueryRow(ctx, `WITH sources AS (
      SELECT i.id issue_id,'issue'::text kind,i.id source_id,NULL::uuid run_id FROM issue i WHERE i.workspace_id=$1
      UNION ALL SELECT c.issue_id,'comment',c.id,NULL FROM comment c JOIN issue i ON i.id=c.issue_id WHERE i.workspace_id=$1
        AND NOT EXISTS(SELECT 1 FROM agent_task_queue t WHERE t.id=c.source_task_id AND t.context->>'type'='quality_supervision')
      UNION ALL SELECT t.issue_id,'task',t.id,t.id FROM agent_task_queue t JOIN issue i ON i.id=t.issue_id WHERE i.workspace_id=$1 AND COALESCE(t.context->>'type','')<>'quality_supervision'
      UNION ALL SELECT t.issue_id,'message',m.id,t.id FROM task_message m JOIN agent_task_queue t ON t.id=m.task_id JOIN issue i ON i.id=t.issue_id WHERE i.workspace_id=$1 AND COALESCE(t.context->>'type','')<>'quality_supervision'
      UNION ALL SELECT e.issue_id,'trace',e.id,e.task_id FROM task_trace_event e WHERE e.workspace_id=$1
    ), missing AS (
      SELECT x.* FROM sources x JOIN supervision_subject s ON s.workspace_id=$1 AND s.issue_id=x.issue_id
      WHERE NOT EXISTS(SELECT 1 FROM supervision_material m WHERE m.workspace_id=$1 AND m.kind=x.kind AND m.source_id=x.source_id)
      ORDER BY x.kind,x.source_id LIMIT 2000
    ), inserted AS (
      INSERT INTO supervision_material(workspace_id,issue_id,kind,source_id,run_id)
      SELECT $1,issue_id,kind,source_id,run_id FROM missing ON CONFLICT DO NOTHING RETURNING issue_id
    ) , dirtied AS (UPDATE supervision_subject SET desired_version=desired_version+1,next_attempt_at=now() WHERE workspace_id=$1 AND issue_id IN(SELECT issue_id FROM inserted) RETURNING issue_id)
 SELECT count(*) FROM inserted`, ws).Scan(&inserted)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE supervision_config SET last_scan_at=now(),last_inventory_at=now(),inventory_complete=($2<2000 AND NOT EXISTS(
      SELECT 1 FROM issue i WHERE i.workspace_id=$1 AND COALESCE(origin_type,'')<>'health_report' AND NOT EXISTS(SELECT 1 FROM supervision_subject s WHERE s.workspace_id=$1 AND s.issue_id=i.id)
    )) WHERE workspace_id=$1`, ws, inserted)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func sourceSQL(kind string) (string, error) {
	switch kind {
	case "issue":
		return `SELECT jsonb_build_object('id',id,'title',title,'description',description,'acceptance_criteria',acceptance_criteria,'status',status,'revision',revision,'parent_issue_id',parent_issue_id,'updated_at',updated_at)::text FROM issue WHERE id=$1 AND workspace_id=$2`, nil
	case "comment":
		return `SELECT jsonb_build_object('id',c.id,'content',c.content,'author_type',c.author_type,'created_at',c.created_at,'updated_at',c.updated_at)::text FROM comment c JOIN issue i ON i.id=c.issue_id WHERE c.id=$1 AND i.workspace_id=$2`, nil
	case "task":
		return `SELECT jsonb_build_object('id',t.id,'status',t.status,'result',t.result,'error',t.error,'work_dir',t.work_dir,'created_at',t.created_at,'started_at',t.started_at,'completed_at',t.completed_at)::text FROM agent_task_queue t JOIN issue i ON i.id=t.issue_id WHERE t.id=$1 AND i.workspace_id=$2`, nil
	case "message":
		return `SELECT jsonb_build_object('id',m.id,'seq',m.seq,'type',m.type,'tool',m.tool,'content',m.content,'input',m.input,'output',m.output,'created_at',m.created_at)::text FROM task_message m JOIN agent_task_queue t ON t.id=m.task_id JOIN issue i ON i.id=t.issue_id WHERE m.id=$1 AND i.workspace_id=$2`, nil
	case "trace":
		return `SELECT jsonb_build_object('id',id,'event_type',event_type,'status',status,'error_type',error_type,'failure_reason',failure_reason,'metadata',metadata,'created_at',created_at)::text FROM task_trace_event WHERE id=$1 AND workspace_id=$2`, nil
	default:
		return "", fmt.Errorf("unknown material kind %q", kind)
	}
}

// materials 使用字符偏移分片，读取正文完整范围前不会确认该引用已覆盖。
func (s *Service) materials(ctx context.Context, tx pgx.Tx, ws, issue string) ([]Material, error) {
	rows, err := tx.Query(ctx, `SELECT m.kind,m.source_id::text,COALESCE(m.run_id::text,''),m.generation,
      CASE WHEN m.generation<>m.reviewed_generation THEN m.reviewed_offset ELSE 0 END
      FROM supervision_material m
      LEFT JOIN task_message tm ON m.kind='message' AND tm.id=m.source_id
      LEFT JOIN agent_task_queue t ON t.id=m.run_id
      WHERE m.workspace_id=$1 AND m.issue_id=$2 AND m.generation>m.reviewed_generation
      ORDER BY CASE m.kind WHEN 'issue' THEN 0 WHEN 'comment' THEN 1 WHEN 'task' THEN 2 WHEN 'message' THEN 3 ELSE 4 END,
        CASE WHEN m.kind='comment' THEN m.changed_at END ASC,
        CASE WHEN m.kind='message' THEN t.created_at END,
        CASE WHEN m.kind='message' THEN m.run_id END,
        tm.seq,m.changed_at,m.source_id LIMIT 24`, ws, issue)
	if err != nil {
		return nil, err
	}
	items := []Material{}
	for rows.Next() {
		var m Material
		if err = rows.Scan(&m.Kind, &m.ID, &m.RunID, &m.Generation, &m.Offset); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	budget := 48000
	out := []Material{}
	for _, m := range items {
		query, err := sourceSQL(m.Kind)
		if err != nil {
			return nil, err
		}
		size := min(40000, budget)
		if size <= 0 {
			break
		}
		m.UnreadOffset = m.Offset
		m.Offset = max(0, m.Offset-min(1000, size/4))
		err = tx.QueryRow(ctx, `SELECT length(body),substring(body from $3::integer for $4::integer) FROM (`+query+`) source(body)`, m.ID, ws, m.Offset+1, size).Scan(&m.Length, &m.Text)
		if errors.Is(err, pgx.ErrNoRows) {
			m.Text = "原始材料已删除，不能确认其原有内容。"
			m.Length = m.Offset + len([]rune(m.Text))
		} else if err != nil {
			return nil, err
		}
		m.End = m.Offset + len([]rune(m.Text))
		budget -= len([]rune(m.Text))
		m.Text = redact.Text(m.Text)
		if m.Kind == "comment" {
			if err = tx.QueryRow(ctx, `SELECT author_type='member' AND lower(ltrim(content))~'^/note([[:space:]]|$)' FROM comment WHERE id=$1`, m.ID).Scan(&m.Note); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
		}
		out = append(out, m)
		if m.End < m.Length {
			break
		}
	}
	return s.withMessageContext(ctx, tx, ws, out)
}

// withMessageContext 保存跨消息边界的原文；辅助片段不推进任何材料的阅读进度。
func (s *Service) withMessageContext(ctx context.Context, tx pgx.Tx, ws string, materials []Material) ([]Material, error) {
	seen := map[string]bool{}
	for _, m := range materials {
		seen[m.ID] = true
	}
	budget := 8000
	out := append([]Material(nil), materials...)
	for _, m := range materials {
		if m.Kind != "message" {
			continue
		}
		for _, before := range []bool{true, false} {
			if budget <= 0 {
				break
			}
			var id string
			// 后继只补已审查的消息，用来复核迟到消息插入后的衔接。
			err := tx.QueryRow(ctx, `SELECT n.id::text FROM task_message n
     JOIN supervision_material sm ON sm.source_id=n.id AND sm.kind='message' AND sm.workspace_id=$2
     JOIN task_message current ON current.id=$1 AND current.task_id=n.task_id
     WHERE (($3::boolean AND (n.seq,n.id)<(current.seq,current.id))
         OR (NOT $3::boolean AND (n.seq,n.id)>(current.seq,current.id)))
     ORDER BY CASE WHEN $3::boolean THEN n.seq END DESC,CASE WHEN $3::boolean THEN n.id END DESC,
       CASE WHEN NOT $3::boolean THEN n.seq END,CASE WHEN NOT $3::boolean THEN n.id END
     LIMIT 1`, m.ID, ws, before).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if seen[id] {
				continue
			}
			var reviewed bool
			if err = tx.QueryRow(ctx, `SELECT reviewed_generation=generation FROM supervision_material WHERE workspace_id=$1 AND kind='message' AND source_id=$2`, ws, id).Scan(&reviewed); err != nil {
				return nil, err
			}
			if !reviewed {
				continue
			}
			query, _ := sourceSQL("message")
			c := Material{Evidence: Evidence{Kind: "message", ID: id, RunID: m.RunID}, ContextOnly: true}
			size := min(1000, budget)
			err = tx.QueryRow(ctx, `SELECT length(body),CASE WHEN $3::boolean THEN greatest(0,length(body)-$4::integer) ELSE 0 END,
     CASE WHEN $3::boolean THEN right(body,$4::integer) ELSE left(body,$4::integer) END FROM (`+query+`) source(body)`, id, ws, before, size).Scan(&c.Length, &c.Offset, &c.Text)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			c.End = c.Offset + len([]rune(c.Text))
			budget -= len([]rune(c.Text))
			c.Text = redact.Text(c.Text)
			out = append(out, c)
			seen[id] = true
		}
	}
	return out, nil
}

func (s *Service) dispatch(ctx context.Context, ws string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var agent, owner string
	var interval int
	var investigation bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(agent_id::text,''),owner_id::text,interval_seconds,investigation_enabled FROM supervision_config WHERE workspace_id=$1 AND enabled FOR UPDATE SKIP LOCKED`, ws).Scan(&agent, &owner, &interval, &investigation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if agent == "" {
		return fmt.Errorf("尚未选择监督 Agent")
	}
	if s.Authorize == nil || !s.Authorize(ctx, ws, owner, agent) {
		return fmt.Errorf("监督配置负责人已失去所选 Agent 的调用权限")
	}
	var capacity, active int
	// Agent 行锁使共享该 Agent 的工作区也不会超额登记。
	if err = tx.QueryRow(ctx, `SELECT max_concurrent_tasks FROM agent WHERE id=$1 FOR UPDATE`, agent).Scan(&capacity); err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND status IN('queued','dispatched','running')`, agent).Scan(&active); err != nil {
		return err
	}
	if active >= capacity {
		return nil
	}
	var issue string
	var version int64
	var summary string
	err = tx.QueryRow(ctx, `SELECT s.issue_id::text,s.desired_version,s.summary FROM supervision_subject s JOIN issue i ON i.id=s.issue_id
      WHERE s.workspace_id=$1 AND s.desired_version>s.reviewed_version AND s.next_attempt_at<=now() AND s.failures<3
      AND NOT EXISTS(SELECT 1 FROM supervision_batch b WHERE b.workspace_id=s.workspace_id AND b.issue_id=s.issue_id AND b.status IN('preparing','running'))
      AND (s.force_review OR s.last_checked_at IS NULL OR s.last_checked_at<now()-make_interval(secs=>$2::double precision)
        OR NOT EXISTS(SELECT 1 FROM agent_task_queue t WHERE t.issue_id=s.issue_id AND t.status IN('queued','dispatched','running')))
      ORDER BY s.force_review DESC,s.last_checked_at NULLS FIRST,s.changed_at,s.issue_id FOR UPDATE OF s SKIP LOCKED LIMIT 1`, ws, interval).Scan(&issue, &version, &summary)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	materials, err := s.materials(ctx, tx, ws, issue)
	if err != nil {
		return err
	}
	if len(materials) == 0 {
		_, err = tx.Exec(ctx, `UPDATE supervision_subject SET reviewed_version=$3,last_checked_at=now(),force_review=false WHERE workspace_id=$1 AND issue_id=$2`, ws, issue, version)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	batch := util.UUIDToString(dbid.NewV7())
	raw, _ := json.Marshal(materials)
	if _, err = tx.Exec(ctx, `INSERT INTO supervision_batch(id,workspace_id,issue_id,target_version,materials,rules_version,created_at) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp())`, batch, ws, issue, version, raw, RulesVersion); err != nil {
		return err
	}
	var current json.RawMessage
	err = tx.QueryRow(ctx, currentIssueSnapshotSQL, issue, ws).Scan(&current)
	if err != nil {
		return err
	}
	var open json.RawMessage
	err = tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'title',title,'description',description,'evidence',evidence)),'[]') FROM supervision_finding WHERE workspace_id=$1 AND issue_id=$2 AND status IN('open','in_progress')`, ws, issue).Scan(&open)
	if err != nil {
		return err
	}
	prompt, err := BuildPrompt(batch, string(current), summary, string(open), materials, investigation)
	if err != nil {
		return err
	}
	task, err := s.Tasks.EnqueueSupervisionTaskTx(ctx, db.New(tx), uuid(ws), uuid(owner), uuid(agent), uuid(batch), prompt, investigation)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE supervision_batch SET task_id=$2,status='running' WHERE id=$1`, batch, task.ID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	s.Tasks.NotifyTaskEnqueued(ctx, task)
	return nil
}

const currentIssueSnapshotSQL = `SELECT jsonb_build_object('title',title,'description',left(description,8000),'acceptance_criteria',left(acceptance_criteria::text,4000),'status',status,
      'requirement_truncated',length(COALESCE(description,''))>8000 OR length(COALESCE(acceptance_criteria::text,''))>4000,
      'pending_materials',(SELECT count(*) FROM supervision_material WHERE workspace_id=$2 AND issue_id=$1 AND generation>reviewed_generation),
      'previous_attempt_error',(SELECT left(error,1000) FROM supervision_subject WHERE workspace_id=$2 AND issue_id=$1),
      'finding_decisions',(SELECT COALESCE(jsonb_agg(x),'[]') FROM (SELECT f.id,f.status,f.human_resolution,left(f.title,300) title,left(a.reason,2000) reason,a.action,a.created_at
        FROM supervision_finding f JOIN LATERAL (SELECT reason,action,created_at FROM supervision_action WHERE workspace_id=$2 AND finding_id=f.id AND action IN('open','in_progress','resolved','ignored','auto_resolved') ORDER BY created_at DESC,id DESC LIMIT 1) a ON true
        WHERE f.workspace_id=$2 AND f.issue_id=$1 ORDER BY a.created_at DESC,f.id DESC LIMIT 100)x),
      'finding_decisions_truncated',(SELECT count(*)>100 FROM supervision_finding f WHERE f.workspace_id=$2 AND f.issue_id=$1 AND EXISTS(SELECT 1 FROM supervision_action a WHERE a.workspace_id=$2 AND a.finding_id=f.id AND a.action IN('open','in_progress','resolved','ignored','auto_resolved'))),
      'latest_user_comments',(SELECT COALESCE(jsonb_agg(x),'[]') FROM (SELECT id,left(content,4000) content,length(content)>4000 truncated,created_at,updated_at FROM comment WHERE issue_id=$1 AND author_type='member' ORDER BY GREATEST(updated_at,created_at) DESC,id DESC LIMIT 6)x))
      FROM issue WHERE id=$1 AND workspace_id=$2`

func BuildPrompt(batch, current, summary, open string, materials []Material, investigate ...bool) (string, error) {
	coverage := []Evidence{}
	for _, material := range materials {
		if !material.ContextOnly {
			coverage = append(coverage, material.Evidence)
		}
	}
	raw, err := json.Marshal(struct {
		BatchID              string          `json:"batch_id"`
		Current              json.RawMessage `json:"current"`
		PreviousSummary      string          `json:"previous_summary"`
		OpenFindings         json.RawMessage `json:"open_findings"`
		Materials            []Material      `json:"materials"`
		Covered              []Evidence      `json:"covered"`
		InvestigationEnabled bool            `json:"investigation_enabled"`
	}{batch, json.RawMessage(redact.Text(current)), redact.Text(summary), json.RawMessage(redact.Text(open)), materials, coverage, len(investigate) > 0 && investigate[0]})
	return string(raw), err
}

func ParseResult(text string) (Result, error) {
	var r Result
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if i := strings.Index(text, "\n"); i >= 0 {
			text = text[i+1:]
			text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "```"))
		}
	}
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return r, fmt.Errorf("监督结果不是合法JSON：%w", err)
	}
	valid := func(v string) bool { return v == "clear" || v == "problem" || v == "uncertain" }
	if r.Summary == "" || !valid(r.Efficiency) || !valid(r.Correctness) || r.Covered == nil || r.Gaps == nil || r.Findings == nil || r.Resolutions == nil {
		return r, fmt.Errorf("监督结果字段不完整")
	}
	return r, nil
}
func ValidateResult(r Result, materials []Material, investigations ...Material) error {
	allowed := map[string]Material{}
	for _, m := range materials {
		if !m.ContextOnly {
			allowed[m.Kind+":"+m.ID] = m
		}
	}
	check := func(e Evidence) bool {
		m, ok := allowed[e.Kind+":"+e.ID]
		return ok && (e.RunID == "" || e.RunID == m.RunID)
	}
	seen := map[string]bool{}
	for _, e := range r.Covered {
		key := e.Kind + ":" + e.ID
		if !check(e) || seen[key] {
			return fmt.Errorf("覆盖引用未知或重复")
		}
		seen[key] = true
	}
	if len(seen) != len(allowed) {
		return fmt.Errorf("本轮材料未全部确认覆盖，进度不推进")
	}
	for _, m := range materials {
		if m.ContextOnly {
			allowed[m.Kind+":"+m.ID] = m
		}
	}
	for _, m := range investigations {
		allowed[m.Kind+":"+m.ID] = m
	}
	for index, f := range r.Findings {
		if f.ID != "" {
			if _, err := util.ParseUUID(f.ID); err != nil {
				return fmt.Errorf("已有问题编号无效")
			}
		}
		for _, field := range []struct{ name, value string }{{"code", f.Code}, {"title", f.Title}, {"description", f.Description}, {"impact", f.Impact}, {"recommendation", f.Recommendation}} {
			if strings.TrimSpace(field.value) == "" {
				return fmt.Errorf("findings[%d].%s 缺失或为空", index, field.name)
			}
		}
		if len(f.Evidence) == 0 {
			return fmt.Errorf("findings[%d].evidence 必须包含本批次证据", index)
		}
		if f.Confidence != "confirmed" && f.Confidence != "risk" {
			return fmt.Errorf("findings[%d].confidence 只能为 confirmed 或 risk", index)
		}
		for _, e := range f.Evidence {
			if !check(e) {
				return fmt.Errorf("问题证据不属于本轮授权范围")
			}
		}
	}
	for _, resolution := range r.Resolutions {
		if resolution.ID == "" || resolution.Reason == "" || len(resolution.Evidence) == 0 {
			return fmt.Errorf("解决记录缺少证据")
		}
		for _, e := range resolution.Evidence {
			if !check(e) {
				return fmt.Errorf("解决证据不属于本轮")
			}
		}
	}
	return nil
}

// Receipt 是平台保存的工具回执，不由模型重复申报阅读位置。
type Receipt struct {
	Path       string `json:"path"`
	Tool       string `json:"tool"`
	Generation int64  `json:"generation"`
	Offset     int    `json:"offset"`
	End        int    `json:"end"`
	Length     int    `json:"length"`
	Error      string `json:"error"`
}

func receiptMaterials(receipts map[string]Receipt) []Material {
	var pages []Material
	for _, page := range receipts {
		parts := strings.Split(page.Path, "/")
		if page.Error != "" || page.Tool != "supervision_material" || len(parts) != 2 || page.Generation < 1 || page.Offset < 0 || page.End <= page.Offset || page.End > page.Length {
			continue
		}
		switch parts[0] {
		case "issue", "comment", "task", "message", "trace":
		default:
			continue
		}
		if _, err := util.ParseUUID(parts[1]); err != nil {
			continue
		}
		pages = append(pages, Material{Evidence: Evidence{Kind: parts[0], ID: parts[1]}, Generation: page.Generation, Offset: page.Offset, End: page.End, Length: page.Length})
	}
	return pages
}

// ContiguousEnd 只推进连续审查的同版本原文，不能跨越未读空洞。
func ContiguousEnd(offset int, pages []Material) int {
	sort.Slice(pages, func(i, j int) bool { return pages[i].Offset < pages[j].Offset })
	for _, page := range pages {
		if page.Offset > offset {
			break
		}
		if page.End > offset {
			offset = page.End
		}
	}
	return offset
}

// ValidateBatch 只读校验，使用与最终提交相同的解析、证据及回执规则。
func (s *Service) ValidateBatch(ctx context.Context, ws, batch, task string, result Result) error {
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT materials FROM supervision_batch WHERE workspace_id=$1 AND id=$2 AND task_id=$3 AND status='running'`, ws, batch, task).Scan(&raw); err != nil {
		return fmt.Errorf("监督批次已失效")
	}
	var materials []Material
	if err := json.Unmarshal(raw, &materials); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id::text,COALESCE(result->>'run_id','') FROM supervision_investigation WHERE workspace_id=$1 AND batch_id=$2 AND task_id=$3`, ws, batch, task)
	if err != nil {
		return err
	}
	var investigations []Material
	for rows.Next() {
		var id, run string
		if err = rows.Scan(&id, &run); err != nil {
			rows.Close()
			return err
		}
		investigations = append(investigations, Material{Evidence: Evidence{Kind: "investigation", ID: id, RunID: run}})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = ValidateResult(result, materials, investigations...); err != nil {
		return err
	}
	for _, id := range append(findingIDs(result), resolutionIDs(result)...) {
		var exists bool
		if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supervision_finding WHERE workspace_id=$1 AND id=$2 AND issue_id=(SELECT issue_id FROM supervision_batch WHERE id=$3))`, ws, id, batch).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("问题编号不属于本任务：%s", id)
		}
	}
	return nil
}
func findingIDs(r Result) []string {
	var out []string
	for _, f := range r.Findings {
		if f.ID != "" {
			out = append(out, f.ID)
		}
	}
	return out
}
func resolutionIDs(r Result) []string {
	var out []string
	for _, f := range r.Resolutions {
		out = append(out, f.ID)
	}
	return out
}

func (s *Service) reconcile(ctx context.Context, ws string) error {
	rows, err := s.Pool.Query(ctx, `SELECT b.id::text,b.task_id::text,COALESCE(t.status,'missing'),COALESCE(t.result->>'output',''),COALESCE(t.error,'') FROM supervision_batch b LEFT JOIN agent_task_queue t ON t.id=b.task_id WHERE b.workspace_id=$1 AND b.status='running' AND (t.id IS NULL OR t.status IN('completed','failed','cancelled'))`, ws)
	if err != nil {
		return err
	}
	type completed struct{ id, task, status, output, err string }
	items := []completed{}
	for rows.Next() {
		var x completed
		if err = rows.Scan(&x.id, &x.task, &x.status, &x.output, &x.err); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, x := range items {
		result, err := ParseResult(x.output)
		if x.status != "completed" {
			err = fmt.Errorf("监督运行%s：%s", x.status, x.err)
		}
		if err != nil {
			if e := s.fail(ctx, x.id, err); e != nil {
				return e
			}
			continue
		}
		if err = s.Commit(ctx, ws, x.id, x.task, result); err != nil {
			if e := s.fail(ctx, x.id, err); e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Service) fail(ctx context.Context, batch string, cause error) error {
	_, err := s.Pool.Exec(ctx, `WITH failed AS(UPDATE supervision_batch SET status='failed',error=$2,completed_at=now() WHERE id=$1 AND status IN('preparing','running') RETURNING workspace_id,issue_id)
      UPDATE supervision_subject s SET failures=failures+1,error=$2,next_attempt_at=now()+make_interval(secs=>60*power(5,LEAST(failures,2))::int)
      FROM failed f WHERE s.workspace_id=f.workspace_id AND s.issue_id=f.issue_id`, batch, cause.Error())
	return err
}

// Commit 将结论与本轮覆盖原子保存。重复提交返回已有结果，不覆盖后继批次。
func (s *Service) Commit(ctx context.Context, ws, batch, task string, result Result) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var issue, status, taskID string
	var target int64
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT issue_id::text,status,COALESCE(task_id::text,''),target_version,materials FROM supervision_batch WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, batch).Scan(&issue, &status, &taskID, &target, &raw)
	if err != nil {
		return err
	}
	if taskID != task {
		return fmt.Errorf("批次运行不匹配")
	}
	if status == "completed" {
		return nil
	}
	if status != "running" {
		return fmt.Errorf("批次已失效")
	}
	// 人工操作与结果提交共用任务锁，避免新决定插入检查与写入之间。
	var subject string
	if err = tx.QueryRow(ctx, `SELECT issue_id::text FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2 FOR UPDATE`, ws, issue).Scan(&subject); err != nil {
		return err
	}
	protectedIDs := map[string]bool{}
	protectedFingerprints := map[string]bool{}
	decisions, err := tx.Query(ctx, `SELECT DISTINCT f.id::text,f.fingerprint FROM supervision_finding f
      JOIN supervision_action a ON a.finding_id=f.id AND a.workspace_id=f.workspace_id
      WHERE f.workspace_id=$1 AND f.issue_id=$2 AND a.action IN('open','in_progress','resolved','ignored')
      AND a.created_at>(SELECT created_at FROM supervision_batch WHERE id=$3)`, ws, issue, batch)
	if err != nil {
		return err
	}
	for decisions.Next() {
		var id, fingerprint string
		if err = decisions.Scan(&id, &fingerprint); err != nil {
			decisions.Close()
			return err
		}
		protectedIDs[id] = true
		protectedFingerprints[fingerprint] = true
	}
	err = decisions.Err()
	decisions.Close()
	if err != nil {
		return err
	}
	var materials []Material
	if err = json.Unmarshal(raw, &materials); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,COALESCE(result->>'run_id',''),result FROM supervision_investigation WHERE workspace_id=$1 AND batch_id=$2 AND task_id=$3`, ws, batch, task)
	if err != nil {
		return err
	}
	var investigations []Material
	receipts := map[string]Receipt{}
	for rows.Next() {
		var m Material
		m.Kind = "investigation"
		var receiptBody []byte
		if err = rows.Scan(&m.ID, &m.RunID, &receiptBody); err != nil {
			rows.Close()
			return err
		}
		var receipt Receipt
		if err = json.Unmarshal(receiptBody, &receipt); err != nil {
			rows.Close()
			return err
		}
		receipts[m.ID] = receipt
		investigations = append(investigations, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = ValidateResult(result, materials, investigations...); err != nil {
		return err
	}
	pages := receiptMaterials(receipts)
	for _, f := range result.Findings {
		if protectedIDs[f.ID] {
			continue
		}
		if f.ID != "" {
			evidence := marshalEvidence(f.Evidence, batch)
			tag, err := tx.Exec(ctx, `UPDATE supervision_finding SET batch_id=$4,title=$5,description=$6,impact=$7,recommendation=$8,confidence=$9,evidence=$10,last_seen_at=now(),
              status=CASE WHEN status='resolved' THEN 'open' ELSE status END,human_resolution=false
              WHERE workspace_id=$1 AND issue_id=$2 AND id=$3 AND first_seen_at<(SELECT created_at FROM supervision_batch WHERE id=$4)`, ws, issue, f.ID, batch, redact.Text(f.Title), redact.Text(f.Description), redact.Text(f.Impact), redact.Text(f.Recommendation), f.Confidence, evidence)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("复查问题未指向已有记录")
			}
			continue
		}
		hash := sha256.Sum256([]byte(f.Code + ":" + f.Evidence[0].Kind + ":" + f.Evidence[0].ID))
		if protectedFingerprints[hex.EncodeToString(hash[:])] {
			continue
		}
		evidence := marshalEvidence(f.Evidence, batch)
		_, err = tx.Exec(ctx, `INSERT INTO supervision_finding(workspace_id,issue_id,fingerprint,batch_id,title,description,impact,recommendation,confidence,evidence)
          VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(workspace_id,issue_id,fingerprint) DO UPDATE
          SET batch_id=EXCLUDED.batch_id,title=EXCLUDED.title,description=EXCLUDED.description,impact=EXCLUDED.impact,recommendation=EXCLUDED.recommendation,
          confidence=EXCLUDED.confidence,evidence=EXCLUDED.evidence,last_seen_at=now(),status=CASE WHEN supervision_finding.status='resolved' THEN 'open' ELSE supervision_finding.status END,
          human_resolution=false`, ws, issue, hex.EncodeToString(hash[:]), batch, redact.Text(f.Title), redact.Text(f.Description), redact.Text(f.Impact), redact.Text(f.Recommendation), f.Confidence, evidence)
		if err != nil {
			return err
		}
	}
	for _, r := range result.Resolutions {
		if protectedIDs[r.ID] {
			continue
		}
		evidence := marshalEvidence(r.Evidence, batch)
		tag, err := tx.Exec(ctx, `UPDATE supervision_finding SET status='resolved',human_resolution=false,last_seen_at=now() WHERE workspace_id=$1 AND issue_id=$2 AND id=$3 AND first_seen_at<(SELECT created_at FROM supervision_batch WHERE id=$4)`, ws, issue, r.ID, batch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("解决记录未指向已有问题")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO supervision_action(workspace_id,finding_id,actor_id,operation_id,action,reason,receipt)
          SELECT workspace_id,$2,owner_id,$3,'auto_resolved',$4,jsonb_build_object('batch_id',$5::text,'evidence',$6::jsonb)
          FROM supervision_config WHERE workspace_id=$1`, ws, r.ID, dbid.NewV7(), redact.Text(r.Reason), batch, evidence); err != nil {
			return err
		}
	}
	groups := map[string][]Material{}
	for _, m := range append(materials, pages...) {
		if !m.ContextOnly {
			key := m.Kind + ":" + m.ID
			groups[key] = append(groups[key], m)
		}
	}
	for _, ranges := range groups {
		first := ranges[0]
		var generation int64
		var offset int
		if err = tx.QueryRow(ctx, `SELECT generation,reviewed_offset FROM supervision_material WHERE workspace_id=$1 AND kind=$2 AND source_id=$3 FOR UPDATE`, ws, first.Kind, first.ID).Scan(&generation, &offset); err != nil {
			return err
		}
		valid := []Material{}
		length := 0
		for _, page := range ranges {
			if page.Generation == generation {
				valid = append(valid, page)
				length = page.Length
			}
		}
		if len(valid) == 0 {
			continue
		}
		end := ContiguousEnd(offset, valid)
		_, err = tx.Exec(ctx, `UPDATE supervision_material SET reviewed_offset=$5::integer,reviewed_generation=CASE WHEN $5::integer>=$6::integer THEN generation ELSE reviewed_generation END WHERE workspace_id=$1 AND kind=$2 AND source_id=$3 AND generation=$4`, ws, first.Kind, first.ID, generation, end, length)
		if err != nil {
			return err
		}
	}

	summary, efficiency, correctness := result.Summary, result.Efficiency, result.Correctness
	currentGaps := result.Gaps
	if len(protectedIDs) > 0 {
		summary = "检查期间有新的人工处理，本轮结论保存在审查历史，当前问题状态以最新人工处理为准。"
		efficiency, correctness = "uncertain", "uncertain"
		currentGaps = []string{"人工处理后尚未重新复核。"}
	}
	gaps, _ := json.Marshal(currentGaps)
	_, err = tx.Exec(ctx, `UPDATE supervision_subject SET summary=$3,efficiency=$4,correctness=$5,gaps=$6,last_checked_at=now(),failures=0,error='',force_review=false,
      reviewed_version=CASE WHEN NOT EXISTS(SELECT 1 FROM supervision_material m WHERE m.workspace_id=$1 AND m.issue_id=$2 AND m.generation>m.reviewed_generation) THEN $7 ELSE reviewed_version END,
      next_attempt_at=now() WHERE workspace_id=$1 AND issue_id=$2`, ws, issue, redact.Text(summary), efficiency, correctness, gaps, target)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `UPDATE supervision_batch SET status='completed',result=$2,completed_at=now() WHERE id=$1`, batch, body); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE supervision_config SET last_success_at=now(),error='' WHERE workspace_id=$1`, ws); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type evidenceSnapshot struct {
	Evidence
	BatchID string `json:"batch_id,omitempty"`
}

func marshalEvidence(evidence []Evidence, batch string) []byte {
	refs := make([]evidenceSnapshot, len(evidence))
	for i, ref := range evidence {
		refs[i].Evidence = ref
		if ref.Kind != "investigation" {
			refs[i].BatchID = batch
		}
	}
	raw, _ := json.Marshal(refs)
	return raw
}

// ReadBatchEvidence 返回审查时保存的有界片段，编辑后的原文不能替换历史证据。
func (s *Service) ReadBatchEvidence(ctx context.Context, ws, batch, kind, id string) (EvidencePage, error) {
	page := EvidencePage{Evidence: Evidence{Kind: kind, ID: id}, BatchID: batch, Snapshot: true}
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT b.issue_id::text,m FROM supervision_batch b CROSS JOIN LATERAL jsonb_array_elements(b.materials) m WHERE b.workspace_id=$1 AND b.id=$2 AND m->>'kind'=$3 AND m->>'id'=$4`, ws, batch, kind, id).Scan(&page.IssueID, &raw)
	if err != nil {
		return page, err
	}
	var material Material
	if err = json.Unmarshal(raw, &material); err != nil {
		return page, err
	}
	if material.Text == "" && material.End > material.Offset {
		return page, pgx.ErrNoRows
	}
	page.RunID, page.Offset, page.End, page.Length = material.RunID, material.Offset, material.End, material.Length
	page.Text = redact.Text(material.Text)
	return page, nil
}

// PollInterval 仅为后台发现节奏；业务分析合并窗口由工作区配置控制。
const PollInterval = 15 * time.Second

type EvidencePage struct {
	Evidence
	BatchID    string `json:"batch_id,omitempty"`
	IssueID    string `json:"issue_id"`
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	End        int    `json:"end"`
	Length     int    `json:"length"`
	Deleted    bool   `json:"deleted"`
	Snapshot   bool   `json:"snapshot"`
	Generation int64  `json:"generation"`
	SHA256     string `json:"sha256"`
}

// ReadEvidence 只读取已登记的工作区材料，按字符分页，原文不可用时保留明确状态。
func (s *Service) ReadEvidence(ctx context.Context, ws, kind, id string, offset int) (EvidencePage, error) {
	page := EvidencePage{Evidence: Evidence{Kind: kind, ID: id}, Offset: offset}
	if kind == "investigation" {
		err := s.Pool.QueryRow(ctx, `SELECT issue_id::text,COALESCE(result->>'run_id',''),length(body),substring(body from $3::integer for 8000) FROM (SELECT issue_id,result,jsonb_build_object('request',request,'result',result)::text body FROM supervision_investigation WHERE workspace_id=$1 AND id=$2) x`, ws, id, offset+1).Scan(&page.IssueID, &page.RunID, &page.Length, &page.Text)
		page.End = offset + len([]rune(page.Text))
		page.Text = redact.Text(page.Text)
		return page, err
	}
	query, err := sourceSQL(kind)
	if err != nil {
		return page, err
	}
	err = s.Pool.QueryRow(ctx, `SELECT m.issue_id::text,COALESCE(m.run_id::text,''),m.generation,COALESCE(length(source.body),0),COALESCE(substring(source.body from $4::integer for 8000),''),COALESCE(encode(sha256(convert_to(source.body,'UTF8')),'hex'),''),source.body IS NULL FROM supervision_material m LEFT JOIN LATERAL (`+query+`) source(body) ON true WHERE m.workspace_id=$2 AND m.kind=$3 AND m.source_id=$1`, id, ws, kind, offset+1).Scan(&page.IssueID, &page.RunID, &page.Generation, &page.Length, &page.Text, &page.SHA256, &page.Deleted)

	if errors.Is(err, pgx.ErrNoRows) {
		page.Deleted = true
		return page, nil
	}
	if err != nil {
		return page, err
	}
	page.End = offset + len([]rune(page.Text))
	page.Text = redact.Text(page.Text)
	return page, nil
}
