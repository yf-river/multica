// 职责定位：in_review 自动审查。负责「审核中」列的审查设置（运行时/模型/提示词）、
// 托管审查 Agent 的维护、进入审核中时的审查派发，以及审查结论落地（打回唤醒开发、
// 停滞交给人、通过写绿勾）。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/testagents"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/review"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// reviewPassedKey 是任务卡片右上角绿勾的数据来源。审查通过写 true，重新进入审核中写 false。
const reviewPassedKey = "review_passed"

// reviewConfig 是 review_config 与托管 Agent 行拼出的运行期配置。
type reviewConfig struct {
	Enabled      bool
	AgentID      pgtype.UUID
	OwnerID      pgtype.UUID
	RuntimeID    pgtype.UUID
	Model        string
	Thinking     string
	Instructions string
}

func (h *Handler) loadReviewConfig(ctx context.Context, ws pgtype.UUID) (reviewConfig, bool, error) {
	var cfg reviewConfig
	err := h.DB.QueryRow(ctx, `SELECT c.enabled, COALESCE(c.agent_id,'00000000-0000-0000-0000-000000000000'::uuid),
		c.owner_id, COALESCE(a.runtime_id,'00000000-0000-0000-0000-000000000000'::uuid),
		COALESCE(a.model,''), COALESCE(a.thinking_level,''), c.instructions
		FROM review_config c
		LEFT JOIN agent a ON a.id = c.agent_id AND a.system_key = $2
		WHERE c.workspace_id = $1`, ws, review.SystemKey).Scan(
		&cfg.Enabled, &cfg.AgentID, &cfg.OwnerID, &cfg.RuntimeID, &cfg.Model, &cfg.Thinking, &cfg.Instructions)
	if errors.Is(err, pgx.ErrNoRows) {
		return reviewConfig{}, false, nil
	}
	if err != nil {
		return reviewConfig{}, false, err
	}
	return cfg, true, nil
}

// reviewScope 与监督设置一致：只接受真实成员，写操作要求 owner/admin。
func (h *Handler) reviewScope(w http.ResponseWriter, r *http.Request, write bool) (string, string, bool) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, http.StatusForbidden, "审查设置需要成员身份")
		return "", "", false
	}
	ws := middleware.WorkspaceIDFromContext(r.Context())
	member, ok := h.requireWorkspaceMember(w, r, ws, "工作区不存在")
	if !ok {
		return "", "", false
	}
	if write && member.Role != "owner" && member.Role != "admin" {
		writeError(w, http.StatusForbidden, "需要工作区管理员权限")
		return "", "", false
	}
	return ws, requestUserID(r), true
}

// GetReviewSettings 返回「审核中」列的审查配置。默认提示词随响应下发，设置页的
// 「恢复默认」不需要再请求一次。
func (h *Handler) GetReviewSettings(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.reviewScope(w, r, false)
	if !ok {
		return
	}
	wsUUID := parseUUID(ws)
	cfg, configured, err := h.loadReviewConfig(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取审查设置失败")
		return
	}
	member, _ := h.getWorkspaceMember(r.Context(), requestUserID(r), ws)
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":           configured,
		"enabled":              cfg.Enabled,
		"agent_id":             nullableUUID(cfg.AgentID),
		"runtime_id":           nullableUUID(cfg.RuntimeID),
		"model":                cfg.Model,
		"thinking_level":       cfg.Thinking,
		"instructions":         review.EffectiveInstructions(cfg.Instructions),
		"default_instructions": review.DefaultInstructions(),
		"max_rounds":           review.MaxRounds,
		"can_manage":           member.Role == "owner" || member.Role == "admin",
	})
}

// UpdateReviewSettings 保存审查运行时、模型与提示词。审查员由系统托管：首次保存时创建，
// 之后只更新这条 Agent 行，它不会出现在智能体列表里。
func (h *Handler) UpdateReviewSettings(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.reviewScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Enabled       bool    `json:"enabled"`
		RuntimeID     *string `json:"runtime_id"`
		Model         *string `json:"model"`
		ThinkingLevel *string `json:"thinking_level"`
		Instructions  *string `json:"instructions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "配置格式错误")
		return
	}
	if body.Instructions != nil && len(*body.Instructions) > 200_000 {
		writeError(w, http.StatusBadRequest, "提示词过长，请控制在20万字符以内")
		return
	}

	ctx := r.Context()
	wsUUID := parseUUID(ws)
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存审查设置失败")
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	var agentID, ownerID, currentInstructions string
	err = tx.QueryRow(ctx, `SELECT COALESCE(agent_id::text,''), owner_id::text, instructions
		FROM review_config WHERE workspace_id=$1 FOR UPDATE`, ws).Scan(&agentID, &ownerID, &currentInstructions)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "读取审查设置失败")
		return
	}

	var current db.Agent
	hasAgent := agentID != ""
	if hasAgent {
		current, err = qtx.GetAgent(ctx, parseUUID(agentID))
		if err != nil || current.WorkspaceID != wsUUID {
			writeError(w, http.StatusConflict, "原审查 Agent 已不可用，请重新选择运行时后再保存")
			return
		}
		if current.SystemKey.Valid && current.SystemKey.String != review.SystemKey {
			writeError(w, http.StatusConflict, "审查 Agent 配置异常，请联系工作区管理员")
			return
		}
	}

	// 运行时：显式传入优先，否则沿用已保存的绑定。
	var targetRuntime db.AgentRuntime
	hasRuntime := false
	runtimeChanged := false
	if body.RuntimeID != nil && strings.TrimSpace(*body.RuntimeID) != "" {
		runtimeUUID, valid := parseUUIDOrBadRequest(w, *body.RuntimeID, "runtime_id")
		if !valid {
			return
		}
		targetRuntime, err = qtx.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: runtimeUUID, WorkspaceID: wsUUID})
		if err != nil {
			writeError(w, http.StatusBadRequest, "运行时不存在或不属于当前工作区")
			return
		}
		hasRuntime = true
		runtimeChanged = !hasAgent || !current.RuntimeID.Valid || current.RuntimeID != targetRuntime.ID
	} else if hasAgent && current.RuntimeID.Valid {
		if targetRuntime, err = qtx.GetAgentRuntime(ctx, current.RuntimeID); err == nil {
			hasRuntime = true
		}
	}
	if hasRuntime && targetRuntime.Provider != "codex" && targetRuntime.Provider != "codebuddy" {
		writeError(w, http.StatusBadRequest, "代码审查只支持 Codex 或 CodeBuddy 运行时")
		return
	}
	if body.Enabled && !hasRuntime {
		writeError(w, http.StatusBadRequest, "请选择审查运行时")
		return
	}
	if body.ThinkingLevel != nil && hasRuntime {
		level := strings.TrimSpace(*body.ThinkingLevel)
		if level != "" && !agentpkg.IsKnownThinkingValue(targetRuntime.Provider, level) {
			writeError(w, http.StatusBadRequest, "思考等级不适用于所选运行时")
			return
		}
	}

	// 与内置默认一致时不落库，继续跟随平台升级。
	instructions := currentInstructions
	if body.Instructions != nil {
		text := strings.TrimSpace(*body.Instructions)
		if text == "" || text == review.DefaultInstructions() {
			instructions = ""
		} else {
			instructions = *body.Instructions
		}
	}
	effective := review.EffectiveInstructions(instructions)

	// 沿用原绑定时保留原负责人，避免第二个管理员只改间隔却让私有运行时失去调用资格。
	effectiveOwner := ownerID
	if hasAgent && current.OwnerID.Valid {
		effectiveOwner = uuidToString(current.OwnerID)
	}
	if !hasAgent || runtimeChanged || effectiveOwner == "" {
		effectiveOwner = user
	}

	if !hasAgent {
		if hasRuntime {
			// 1. The managed reviewer cannot be updated through the generic agent
			// endpoint, so its avatar has to be written at creation time.
			err = tx.QueryRow(ctx, `INSERT INTO agent (
				workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,permission_mode,
				max_concurrent_tasks,owner_id,instructions,custom_env,custom_args,mcp_config,model,thinking_level,service_tier,system_key,avatar_url)
			VALUES ($1,$2,'由审查设置自动维护，请到审核中列的审查设置里修改',$3,'{}'::jsonb,$4,'private','private',
				1,$5,$6,'{}'::jsonb,'[]'::jsonb,'{}'::jsonb,NULLIF($7,''),NULLIF($8,''),'',$9,$10)
			RETURNING id::text`, ws, review.DefaultName, targetRuntime.RuntimeMode, targetRuntime.ID,
				effectiveOwner, effective, derefTrimmed(body.Model), derefTrimmed(body.ThinkingLevel), review.SystemKey, review.DefaultAvatar).Scan(&agentID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "创建审查 Agent 失败")
				return
			}
		}
	} else {
		model := current.Model.String
		if body.Model != nil {
			model = strings.TrimSpace(*body.Model)
		}
		thinking := current.ThinkingLevel.String
		if body.ThinkingLevel != nil {
			thinking = strings.TrimSpace(*body.ThinkingLevel)
		}
		if hasRuntime {
			if body.Model == nil && runtimeChanged && model != "" && agentpkg.ModelKnownIncompatibleWithProvider(targetRuntime.Provider, model) {
				model = ""
			}
			if _, err = tx.Exec(ctx, `UPDATE agent SET runtime_id=$2,runtime_mode=$3,model=NULLIF($4,''),thinking_level=NULLIF($5,''),
				owner_id=$6,system_key=$7,instructions=$8,archived_at=NULL,archived_by=NULL,updated_at=now() WHERE id=$1`,
				agentID, targetRuntime.ID, targetRuntime.RuntimeMode, model, thinking, effectiveOwner, review.SystemKey, effective); err != nil {
				writeError(w, http.StatusInternalServerError, "更新审查 Agent 失败")
				return
			}
		} else if _, err = tx.Exec(ctx, `UPDATE agent SET system_key=$2,instructions=$3,updated_at=now() WHERE id=$1`,
			agentID, review.SystemKey, effective); err != nil {
			writeError(w, http.StatusInternalServerError, "更新审查 Agent 失败")
			return
		}
	}

	_, err = tx.Exec(ctx, `INSERT INTO review_config(workspace_id,agent_id,owner_id,enabled,instructions)
		VALUES($1,NULLIF($2,'')::uuid,$3,$4,$5) ON CONFLICT(workspace_id) DO UPDATE SET
		agent_id=EXCLUDED.agent_id,owner_id=EXCLUDED.owner_id,enabled=EXCLUDED.enabled,
		instructions=EXCLUDED.instructions,updated_at=now()`,
		ws, agentID, effectiveOwner, body.Enabled, instructions)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存审查设置失败")
		return
	}
	if agentID != "" {
		if err := testagents.Apply(ctx, tx, ws, agentID); err != nil {
			writeError(w, 503, err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "保存审查设置失败")
		return
	}
	h.GetReviewSettings(w, r)
}

// ReviewVerdictRequest 是审查 Agent 提交的一轮结论。
type ReviewVerdictRequest struct {
	Verdict   string          `json:"verdict"`
	CommentID string          `json:"comment_id"`
	Findings  review.Findings `json:"findings"`
}

// SubmitIssueReviewVerdict 处理审查结论：通过写绿勾、打回回到进行中并唤醒开发、
// 停滞或到达轮次上限则停下交给用户。只有该工作区配置的审查 Agent 能提交。
func (h *Handler) SubmitIssueReviewVerdict(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, uuidToString(id))
	if !ok {
		return
	}
	ws := issue.WorkspaceID
	cfg, configured, err := h.loadReviewConfig(r.Context(), ws)
	if err != nil || !configured || !cfg.Enabled || !cfg.AgentID.Valid {
		writeError(w, http.StatusConflict, "该工作区未启用审查")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(ws))
	if actorType != "agent" || actorID != uuidToString(cfg.AgentID) {
		writeError(w, http.StatusForbidden, "只有审查 Agent 可以提交审查结论")
		return
	}

	// 正在执行的旧轮次不能覆盖已经派发的新轮次。
	if taskID := r.Header.Get("X-Task-ID"); taskID != "" {
		if _, valid := parseUUIDOrBadRequest(w, taskID, "task_id"); !valid {
			return
		}
		var current bool
		err := h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_task_queue q WHERE q.id=$1 AND q.issue_id=$2 AND q.agent_id=$3 AND q.status='running' AND NOT EXISTS(SELECT 1 FROM agent_task_queue n WHERE n.issue_id=q.issue_id AND n.agent_id=q.agent_id AND n.created_at>q.created_at))`, taskID, issue.ID, cfg.AgentID).Scan(&current)
		if err != nil || !current {
			writeError(w, http.StatusConflict, "本次审查执行已失效，请读取当前轮次")
			return
		}
	}

	var body ReviewVerdictRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "结论格式错误")
		return
	}
	verdict := review.ParseVerdict(body.Verdict)
	if verdict == "" {
		writeError(w, http.StatusBadRequest, "verdict 只能是 pass、reject、stalled 或 no_action")
		return
	}

	var round int
	var state string
	err = h.DB.QueryRow(r.Context(), `SELECT round, state FROM issue_review WHERE issue_id=$1 AND workspace_id=$2`, issue.ID, ws).Scan(&round, &state)
	if errors.Is(err, pgx.ErrNoRows) || state != review.StateDispatched {
		writeError(w, http.StatusConflict, "当前没有进行中的审查轮次")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取审查轮次失败")
		return
	}
	if issuestatus.Effective(r.Context(), h.Queries, ws, issue.Status) != issuestatus.InReview {
		writeError(w, http.StatusConflict, "任务已不在审核中，本轮结论不再生效")
		return
	}

	findings := body.Findings.Normalize(verdict)
	findingsJSON, err := json.Marshal(findings)
	if err != nil {
		writeError(w, http.StatusBadRequest, "结论格式错误")
		return
	}
	commentID := pgtype.UUID{}
	if body.CommentID != "" {
		if parsed, parseErr := util.ParseUUID(body.CommentID); parseErr == nil {
			commentID = parsed
		}
	}

	switch {
	case verdict == review.VerdictNoAction:
		h.setReviewPassed(r.Context(), issue, false)
		h.recordReviewState(r.Context(), issue.ID, ws, cfg.AgentID, review.StateNoAction, round, findingsJSON, findings.Summary)
		writeJSON(w, http.StatusOK, map[string]any{"state": review.StateNoAction, "round": round})
	case verdict == review.VerdictPass:
		h.recordReviewState(r.Context(), issue.ID, ws, cfg.AgentID, review.StatePassed, round, findingsJSON, findings.Summary)
		h.setReviewPassed(r.Context(), issue, true)
		writeJSON(w, http.StatusOK, map[string]any{"state": review.StatePassed, "round": round})

	case verdict == review.VerdictReject:
		if reason := review.StopReason(round, verdict); reason != "" {
			h.stopReviewAutomation(r.Context(), issue, cfg, round, reason, findings, findingsJSON)
			writeJSON(w, http.StatusOK, map[string]any{"state": review.StateStopped, "round": round, "reason": reason})
			return
		}
		h.recordReviewState(r.Context(), issue.ID, ws, cfg.AgentID, review.StateRejected, round, findingsJSON, findings.Summary)
		h.setReviewPassed(r.Context(), issue, false)
		h.reviewSetStatus(r.Context(), issue, "in_progress")
		h.wakeReviewDeveloper(r.Context(), issue, cfg.AgentID, commentID)
		writeJSON(w, http.StatusOK, map[string]any{"state": review.StateRejected, "round": round, "next_round": round + 1})

	default:
		reason := review.StopReason(round, verdict)
		if reason == "" {
			reason = "审查员报告上一轮问题没有实质改动"
		}
		h.stopReviewAutomation(r.Context(), issue, cfg, round, reason, findings, findingsJSON)
		writeJSON(w, http.StatusOK, map[string]any{"state": review.StateStopped, "round": round, "reason": reason})
	}
}

// afterIssueStatusChangeForReview 在任务进入「审核中」时启动新一轮审查。只在类别变化时
// 触发：同一列内的字段更新、位置调整不会重复派发。
func (h *Handler) afterIssueStatusChangeForReview(ctx context.Context, prev, issue db.Issue) {
	if !issue.WorkspaceID.Valid {
		return
	}
	prevCategory := issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, prev.Status)
	currentCategory := issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status)
	if currentCategory != issuestatus.InReview || prevCategory == issuestatus.InReview {
		return
	}
	h.startReviewRound(ctx, issue)
}

// startReviewRound 判定轮次、做停留检查、清掉上一轮的绿勾并派发审查任务。
func (h *Handler) startReviewRound(ctx context.Context, issue db.Issue) {
	cfg, configured, err := h.loadReviewConfig(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("review: load config failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	if !configured || !cfg.Enabled || !cfg.AgentID.Valid || !cfg.RuntimeID.Valid {
		return
	}

	headSHA := h.TaskService.ResolveIssueReviewSHA(ctx, issue.ID)
	pending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: cfg.AgentID,
		HeadSha: h.TaskService.ResolveIssueReviewSHAParam(ctx, issue.ID),
	})
	if err != nil {
		slog.Warn("review: pending check failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	if pending {
		return
	}

	round := 1
	previousState := ""
	previousHead := ""
	previousFindings := pgtype.Text{}
	var lastRound int
	err = h.DB.QueryRow(ctx, `SELECT round, state, head_sha, findings::text FROM issue_review WHERE issue_id=$1 AND workspace_id=$2`,
		issue.ID, issue.WorkspaceID).Scan(&lastRound, &previousState, &previousHead, &previousFindings)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("review: load state failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	if err == nil {
		if previousState == review.StateNoAction || previousState == review.StatePassed || previousState == review.StateRejected {
			unchanged, e := h.reviewInputUnchanged(ctx, issue, headSHA)
			if e != nil || unchanged {
				return
			}
		}
		switch previousState {
		case review.StateRejected:
			round = lastRound + 1
		case review.StateDispatched:
			round = lastRound
		}
	}

	// 打回后 PR head 没变，说明开发连提交都没有；不必再烧一轮审查。
	if err == nil && previousState == review.StateRejected && previousHead != "" && previousHead == headSHA {
		findings := review.Findings{Summary: "打回后没有新的提交"}
		findingsJSON, _ := json.Marshal(findings)
		h.stopReviewAutomation(ctx, issue, cfg, lastRound, "打回后没有新的提交，任务没有实质进展", findings, findingsJSON)
		return
	}

	if round > review.MaxRounds {
		findings := review.Findings{Summary: "累计审查轮次达到上限"}
		findingsJSON, _ := json.Marshal(findings)
		h.stopReviewAutomation(ctx, issue, cfg, lastRound, "累计审查轮次达到上限", findings, findingsJSON)
		return
	}

	if _, err := h.DB.Exec(ctx, `INSERT INTO issue_review(issue_id,workspace_id,agent_id,developer_agent_id,round,state,head_sha,input_version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(issue_id) DO UPDATE SET
		workspace_id=EXCLUDED.workspace_id,agent_id=EXCLUDED.agent_id,developer_agent_id=EXCLUDED.developer_agent_id,
		round=EXCLUDED.round,state=EXCLUDED.state,head_sha=EXCLUDED.head_sha,input_version=EXCLUDED.input_version,updated_at=now()`,
		issue.ID, issue.WorkspaceID, cfg.AgentID, nullableUUID(issue.AssigneeID), round, review.StateDispatched, headSHA, h.reviewInputVersion(ctx, issue, headSHA)); err != nil {
		slog.Warn("review: record round failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}

	// 新一轮开始，上一轮的通过标记立即失效，避免修复过程中旧勾还挂着。
	h.setReviewPassed(ctx, issue, false)

	handoff := buildReviewHandoff(round, headSHA, previousState, previousFindings.String)
	if _, err := h.TaskService.EnqueueIssueReviewTask(ctx, issue, cfg.AgentID, cfg.OwnerID, handoff, headSHA); err != nil && !errors.Is(err, service.ErrDuplicatePendingTask) {
		slog.Warn("review: enqueue failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
}

// buildReviewHandoff 生成本轮开场说明：轮次、被审提交与上一轮问题清单。
func buildReviewHandoff(round int, headSHA, previousState, previousFindings string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 本次审查\n\n- 轮次：第 %d 轮（上限 %d 轮）\n", round, review.MaxRounds)
	if headSHA != "" {
		fmt.Fprintf(&b, "- 被审提交：%s\n", headSHA)
	} else {
		b.WriteString("- 被审提交：未绑定代码候选；只判断交付材料是否需要处理。仅调查咨询可提交 no_action；要求实现却缺少候选时报告缺口，禁止读取无关目录代码\n")
	}
	if previousState == review.StateRejected && strings.TrimSpace(previousFindings) != "" && previousFindings != "{}" {
		b.WriteString("- 上一轮问题（逐条核对是否已改动）：\n\n```json\n")
		b.WriteString(strings.TrimSpace(previousFindings))
		b.WriteString("\n```\n")
	} else {
		b.WriteString("- 上一轮问题：无，这是第一轮审查\n")
	}
	// 修改 1. 通过、打回、停滞三种结论都要提交：旧写法把「不改任务状态」和「打回或停滞必须
	// submit」写在一起，审查员据此认定通过不用提交，轮次停在派发态、绿勾永远不会落。
	b.WriteString("\n通过、打回、停滞都要用 `multica issue review submit` 提交结论；提交本身会写状态，不要自行改任务状态、受理人或元数据。\n")
	return b.String()
}

// recordReviewState 更新当前轮次的结论与问题清单。
func (h *Handler) recordReviewState(ctx context.Context, issueID, ws, agentID pgtype.UUID, state string, round int, findings []byte, summary string) {
	_, err := h.DB.Exec(ctx, `UPDATE issue_review SET state=$4,findings=$5,summary=$6,updated_at=now()
		WHERE issue_id=$1 AND workspace_id=$2 AND round=$3`,
		issueID, ws, round, state, findings, summary)
	if err != nil {
		slog.Warn("review: record state failed", "issue_id", uuidToString(issueID), "state", state, "error", err)
	}
}

// stopReviewAutomation 停止自动返工：任务置为阻塞、清掉绿勾，并由审查 Agent 留一条
// 带 @负责人 的评论说明分歧。
func (h *Handler) stopReviewAutomation(ctx context.Context, issue db.Issue, cfg reviewConfig, round int, reason string, findings review.Findings, findingsJSON []byte) {
	h.recordReviewState(ctx, issue.ID, issue.WorkspaceID, cfg.AgentID, review.StateStopped, round, findingsJSON, findings.Summary)
	h.setReviewPassed(ctx, issue, false)
	h.reviewSetStatus(ctx, issue, "blocked")
	h.postReviewStopComment(ctx, issue, cfg.AgentID, round, reason, findings)
}

// reviewSetStatus 由服务端直接推进审查结论对应的状态，并广播与 HTTP 更新同形状的事件。
func (h *Handler) reviewSetStatus(ctx context.Context, issue db.Issue, status string) {
	if issue.Status == status {
		return
	}
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      status,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("review: set issue status failed", "issue_id", uuidToString(issue.ID), "status", status, "error", err)
		return
	}
	prefix := h.getIssuePrefix(ctx, updated.WorkspaceID)
	resp := issueToResponse(updated, prefix)
	h.fillStatusCategory(ctx, updated.WorkspaceID, &resp)
	h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", map[string]any{
		"issue":          resp,
		"status_changed": true,
		"prev_status":    issue.Status,
		"creator_type":   issue.CreatorType,
		"creator_id":     uuidToString(issue.CreatorID),
	})
}

// setReviewPassed 写入/清除绿勾标记，并落一次 metadata 变更事件让前端即时刷新。
func (h *Handler) setReviewPassed(ctx context.Context, issue db.Issue, passed bool) {
	// 值没变就不写，避免无意义的 revision 递增与前端抖动。
	if current, ok := parseIssueMetadata(issue.Metadata)[reviewPassedKey]; ok && current == passed {
		return
	}
	value, _ := json.Marshal(passed)
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("review: begin metadata tx failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         reviewPassedKey,
		Value:       value,
	})
	if err != nil {
		slog.Warn("review: set passed flag failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	event, err := service.RecordDurableEventTx(ctx, qtx, events.Event{
		Type:           protocol.EventIssueMetadataChanged,
		IdempotencyKey: "issue_metadata:changed:" + util.UUIDToString(updated.ID) + ":" + fmt.Sprint(updated.Revision),
		StreamKey:      "issue:" + util.UUIDToString(updated.ID),
		WorkspaceID:    uuidToString(updated.WorkspaceID),
		ActorType:      "system",
		Payload: map[string]any{
			"issue_id":       uuidToString(updated.ID),
			"metadata":       parseIssueMetadata(updated.Metadata),
			"issue_revision": updated.Revision,
		},
	})
	if err != nil {
		slog.Warn("review: record metadata event failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("review: commit passed flag failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	h.publishEvent(event)
}

// wakeReviewDeveloper 打回后唤醒原开发 Agent（或群组长）。人类受理人只改状态，不发任务。
func (h *Handler) wakeReviewDeveloper(ctx context.Context, issue db.Issue, reviewAgentID, explicitCommentID pgtype.UUID) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return
	}
	triggerCommentID := explicitCommentID
	if !triggerCommentID.Valid {
		var commentID string
		if err := h.DB.QueryRow(ctx, `SELECT id::text FROM comment
			WHERE issue_id=$1 AND author_type='agent' AND author_id=$2
			ORDER BY created_at DESC LIMIT 1`, issue.ID, reviewAgentID).Scan(&commentID); err == nil {
			if parsed, parseErr := util.ParseUUID(commentID); parseErr == nil {
				triggerCommentID = parsed
			}
		}
	}
	switch issue.AssigneeType.String {
	case "agent":
		if _, err := h.TaskService.EnqueueTaskForMention(ctx, issue, issue.AssigneeID, triggerCommentID); err != nil && !errors.Is(err, service.ErrDuplicatePendingTask) {
			slog.Warn("review: wake developer failed", "issue_id", uuidToString(issue.ID), "error", err)
		}
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return
		}
		if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, squad.ID, triggerCommentID); err != nil && !errors.Is(err, service.ErrDuplicatePendingTask) {
			slog.Warn("review: wake squad leader failed", "issue_id", uuidToString(issue.ID), "error", err)
		}
	}
}

// postReviewStopComment 以审查 Agent 的身份留言并 @工作区负责人，让通知真正送达。
func (h *Handler) postReviewStopComment(ctx context.Context, issue db.Issue, reviewAgentID pgtype.UUID, round int, reason string, findings review.Findings) {
	mention := h.workspaceOwnerMention(ctx, issue.WorkspaceID)
	content := fmt.Sprintf("%s审查在 %d 轮后停止自动返工：%s。\n\n本轮结论：%s\n\n请判断是继续返工、调整需求还是接受现状。", mention, round, reason, findings.Summary)
	if len(findings.Items) > 0 {
		content += "\n\n未解决的问题：\n"
		for _, item := range findings.Items {
			content += fmt.Sprintf("- [%s] %s %s\n", item.Level, item.File, item.Detail)
		}
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("review: begin stop comment tx failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "agent",
		AuthorID:    reviewAgentID,
		Content:     content,
		Type:        "comment",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("review: create stop comment failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	comment := created.Comment()
	event, err := eventoutbox.Enqueue(ctx, qtx, buildCommentCreatedEvent(issue, commentToResponse(comment, nil, nil), "agent", uuidToString(reviewAgentID)))
	if err != nil {
		slog.Warn("review: enqueue stop comment event failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("review: commit stop comment failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	h.publishEvent(event)
}

// workspaceOwnerMention 返回工作区 owner 的提及链接；查不到时返回空串。
func (h *Handler) workspaceOwnerMention(ctx context.Context, ws pgtype.UUID) string {
	var userID, name string
	err := h.DB.QueryRow(ctx, `SELECT m.user_id::text, COALESCE(u.name,'')
		FROM member m JOIN "user" u ON u.id=m.user_id
		WHERE m.workspace_id=$1 AND m.role='owner'
		ORDER BY m.created_at LIMIT 1`, ws).Scan(&userID, &name)
	if err != nil {
		return ""
	}
	label := sanitizeMentionLabel(name)
	if label == "" {
		label = "负责人"
	}
	return fmt.Sprintf("[@%s](mention://member/%s) ", label, userID)
}

func nullableUUID(id pgtype.UUID) any {
	if !id.Valid {
		return nil
	}
	return uuidToString(id)
}

func derefTrimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (h *Handler) reviewInputVersion(ctx context.Context, issue db.Issue, head string) string {
	var version string
	err := h.DB.QueryRow(ctx, `SELECT md5(COALESCE(title,'')||COALESCE(description,'')||COALESCE(acceptance_criteria::text,'')||$2||COALESCE((SELECT string_agg(c.id::text||':'||c.revision::text,',' ORDER BY c.id) FROM comment c WHERE c.issue_id=i.id AND c.author_type='member' AND NOT EXISTS(SELECT 1 FROM delivery_notice n WHERE n.comment_id=c.id)),'')) FROM issue i WHERE i.id=$1`, issue.ID, head).Scan(&version)
	if err != nil {
		return ""
	}
	return version
}
func (h *Handler) reviewInputUnchanged(ctx context.Context, issue db.Issue, head string) (bool, error) {
	var previous string
	err := h.DB.QueryRow(ctx, `SELECT input_version FROM issue_review WHERE issue_id=$1 AND workspace_id=$2`, issue.ID, issue.WorkspaceID).Scan(&previous)
	current := h.reviewInputVersion(ctx, issue, head)
	return previous != "" && current != "" && previous == current, err
}
