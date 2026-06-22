package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Response types ──────────────────────────────────────────────────────────

type SquadResponse struct {
	ID            string                       `json:"id"`
	WorkspaceID   string                       `json:"workspace_id"`
	Name          string                       `json:"name"`
	Description   string                       `json:"description"`
	Instructions  string                       `json:"instructions"`
	SOPProfile    any                          `json:"sop_profile"`
	AvatarURL     *string                      `json:"avatar_url"`
	LeaderID      string                       `json:"leader_id"`
	CreatorID     string                       `json:"creator_id"`
	CreatedAt     string                       `json:"created_at"`
	UpdatedAt     string                       `json:"updated_at"`
	ArchivedAt    *string                      `json:"archived_at"`
	ArchivedBy    *string                      `json:"archived_by"`
	MemberCount   int                          `json:"member_count"`
	MemberPreview []SquadMemberPreviewResponse `json:"member_preview"`
}

type SquadMemberPreviewResponse struct {
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
}

type squadMemberSummary struct {
	count   int
	preview []SquadMemberPreviewResponse
}

type SquadMemberResponse struct {
	ID         string `json:"id"`
	SquadID    string `json:"squad_id"`
	MemberType string `json:"member_type"`
	MemberID   string `json:"member_id"`
	Role       string `json:"role"`
	CreatedAt  string `json:"created_at"`
}

type InternalSquadTemplateResponse struct {
	Squad  SquadResponse        `json:"squad"`
	Agents []InternalSquadAgent `json:"agents"`
}

type InternalSquadAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	RoleKey string `json:"role_key"`
	Role    string `json:"role"`
}

type internalSquadTemplate struct {
	Key          string
	Name         string
	Description  string
	Instructions string
	Model        string
	Roles        []internalSquadRole
	Profile      map[string]any
}

type internalSquadRole struct {
	Key         string
	Name        string
	Instruction string
	MemberRole  string
}

func internalSquadTemplateByKey(key string) (internalSquadTemplate, bool) {
	switch strings.TrimSpace(key) {
	case "user-center":
		return internalSquadTemplate{
			Key:          "user-center-sop-flow",
			Name:         "user-center 小队",
			Description:  "面向 user-center 项目的内部 SOP 小队，由队长按阶段链分派 user-center skill 队员执行。",
			Instructions: "队长按 user-center SOP 分阶段推进；每个阶段都要记录输入、输出、失败原因、耗时和验收证据；不得跳过验收。",
			Model:        "minimax-m2.7-ioa",
			Roles: []internalSquadRole{
				{Key: "captain", Name: "队长", Instruction: "负责接收 issue、判断阶段、拆解任务、汇总证据并推进下一阶段。", MemberRole: "队长"},
				{Key: "skill-member", Name: "skill 队员", Instruction: "按 user-center skill 边界执行具体处理，不越权修改无关模块。", MemberRole: "skill 队员"},
				{Key: "acceptor", Name: "验收者", Instruction: "独立检查实现、测试结果和回写记录。", MemberRole: "验收者"},
			},
			Profile: map[string]any{
				"profile_key": "user-center-sop-flow",
				"project":     "user-center",
				"repo":        "/data/ida/user-center",
				"mode":        "stage_chain",
				"roles": []map[string]any{
					{"key": "captain", "name": "队长", "responsibility": "接收 issue、按阶段链推进、汇总证据并决定是否进入下一阶段。"},
					{"key": "skill-member", "name": "skill 队员", "responsibility": "按 user-center skill 边界执行具体处理，不越权修改无关模块。"},
					{"key": "acceptor", "name": "验收者", "responsibility": "独立检查实现、测试结果和回写记录。"},
				},
				"steps": []map[string]any{
					{"key": "clarify", "name": "需求澄清", "role_key": "captain"},
					{"key": "design", "name": "方案拆解", "role_key": "captain"},
					{"key": "skill_execution", "name": "skill 执行", "role_key": "skill-member"},
					{"key": "acceptance", "name": "验收", "role_key": "acceptor"},
					{"key": "summary", "name": "回写总结", "role_key": "captain"},
				},
				"stage_skills":      []string{"user-center/01-clarify", "user-center/02-design", "user-center/03-task-split", "user-center/04-implement", "user-center/05-verify", "user-center/06-archive"},
				"operation_skills":  []string{"user-center/add-api"},
				"acceptance":        []string{"阶段产物完整", "测试证据完整", "交接说明明确"},
				"forbidden_actions": []string{"跳过验收直接完成", "缺少测试证据时宣称完成", "越过 user-center skill 边界修改无关仓库"},
			},
		}, true
	case "multica-coding":
		roles := []internalSquadRole{
			{Key: "captain", Name: "队长", Instruction: "接需求、判断流程、拆任务、分派给不同 AI、跟踪进度。", MemberRole: "队长"},
			{Key: "designer", Name: "方案设计者", Instruction: "编写技术方案、影响面、任务拆解、测试方案；重大开发前先给人确认。", MemberRole: "方案设计者"},
			{Key: "developer", Name: "开发者", Instruction: "只按分配范围改代码，包括前端、后端、测试或部署中的一块。", MemberRole: "开发者"},
			{Key: "acceptor", Name: "验收者", Instruction: "独立检查代码、测试结果、漏改和回归风险。", MemberRole: "验收者"},
			{Key: "spec-maintainer", Name: "规约维护者", Instruction: "判断是否同步流程文档、测试数据说明、接口索引、技能说明。", MemberRole: "规约维护者"},
			{Key: "operator", Name: "部署运行者", Instruction: "负责端口、环境变量、数据库、启动服务、健康检查、部署验证；不能泄露密钥。", MemberRole: "部署运行者"},
		}
		return internalSquadTemplate{
			Key:          "multica-coding",
			Name:         "Multica 编码小队",
			Description:  "用于开发 Multica 自身的生产级编码小队，包含队长、方案设计者、开发者、验收者、规约维护者和部署运行者。",
			Instructions: "队长先澄清需求和验收口径，再按角色分派；开发者不得越界；验收者必须独立给出证据；所有指标和输出使用中文。",
			Model:        "minimax-m2.7-ioa",
			Roles:        roles,
			Profile: map[string]any{
				"profile_key": "multica-coding",
				"project":     "multica",
				"repo":        "/data/ida/goal-test",
				"mode":        "coding_squad",
				"roles": []map[string]any{
					{"key": "captain", "name": "队长", "responsibility": "接需求、判断流程、拆任务、分派给不同 AI、跟踪进度。"},
					{"key": "designer", "name": "方案设计者", "responsibility": "编写技术方案、影响面、任务拆解、测试方案。"},
					{"key": "developer", "name": "开发者", "responsibility": "按分配范围改代码，不能越界。"},
					{"key": "acceptor", "name": "验收者", "responsibility": "独立检查代码、测试结果和漏改。"},
					{"key": "spec-maintainer", "name": "规约维护者", "responsibility": "同步流程文档、测试数据说明、接口索引、技能说明。"},
					{"key": "operator", "name": "部署运行者", "responsibility": "负责环境、启动、日志和健康检查。"},
				},
				"steps": []map[string]any{
					{"key": "receive", "name": "接收需求", "role_key": "captain"},
					{"key": "design_review", "name": "方案设计与确认", "role_key": "designer"},
					{"key": "implementation", "name": "分工开发", "role_key": "developer"},
					{"key": "independent_acceptance", "name": "独立验收", "role_key": "acceptor"},
					{"key": "spec_sync", "name": "规约同步", "role_key": "spec-maintainer"},
					{"key": "deploy_verify", "name": "部署运行验证", "role_key": "operator"},
					{"key": "final_report", "name": "证据汇总", "role_key": "captain"},
				},
				"model_policy":      map[string]any{"默认模型": "minimax", "代码测试复杂审查": "gpt", "策略说明": "minimax 用于大批量普通执行；涉及代码、测试、复杂审查时使用 gpt。"},
				"stage_skills":      []string{},
				"operation_skills":  []string{},
				"acceptance":        []string{"方案经确认", "代码范围清晰", "验收者独立给结论", "测试证据完整", "规约同步或说明无需同步", "运行验证完成"},
				"forbidden_actions": []string{"泄露密钥", "开发者越权改范围外代码", "未独立验收就完成", "跳过测试证据", "文档接口语义停留在旧版本"},
			},
		}, true
	default:
		return internalSquadTemplate{}, false
	}
}

// ── Converters ──────────────────────────────────────────────────────────────

func squadToResponse(s db.Squad) SquadResponse {
	return SquadResponse{
		ID:            uuidToString(s.ID),
		WorkspaceID:   uuidToString(s.WorkspaceID),
		Name:          s.Name,
		Description:   s.Description,
		Instructions:  s.Instructions,
		SOPProfile:    decodeSquadSOPProfile(s.SopProfile),
		AvatarURL:     textToPtr(s.AvatarUrl),
		LeaderID:      uuidToString(s.LeaderID),
		CreatorID:     uuidToString(s.CreatorID),
		CreatedAt:     timestampToString(s.CreatedAt),
		UpdatedAt:     timestampToString(s.UpdatedAt),
		ArchivedAt:    timestampToPtr(s.ArchivedAt),
		ArchivedBy:    uuidToPtr(s.ArchivedBy),
		MemberPreview: []SquadMemberPreviewResponse{},
	}
}

func decodeSquadSOPProfile(raw []byte) any {
	var profile any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &profile)
	}
	if profile == nil {
		return map[string]any{}
	}
	return profile
}

func squadMemberToResponse(m db.SquadMember) SquadMemberResponse {
	return SquadMemberResponse{
		ID:         uuidToString(m.ID),
		SquadID:    uuidToString(m.SquadID),
		MemberType: m.MemberType,
		MemberID:   uuidToString(m.MemberID),
		Role:       m.Role,
		CreatedAt:  timestampToString(m.CreatedAt),
	}
}

func addSquadMemberPreview(summary *squadMemberSummary, memberType string, memberID pgtype.UUID, role string) {
	summary.count++
	if len(summary.preview) >= 3 {
		return
	}
	summary.preview = append(summary.preview, SquadMemberPreviewResponse{
		MemberType: memberType,
		MemberID:   uuidToString(memberID),
		Role:       role,
	})
}

func applySquadMemberSummary(resp *SquadResponse, summary *squadMemberSummary) {
	if summary == nil {
		return
	}
	resp.MemberCount = summary.count
	resp.MemberPreview = summary.preview
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// loadSquadInWorkspace loads a squad scoped to the current workspace.
func (h *Handler) loadSquadInWorkspace(w http.ResponseWriter, r *http.Request) (db.Squad, string, bool) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squadID := chi.URLParam(r, "id")
	squadUUID, ok := parseUUIDOrBadRequest(w, squadID, "squad id")
	if !ok {
		return db.Squad{}, "", false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.Squad{}, "", false
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID:          squadUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return db.Squad{}, "", false
	}
	return squad, workspaceID, true
}

func (h *Handler) loadSquadMemberSummary(ctx context.Context, squadID pgtype.UUID) (*squadMemberSummary, error) {
	rows, err := h.Queries.ListSquadMemberPreviewRowsBySquad(ctx, squadID)
	if err != nil {
		return nil, err
	}
	summary := &squadMemberSummary{}
	for _, row := range rows {
		addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
	}
	return summary, nil
}

func (h *Handler) squadToResponseWithPreview(ctx context.Context, squad db.Squad) (SquadResponse, error) {
	resp := squadToResponse(squad)
	summary, err := h.loadSquadMemberSummary(ctx, squad.ID)
	if err != nil {
		return resp, err
	}
	applySquadMemberSummary(&resp, summary)
	return resp, nil
}

// ── Handlers ────────────────────────────────────────────────────────────────

func (h *Handler) ListSquads(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	squads, err := h.Queries.ListSquads(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squads")
		return
	}

	previewRows, err := h.Queries.ListSquadMemberPreviewRows(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad member preview")
		return
	}
	summaries := make(map[string]*squadMemberSummary, len(squads))
	for _, row := range previewRows {
		squadID := uuidToString(row.SquadID)
		summary := summaries[squadID]
		if summary == nil {
			summary = &squadMemberSummary{}
			summaries[squadID] = summary
		}
		addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
	}

	resp := make([]SquadResponse, len(squads))
	for i, s := range squads {
		resp[i] = squadToResponse(s)
		applySquadMemberSummary(&resp[i], summaries[uuidToString(s.ID)])
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CreateSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}

	var req struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		LeaderID    string          `json:"leader_id"`
		AvatarURL   *string         `json:"avatar_url"`
		SOPProfile  json.RawMessage `json:"sop_profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.LeaderID == "" {
		writeError(w, http.StatusBadRequest, "leader_id is required")
		return
	}

	leaderUUID, ok := parseUUIDOrBadRequest(w, req.LeaderID, "leader_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// Validate leader is an agent in this workspace.
	_, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          leaderUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
		return
	}

	avatarURL := pgtype.Text{}
	if req.AvatarURL != nil {
		avatarURL = pgtype.Text{String: *req.AvatarURL, Valid: true}
	}
	var sopProfile []byte
	if len(req.SOPProfile) > 0 {
		if !json.Valid(req.SOPProfile) {
			writeError(w, http.StatusBadRequest, "sop_profile must be valid JSON")
			return
		}
		sopProfile = req.SOPProfile
	}

	squad, err := h.Queries.CreateSquad(r.Context(), db.CreateSquadParams{
		WorkspaceID: wsUUID,
		Name:        req.Name,
		Description: req.Description,
		LeaderID:    leaderUUID,
		CreatorID:   member.UserID,
		AvatarUrl:   avatarURL,
		SopProfile:  sopProfile,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create squad")
		return
	}

	// Auto-add leader as a member with role "leader".
	h.Queries.AddSquadMember(r.Context(), db.AddSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: "agent",
		MemberID:   leaderUUID,
		Role:       "leader",
	})

	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	h.publish(protocol.EventSquadCreated, workspaceID, "member", uuidToString(member.UserID), map[string]any{"squad": resp})
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.SquadCreated(
		uuidToString(member.UserID),
		workspaceID,
		uuidToString(squad.ID),
		1,
	))
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) EnsureInternalSquadTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		TemplateKey string `json:"template_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	template, ok := internalSquadTemplateByKey(req.TemplateKey)
	if !ok {
		writeError(w, http.StatusBadRequest, "template_key must be user-center or multica-coding")
		return
	}

	runtime, ok := h.selectInternalSquadRuntime(w, r, wsUUID, member)
	if !ok {
		return
	}
	agents, err := h.ensureInternalSquadAgents(r.Context(), wsUUID, member.UserID, runtime, template)
	if err != nil {
		slog.Warn("ensure internal squad agents failed", append(logger.RequestAttrs(r), "error", err, "template", template.Key)...)
		writeError(w, http.StatusInternalServerError, "failed to create internal squad agents")
		return
	}
	squad, err := h.ensureInternalSquad(r.Context(), wsUUID, member.UserID, template, agents)
	if err != nil {
		slog.Warn("ensure internal squad failed", append(logger.RequestAttrs(r), "error", err, "template", template.Key)...)
		writeError(w, http.StatusInternalServerError, "failed to create internal squad")
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load internal squad")
		return
	}
	writeJSON(w, http.StatusOK, InternalSquadTemplateResponse{Squad: resp, Agents: agents})
}

func (h *Handler) selectInternalSquadRuntime(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, member db.Member) (db.AgentRuntime, bool) {
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return db.AgentRuntime{}, false
	}
	checkedAt := time.Now().UTC()
	var best *db.AgentRuntime
	for i := range runtimes {
		runtime := runtimes[i]
		if !strings.EqualFold(runtime.Provider, "codebuddy") || !canUseRuntimeForAgent(member, runtime) {
			continue
		}
		if best == nil || runtimeReadinessRank(runtime, checkedAt) > runtimeReadinessRank(*best, checkedAt) {
			best = &runtime
		}
	}
	if best == nil {
		writeError(w, http.StatusServiceUnavailable, "当前 workspace 没有可用的 CodeBuddy runtime，无法创建真实可执行的内部小队。请先启动 multica daemon 并确认 /api/runtimes 出现 provider=codebuddy 的在线 runtime。")
		return db.AgentRuntime{}, false
	}
	if best.Status != "online" || !best.LastSeenAt.Valid || checkedAt.Sub(best.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		writeError(w, http.StatusServiceUnavailable, "CodeBuddy runtime 当前未就绪，无法创建真实可执行的内部小队。请启动 daemon 并等待 runtime 心跳刷新。")
		return db.AgentRuntime{}, false
	}
	return *best, true
}

func (h *Handler) ensureInternalSquadAgents(ctx context.Context, workspaceID pgtype.UUID, ownerID pgtype.UUID, runtime db.AgentRuntime, template internalSquadTemplate) ([]InternalSquadAgent, error) {
	existing, err := h.Queries.ListAgents(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	byName := map[string]db.Agent{}
	for _, agent := range existing {
		byName[agent.Name] = agent
	}
	result := make([]InternalSquadAgent, 0, len(template.Roles))
	for _, role := range template.Roles {
		name := template.Name + " · " + role.Name
		agentRow, ok := byName[name]
		if !ok {
			runtimeConfig := mustJSONBytes(map[string]any{
				"provider": runtime.Provider,
				"用途":       template.Name,
				"角色":       role.Name,
				"模板":       template.Key,
			})
			agentRow, err = h.Queries.CreateAgent(ctx, db.CreateAgentParams{
				WorkspaceID:        workspaceID,
				Name:               name,
				Description:        template.Description,
				Instructions:       "你是" + template.Name + "的" + role.Name + "。" + role.Instruction + "所有输出必须使用中文，并保留可验收证据。",
				RuntimeMode:        runtime.RuntimeMode,
				RuntimeConfig:      runtimeConfig,
				RuntimeID:          runtime.ID,
				Visibility:         "workspace",
				MaxConcurrentTasks: 2,
				OwnerID:            ownerID,
				CustomEnv:          []byte("{}"),
				CustomArgs:         []byte("[]"),
				Model:              pgtype.Text{String: template.Model, Valid: template.Model != ""},
			})
			if err != nil {
				return nil, err
			}
		}
		result = append(result, InternalSquadAgent{
			ID:      uuidToString(agentRow.ID),
			Name:    agentRow.Name,
			RoleKey: role.Key,
			Role:    role.Name,
		})
	}
	return result, nil
}

func (h *Handler) ensureInternalSquad(ctx context.Context, workspaceID pgtype.UUID, creatorID pgtype.UUID, template internalSquadTemplate, agents []InternalSquadAgent) (db.Squad, error) {
	squads, err := h.Queries.ListSquads(ctx, workspaceID)
	if err != nil {
		return db.Squad{}, err
	}
	var squad db.Squad
	for _, item := range squads {
		profile := decodeJSONDefault(item.SopProfile, map[string]any{})
		profileMap, _ := profile.(map[string]any)
		if item.Name == template.Name || stringFromAny(profileMap["profile_key"]) == template.Key {
			squad = item
			break
		}
	}
	if uuidToString(squad.ID) == "" {
		if len(agents) == 0 {
			return db.Squad{}, pgx.ErrNoRows
		}
		sopProfile := mustJSONBytes(template.Profile)
		squad, err = h.Queries.CreateSquad(ctx, db.CreateSquadParams{
			WorkspaceID: workspaceID,
			Name:        template.Name,
			Description: template.Description,
			LeaderID:    parseUUID(agents[0].ID),
			CreatorID:   creatorID,
			SopProfile:  sopProfile,
		})
		if err != nil {
			return db.Squad{}, err
		}
	} else {
		profileBytes := mustJSONBytes(template.Profile)
		if !bytes.Equal(bytes.TrimSpace(squad.SopProfile), bytes.TrimSpace(profileBytes)) || squad.Instructions != template.Instructions {
			squad, err = h.Queries.UpdateSquad(ctx, db.UpdateSquadParams{
				ID:           squad.ID,
				Description:  pgtype.Text{String: template.Description, Valid: true},
				Instructions: pgtype.Text{String: template.Instructions, Valid: true},
				SopProfile:   profileBytes,
			})
			if err != nil {
				return db.Squad{}, err
			}
		}
	}
	existingMembers, err := h.Queries.ListSquadMembers(ctx, squad.ID)
	if err != nil {
		return db.Squad{}, err
	}
	existingMemberRoles := map[string]string{}
	for _, member := range existingMembers {
		existingMemberRoles[member.MemberType+":"+uuidToString(member.MemberID)] = member.Role
	}
	for _, agent := range agents {
		role := "member"
		for _, templateRole := range template.Roles {
			if templateRole.Key == agent.RoleKey {
				role = templateRole.MemberRole
				break
			}
		}
		memberID := parseUUID(agent.ID)
		memberKey := "agent:" + agent.ID
		if existingRole, exists := existingMemberRoles[memberKey]; exists {
			if existingRole != role {
				if _, err := h.Queries.UpdateSquadMemberRole(ctx, db.UpdateSquadMemberRoleParams{
					SquadID:    squad.ID,
					MemberType: "agent",
					MemberID:   memberID,
					Role:       role,
				}); err != nil {
					return db.Squad{}, err
				}
			}
			continue
		}
		if _, err := h.Queries.AddSquadMember(ctx, db.AddSquadMemberParams{
			SquadID:    squad.ID,
			MemberType: "agent",
			MemberID:   memberID,
			Role:       role,
		}); err != nil {
			return db.Squad{}, err
		}
		existingMemberRoles[memberKey] = role
	}
	return squad, nil
}

func (h *Handler) GetSquad(w http.ResponseWriter, r *http.Request) {
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), squad)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) UpdateSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		Name         *string         `json:"name"`
		Description  *string         `json:"description"`
		Instructions *string         `json:"instructions"`
		LeaderID     *string         `json:"leader_id"`
		AvatarURL    *string         `json:"avatar_url"`
		SOPProfile   json.RawMessage `json:"sop_profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateSquadParams{ID: squad.ID}
	if req.Name != nil {
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Instructions != nil {
		params.Instructions = pgtype.Text{String: *req.Instructions, Valid: true}
	}
	if req.AvatarURL != nil {
		params.AvatarUrl = pgtype.Text{String: *req.AvatarURL, Valid: true}
	}
	if len(req.SOPProfile) > 0 {
		if !json.Valid(req.SOPProfile) {
			writeError(w, http.StatusBadRequest, "sop_profile must be valid JSON")
			return
		}
		params.SopProfile = req.SOPProfile
	}
	if req.LeaderID != nil {
		lid, ok := parseUUIDOrBadRequest(w, *req.LeaderID, "leader_id")
		if !ok {
			return
		}
		// Validate new leader is an agent in workspace.
		if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: lid, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
			return
		}
		// Ensure new leader is a squad member; auto-add if not.
		isMember, _ := h.Queries.IsSquadMember(r.Context(), db.IsSquadMemberParams{
			SquadID: squad.ID, MemberType: "agent", MemberID: lid,
		})
		if !isMember {
			h.Queries.AddSquadMember(r.Context(), db.AddSquadMemberParams{
				SquadID: squad.ID, MemberType: "agent", MemberID: lid, Role: "leader",
			})
		}
		params.LeaderID = lid
	}

	updated, err := h.Queries.UpdateSquad(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update squad")
		return
	}

	resp, err := h.squadToResponseWithPreview(r.Context(), updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{"squad": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}

	if squad.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "squad is already archived")
		return
	}

	// Transfer issues assigned to this squad to the leader agent.
	if err := h.Queries.TransferSquadAssignees(r.Context(), db.TransferSquadAssigneesParams{
		AssigneeID:   squad.ID,
		AssigneeID_2: squad.LeaderID,
	}); err != nil {
		slog.Warn("transfer squad assignees failed", "squad_id", uuidToString(squad.ID), "error", err)
	}

	// Mirror the issue-assignee transfer for autopilots that target this
	// squad. Without this, autopilot.assignee_id would still point at the
	// archived squad row and every subsequent dispatch would skip with
	// "assignee squad is archived" — visible to ops but useless to the
	// owner. Rewriting to the leader keeps the autopilot semantics
	// unchanged (Path A from MUL-2429 is leader-only execution anyway).
	if err := h.Queries.TransferSquadAutopilotsToLeader(r.Context(), db.TransferSquadAutopilotsToLeaderParams{
		AssigneeID:   squad.ID,
		AssigneeID_2: squad.LeaderID,
	}); err != nil {
		slog.Warn("transfer squad autopilots failed", "squad_id", uuidToString(squad.ID), "error", err)
	}

	userID := requestUserID(r)
	userUUID, _ := parseUUIDOrBadRequest(w, userID, "user_id")

	if _, err := h.Queries.ArchiveSquad(r.Context(), db.ArchiveSquadParams{
		ID:         squad.ID,
		ArchivedBy: userUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive squad")
		return
	}

	h.publish(protocol.EventSquadDeleted, workspaceID, "member", userID, map[string]any{
		"squad_id":  uuidToString(squad.ID),
		"leader_id": uuidToString(squad.LeaderID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// ── Squad Members ───────────────────────────────────────────────────────────

func (h *Handler) ListSquadMembers(w http.ResponseWriter, r *http.Request) {
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	members, err := h.Queries.ListSquadMembers(r.Context(), squad.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad members")
		return
	}
	resp := make([]SquadMemberResponse, len(members))
	for i, m := range members {
		resp[i] = squadMemberToResponse(m)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── Squad Member Status ────────────────────────────────────────────────────

// SquadMemberStatus is the per-member entry in the squad member status
// response. Agent members carry a derived working/idle/offline/unstable
// status plus any active issues; human members are returned with member_type
// only so the front-end can render them in the same list without
// reordering.
type SquadMemberStatusResponse struct {
	MemberType   string                  `json:"member_type"`
	MemberID     string                  `json:"member_id"`
	Status       *string                 `json:"status"`
	ActiveIssues []SquadActiveIssueBrief `json:"active_issues"`
	LastActiveAt *string                 `json:"last_active_at"`
}

type SquadActiveIssueBrief struct {
	IssueID     string `json:"issue_id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	IssueStatus string `json:"issue_status"`
}

type SquadMemberStatusListResponse struct {
	Members []SquadMemberStatusResponse `json:"members"`
}

// deriveSquadMemberStatus collapses runtime + task signals into the five
// status buckets used by the squad UI. Mirrors the workload+availability
// split in packages/core/agents/derive-presence.ts: working wins over
// runtime health (an agent that is in the middle of dispatched/running
// work counts as working even if the runtime briefly drops), then
// availability buckets decide between idle / unstable / offline.
//
// Thresholds match deriveRuntimeHealth: any offline runtime whose
// last_seen_at is within the last 5 minutes is reported as "unstable" so
// the squad UI surfaces transient drops the same way the agent dot does.
//
// Archived agents always report `archived` regardless of any leftover
// runtime row or task — they should appear in the list but never look
// like they're still working or merely offline (a leftover online
// runtime row would otherwise read as "offline" and hide the fact that
// the agent has been archived). Per the RFC decision (see MUL-2319), we
// surface archived agents in this endpoint rather than filtering them
// out in the SQL.
func deriveSquadMemberStatus(
	archived bool,
	runtimeStatus pgtype.Text,
	lastSeen pgtype.Timestamptz,
	hasActiveTask bool,
	now time.Time,
) string {
	if archived {
		return "archived"
	}
	if hasActiveTask {
		return "working"
	}
	if !runtimeStatus.Valid {
		return "offline"
	}
	if runtimeStatus.String == "online" {
		return "idle"
	}
	if !lastSeen.Valid {
		return "offline"
	}
	if now.Sub(lastSeen.Time) < 5*time.Minute {
		return "unstable"
	}
	return "offline"
}

// ListSquadMemberStatus returns one entry per squad member with derived
// status, the issues each agent member is currently running, and the last
// observed runtime activity. The endpoint is read-only and inherits the
// workspace-membership guard from the route middleware — any member of the
// workspace can read it.
func (h *Handler) ListSquadMemberStatus(w http.ResponseWriter, r *http.Request) {
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}

	rows, err := h.Queries.ListSquadMemberStatusRows(r.Context(), squad.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squad member status")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), squad.WorkspaceID)
	now := time.Now()

	// Group rows by member_id while preserving the SQL ORDER BY (squad_member
	// insertion order). One member may appear in multiple rows when they have
	// more than one active task.
	type memberAcc struct {
		response       SquadMemberStatusResponse
		archived       bool
		hasActiveTask  bool
		runtimeStatus  pgtype.Text
		runtimeSeenAt  pgtype.Timestamptz
		latestActiveAt pgtype.Timestamptz
	}
	order := make([]string, 0, len(rows))
	acc := make(map[string]*memberAcc, len(rows))

	for _, row := range rows {
		memberID := uuidToString(row.MemberID)
		entry, exists := acc[memberID]
		if !exists {
			entry = &memberAcc{
				response: SquadMemberStatusResponse{
					MemberType:   row.MemberType,
					MemberID:     memberID,
					ActiveIssues: []SquadActiveIssueBrief{},
				},
				archived:      row.AgentArchivedAt.Valid,
				runtimeStatus: row.RuntimeStatus,
				runtimeSeenAt: row.RuntimeLastSeenAt,
			}
			acc[memberID] = entry
			order = append(order, memberID)
		}

		if row.MemberType != "agent" {
			continue
		}

		// A dispatched/running task occupies an agent slot even when it
		// has no associated issue (chat / quick-create tasks set
		// agent_task_queue.issue_id = NULL). The `working` bucket is
		// defined by task presence, not by whether we can render an
		// issue link, so flag the agent here regardless of issue_id.
		if row.TaskID.Valid {
			entry.hasActiveTask = true

			if row.TaskIssueID.Valid {
				brief := SquadActiveIssueBrief{
					IssueID:    uuidToString(row.TaskIssueID),
					Identifier: prefix + "-" + strconv.Itoa(int(row.IssueNumber.Int32)),
					Title:      row.IssueTitle.String,
					IssueStatus: func() string {
						if row.IssueStatus.Valid {
							return row.IssueStatus.String
						}
						return ""
					}(),
				}
				entry.response.ActiveIssues = append(entry.response.ActiveIssues, brief)
			}

			if row.TaskDispatchedAt.Valid && (!entry.latestActiveAt.Valid ||
				row.TaskDispatchedAt.Time.After(entry.latestActiveAt.Time)) {
				entry.latestActiveAt = row.TaskDispatchedAt
			}
		}
	}

	resp := SquadMemberStatusListResponse{
		Members: make([]SquadMemberStatusResponse, 0, len(order)),
	}
	for _, id := range order {
		entry := acc[id]
		if entry.response.MemberType == "agent" {
			status := deriveSquadMemberStatus(
				entry.archived,
				entry.runtimeStatus,
				entry.runtimeSeenAt,
				entry.hasActiveTask,
				now,
			)
			entry.response.Status = &status
			// last_active_at prefers the freshest active-task dispatch
			// over the runtime heartbeat: a working agent should not
			// look stale because the runtime heartbeat is a few seconds
			// behind. Falls back to runtime last_seen_at otherwise.
			if entry.latestActiveAt.Valid {
				entry.response.LastActiveAt = timestampToPtr(entry.latestActiveAt)
			} else if entry.runtimeSeenAt.Valid {
				entry.response.LastActiveAt = timestampToPtr(entry.runtimeSeenAt)
			}
		}
		resp.Members = append(resp.Members, entry.response)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) AddSquadMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MemberType != "agent" && req.MemberType != "member" {
		writeError(w, http.StatusBadRequest, "member_type must be 'agent' or 'member'")
		return
	}
	if req.MemberID == "" {
		writeError(w, http.StatusBadRequest, "member_id is required")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	// Validate the member belongs to this workspace.
	if req.MemberType == "agent" {
		if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: memberUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "agent not found in this workspace")
			return
		}
	} else {
		if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID: memberUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "member not found in this workspace")
			return
		}
	}

	sm, err := h.Queries.AddSquadMember(r.Context(), db.AddSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
		Role:       req.Role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "member already in squad")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to add squad member")
		return
	}

	writeJSON(w, http.StatusCreated, squadMemberToResponse(sm))
	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{
		"squad_id": uuidToString(squad.ID),
	})
}

func (h *Handler) RemoveSquadMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	// Prevent removing the leader.
	if req.MemberType == "agent" && uuidToString(squad.LeaderID) == req.MemberID {
		writeError(w, http.StatusBadRequest, "cannot remove the squad leader; change leader first")
		return
	}

	rows, err := h.Queries.RemoveSquadMember(r.Context(), db.RemoveSquadMemberParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove squad member")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "squad member not found")
		return
	}

	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{
		"squad_id": uuidToString(squad.ID),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) UpdateSquadMemberRole(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}

	var req struct {
		MemberType string `json:"member_type"`
		MemberID   string `json:"member_id"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}

	sm, err := h.Queries.UpdateSquadMemberRole(r.Context(), db.UpdateSquadMemberRoleParams{
		SquadID:    squad.ID,
		MemberType: req.MemberType,
		MemberID:   memberUUID,
		Role:       req.Role,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad member not found")
		return
	}

	h.publish(protocol.EventSquadUpdated, workspaceID, "member", requestUserID(r), map[string]any{
		"squad_id": uuidToString(squad.ID),
	})
	writeJSON(w, http.StatusOK, squadMemberToResponse(sm))
}

// ── Squad Leader Evaluation ──────────────────────────────────────────────────

// RecordSquadLeaderEvaluation records a squad leader's evaluation decision
// into the unified activity_log. Called by the leader agent via CLI after
// each trigger to record whether it took action, stayed silent, or failed.
func (h *Handler) RecordSquadLeaderEvaluation(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		Outcome string `json:"outcome"` // action | no_action | failed
		Reason  string `json:"reason"`  // short explanation from leader
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Outcome != "action" && req.Outcome != "no_action" && req.Outcome != "failed" {
		writeError(w, http.StatusBadRequest, "outcome must be 'action', 'no_action', or 'failed'")
		return
	}

	// The issue must be assigned to a squad.
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		writeError(w, http.StatusBadRequest, "issue is not assigned to a squad")
		return
	}

	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return
	}

	// Security: only the squad leader agent can record evaluations.
	workspaceID := uuidToString(issue.WorkspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if actorType != "agent" || actorID != uuidToString(squad.LeaderID) {
		writeError(w, http.StatusForbidden, "only the squad leader agent can record evaluations")
		return
	}

	taskID := r.Header.Get("X-Task-ID")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task id")
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil || !task.IssueID.Valid || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusBadRequest, "task does not belong to issue")
		return
	}

	details, _ := json.Marshal(map[string]string{
		"squad_id": uuidToString(squad.ID),
		"task_id":  util.UUIDToString(taskUUID),
		"outcome":  req.Outcome,
		"reason":   req.Reason,
	})

	activity, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		ActorType:   pgtype.Text{String: "agent", Valid: true},
		ActorID:     squad.LeaderID,
		Action:      "squad_leader_evaluated",
		Details:     details,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record evaluation")
		return
	}

	h.publish(protocol.EventActivityCreated, uuidToString(issue.WorkspaceID), "agent", actorID, map[string]any{
		"issue_id": uuidToString(issue.ID),
		"entry": map[string]any{
			"type":       "activity",
			"id":         uuidToString(activity.ID),
			"actor_type": "agent",
			"actor_id":   actorID,
			"action":     activity.Action,
			"details":    json.RawMessage(details),
			"created_at": timestampToString(activity.CreatedAt),
		},
	})

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":         uuidToString(activity.ID),
		"action":     activity.Action,
		"created_at": timestampToString(activity.CreatedAt),
	})
}

// ── Squad Trigger Logic ─────────────────────────────────────────────────────

// lastTaskWasLeader returns true when the agent's most recent task on the
// issue was enqueued in the squad-leader role. Used by the self-trigger
// guards to tell apart a comment posted while the agent was acting as
// leader (skip) from one posted while it was acting as a worker (do not
// skip). When the agent has no prior task on this issue the role is
// undetermined and we treat it as non-leader so a brand-new external
// trigger can still reach the leader.
func (h *Handler) lastTaskWasLeader(ctx context.Context, issueID, agentID pgtype.UUID) bool {
	flag, err := h.Queries.GetLatestTaskIsLeaderForIssueAndAgent(ctx, db.GetLatestTaskIsLeaderForIssueAndAgentParams{
		IssueID: issueID,
		AgentID: agentID,
	})
	if err != nil {
		return false
	}
	return flag
}

// commentMentionsAnyone returns true when the comment body contains at least
// one routing-style mention — [@Name](mention://agent|member|squad|all/<id>).
// Issue cross-references (mention://issue/...) are ignored because they are
// not directed at a participant. Only the current comment is inspected —
// parent (thread root) mentions are NOT inherited here.
func commentMentionsAnyone(content string) bool {
	for _, m := range util.ParseMentions(content) {
		switch m.Type {
		case "agent", "member", "squad", "all":
			return true
		}
	}
	return false
}

// shouldEnqueueSquadLeaderOnAssign returns true when assigning an issue to a
// squad (or creating an issue pre-assigned to a squad) should immediately
// trigger the squad leader. Mirrors shouldEnqueueAgentTask: backlog issues
// are skipped (parking lot), and the leader agent must have a runtime and
// not be archived.
func (h *Handler) shouldEnqueueSquadLeaderOnAssign(ctx context.Context, issue db.Issue) bool {
	if issue.Status == "backlog" {
		return false
	}
	return h.isSquadLeaderReady(ctx, issue)
}

// isSquadLeaderReady returns true when the issue is assigned to a squad whose
// leader agent can accept work right now. Readiness criteria (archived,
// runtime bound, runtime online) are shared with the autopilot admission
// gate via service.AgentReadiness — both paths must move together or one
// will start enqueueing tasks the other refuses (MUL-2429 RFC §4.b B4).
func (h *Handler) isSquadLeaderReady(ctx context.Context, issue db.Issue) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return false
	}
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	agent, err := h.Queries.GetAgent(ctx, squad.LeaderID)
	if err != nil {
		return false
	}
	ready, _, err := service.AgentReadiness(ctx, h.Queries, agent)
	if err != nil {
		// Fail closed when we can't tell — same posture as the rest of
		// this function (any error path returns false).
		return false
	}
	return ready
}

// enqueueSquadLeaderTask triggers the squad leader agent for an issue assigned
// to a squad. Assign and backlog-promotion paths use this directly; comment
// paths go through computeCommentAgentTriggers so preview and create share the
// same trigger set.
func (h *Handler) enqueueSquadLeaderTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, authorType, authorID string) {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return
	}

	if !h.canEnqueueSquadLeader(ctx, squad.LeaderID, authorType, authorID, uuidToString(issue.WorkspaceID)) {
		return
	}

	hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil || hasPending {
		return
	}

	if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, triggerCommentID); err != nil {
		slog.Warn("enqueue squad leader task failed",
			"issue_id", uuidToString(issue.ID),
			"squad_id", uuidToString(squad.ID),
			"leader_id", uuidToString(squad.LeaderID),
			"error", err)
	}
}
