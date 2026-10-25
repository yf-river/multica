package supervision

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// Modification 1. Keep the page, candidates and detail offset in the existing durable JSON cursor.
type tracePreparation struct {
	ID, Workspace, Issue, Round, Run, Runtime string
	Attempts                                  int
	Version                                   int64
	Cursor                                    int
	DetailCursor                              int
	Page                                      int
	Candidates                                []string
	PageComplete                              bool
	PageVerified                              bool
	PageTotal                                 int
	Started, Completed                        time.Time
	Manual                                    bool
	Report                                    traceReport
}

type traceReport struct {
	Status           string                    `json:"preparation_status"`
	Version          int64                     `json:"material_version"`
	Local            []agent.ExecutionEvidence `json:"local_session_evidence"`
	Comparisons      []requestComparison       `json:"request_comparisons"`
	Run              string                    `json:"run_id"`
	Runtime          string                    `json:"runtime"`
	RuntimeSource    string                    `json:"runtime_source"`
	Session          string                    `json:"session_id"`
	QueryStart       time.Time                 `json:"query_start"`
	QueryEnd         time.Time                 `json:"query_end"`
	Usage            json.RawMessage           `json:"platform_usage"`
	References       []traceReference          `json:"references"`
	External         []json.RawMessage         `json:"agentlens_traces"`
	Gaps             []string                  `json:"gaps"`
	Requests         int                       `json:"preparation_http_requests"`
	DiscoveryPending bool                      `json:"discovery_pending"`
	DurationMS       int64                     `json:"preparation_duration_ms"`
	QueriedAt        time.Time                 `json:"queried_at"`
}

type traceReference struct {
	ID          string          `json:"provider_trace_id"`
	Session     string          `json:"session_id"`
	Correlation string          `json:"correlation"`
	Source      string          `json:"source"`
	Metadata    json.RawMessage `json:"metadata"`
}

// Preparation is opt-in with a durable rollout cutoff, so enabling it never reopens historical supervision.
func (s *Service) prepareConfiguredTraceMaterials(ctx context.Context, ws string) error {
	value := os.Getenv("MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE")
	if value == "" {
		return nil
	}
	since, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return errors.New("invalid supervision trace preparation cutoff")
	}
	return s.prepareTraceMaterialsSince(ctx, ws, since)
}

// prepareTraceMaterials performs bounded I/O before dispatch, outside transactions.
// 1. Historical task.started events select the runtime; mutable Agent settings never do.
func (s *Service) prepareTraceMaterials(ctx context.Context, ws string) error {
	return s.prepareTraceMaterialsSince(ctx, ws, time.Time{})
}

func (s *Service) prepareTraceMaterialsSince(ctx context.Context, ws string, since time.Time) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO supervision_trace_material(id,workspace_id,issue_id,round_id,run_id,runtime,source,status)
 SELECT gen_random_uuid(),r.workspace_id,r.issue_id,r.id,t.id,
 COALESCE((SELECT NULLIF(e.provider,'') FROM task_trace_event e WHERE e.workspace_id=r.workspace_id AND e.task_id=t.id AND e.event_type='task.started' ORDER BY e.created_at,e.id LIMIT 1),''),
 'execution','pending'
 FROM supervision_round r JOIN supervision_config c ON c.workspace_id=r.workspace_id
 JOIN agent_task_queue t ON t.issue_id=r.issue_id
 WHERE r.workspace_id=$1 AND r.status='open' AND r.material_cutoff >= $2 AND c.enabled AND t.created_at<=r.material_cutoff
 AND t.completed_at IS NOT NULL AND t.completed_at<=r.material_cutoff
 AND t.status IN('completed','failed','cancelled') AND COALESCE(t.context->>'type','')<>'quality_supervision'
 AND NOT EXISTS(SELECT 1 FROM supervision_trace_material p WHERE p.workspace_id=r.workspace_id AND p.round_id=r.id AND p.run_id=t.id AND p.source='execution')
 LIMIT 100 ON CONFLICT DO NOTHING`, ws, since)
	if err != nil {
		return err
	}
	var p tracePreparation
	var saved, checkpoint []byte
	// 2. The lease survives process loss; one worker owns a row until its bounded call ends.
	err = s.Pool.QueryRow(ctx, `UPDATE supervision_trace_material p SET status='processing',attempts=attempts+1,lease_until=now()+interval '150 seconds',updated_at=now()
 FROM (SELECT p.id FROM supervision_trace_material p JOIN supervision_trace_scope r ON r.id=p.round_id AND r.workspace_id=p.workspace_id
 WHERE p.workspace_id=$1 AND r.status='open' AND p.next_attempt_at<=now()
 AND NOT EXISTS(SELECT 1 FROM supervision_subject sub WHERE sub.workspace_id=p.workspace_id AND sub.issue_id=p.issue_id AND sub.next_attempt_at='infinity')
 AND (p.runtime NOT IN('codex','codebuddy') OR p.created_at<now()-interval '90 seconds' OR NOT EXISTS(SELECT 1 FROM supervision_config c WHERE c.workspace_id=p.workspace_id AND c.investigation_enabled) OR EXISTS(SELECT 1 FROM task_message m WHERE m.task_id=p.run_id AND m.type='execution_evidence'))
 AND (p.status='pending' OR (p.status='processing' AND p.lease_until<now()))
 ORDER BY p.created_at,p.id LIMIT 1 FOR UPDATE OF p SKIP LOCKED) candidate
 WHERE p.id=candidate.id RETURNING p.id::text,p.workspace_id::text,p.issue_id::text,p.round_id::text,p.run_id::text,p.runtime,p.attempts,COALESCE((p.cursor->>'reference')::integer,0),p.report,p.material_version,p.manual,p.cursor`, ws).
		Scan(&p.ID, &p.Workspace, &p.Issue, &p.Round, &p.Run, &p.Runtime, &p.Attempts, &p.Cursor, &saved, &p.Version, &p.Manual, &checkpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(saved) > 0 {
		if err = json.Unmarshal(saved, &p.Report); err != nil {
			return err
		}
	}
	var cursor struct {
		Reference  int      `json:"reference"`
		Detail     int      `json:"detail"`
		Page       int      `json:"page"`
		Candidates []string `json:"candidates"`
		Complete   bool     `json:"complete"`
		Verified   bool     `json:"verified"`
		Total      int      `json:"total"`
	}
	if err = json.Unmarshal(checkpoint, &cursor); err != nil {
		return err
	}
	p.DetailCursor, p.Page, p.Candidates, p.PageComplete = cursor.Detail, cursor.Page, cursor.Candidates, cursor.Complete
	p.PageVerified, p.PageTotal = cursor.Verified, cursor.Total
	return s.prepareTrace(ctx, p)
}

// 3. Source rows remain authoritative; this report is evidence, never a second usage ledger.
func (s *Service) prepareTrace(ctx context.Context, p tracePreparation) error {
	started := time.Now()
	p.Report.Gaps = []string{}
	var investigate bool
	if err := s.Pool.QueryRow(ctx, `SELECT investigation_enabled FROM supervision_config WHERE workspace_id=$1`, p.Workspace).Scan(&investigate); err != nil {
		return err
	}
	p.Report.Run, p.Report.Runtime, p.Report.RuntimeSource = p.Run, p.Runtime, "task_trace_event.task.started.provider"
	p.Report.QueriedAt = started.UTC()
	var refs []byte
	var completed pgtype.Timestamptz
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE(t.session_id,''),COALESCE(t.started_at,t.created_at),t.completed_at,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('provider',u.provider,'model',u.model,'input_tokens',u.input_tokens,'output_tokens',u.output_tokens,'cache_read_tokens',u.cache_read_tokens,'cache_write_tokens',u.cache_write_tokens)) FROM task_usage u WHERE u.task_id=t.id),'[]'),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('provider_trace_id',x.provider_trace_id,'session_id',x.session_id,'correlation',x.correlation,'source',x.source,'metadata',x.metadata) ORDER BY x.created_at,x.id) FROM task_provider_trace x WHERE x.task_id=t.id AND x.workspace_id=i.workspace_id AND x.provider='codebuddy'),'[]')
 FROM agent_task_queue t JOIN issue i ON i.id=t.issue_id WHERE t.id=$1 AND i.id=$2 AND i.workspace_id=$3`, p.Run, p.Issue, p.Workspace).Scan(&p.Report.Session, &p.Started, &completed, &p.Report.Usage, &refs)
	if err != nil {
		return err
	}
	if completed.Valid {
		p.Completed = completed.Time
	}
	if err = json.Unmarshal(refs, &p.Report.References); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `SELECT content FROM task_message WHERE task_id=$1 AND type='execution_evidence' AND tool='execution_evidence' ORDER BY seq,id`, p.Run)
	if err != nil {
		return err
	}
	p.Report.Local = nil
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return err
		}
		var e agent.ExecutionEvidence
		if json.Unmarshal(body, &e) == nil && e.Runtime == p.Runtime {
			p.Report.Local = append(p.Report.Local, e)
			p.Report.Gaps = append(p.Report.Gaps, e.Gaps...)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	if investigate && (p.Runtime == "codebuddy" || p.Runtime == "codex") && len(p.Report.Local) == 0 {
		p.Report.Gaps = append(p.Report.Gaps, "owning_daemon_session_snapshot_unavailable")
	}
	// Modification 2. Freeze discovery to the owned execution, with explicit clock-skew margins.
	if !p.Completed.IsZero() {
		p.Report.QueryStart, p.Report.QueryEnd = traceQueryRange(p.Started, p.Completed)
	}
	status := "partial"
	recheck := false
	// The round owns the allowance, including all runs and delayed versions.
	var budgetStarted time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(preparation_started_at,now()) FROM supervision_trace_scope WHERE workspace_id=$1 AND id=$2`, p.Workspace, p.Round).Scan(&budgetStarted); err != nil {
		return err
	}
	budget := lensBudget{Started: budgetStarted, Reserve: func(callCtx context.Context) error {
		return s.reserveTraceRequest(callCtx, p.Workspace, p.Round)
	}}
	switch {
	case p.Runtime == "":
		p.Report.Gaps = append(p.Report.Gaps, "Historical runtime identity unavailable; external lookup was not attempted.")
	case !investigate:
		status = "disabled"
		p.Report.Gaps = append(p.Report.Gaps, "Additional investigation is disabled.")
	case p.Runtime != "codebuddy":
		status = "not_applicable"
		p.Report.Gaps = append(p.Report.Gaps, "AgentLens does not apply to this runtime; local session evidence and run-level usage are provided separately.")
	case p.Completed.IsZero() || p.Completed.Before(p.Started):
		p.Report.Gaps = append(p.Report.Gaps, "execution_time_boundary_unavailable")
	case len(p.Report.References) == 0:
		p.Report.Gaps = append(p.Report.Gaps, "No exact provider request identity; time-window matching cannot prove ownership.")
	default:
		scope, scopeErr := loadAgentLensScope(os.Getenv("MULTICA_AGENTLENS_CREDENTIAL_FILE"), p.Workspace)
		if scopeErr != nil {
			status = "failed"
			p.Report.Gaps = append(p.Report.Gaps, scopeErr.Error())
			break
		}
		client := agentLensClient{Scope: scope}
		status = "complete"
		for p.Cursor < len(p.Report.References) {
			ref := p.Report.References[p.Cursor]
			if ref.Correlation != "exact" || ref.Session == "" || ref.ID == "" {
				p.Report.Gaps = append(p.Report.Gaps, "A reference lacks exact request/session identity.")
				p.Cursor++
				status = "partial"
				continue
			}

			if p.Page == 0 {
				p.Page = 1
			}
			// Modification 3. Persist the exact page and next detail before consuming more budget.
			if p.Candidates == nil {
				tags, _ := json.Marshal([]map[string]string{{"key": "message_id", "value": ref.ID, "operator": "eq"}})
				listed, queryErr := client.query(ctx, &budget, "traceList", map[string]any{"tags": string(tags), "session_id": ref.Session, "page_size": 50, "page_num": p.Page, "query_order": "BY_START_TIME", "start_time": strconv.FormatInt(p.Report.QueryStart.UnixNano(), 10), "end_time": strconv.FormatInt(p.Report.QueryEnd.UnixNano(), 10)})
				if queryErr != nil {
					p.Report.Gaps = append(p.Report.Gaps, queryErr.Error())
					status = "partial"
					break
				}
				p.Candidates, p.PageComplete, p.PageVerified = lensCandidatePage(listed, p.Page)
				var pageMeta struct {
					List struct {
						Total int `json:"total"`
					} `json:"list"`
				}
				_ = json.Unmarshal(listed, &pageMeta)
				if p.PageTotal != 0 && p.PageTotal != pageMeta.List.Total {
					p.PageVerified, p.PageComplete = false, false
				}
				if p.PageTotal == 0 {
					p.PageTotal = pageMeta.List.Total
				}
				if !p.PageVerified {
					p.Report.Gaps = append(p.Report.Gaps, "trace_pagination_unverified_pending_recheck")
					status = "partial"
					if len(p.Candidates) == 0 {
						break
					}
				}
			}
			if len(p.Candidates) == 0 {
				p.Report.Gaps = append(p.Report.Gaps, "external_trace_not_yet_visible")
				recheck = true
				status = "partial"
				break
			}
			if !p.PageVerified {
				p.Report.Gaps = append(p.Report.Gaps, "trace_discovery_incomplete_pending_recheck")
				status = "partial"
			}
			referenceComplete := true
			// Resume within the saved page after a bounded request failure.
			for p.DetailCursor < len(p.Candidates) {
				candidate := p.Candidates[p.DetailCursor]
				data, queryErr := client.query(ctx, &budget, "traceDetail", map[string]any{"trace_id": candidate, "attr_type": "attr"})
				if queryErr != nil {
					p.Report.Gaps = append(p.Report.Gaps, queryErr.Error())
					status = "partial"
					referenceComplete = false
					break
				}
				comparisons, gaps := mapLensRequests(data, ref, p.Report.Local)
				p.Report.Gaps = append(p.Report.Gaps, gaps...)
				if len(gaps) > 0 {
					status = "partial"
				}
				for _, comparison := range comparisons {
					comparison.RawSpan = json.RawMessage(sanitizeTraceJSON(comparison.RawSpan, scope.Token))
					duplicate := false
					for i, old := range p.Report.Comparisons {
						if old.CID == comparison.CID && old.Session == comparison.Session && old.Message == comparison.Message {
							duplicate = true
							p.Report.Comparisons[i] = comparison
							break
						}
					}
					if !duplicate {
						p.Report.Comparisons = append(p.Report.Comparisons, comparison)
					}
				}
				p.DetailCursor++
			}
			if !referenceComplete || !p.PageVerified {
				break
			}
			p.Candidates = nil
			p.DetailCursor = 0
			if !p.PageComplete {
				p.Page++
				continue
			}
			p.Page = 1
			p.PageTotal = 0
			p.Cursor++
		}
	}
	p.Report.DiscoveryPending = investigate && p.Runtime == "codebuddy" && p.Cursor < len(p.Report.References)
	p.Report.Requests += budget.Requests
	p.Report.DurationMS += time.Since(started).Milliseconds()
	if len(p.Report.Gaps) > 0 && status == "complete" {
		status = "partial"
	}
	p.Report.Status, p.Report.Version = status, p.Version
	encoded, err := json.Marshal(p.Report)
	if err != nil {
		return err
	}
	// Modification 4. Publish atomically only for the still-open round. Terminal bodies are never overwritten.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var roundOpen bool
	scopeTable := "supervision_round"
	if p.Manual {
		scopeTable = "supervision_manual_preparation"
	}
	if err = tx.QueryRow(ctx, `SELECT status='open' FROM `+scopeTable+`  WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, p.Workspace, p.Round).Scan(&roundOpen); err != nil {
		return err
	}
	if !roundOpen {
		status = "partial"
		p.Report.Status = status
		encoded, err = json.Marshal(p.Report)
		if err != nil {
			return err
		}
	}
	differences := 0
	for _, c := range p.Report.Comparisons {
		if c.Input == "different" || c.Output == "different" || c.CacheRead == "different" {
			differences++
		}
	}
	coverage, _ := json.Marshal(map[string]any{"matched_requests": len(p.Report.Comparisons), "local_snapshots": len(p.Report.Local), "preparation_http_requests": p.Report.Requests, "preparation_duration_ms": p.Report.DurationMS, "differences": differences})
	gaps, _ := json.Marshal(p.Report.Gaps)
	checkpoint, err := json.Marshal(map[string]any{"reference": p.Cursor, "detail": p.DetailCursor, "page": p.Page, "candidates": p.Candidates, "complete": p.PageComplete, "verified": p.PageVerified, "total": p.PageTotal})
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE supervision_trace_material SET status=$3,report=$4,coverage=$8::jsonb,gaps=$9::jsonb,cursor=$5::jsonb,queried_at=$6,lease_until=NULL,updated_at=now()
 WHERE workspace_id=$1 AND id=$2 AND status='processing' AND attempts=$7`, p.Workspace, p.ID, status, string(encoded), string(checkpoint), started, p.Attempts, coverage, gaps)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if roundOpen {
		_, err = tx.Exec(ctx, `WITH inserted AS (INSERT INTO supervision_material(workspace_id,issue_id,kind,source_id,run_id,changed_at)
 SELECT p.workspace_id,p.issue_id,'trace_usage',p.id,p.run_id,r.material_cutoff FROM supervision_trace_material p JOIN supervision_trace_scope r ON r.id=p.round_id AND r.workspace_id=p.workspace_id
 WHERE p.workspace_id=$1 AND p.id=$2 ON CONFLICT DO NOTHING RETURNING issue_id) UPDATE supervision_subject SET desired_version=desired_version+1 WHERE workspace_id=$1 AND issue_id IN(SELECT issue_id FROM inserted)`, p.Workspace, p.ID)
		if err != nil {
			return err
		}
	}
	if roundOpen && recheck && p.Version == 1 {
		permit, permitErr := tx.Exec(ctx, `UPDATE `+scopeTable+` SET preparation_recheck_scheduled=true WHERE workspace_id=$1 AND id=$2 AND NOT preparation_recheck_scheduled`, p.Workspace, p.Round)
		if permitErr != nil {
			return permitErr
		}
		if permit.RowsAffected() == 0 {
			return tx.Commit(ctx)
		}
		// Allow one delayed continuation, with a new immutable material identity.
		_, err = tx.Exec(ctx, `INSERT INTO supervision_trace_material(id,workspace_id,issue_id,round_id,run_id,runtime,source,status,material_version,report,cursor,next_attempt_at,manual)
        SELECT gen_random_uuid(),workspace_id,issue_id,round_id,run_id,runtime,source,'pending',2,report,cursor,now()+interval '60 seconds',manual
        FROM supervision_trace_material WHERE workspace_id=$1 AND id=$2 ON CONFLICT DO NOTHING`, p.Workspace, p.ID)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// 6. Strip secret-bearing objects before storage, then apply the shared text redactor.
func sanitizeTraceJSON(data []byte, token string) string {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return `{"gap":"unknown_format"}`
	}
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			// Attribute arrays encode secret names as values rather than object keys.
			if name, ok := x["key"].(string); ok && secretTraceKey(name) {
				x["value"] = "[REDACTED]"
			}
			for key, v := range x {
				if secretTraceKey(key) {
					x[key] = "[REDACTED]"
				} else {
					x[key] = clean(v)
				}
			}
		case []any:
			for i, v := range x {
				x[i] = clean(v)
			}
		case string:
			if token != "" {
				x = strings.ReplaceAll(x, token, "[REDACTED]")
			}
			return redact.Text(x)
		}
		return v
	}
	encoded, _ := json.Marshal(clean(value))
	return string(encoded)
}

func secretTraceKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "authorization") || strings.Contains(key, "api_key") || strings.Contains(key, "api.key") || strings.Contains(key, "apikey") || strings.Contains(key, "password") || strings.Contains(key, "cookie") || key == "token" || strings.HasSuffix(key, "access_token") || strings.Contains(key, "secret")
}

// 7. Reserve before sending HTTP: crashes can lose a response, but can never replenish quota.
func (s *Service) reserveTraceRequest(ctx context.Context, workspace, round string) error {
	tag, err := s.Pool.Exec(ctx, `WITH auto AS (UPDATE supervision_round
 SET preparation_http_requests=preparation_http_requests+1,
 preparation_started_at=COALESCE(preparation_started_at,now())
 WHERE workspace_id=$1 AND id=$2 AND status='open'
 AND preparation_http_requests<40
 AND (preparation_started_at IS NULL OR preparation_started_at+interval '120 seconds'>clock_timestamp()) RETURNING id), manual AS (UPDATE supervision_manual_preparation
 SET preparation_http_requests=preparation_http_requests+1,preparation_started_at=COALESCE(preparation_started_at,now())
 WHERE workspace_id=$1 AND id=$2 AND status='open' AND preparation_http_requests<40
 AND (preparation_started_at IS NULL OR preparation_started_at+interval '120 seconds'>clock_timestamp()) RETURNING id) SELECT id FROM auto UNION ALL SELECT id FROM manual`, workspace, round)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return &agentLensFailure{Kind: "budget_exhausted"}
	}
	return nil
}

// Modification 5. Delayed supervision keeps the same range rather than the provider default last hour.
func traceQueryRange(started, completed time.Time) (time.Time, time.Time) {
	return started.Add(-5 * time.Minute).UTC(), completed.Add(5 * time.Minute).UTC()
}
