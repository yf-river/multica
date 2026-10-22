package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/upstream"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// upstreamScope 是人类侧入口：成员可读，owner/admin 可写。
// 台账是平台级的，这里的成员校验只用来回答"这个人能不能改全平台的配置"。
func (h *Handler) upstreamScope(w http.ResponseWriter, r *http.Request, write bool) (string, bool, bool) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, 403, "上游同步配置与标记需要成员身份")
		return "", false, false
	}
	ws := middleware.WorkspaceIDFromContext(r.Context())
	member, ok := h.requireWorkspaceMember(w, r, ws, "工作区不存在")
	if !ok {
		return "", false, false
	}
	canWrite := member.Role == "owner" || member.Role == "admin"
	if write && !canWrite {
		writeError(w, 403, "需要工作区管理员权限")
		return "", false, false
	}
	return requestUserID(r), canWrite, true
}

// upstreamTaskScope 校验 Agent 身份。批次 id 从任务反查，不接受客户端传入，
// 避免执行体写错别人的批次。
func (h *Handler) upstreamTaskScope(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, 403, "需要有效的同步运行身份")
		return "", false
	}
	taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if taskID == "" || agentID == "" {
		writeError(w, 403, "缺少任务或 Agent 标识")
		return "", false
	}
	var bound string
	err := h.DB.QueryRow(r.Context(), `SELECT c.agent_id::text
 FROM upstream_sync_config c
 JOIN agent_task_queue t ON t.id=$1 AND t.agent_id=c.agent_id AND t.status='running'
 WHERE c.singleton=1 AND c.agent_id=$2`, taskID, agentID).Scan(&bound)
	if err != nil || bound != agentID {
		writeError(w, 403, "同步 Agent 未绑定或任务已结束")
		return "", false
	}
	if run := chi.URLParam(r, "runId"); run != "" {
		var owner string
		if err := h.DB.QueryRow(r.Context(), `SELECT task_id::text FROM upstream_sync_run WHERE id=$1`, run).Scan(&owner); err != nil || owner != taskID {
			writeError(w, 403, "不能操作其他任务的同步批次")
			return "", false
		}
	}
	return taskID, true
}

func writeUpstreamError(w http.ResponseWriter, err error) {
	var verr upstream.ValidationError
	switch {
	case errors.As(err, &verr):
		writeError(w, 400, verr.Error())
	case errors.Is(err, upstream.ErrRunExpired):
		// 1. 用 410 而不是 409：提示词把 409 定义为"没有新工作，正常结束"，
		// 租约失效必须显式区分出来，否则 Agent 会静默收工。
		writeError(w, 410, err.Error())
	case errors.Is(err, upstream.ErrBusy):
		writeError(w, 409, "另一批次正在运行")
	case errors.Is(err, upstream.ErrRunGone):
		writeError(w, 409, "批次不存在或已结束")
	default:
		writeError(w, 500, "上游同步操作失败")
	}
}

func (h *Handler) GetUpstream(w http.ResponseWriter, r *http.Request) {
	_, canWrite, ok := h.upstreamScope(w, r, false)
	if !ok {
		return
	}
	snap, err := h.Upstream.Snapshot(r.Context())
	if err != nil {
		writeError(w, 500, "读取上游同步状态失败")
		return
	}
	snap.CanWrite = canWrite
	writeJSON(w, 200, snap)
}

// GetUpstreamInstructions 单独返回内置提示词，供设置页展示与「恢复默认」。
// 成员也能读到，只读成员打开设置时看到的是同一份文本；不在每次轮询里重复传输。
func (h *Handler) GetUpstreamInstructions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.upstreamScope(w, r, false); !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"instructions": service.UpstreamSyncInstructions()})
}

// UpdateUpstreamConfig 保存同步范围、执行体与计划。没有运行时时也能保存暂停、
// 批量和提示词；带 runtime_id 时才绑定或切换执行体。
func (h *Handler) UpdateUpstreamConfig(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.upstreamScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		UpstreamURL    string  `json:"upstream_url"`
		UpstreamBranch string  `json:"upstream_branch"`
		LocalRef       string  `json:"local_ref"`
		RuntimeID      string  `json:"runtime_id"`
		Model          string  `json:"model"`
		ThinkingLevel  string  `json:"thinking_level"`
		Instructions   *string `json:"instructions"`
		Enabled        *bool   `json:"enabled"`
		BatchSize      int     `json:"batch_size"`
		ScheduleHour   *int    `json:"schedule_hour"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if body.UpstreamURL == "" {
		body.UpstreamURL = "https://github.com/multica-ai/multica.git"
	}
	if body.UpstreamBranch == "" {
		body.UpstreamBranch = "main"
	}
	if body.LocalRef == "" {
		body.LocalRef = "origin/personal-studio-upstream"
	}
	if body.BatchSize == 0 {
		body.BatchSize = 200
	}
	scheduleHour := upstream.DefaultScheduleHour
	if body.ScheduleHour != nil {
		scheduleHour = *body.ScheduleHour
	} else if err := h.DB.QueryRow(r.Context(), `SELECT schedule_hour FROM upstream_sync_settings WHERE singleton=1`).Scan(&scheduleHour); err != nil {
		scheduleHour = upstream.DefaultScheduleHour
	}

	if body.RuntimeID != "" {
		var runtimeWorkspace string
		if err := h.DB.QueryRow(r.Context(), `SELECT workspace_id::text FROM agent_runtime WHERE id=$1 AND provider IN ('codex','codebuddy') AND (visibility='workspace' OR owner_id=$2 OR owner_id IS NULL)`, body.RuntimeID, user).Scan(&runtimeWorkspace); err != nil || runtimeWorkspace != middleware.WorkspaceIDFromContext(r.Context()) {
			writeError(w, 403, "只能绑定当前有管理权限工作区的运行时")
			return
		}
	}
	agentID, autopilotID, err := h.Upstream.SaveSettings(r.Context(), upstream.Settings{
		UpstreamURL:         body.UpstreamURL,
		UpstreamBranch:      body.UpstreamBranch,
		LocalRef:            body.LocalRef,
		BatchSize:           body.BatchSize,
		ScheduleHour:        scheduleHour,
		Enabled:             body.Enabled == nil || *body.Enabled,
		Instructions:        body.Instructions,
		DefaultInstructions: service.UpstreamSyncInstructions(),
		RuntimeID:           body.RuntimeID,
		Model:               strings.TrimSpace(body.Model),
		ThinkingLevel:       strings.TrimSpace(body.ThinkingLevel),
	}, user)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	result := map[string]any{"ok": true, "agent_id": agentID, "autopilot_id": autopilotID}
	writeJSON(w, 200, result)
}

func (h *Handler) ListUpstreamItems(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.upstreamScope(w, r, false); !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	status := q.Get("status")
	if status == "" {
		status = "open"
	}
	presence := q.Get("presence")
	if presence == "" {
		presence = "all"
	}
	items, total, err := h.Upstream.ListItems(r.Context(), upstream.ItemFilter{
		Status: status, Verdict: q.Get("verdict"),
		Presence: presence, Query: q.Get("q"),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		writeError(w, 500, "读取待吸收清单失败")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total})
}

func (h *Handler) MarkUpstreamItems(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.upstreamScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		IDs    []string `json:"ids"`
		Status string   `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	n, err := h.Upstream.MarkItems(r.Context(), body.IDs, body.Status, user)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"updated": n})
}

// CreateUpstreamAbsorptionTask creates a normal issue for a human-selected suggestion.
// It records the link atomically with issue creation; it never applies code or starts a release.
func (h *Handler) CreateUpstreamAbsorptionTask(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.upstreamScope(w, r, true)
	if !ok {
		return
	}
	itemID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "itemId"), "item_id")
	if !ok {
		return
	}
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	wsID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var title, summary, rationale string
	var paths, shas []string
	var existing *string
	err := h.DB.QueryRow(r.Context(), `SELECT i.title,i.summary,i.rationale,i.paths,i.commit_shas,COALESCE(a.issue_id::text,'')
 FROM upstream_sync_item i LEFT JOIN upstream_sync_absorption_task a ON a.item_id=i.id WHERE i.id=$1`, itemID).Scan(&title, &summary, &rationale, &paths, &shas, &existing)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "上游建议不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取上游建议失败")
		return
	}
	if existing != nil && *existing != "" {
		writeJSON(w, http.StatusOK, map[string]any{"issue_id": *existing, "status": "task_created", "existing": true})
		return
	}
	description := fmt.Sprintf("上游吸收建议\n\n%s\n\n判断依据：%s\n\n上游提交：%s\n涉及路径：%s\n\n请在联调环境验证实际收益、兼容性和维护成本；不要直接发布生产。", summary, rationale, strings.Join(shas, ", "), strings.Join(paths, ", "))
	creatorUUID, err := uuid.Parse(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "当前用户身份无效")
		return
	}
	creator := pgtype.UUID{Bytes: creatorUUID, Valid: true}
	alreadyCreated := errors.New("absorption task already created")
	var concurrentIssue string
	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:    wsID,
		Title:          "评估上游改动：" + title,
		Description:    pgtype.Text{String: description, Valid: true},
		Status:         "todo",
		Priority:       "not_important_not_urgent",
		CreatorType:    "member",
		CreatorID:      creator,
		AllowDuplicate: true,
	}, service.IssueCreateOpts{
		ActorID:  user,
		Platform: "web",
		RelatedRecord: func(ctx context.Context, tx pgx.Tx, issue db.Issue) error {
			var linked string
			err := tx.QueryRow(ctx, `INSERT INTO upstream_sync_absorption_task(item_id,issue_id,status) VALUES($1,$2,'task_created') ON CONFLICT(item_id) DO NOTHING RETURNING issue_id::text`, itemID, issue.ID).Scan(&linked)
			if errors.Is(err, pgx.ErrNoRows) {
				if err := tx.QueryRow(ctx, `SELECT issue_id::text FROM upstream_sync_absorption_task WHERE item_id=$1`, itemID).Scan(&concurrentIssue); err != nil {
					return err
				}
				// 并发请求已创建关联时，回滚本次新任务并返回同一个任务。
				return alreadyCreated
			}
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE upstream_sync_item SET status='adopted',adopted_at=COALESCE(adopted_at,now()),adopted_by=COALESCE(adopted_by,$2),updated_at=now() WHERE id=$1`, itemID, creator)
			return err
		},
	})
	if errors.Is(err, alreadyCreated) {
		writeJSON(w, http.StatusOK, map[string]any{"issue_id": concurrentIssue, "status": "task_created", "existing": true})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "创建吸收任务失败")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"issue_id": result.Issue.ID, "status": "task_created", "existing": false})
}

func (h *Handler) ListUpstreamRuns(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.upstreamScope(w, r, false); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		writeError(w, 400, "分页位置不能为负数")
		return
	}
	runs, err := h.Upstream.ListRuns(r.Context(), limit, offset)
	if err != nil {
		writeError(w, 500, "读取同步记录失败")
		return
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

func (h *Handler) StartUpstreamRun(w http.ResponseWriter, r *http.Request) {
	taskID, ok := h.upstreamTaskScope(w, r)
	if !ok {
		return
	}
	trigger := "manual"
	if err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(a.source,'manual') FROM agent_task_queue t LEFT JOIN autopilot_run a ON a.id=t.autopilot_run_id WHERE t.id=$1`, taskID).Scan(&trigger); err != nil {
		writeError(w, 403, "任务来源不可用")
		return
	}
	res, err := h.Upstream.Start(r.Context(), taskID, r.Header.Get("X-Agent-ID"), trigger)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (h *Handler) AppendUpstreamItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	var rep upstream.Report
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if err := upstream.ValidateReport(rep); err != nil {
		writeUpstreamError(w, err)
		return
	}
	n, err := h.Upstream.AppendReport(r.Context(), chi.URLParam(r, "runId"), rep)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"items": n})
}

func (h *Handler) FinishUpstreamRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	var body struct {
		CursorSHA string `json:"cursor_sha"`
		HasMore   bool   `json:"has_more"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if err := h.Upstream.Finish(r.Context(), chi.URLParam(r, "runId"), body.CursorSHA, body.HasMore); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) FailUpstreamRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, 400, "请求体格式错误")
		return
	}
	if err := h.Upstream.Fail(r.Context(), chi.URLParam(r, "runId"), body.Error); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (h *Handler) PrepareUpstreamRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	var m upstream.BatchManifest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&m); err != nil {
		writeError(w, 400, "提交清单格式错误")
		return
	}
	if err := h.Upstream.Prepare(r.Context(), chi.URLParam(r, "runId"), m); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (h *Handler) HeartbeatUpstreamRun(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.upstreamTaskScope(w, r); !ok {
		return
	}
	if err := h.Upstream.Heartbeat(r.Context(), chi.URLParam(r, "runId")); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
