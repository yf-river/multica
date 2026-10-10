package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/health"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type healthScope struct {
	workspace, user string
	task            *db.AgentTaskQueue
}
type healthReport struct {
	ID                    string               `json:"id"`
	Filename              string               `json:"filename"`
	SHA256                string               `json:"sha256"`
	SizeBytes             int64                `json:"size_bytes"`
	PageCount             int                  `json:"page_count"`
	IssueID               *string              `json:"issue_id"`
	AgentID               *string              `json:"agent_id"`
	Attempt               int                  `json:"attempt"`
	Epoch                 int64                `json:"epoch"`
	StartedAt             *time.Time           `json:"attempt_started_at"`
	CreatedAt             time.Time            `json:"created_at"`
	TaskStatus            string               `json:"task_status"`
	IssueStatus           string               `json:"issue_status"`
	Result                *health.Result       `json:"result"`
	ResultRevision        int64                `json:"result_revision"`
	Applied               bool                 `json:"applied"`
	EffectiveObservations []health.Observation `json:"effective_observations"`
}

var errHealthConflict = errors.New("身体数据已变化，请刷新后重试")

func healthIssueVisible(issue db.Issue, userID string) bool {
	return !issue.OriginType.Valid || issue.OriginType.String != "health_report" || (issue.CreatorType == "member" && uuidToString(issue.CreatorID) == userID)
}

// healthRequestScope 只接受本人或本人发起的运行中任务，不使用工作区管理员替代属主。
func (h *Handler) healthRequestScope(w http.ResponseWriter, r *http.Request, memberOnly bool) (healthScope, bool) {
	user, ok := requireUserID(w, r)
	if !ok {
		return healthScope{}, false
	}
	workspace := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspace, "workspace_id")
	if !ok {
		return healthScope{}, false
	}
	workspace = uuidToString(workspaceUUID)
	if _, err := h.getWorkspaceMember(r.Context(), user, workspace); err != nil {
		writeError(w, 404, "身体数据不存在")
		return healthScope{}, false
	}
	actor, id := h.resolveActor(r, user, workspace)
	s := healthScope{workspace: workspace, user: user}
	if actor == "member" {
		return s, true
	}
	if memberOnly || actor != "agent" {
		writeError(w, 403, "此操作需要本人执行")
		return s, false
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task_id")
	if !ok {
		return s, false
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{ID: taskID, WorkspaceID: parseUUID(workspace)})
	if err != nil || task.Status != "running" || uuidToString(task.AgentID) != id {
		writeError(w, 403, "需要对应的运行中任务")
		return s, false
	}
	if task.InitiatorUserID.Valid && uuidToString(task.InitiatorUserID) != user {
		writeError(w, 403, "任务发起人不匹配")
		return s, false
	}
	if !task.InitiatorUserID.Valid {
		owner := ""
		if task.ChatSessionID.Valid {
			session, e := h.Queries.GetChatSession(r.Context(), task.ChatSessionID)
			if e == nil {
				owner = uuidToString(session.CreatorID)
			}
		} else if task.IssueID.Valid {
			issue, e := h.Queries.GetIssue(r.Context(), task.IssueID)
			if e == nil && issue.CreatorType == "member" {
				owner = uuidToString(issue.CreatorID)
			}
		}
		if owner != user {
			writeError(w, 403, "任务无法归属到本人")
			return s, false
		}
	}
	s.task = &task
	return s, true
}

func healthPrivateRoot() string {
	if root := os.Getenv("HEALTH_STORAGE_DIR"); root != "" {
		return root
	}
	if root := os.Getenv("LOCAL_UPLOAD_DIR"); root != "" {
		return filepath.Clean(root) + "-health"
	}
	return "./data/health"
}
func healthPDFPath(s healthScope, id string) string {
	return filepath.Join(healthPrivateRoot(), s.workspace, s.user, id, "original.pdf")
}
func healthCachePath(s healthScope, id string) string {
	return filepath.Join(healthPrivateRoot(), s.workspace, s.user, id, health.PreparationVersion)
}

func (h *Handler) healthState(ctx context.Context, q dbExecutor, s healthScope, lock bool) (health.State, int64, int64, error) {
	if _, err := q.Exec(ctx, `INSERT INTO health_state(workspace_id,user_id) VALUES($1,$2) ON CONFLICT(workspace_id,user_id) DO NOTHING`, s.workspace, s.user); err != nil {
		return health.State{}, 0, 0, err
	}
	sql := `SELECT data,revision,epoch FROM health_state WHERE workspace_id=$1 AND user_id=$2`
	if lock {
		sql += " FOR UPDATE"
	}
	var data []byte
	var revision, epoch int64
	err := q.QueryRow(ctx, sql, s.workspace, s.user).Scan(&data, &revision, &epoch)
	if err != nil {
		return health.State{}, 0, 0, err
	}
	var state health.State
	if err = json.Unmarshal(data, &state); err != nil {
		return state, 0, 0, err
	}
	if state.ObservationIDs == nil {
		state.ObservationIDs = []string{}
	}
	if state.CurrentObservationIDs == nil {
		observations, err := h.healthObservations(ctx, q, s, state.ObservationIDs)
		if err != nil {
			return state, 0, 0, err
		}
		health.ResolveCurrent(&state, observations)
	}
	return state, revision, epoch, nil
}

func (h *Handler) healthObservations(ctx context.Context, q dbExecutor, s healthScope, ids []string) ([]health.Observation, error) {
	rows, err := q.Query(ctx, `SELECT data FROM health_observation WHERE workspace_id=$1 AND user_id=$2 AND id=ANY($3::uuid[]) ORDER BY array_position($3::uuid[],id)`, s.workspace, s.user, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []health.Observation{}
	for rows.Next() {
		var raw []byte
		var o health.Observation
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return result, rows.Err()
}
func (h *Handler) saveHealthState(ctx context.Context, tx pgx.Tx, s healthScope, old, newState health.State, revision int64, reason string, restore bool) error {
	before, err := json.Marshal(old)
	if err != nil {
		return err
	}
	after, err := json.Marshal(newState)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO health_revision(workspace_id,user_id,revision,data,reason) VALUES($1,$2,$3,$4,$5)`, s.workspace, s.user, revision, before, reason); err != nil {
		return err
	}
	increment := 0
	if restore {
		increment = 1
	}
	if _, err = tx.Exec(ctx, `UPDATE health_state SET data=$3,revision=revision+1,epoch=epoch+$4,updated_at=now() WHERE workspace_id=$1 AND user_id=$2`, s.workspace, s.user, after, increment); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM health_revision WHERE workspace_id=$1 AND user_id=$2 AND revision<$3`, s.workspace, s.user, revision+1-10)
	return err
}
func healthFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, errHealthConflict) {
		writeError(w, 409, err.Error())
	} else {
		writeError(w, 500, "身体数据保存失败")
	}
}
func decodeHealth(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(out); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			writeError(w, 400, fmt.Sprintf("字段 %s 必须为 %s", typeError.Field, typeError.Type))
		} else {
			writeError(w, 400, "请求内容无效")
		}
		return false
	}
	return true
}

func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, false)
	if !ok {
		return
	}
	state, revision, epoch, err := h.healthState(r.Context(), h.DB, s, false)
	if err != nil {
		healthFailure(w, err)
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT data FROM health_observation WHERE workspace_id=$1 AND user_id=$2 AND id=ANY($3::uuid[]) ORDER BY created_at DESC`, s.workspace, s.user, state.ObservationIDs)
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer rows.Close()
	observations := []health.Observation{}
	for rows.Next() {
		var data []byte
		var o health.Observation
		if err := rows.Scan(&data); err != nil {
			healthFailure(w, err)
			return
		}
		if err := json.Unmarshal(data, &o); err != nil {
			healthFailure(w, err)
			return
		}
		observations = append(observations, o)
	}
	if err := rows.Err(); err != nil {
		healthFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"profile": state.Profile, "revision": revision, "epoch": epoch, "age": health.Age(state.Profile.BirthDate, time.Now()), "observations": observations, "current": health.Selected(observations, state.CurrentObservationIDs)})
}

func (h *Handler) UpdateHealthProfile(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Profile  health.Profile `json:"profile"`
		Revision int64          `json:"revision"`
	}
	if !decodeHealth(w, r, &input) {
		return
	}
	if err := health.ValidateProfile(input.Profile); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	old, rev, _, err := h.healthState(r.Context(), tx, s, true)
	if err != nil {
		healthFailure(w, err)
		return
	}
	if rev != input.Revision {
		healthFailure(w, errHealthConflict)
		return
	}
	next := old
	next.Profile = input.Profile
	if err = h.saveHealthState(r.Context(), tx, s, old, next, rev, "修改基础资料", false); err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		healthFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": rev + 1})
}

func (h *Handler) SaveHealthObservation(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Observation  health.Observation   `json:"observation"`
		Observations []health.Observation `json:"observations"`
		Revision     int64                `json:"revision"`
	}
	if !decodeHealth(w, r, &input) {
		return
	}
	observations := input.Observations
	if len(observations) == 0 && input.Observation.Name != "" {
		observations = []health.Observation{input.Observation}
	}
	if len(observations) == 0 || len(observations) > 20 {
		writeError(w, 400, "身体指标数量须为 1 到 20 条")
		return
	}
	groupID := ""
	if len(observations) > 1 {
		groupID = uuid.NewString()
	}
	for i := range observations {
		o := &observations[i]
		o.Source = "manual"
		o.Status = "accepted"
		o.ID = uuid.NewString()
		if groupID != "" {
			o.MeasurementGroupID = groupID
		}
		if err := health.Normalize(o); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	old, rev, _, err := h.healthState(r.Context(), tx, s, true)
	if err != nil {
		healthFailure(w, err)
		return
	}
	if rev != input.Revision {
		healthFailure(w, errHealthConflict)
		return
	}
	next := old
	next.ObservationIDs = append([]string{}, old.ObservationIDs...)
	for i := range observations {
		o := &observations[i]
		if o.Supersedes != "" {
			if _, ok := parseUUIDOrBadRequest(w, o.Supersedes, "supersedes"); !ok {
				return
			}
			found := false
			ids := []string{}
			for _, id := range next.ObservationIDs {
				if id == o.Supersedes {
					found = true
				} else {
					ids = append(ids, id)
				}
			}
			if !found {
				writeError(w, 409, "待修正的指标已不在当前版本")
				return
			}
			next.ObservationIDs = ids
			var originalData []byte
			if err := tx.QueryRow(r.Context(), `SELECT data FROM health_observation WHERE id=$1 AND workspace_id=$2 AND user_id=$3`, o.Supersedes, s.workspace, s.user).Scan(&originalData); err != nil {
				healthFailure(w, err)
				return
			}
			var original health.Observation
			if err := json.Unmarshal(originalData, &original); err != nil {
				healthFailure(w, err)
				return
			}
			o.ReportID = original.ReportID
			o.Page = original.Page
			o.Evidence = original.Evidence
			o.OriginalName = original.OriginalName
			o.OriginalValue = original.OriginalValue
			o.OriginalUnit = original.OriginalUnit
		} else {
			o.ReportID = ""
			o.Page = 0
			o.Evidence = ""
		}
		data, _ := json.Marshal(o)
		if _, err = tx.Exec(r.Context(), `INSERT INTO health_observation(id,workspace_id,user_id,data) VALUES($1,$2,$3,$4)`, o.ID, s.workspace, s.user, data); err != nil {
			healthFailure(w, err)
			return
		}
	}
	existing, err := h.healthObservations(r.Context(), tx, s, old.ObservationIDs)
	if err != nil {
		healthFailure(w, err)
		return
	}
	next = health.Apply(next, existing, observations)
	if err = h.saveHealthState(r.Context(), tx, s, old, next, rev, "记录或修正身体指标", false); err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		healthFailure(w, err)
		return
	}
	for _, o := range observations {
		if o.ReportID != "" {
			h.syncHealthReportIssue(r, s, o.ReportID)
		}
	}
	writeJSON(w, 200, map[string]any{"revision": rev + 1, "observation": observations[0], "observations": observations})
}

func (h *Handler) GetHealthRevisions(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, true)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT revision,data,reason,created_at FROM health_revision WHERE workspace_id=$1 AND user_id=$2 ORDER BY revision DESC LIMIT 10`, s.workspace, s.user)
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	allIDs := []string{}
	for rows.Next() {
		var rev int64
		var data json.RawMessage
		var reason string
		var at time.Time
		if err := rows.Scan(&rev, &data, &reason, &at); err != nil {
			healthFailure(w, err)
			return
		}
		items = append(items, map[string]any{"revision": rev, "state": data, "reason": reason, "created_at": at})
		var snapshot health.State
		if err := json.Unmarshal(data, &snapshot); err != nil {
			healthFailure(w, err)
			return
		}
		allIDs = append(allIDs, snapshot.ObservationIDs...)
	}
	if err := rows.Err(); err != nil {
		healthFailure(w, err)
		return
	}
	rows.Close()
	values, err := h.DB.Query(r.Context(), `SELECT id::text,data FROM health_observation WHERE workspace_id=$1 AND user_id=$2 AND id=ANY($3::uuid[])`, s.workspace, s.user, allIDs)
	if err != nil {
		healthFailure(w, err)
		return
	}
	byID := map[string]health.Observation{}
	for values.Next() {
		var id string
		var raw []byte
		var o health.Observation
		if err := values.Scan(&id, &raw); err != nil {
			values.Close()
			healthFailure(w, err)
			return
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			values.Close()
			healthFailure(w, err)
			return
		}
		byID[id] = o
	}
	err = values.Err()
	values.Close()
	if err != nil {
		healthFailure(w, err)
		return
	}
	for _, item := range items {
		var snapshot health.State
		json.Unmarshal(item["state"].(json.RawMessage), &snapshot)
		observations := []health.Observation{}
		for _, id := range snapshot.ObservationIDs {
			if o, ok := byID[id]; ok {
				observations = append(observations, o)
			}
		}
		item["observations"] = observations
		health.ResolveCurrent(&snapshot, observations)
		item["state"] = snapshot
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (h *Handler) RestoreHealthRevision(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Revision int64 `json:"revision"`
		Target   int64 `json:"target"`
	}
	if !decodeHealth(w, r, &input) {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	old, rev, _, err := h.healthState(r.Context(), tx, s, true)
	if err != nil {
		healthFailure(w, err)
		return
	}
	if rev != input.Revision {
		healthFailure(w, errHealthConflict)
		return
	}
	var data []byte
	if err = tx.QueryRow(r.Context(), `SELECT data FROM health_revision WHERE workspace_id=$1 AND user_id=$2 AND revision=$3`, s.workspace, s.user, input.Target).Scan(&data); err != nil {
		writeError(w, 404, "该历史版本不可恢复")
		return
	}
	var next health.State
	if err = json.Unmarshal(data, &next); err == nil {
		observations, readErr := h.healthObservations(r.Context(), tx, s, next.ObservationIDs)
		if readErr != nil {
			healthFailure(w, readErr)
			return
		}
		health.ResolveCurrent(&next, observations)
		err = h.saveHealthState(r.Context(), tx, s, old, next, rev, fmt.Sprintf("恢复到版本 %d", input.Target), true)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		healthFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revision": rev + 1})
}

const healthReportSelect = `SELECT id::text,filename,sha256,size_bytes,page_count,issue_id::text,agent_id::text,attempt,epoch,attempt_started_at,created_at FROM health_report`

func scanHealthReport(row pgx.Row) (healthReport, error) {
	report := healthReport{EffectiveObservations: []health.Observation{}}
	err := row.Scan(&report.ID, &report.Filename, &report.SHA256, &report.SizeBytes, &report.PageCount, &report.IssueID, &report.AgentID, &report.Attempt, &report.Epoch, &report.StartedAt, &report.CreatedAt)
	return report, err
}
func (h *Handler) loadHealthReport(w http.ResponseWriter, r *http.Request, s healthScope) (healthReport, bool) {
	id := chi.URLParam(r, "reportId")
	if _, ok := parseUUIDOrBadRequest(w, id, "report_id"); !ok {
		return healthReport{}, false
	}
	report, err := scanHealthReport(h.DB.QueryRow(r.Context(), healthReportSelect+` WHERE id=$1 AND workspace_id=$2 AND user_id=$3`, id, s.workspace, s.user))
	if err != nil {
		writeError(w, 404, "体检报告不存在")
		return report, false
	}
	if s.task != nil && !s.task.ChatSessionID.Valid {
		if report.IssueID == nil || *report.IssueID != uuidToString(s.task.IssueID) || report.AgentID == nil || *report.AgentID != uuidToString(s.task.AgentID) {
			writeError(w, 403, "该任务未获此报告权限")
			return report, false
		}
	}
	if s.task != nil && s.task.ChatSessionID.Valid {
		var linked bool
		if err := h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM chat_message WHERE task_id=$1 AND role='user' AND $2::uuid=ANY(health_report_ids))`, s.task.ID, report.ID).Scan(&linked); err != nil || !linked {
			writeError(w, 403, "该聊天任务未获报告权限")
			return report, false
		}
	}
	return report, true
}

func (h *Handler) UploadHealthReport(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, false)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeError(w, 400, "PDF 上传失败，文件上限为 100 MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "请选择 PDF 文件")
		return
	}
	defer file.Close()
	id := uuid.NewString()
	path := healthPDFPath(s, id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		healthFailure(w, err)
		return
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(filepath.Dir(path))
		}
	}()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		healthFailure(w, err)
		return
	}
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, digest), file)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		writeError(w, 400, "PDF 上传未完成")
		return
	}
	prefix := make([]byte, 5)
	rf, err := os.Open(path)
	if err != nil {
		healthFailure(w, err)
		return
	}
	_, err = io.ReadFull(rf, prefix)
	rf.Close()
	if err != nil || string(prefix) != "%PDF-" {
		writeError(w, 400, "只支持真实 PDF 文件")
		return
	}
	sum := hex.EncodeToString(digest.Sum(nil))
	if existing, err := scanHealthReport(h.DB.QueryRow(r.Context(), healthReportSelect+` WHERE workspace_id=$1 AND user_id=$2 AND sha256=$3`, s.workspace, s.user, sum)); err == nil {
		writeJSON(w, 200, existing)
		return
	}
	pages, err := health.PDFInfo(r.Context(), path)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	_, err = h.DB.Exec(r.Context(), `INSERT INTO health_report(id,workspace_id,user_id,filename,sha256,size_bytes,page_count) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(workspace_id,user_id,sha256) DO NOTHING`, id, s.workspace, s.user, filepath.Base(header.Filename), sum, size, pages)
	if err != nil {
		healthFailure(w, err)
		return
	}
	report, err := scanHealthReport(h.DB.QueryRow(r.Context(), healthReportSelect+` WHERE workspace_id=$1 AND user_id=$2 AND sha256=$3`, s.workspace, s.user, sum))
	if err != nil {
		healthFailure(w, err)
		return
	}
	success = report.ID == id
	writeJSON(w, 200, report)
}

func (h *Handler) enrichHealthReport(ctx context.Context, s healthScope, p *healthReport) error {
	var data []byte
	err := h.DB.QueryRow(ctx, `SELECT data,revision FROM health_report_result WHERE report_id=$1 ORDER BY attempt DESC LIMIT 1`, p.ID).Scan(&data, &p.ResultRevision)
	if err == nil {
		var result health.Result
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		p.Result = &result
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if p.IssueID != nil {
		if err := h.DB.QueryRow(ctx, `SELECT status FROM issue WHERE id=$1 AND workspace_id=$2`, *p.IssueID, s.workspace).Scan(&p.IssueStatus); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err := h.DB.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE issue_id=$1 ORDER BY created_at DESC LIMIT 1`, *p.IssueID).Scan(&p.TaskStatus)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	state, _, _, err := h.healthState(ctx, h.DB, s, false)
	if err != nil {
		return err
	}
	if p.Result != nil {
		active := map[string]bool{}
		for _, id := range state.ObservationIDs {
			active[id] = true
		}
		for _, o := range p.Result.Observations {
			if active[o.ID] {
				p.Applied = true
				break
			}
		}
	}
	p.EffectiveObservations = []health.Observation{}
	rows, err := h.DB.Query(ctx, `SELECT data FROM health_observation WHERE workspace_id=$1 AND user_id=$2 AND id=ANY($3::uuid[]) AND data->>'report_id'=$4`, s.workspace, s.user, state.ObservationIDs, p.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		var o health.Observation
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			rows.Close()
			return err
		}
		p.EffectiveObservations = append(p.EffectiveObservations, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(p.EffectiveObservations) > 0 {
		p.Applied = true
	}
	return nil
}
func (h *Handler) ListHealthReports(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, true)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), healthReportSelect+` WHERE workspace_id=$1 AND user_id=$2 ORDER BY created_at DESC`, s.workspace, s.user)
	if err != nil {
		healthFailure(w, err)
		return
	}
	items := []healthReport{}
	for rows.Next() {
		p, err := scanHealthReport(rows)
		if err != nil {
			rows.Close()
			healthFailure(w, err)
			return
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		healthFailure(w, err)
		return
	}
	for i := range items {
		if err := h.enrichHealthReport(r.Context(), s, &items[i]); err != nil {
			healthFailure(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (h *Handler) GetHealthReport(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, false)
	if !ok {
		return
	}
	p, ok := h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	if err := h.enrichHealthReport(r.Context(), s, &p); err != nil {
		healthFailure(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (h *Handler) HealthReportResults(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, true)
	if !ok {
		return
	}
	p, ok := h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT attempt,revision,data,created_at FROM health_report_result WHERE report_id=$1 AND workspace_id=$2 AND user_id=$3 ORDER BY attempt DESC`, p.ID, s.workspace, s.user)
	if err != nil {
		healthFailure(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var attempt int
		var revision int64
		var raw json.RawMessage
		var at time.Time
		if err := rows.Scan(&attempt, &revision, &raw, &at); err != nil {
			healthFailure(w, err)
			return
		}
		items = append(items, map[string]any{"attempt": attempt, "revision": revision, "result": raw, "created_at": at})
	}
	if err := rows.Err(); err != nil {
		healthFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (h *Handler) HealthReportFile(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, false)
	if !ok {
		return
	}
	p, ok := h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	path := healthPDFPath(s, p.ID)
	contentType := "application/pdf"
	if pageString := chi.URLParam(r, "page"); pageString != "" {
		page, err := strconv.Atoi(pageString)
		if err != nil || page < 1 || page > p.PageCount {
			writeError(w, 404, "页面不存在")
			return
		}
		path = filepath.Join(healthCachePath(s, p.ID), fmt.Sprintf("page-%d.png", page))
		if raw, e := os.ReadFile(filepath.Join(healthCachePath(s, p.ID), fmt.Sprintf("page-%d.json", page))); e == nil {
			var prepared health.PDFPage
			if json.Unmarshal(raw, &prepared) == nil && prepared.Rotation != 0 {
				path = filepath.Join(healthCachePath(s, p.ID), fmt.Sprintf("page-%d-rot%d.png", page, prepared.Rotation))
			}
		}
		contentType = "image/png"
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, 404, "报告文件或页面尚未准备")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.setAttachmentPreviewSecurityHeaders(w)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(p.Filename)))
	}
	http.ServeContent(w, r, p.Filename, time.Time{}, file)
}

func (h *Handler) PrepareHealthReport(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, false)
	if !ok {
		return
	}
	p, ok := h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	prepared, err := health.Prepare(r.Context(), healthPDFPath(s, p.ID), healthCachePath(s, p.ID), p.PageCount)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	pages := []map[string]any{}
	for _, page := range prepared.Pages {
		pages = append(pages, map[string]any{"page": page.Page, "text_characters": len([]rune(page.Text)), "ocr_characters": len([]rune(page.OCR)), "needs_review": page.NeedsReview, "rotation": page.Rotation, "has_image": page.HasImage})
	}
	writeJSON(w, 200, map[string]any{"report_id": p.ID, "version": prepared.Version, "pages": pages, "attempt": p.Attempt, "epoch": p.Epoch})
}
func (h *Handler) HealthReportPages(w http.ResponseWriter, r *http.Request) {
	s, ok := h.healthRequestScope(w, r, false)
	if !ok {
		return
	}
	p, ok := h.loadHealthReport(w, r, s)
	if !ok {
		return
	}
	data, err := os.ReadFile(filepath.Join(healthCachePath(s, p.ID), "index.json"))
	if err != nil {
		writeError(w, 409, "请先准备报告页面")
		return
	}
	var prep health.Preparation
	if err = json.Unmarshal(data, &prep); err != nil {
		healthFailure(w, err)
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	if from < 1 {
		from = 1
	}
	to, _ := strconv.Atoi(r.URL.Query().Get("to"))
	if to < from {
		to = from + 3
	}
	if to-from > 3 {
		writeError(w, 400, "每次最多读取四页")
		return
	}
	out := []map[string]any{}
	for _, page := range prep.Pages {
		if page.Page >= from && page.Page <= to {
			item := map[string]any{"page": page.Page, "text": page.Text, "ocr": page.OCR, "rotation": page.Rotation, "has_image": page.HasImage, "needs_review": page.NeedsReview}
			numbers := []map[string]any{}
			for _, word := range page.Words {
				hasDigit := false
				for _, char := range word.Text {
					if char >= '0' && char <= '9' {
						hasDigit = true
						break
					}
				}
				if hasDigit {
					numbers = append(numbers, map[string]any{"text": word.Text, "confidence": word.Confidence})
				}
			}
			item["ocr_numeric_words"] = numbers
			if r.URL.Query().Get("layout") == "1" {
				item["layout"] = page.Layout
				item["words"] = page.Words
			}
			out = append(out, item)
		}
	}
	writeJSON(w, 200, map[string]any{"pages": out})
}
