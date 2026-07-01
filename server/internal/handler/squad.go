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
	Visibility    string                       `json:"visibility"`
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
	AgentName   string
	Description string
	Instruction string
	MemberRole  string
	MCPConfig   []byte
}

const sopPMRoutingRule = "调度规则：只有 pm 可以 @mention 下一阶段 Agent；每次只 @mention 一个下一阶段；收到阶段 handoff 后先判断通过、返工、推进或收口，再由 pm 发出唯一调度评论；不要先发无 mention 的重复调度评论。05-verify 通过且无阻断时，pm 必须在最终收口中把 issue 状态更新为 done，并说明运行复盘数据是否完整。"
const sopWorkerRoutingRule = "阶段路由规则：本角色不得 @mention 任何 Agent、Squad、Member 或 all，不得直接触发下一阶段；只输出本阶段结论、证据、阻断和 handoff 给 pm，由 pm 判断通过、返工、推进或收口。"
const internalSquadDefaultProvider = "codebuddy"
const internalSquadDefaultModel = "deepseek-v4-pro"

func internalSquadTemplateByKey(key string) (internalSquadTemplate, bool) {
	switch strings.TrimSpace(key) {
	case "user-center":
		mcpConfig := userCenterSOPMCPConfig()
		return internalSquadTemplate{
			Key:          "user-center-sop-flow",
			Name:         "pm",
			Description:  "唯一 SOP 流程执行小队，由 pm 按 pm -> 01-clarify -> 02-design -> 03-task-split -> 04-implement -> 05-verify 阶段链推进，并根据 issue 指定的项目、仓库和 source_context 选择对应项目 skill。",
			Instructions: "pm 按 SOP 分阶段推进；每个阶段都要记录输入、输出、失败原因、耗时和验收证据；不得跳过验收。" + sopPMRoutingRule + "目标项目、仓库、分支、TAPD/Gongfeng 真源和可用 operation skill 必须来自 issue、项目资源或 source_context，不能写死为某几个仓库。单项目需求、TAPD 正文抓取后的真实需求、以及 01-05 阶段推进，默认都在当前 issue 的评论、任务轨迹和阶段流中继续，不得为了进入下一阶段创建同项目 child issue。只有遇到明确跨项目依赖时，pm 才能先创建对应目标项目的待规划子 issue，并确认父子关系、项目和目标小队指派正确；不得只评论或委派 03 代替创建。06-archive 不属于必跑阶段。",
			Model:        internalSquadDefaultModel,
			Roles: []internalSquadRole{
				{Key: "pm", Name: "pm", AgentName: "pm", Description: "SOP 队长：读取 issue、项目资源和 source_context，识别目标项目与跨项目依赖，调度 01-05 阶段；只有跨项目协作才创建必要 child issue。", Instruction: "接收 issue 和 TAPD 输入，必须先根据 source_context 使用 mcp-server-tapd 读取 TAPD 正文；遇到 git.code.tencent.com 链接或项目资源时使用 gongfeng MCP 解析。根据 issue 指定项目、项目资源、仓库路径和可用 operation skill 决定本轮目标项目，推进 pm -> 01-clarify -> 02-design -> 03-task-split -> 04-implement -> 05-verify。" + sopPMRoutingRule + "TAPD 正文抓取后得到的真实需求仍属于当前 issue，不得复制成同项目 child issue；不得为了进入 01-clarify、02-design、03-task-split、04-implement 或 05-verify 创建 child issue。若父任务或 profile 要求创建跨项目子 issue，pm 必须本人先创建对应目标项目的 backlog 子 issue，并确认 parent、project、assignee 都正确；不得只写评论、不得等待或委派 03 创建。", MemberRole: "pm", MCPConfig: mcpConfig},
				{Key: "01-clarify", Name: "01-需求澄清", AgentName: "01-clarify", Description: "需求澄清：读取 TAPD/source_context 和项目资源，明确需求边界、验收口径、目标仓库与可用/缺失 operation skill。", Instruction: "执行目标项目的 01-clarify；先读取 source_context 中的 TAPD 正文和项目资源，产出需求边界、验收口径、适用仓库、可用/缺失 operation skill 和 handoff。" + sopWorkerRoutingRule, MemberRole: "01-clarify", MCPConfig: mcpConfig},
				{Key: "02-design", Name: "02-方案设计", AgentName: "02-design", Description: "方案设计：结合目标仓库上下文输出方案、影响面、接口/数据契约和项目 skill 调用计划。", Instruction: "执行目标项目的 02-design；需要仓库上下文时使用 gongfeng MCP 或本地仓库，产出方案、影响面、接口/数据契约、项目 skill 调用计划和 handoff。" + sopWorkerRoutingRule, MemberRole: "02-design", MCPConfig: mcpConfig},
				{Key: "03-task-split", Name: "03-任务拆分", AgentName: "03-task-split", Description: "任务拆分：识别跨项目依赖，产出目标项目列表、operation graph、缺失 skill 和 handoff。", Instruction: "执行目标项目的 03-task-split；用 TAPD/Gongfeng/项目资源上下文识别跨项目依赖，产出任务拆分、目标项目列表、operation graph、缺失 skill 和 handoff。" + sopWorkerRoutingRule, MemberRole: "03-task-split", MCPConfig: mcpConfig},
				{Key: "04-implement", Name: "04-开发", AgentName: "04-implement", Description: "代码实现：按既定边界和目标项目 operation skill 执行修改，保留实现证据，不越权扩散。", Instruction: "执行目标项目的 04-implement，按既定边界和对应项目 operation skill 实现，不越权修改无关模块；需要工蜂上下文时使用 gongfeng MCP。" + sopWorkerRoutingRule, MemberRole: "04-implement", MCPConfig: mcpConfig},
				{Key: "05-verify", Name: "05-测试", AgentName: "05-verify", Description: "测试验证：独立检查实现、测试结果、回写记录和最终 handoff，确认可验收证据。", Instruction: "执行目标项目的 05-verify，独立检查实现、测试结果和最终 handoff；核对 TAPD/Gongfeng/source_context 证据。" + sopWorkerRoutingRule, MemberRole: "05-verify", MCPConfig: mcpConfig},
			},
			Profile: map[string]any{
				"profile_key": "generic-project-sop-flow",
				"project":     "<target-project>",
				"repo":        "<target-repo-from-project-resource>",
				"mode":        "stage_chain",
				"roles": []map[string]any{
					{"key": "pm", "name": "pm", "responsibility": "接收 issue/TAPD 输入，读取项目资源和 source_context，检查阶段产物，处理阻断，推进 pm -> 01-clarify -> 02-design -> 03-task-split -> 04-implement -> 05-verify；只有 pm 可以 @mention 下一阶段 Agent；05-verify 通过后必须把 issue 状态更新为 done；单项目阶段推进必须留在当前 issue，遇到跨项目依赖时才直接创建对应目标项目的 backlog 子 issue，并确认父子关系、项目和目标小队指派正确，不能只委派 03 或写评论。"},
					{"key": "01-clarify", "name": "01-需求澄清", "responsibility": "执行目标项目的 01-clarify，明确需求边界、验收口径、目标仓库、可用/缺失 operation skill 和 handoff；不得 @mention 下一阶段或任何负责人。"},
					{"key": "02-design", "name": "02-方案设计", "responsibility": "执行目标项目的 02-design，输出方案、影响面、接口/数据契约、项目 skill 调用计划和 handoff；不得 @mention 下一阶段或任何负责人。"},
					{"key": "03-task-split", "name": "03-任务拆分", "responsibility": "执行目标项目的 03-task-split，输出任务拆分、跨项目依赖、operation graph 和 handoff；不得 @mention 下一阶段或任何负责人。"},
					{"key": "04-implement", "name": "04-开发", "responsibility": "执行目标项目的 04-implement，按边界和对应项目 operation skill 实现并保留证据；不得 @mention 下一阶段或任何负责人。"},
					{"key": "05-verify", "name": "05-测试", "responsibility": "执行目标项目的 05-verify，独立验证、总结证据和最终 handoff；不得 @mention 下一阶段或任何负责人。"},
				},
				"steps": []map[string]any{
					{"key": "pm", "name": "pm", "role_key": "pm"},
					{"key": "01-clarify", "name": "01-需求澄清", "role_key": "01-clarify", "skill": "<target-project>/01-clarify"},
					{"key": "02-design", "name": "02-方案设计", "role_key": "02-design", "skill": "<target-project>/02-design"},
					{"key": "03-task-split", "name": "03-任务拆分", "role_key": "03-task-split", "skill": "<target-project>/03-task-split"},
					{"key": "04-implement", "name": "04-开发", "role_key": "04-implement", "skill": "<target-project>/04-implement"},
					{"key": "05-verify", "name": "05-测试", "role_key": "05-verify", "skill": "<target-project>/05-verify"},
				},
				"stage_skills":     []string{"<target-project>/01-clarify", "<target-project>/02-design", "<target-project>/03-task-split", "<target-project>/04-implement", "<target-project>/05-verify"},
				"operation_skills": []string{"<target-project>/<operation-skill>"},
				"mcp_servers":      []string{"mcp-server-tapd", "gongfeng"},
				"model_policy": map[string]any{
					"默认提供方": internalSquadDefaultProvider,
					"默认模型":  internalSquadDefaultModel,
					"降级模型":  fallbackPromptEvaluationAgentModel,
					"策略说明":  "创建 pm 内置小队时可指定默认 Agent provider 和模型；留空时使用 CodeBuddy / deepseek-v4-pro。",
				},
				"source_context": map[string]any{
					"tapd":     "从 task.source_context.tapd 获取 workspace_id/resource_type/resource_id/fetch_status；状态为 blocked_missing_profile 时必须阻断并要求用户配置账号级 TAPD profile。",
					"gongfeng": "从 project_resources.gongfeng_repo 或 git.code.tencent.com 链接解析项目、仓库、分支、提交和文件上下文；需要账号级 Gongfeng profile。",
					"project":  "目标项目必须来自 issue.project、project_resources、source_context 或用户明确输入；不得假设固定三仓。",
				},
				"acceptance": []string{"阶段产物完整", "测试证据完整", "交接说明明确", "跨项目子 issue 由 PM 直接创建并可回读", "05-verify 通过后 issue 状态为 done"},
				"cross_project_policy": map[string]any{
					"creation_owner":          "pm",
					"required_initial_status": "backlog",
					"required_assignee_type":  "squad",
					"delegation_rule":         "03-task-split 只能在 child issue 已存在后细化拆分和 handoff；PM 不得用评论或等待 03 代替创建 child issue。",
					"completion_gate":         "PM 完成前必须通过公开 issue children 回读确认所有必要目标项目子 issue 都存在，且 parent_issue_id、project_id、assignee_id 与本轮拆分计划一致。",
				},
				"cross_project_child_issues": []map[string]any{
					{
						"target_project": "<target-project>",
						"trigger":        "需求影响多个项目、仓库、服务、权限、部署或联调边界时，为每个目标项目创建对应 child issue；单项目阶段推进、TAPD 正文抓取和同项目澄清设计不得触发 child issue。",
						"assignee":       "目标项目负责人或对应小队",
						"title":          "为父 issue 补充目标项目交付项",
						"body":           "说明父 issue、目标项目、仓库路径、相关 operation skill、接口/配置/数据/验证要求和期望交付物。",
					},
				},
				"archive_policy":    "06-archive 不作为必跑阶段；最终结论、证据摘要和 handoff 状态由 05-verify 输出。",
				"forbidden_actions": []string{"跳过验收直接完成", "缺少测试证据时宣称完成", "未确认目标项目就调用项目 skill", "把 06-archive 当作必跑验收阶段", "只评论或委派 03 代替 PM 创建跨项目子 issue", "把 TAPD 正文抓取后的真实需求复制成同项目 child issue", "为了进入 01-clarify/02-design/03-task-split/04-implement/05-verify 创建 child issue", "01-05 阶段 Agent @mention 下一阶段或任何负责人", "PM 一次评论 @mention 多个下一阶段", "05-verify 通过后只写验收通过但不更新 issue 状态为 done"},
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
			Model:        internalSquadDefaultModel,
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
				"model_policy": map[string]any{
					"默认提供方":    internalSquadDefaultProvider,
					"默认模型":     internalSquadDefaultModel,
					"降级模型":     fallbackPromptEvaluationAgentModel,
					"代码测试复杂审查": "Codex/gpt 类模型",
					"策略说明":     "内置小队默认使用 CodeBuddy / deepseek-v4-pro；创建时可指定当前机器已探测到的其它 Agent provider 和模型。",
				},
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

func userCenterSOPMCPConfig() []byte {
	return mustJSONBytes(map[string]any{
		"mcpServers": map[string]any{
			"mcp-server-tapd": map[string]any{
				"command": "uvx",
				"args": []string{
					"mcp-server-tapd",
					"--api-base-url=https://api.tapd.cn",
					"--tapd-base-url=https://www.tapd.cn",
					"--keep-links=true",
					"--tools-set=lookup_tapd_tool",
				},
			},
			"gongfeng": map[string]any{
				"command": "zsh",
				"args": []string{
					"-lc",
					". /root/.config/gongfeng-mcp/env && exec node /data/ida/gongfeng-mcp-server/dist/index.js",
				},
			},
		},
	})
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
		Visibility:    s.Visibility,
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

func (h *Handler) requireSquadVisible(w http.ResponseWriter, r *http.Request, squad db.Squad, workspaceID string) bool {
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if h.canUseSquad(r.Context(), squad, actorType, actorID, workspaceID) {
		return true
	}
	writeError(w, http.StatusNotFound, "squad not found")
	return false
}

func (h *Handler) requireSquadManager(w http.ResponseWriter, r *http.Request, squad db.Squad, workspaceID string) (db.Member, bool) {
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.Member{}, false
	}
	if !memberCanManageSquad(squad, member) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return db.Member{}, false
	}
	return member, true
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
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	var squads []db.Squad
	var err error
	if includeArchived {
		squads, err = h.Queries.ListAllSquads(r.Context(), wsUUID)
	} else {
		squads, err = h.Queries.ListSquads(r.Context(), wsUUID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list squads")
		return
	}

	summaries := make(map[string]*squadMemberSummary, len(squads))
	if includeArchived {
		previewRows, err := h.Queries.ListAllSquadMemberPreviewRows(r.Context(), wsUUID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list squad member preview")
			return
		}
		for _, row := range previewRows {
			squadID := uuidToString(row.SquadID)
			summary := summaries[squadID]
			if summary == nil {
				summary = &squadMemberSummary{}
				summaries[squadID] = summary
			}
			addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
		}
	} else {
		previewRows, err := h.Queries.ListSquadMemberPreviewRows(r.Context(), wsUUID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list squad member preview")
			return
		}
		for _, row := range previewRows {
			squadID := uuidToString(row.SquadID)
			summary := summaries[squadID]
			if summary == nil {
				summary = &squadMemberSummary{}
				summaries[squadID] = summary
			}
			addSquadMemberPreview(summary, row.MemberType, row.MemberID, row.Role)
		}
	}

	resp := make([]SquadResponse, 0, len(squads))
	for _, s := range squads {
		if !memberCanUseSquad(s, member) {
			continue
		}
		item := squadToResponse(s)
		applySquadMemberSummary(&item, summaries[uuidToString(s.ID)])
		resp = append(resp, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CreateSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	var req struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		LeaderID    string          `json:"leader_id"`
		AvatarURL   *string         `json:"avatar_url"`
		Visibility  string          `json:"visibility"`
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
	visibility, validVisibility := normalizeSquadVisibility(req.Visibility)
	if !validVisibility {
		writeError(w, http.StatusBadRequest, "visibility must be 'workspace' or 'personal'")
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
	leader, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          leaderUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
		return
	}
	if !h.canAccessPrivateAgent(r.Context(), leader, "member", uuidToString(member.UserID), workspaceID) {
		writeError(w, http.StatusForbidden, "cannot use private leader agent")
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
		WorkspaceID:  wsUUID,
		Name:         req.Name,
		Description:  req.Description,
		LeaderID:     leaderUUID,
		CreatorID:    member.UserID,
		AvatarUrl:    avatarURL,
		Visibility:   pgtype.Text{String: visibility, Valid: true},
		Instructions: pgtype.Text{},
		SopProfile:   sopProfile,
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
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req struct {
		TemplateKey     string `json:"template_key"`
		RuntimeProvider string `json:"runtime_provider"`
		Model           string `json:"model"`
		Visibility      string `json:"visibility"`
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
	if model := strings.TrimSpace(req.Model); model != "" {
		template.Model = model
		if policy, ok := template.Profile["model_policy"].(map[string]any); ok {
			policy["默认模型"] = model
		}
	}
	provider := normalizeProvider(req.RuntimeProvider)
	if provider == "" {
		provider = internalSquadDefaultProvider
	}
	if policy, ok := template.Profile["model_policy"].(map[string]any); ok {
		policy["默认提供方"] = provider
	}
	visibility, validVisibility := normalizeSquadVisibility(req.Visibility)
	if !validVisibility {
		writeError(w, http.StatusBadRequest, "visibility must be 'workspace' or 'personal'")
		return
	}

	runtime, ok := h.selectInternalSquadRuntime(w, r, wsUUID, member, provider)
	if !ok {
		return
	}
	agents, err := h.ensureInternalSquadAgents(r.Context(), wsUUID, member.UserID, runtime, template, visibility)
	if err != nil {
		slog.Warn("ensure internal squad agents failed", append(logger.RequestAttrs(r), "error", err, "template", template.Key)...)
		writeError(w, http.StatusInternalServerError, "failed to create internal squad agents")
		return
	}
	squad, err := h.ensureInternalSquad(r.Context(), wsUUID, member.UserID, template, visibility, agents)
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

func (h *Handler) selectInternalSquadRuntime(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, member db.Member, provider string) (db.AgentRuntime, bool) {
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return db.AgentRuntime{}, false
	}
	checkedAt := time.Now().UTC()
	provider = normalizeProvider(provider)
	if provider == "" {
		provider = internalSquadDefaultProvider
	}
	providerName := provider
	if len(providerName) > 0 {
		providerName = strings.ToUpper(providerName[:1]) + providerName[1:]
	}
	var best *db.AgentRuntime
	for i := range runtimes {
		runtime := runtimes[i]
		if !strings.EqualFold(runtime.Provider, provider) || !canUseRuntimeForAgent(member, runtime) {
			continue
		}
		if best == nil || runtimeReadinessRank(runtime, checkedAt) > runtimeReadinessRank(*best, checkedAt) {
			best = &runtime
		}
	}
	if best == nil {
		writeError(w, http.StatusServiceUnavailable, "当前 workspace 没有可用的 "+providerName+" runtime，无法创建真实可执行的内部小队。请先启动 multica daemon 并确认 /api/runtimes 出现 provider="+provider+" 的在线 runtime。")
		return db.AgentRuntime{}, false
	}
	if best.Status != "online" || !best.LastSeenAt.Valid || checkedAt.Sub(best.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		writeError(w, http.StatusServiceUnavailable, providerName+" runtime 当前未就绪，无法创建真实可执行的内部小队。请启动 daemon 并等待 runtime 心跳刷新。")
		return db.AgentRuntime{}, false
	}
	return *best, true
}

func (h *Handler) ensureInternalSquadAgents(ctx context.Context, workspaceID pgtype.UUID, ownerID pgtype.UUID, runtime db.AgentRuntime, template internalSquadTemplate, squadVisibility string) ([]InternalSquadAgent, error) {
	existing, err := h.Queries.ListAllAgents(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]InternalSquadAgent, 0, len(template.Roles))
	agentVisibility := internalSquadAgentVisibility(squadVisibility)
	for _, role := range template.Roles {
		name := strings.TrimSpace(role.AgentName)
		if name == "" {
			name = template.Name + " · " + role.Name
		}
		runtimeConfig := internalSquadAgentRuntimeConfig(runtime, template, role, squadVisibility, agentVisibility, ownerID)
		instructions := "你是" + template.Name + "小队的" + role.Name + "。" + role.Instruction + "所有输出必须使用中文，并保留可验收证据。"
		description := internalSquadRoleDescription(template, role)
		model := pgtype.Text{String: template.Model, Valid: template.Model != ""}
		agentRow, ok := findInternalSquadAgent(existing, name, template, role, squadVisibility, agentVisibility, ownerID)
		if !ok {
			agentRow, err = h.Queries.CreateAgent(ctx, db.CreateAgentParams{
				WorkspaceID:        workspaceID,
				Name:               name,
				Description:        description,
				Instructions:       instructions,
				RuntimeMode:        runtime.RuntimeMode,
				RuntimeConfig:      runtimeConfig,
				RuntimeID:          runtime.ID,
				Visibility:         agentVisibility,
				MaxConcurrentTasks: 2,
				OwnerID:            ownerID,
				CustomEnv:          []byte("{}"),
				CustomArgs:         []byte("[]"),
				McpConfig:          role.MCPConfig,
				Model:              model,
			})
			if err != nil {
				return nil, err
			}
		} else {
			if agentRow.ArchivedAt.Valid {
				agentRow, err = h.Queries.RestoreAgent(ctx, agentRow.ID)
				if err != nil {
					return nil, err
				}
			}
			if internalSquadAgentNeedsSync(agentRow, runtime, template, role, runtimeConfig, instructions, description, model, agentVisibility) {
				agentRow, err = h.Queries.UpdateAgent(ctx, db.UpdateAgentParams{
					ID:                 agentRow.ID,
					Description:        pgtype.Text{String: description, Valid: true},
					RuntimeConfig:      runtimeConfig,
					RuntimeMode:        pgtype.Text{String: runtime.RuntimeMode, Valid: true},
					RuntimeID:          runtime.ID,
					Visibility:         pgtype.Text{String: agentVisibility, Valid: true},
					MaxConcurrentTasks: pgtype.Int4{Int32: 2, Valid: true},
					Instructions:       pgtype.Text{String: instructions, Valid: true},
					CustomArgs:         []byte("[]"),
					McpConfig:          role.MCPConfig,
					Model:              model,
				})
				if err != nil {
					return nil, err
				}
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

func internalSquadAgentVisibility(squadVisibility string) string {
	if squadVisibility == squadVisibilityPersonal {
		return "private"
	}
	return "workspace"
}

func internalSquadAgentRuntimeConfig(runtime db.AgentRuntime, template internalSquadTemplate, role internalSquadRole, squadVisibility string, agentVisibility string, ownerID pgtype.UUID) []byte {
	scopeOwnerID := ""
	if squadVisibility == squadVisibilityPersonal {
		scopeOwnerID = uuidToString(ownerID)
	}
	return mustJSONBytes(map[string]any{
		"provider": runtime.Provider,
		"用途":       template.Name,
		"角色":       role.Name,
		"模板":       template.Key,
		"internal_squad": map[string]any{
			"template_key":     template.Key,
			"role_key":         role.Key,
			"squad_visibility": squadVisibility,
			"agent_visibility": agentVisibility,
			"owner_id":         scopeOwnerID,
		},
	})
}

func findInternalSquadAgent(agents []db.Agent, name string, template internalSquadTemplate, role internalSquadRole, squadVisibility string, agentVisibility string, ownerID pgtype.UUID) (db.Agent, bool) {
	var archivedMatch db.Agent
	for _, agent := range agents {
		if !matchesInternalSquadAgent(agent, name, template, role, squadVisibility, agentVisibility, ownerID) {
			continue
		}
		if !agent.ArchivedAt.Valid {
			return agent, true
		}
		if uuidToString(archivedMatch.ID) == "" || agent.UpdatedAt.Time.After(archivedMatch.UpdatedAt.Time) {
			archivedMatch = agent
		}
	}
	if uuidToString(archivedMatch.ID) != "" {
		return archivedMatch, true
	}
	return db.Agent{}, false
}

func matchesInternalSquadAgent(agent db.Agent, name string, template internalSquadTemplate, role internalSquadRole, squadVisibility string, agentVisibility string, ownerID pgtype.UUID) bool {
	if agent.Name != name || agent.Visibility != agentVisibility {
		return false
	}
	if squadVisibility == squadVisibilityPersonal && uuidToString(agent.OwnerID) != uuidToString(ownerID) {
		return false
	}
	var runtimeConfig map[string]any
	if len(bytes.TrimSpace(agent.RuntimeConfig)) == 0 || json.Unmarshal(agent.RuntimeConfig, &runtimeConfig) != nil {
		return false
	}
	if scope, ok := runtimeConfig["internal_squad"].(map[string]any); ok {
		if stringFromAny(scope["template_key"]) != template.Key ||
			stringFromAny(scope["role_key"]) != role.Key ||
			stringFromAny(scope["squad_visibility"]) != squadVisibility ||
			stringFromAny(scope["agent_visibility"]) != agentVisibility {
			return false
		}
		if squadVisibility == squadVisibilityPersonal && stringFromAny(scope["owner_id"]) != uuidToString(ownerID) {
			return false
		}
		return true
	}
	return stringFromAny(runtimeConfig["模板"]) == template.Key && stringFromAny(runtimeConfig["角色"]) == role.Name
}

func internalSquadAgentNeedsSync(agent db.Agent, runtime db.AgentRuntime, template internalSquadTemplate, role internalSquadRole, runtimeConfig []byte, instructions string, description string, model pgtype.Text, visibility string) bool {
	if agent.Description != description ||
		agent.RuntimeMode != runtime.RuntimeMode ||
		uuidToString(agent.RuntimeID) != uuidToString(runtime.ID) ||
		agent.Visibility != visibility ||
		agent.MaxConcurrentTasks != 2 ||
		agent.Instructions != instructions ||
		!bytes.Equal(bytes.TrimSpace(agent.RuntimeConfig), bytes.TrimSpace(runtimeConfig)) ||
		!bytes.Equal(bytes.TrimSpace(agent.CustomArgs), []byte("[]")) ||
		!bytes.Equal(bytes.TrimSpace(agent.McpConfig), bytes.TrimSpace(role.MCPConfig)) {
		return true
	}
	if model.Valid != agent.Model.Valid {
		return true
	}
	if model.Valid && model.String != agent.Model.String {
		return true
	}
	return false
}

func internalSquadRoleDescription(template internalSquadTemplate, role internalSquadRole) string {
	if strings.TrimSpace(role.Description) != "" {
		return strings.TrimSpace(role.Description)
	}
	return template.Description
}

func (h *Handler) ensureInternalSquad(ctx context.Context, workspaceID pgtype.UUID, creatorID pgtype.UUID, template internalSquadTemplate, visibility string, agents []InternalSquadAgent) (db.Squad, error) {
	if len(agents) == 0 {
		return db.Squad{}, pgx.ErrNoRows
	}
	squads, err := h.Queries.ListAllSquads(ctx, workspaceID)
	if err != nil {
		return db.Squad{}, err
	}
	var squad db.Squad
	var archivedMatch db.Squad
	for _, item := range squads {
		if !matchesInternalSquadTemplate(item, template, visibility, creatorID) {
			continue
		}
		if !item.ArchivedAt.Valid {
			squad = item
			break
		}
		if uuidToString(archivedMatch.ID) == "" || item.UpdatedAt.Time.After(archivedMatch.UpdatedAt.Time) {
			archivedMatch = item
		}
	}
	if uuidToString(squad.ID) == "" {
		if uuidToString(archivedMatch.ID) != "" {
			squad, err = h.Queries.RestoreSquad(ctx, archivedMatch.ID)
			if err != nil {
				return db.Squad{}, err
			}
		} else {
			sopProfile := mustJSONBytes(template.Profile)
			squad, err = h.Queries.CreateSquad(ctx, db.CreateSquadParams{
				WorkspaceID:  workspaceID,
				Name:         template.Name,
				Description:  template.Description,
				LeaderID:     parseUUID(agents[0].ID),
				CreatorID:    creatorID,
				Visibility:   pgtype.Text{String: visibility, Valid: true},
				Instructions: pgtype.Text{String: template.Instructions, Valid: true},
				SopProfile:   sopProfile,
			})
			if err != nil {
				return db.Squad{}, err
			}
		}
	}
	if uuidToString(squad.ID) != "" {
		profileBytes := mustJSONBytes(template.Profile)
		leaderID := parseUUID(agents[0].ID)
		if itemNeedsInternalSquadSync(squad, template, profileBytes, leaderID, visibility) {
			params := db.UpdateSquadParams{
				ID:           squad.ID,
				Name:         pgtype.Text{String: template.Name, Valid: squad.Name != template.Name},
				Description:  pgtype.Text{String: template.Description, Valid: true},
				LeaderID:     leaderID,
				Visibility:   pgtype.Text{String: visibility, Valid: true},
				Instructions: pgtype.Text{String: template.Instructions, Valid: true},
				SopProfile:   profileBytes,
			}
			squad, err = h.Queries.UpdateSquad(ctx, params)
			if err != nil {
				return db.Squad{}, err
			}
		}
	}
	existingMembers, err := h.Queries.ListSquadMembers(ctx, squad.ID)
	if err != nil {
		return db.Squad{}, err
	}
	desiredAgentMembers := map[string]struct{}{}
	for _, agent := range agents {
		desiredAgentMembers[agent.ID] = struct{}{}
	}
	existingMemberRoles := map[string]string{}
	for _, member := range existingMembers {
		memberID := uuidToString(member.MemberID)
		if member.MemberType == "agent" {
			if _, keep := desiredAgentMembers[memberID]; !keep {
				if _, err := h.Queries.RemoveSquadMember(ctx, db.RemoveSquadMemberParams{
					SquadID:    squad.ID,
					MemberType: member.MemberType,
					MemberID:   member.MemberID,
				}); err != nil {
					return db.Squad{}, err
				}
				continue
			}
		}
		existingMemberRoles[member.MemberType+":"+memberID] = member.Role
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

func matchesInternalSquadTemplate(squad db.Squad, template internalSquadTemplate, visibility string, creatorID pgtype.UUID) bool {
	profile := decodeJSONDefault(squad.SopProfile, map[string]any{})
	profileMap, _ := profile.(map[string]any)
	sameTemplate := squad.Name == template.Name || stringFromAny(profileMap["profile_key"]) == template.Key
	sameVisibility := squad.Visibility == visibility
	sameCreator := visibility != squadVisibilityPersonal || uuidToString(squad.CreatorID) == uuidToString(creatorID)
	return sameTemplate && sameVisibility && sameCreator
}

func itemNeedsInternalSquadSync(squad db.Squad, template internalSquadTemplate, profileBytes []byte, leaderID pgtype.UUID, visibility string) bool {
	return squad.Name != template.Name ||
		squad.Description != template.Description ||
		uuidToString(squad.LeaderID) != uuidToString(leaderID) ||
		squad.Visibility != visibility ||
		!bytes.Equal(bytes.TrimSpace(squad.SopProfile), bytes.TrimSpace(profileBytes)) ||
		squad.Instructions != template.Instructions
}

func (h *Handler) GetSquad(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !h.requireSquadVisible(w, r, squad, workspaceID) {
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
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	member, ok := h.requireSquadManager(w, r, squad, workspaceID)
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
		Visibility   *string         `json:"visibility"`
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
	if req.Visibility != nil {
		visibility, validVisibility := normalizeSquadVisibility(*req.Visibility)
		if !validVisibility {
			writeError(w, http.StatusBadRequest, "visibility must be 'workspace' or 'personal'")
			return
		}
		params.Visibility = pgtype.Text{String: visibility, Valid: true}
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
		leader, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: lid, WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "leader must be a valid agent in this workspace")
			return
		}
		if !h.canAccessPrivateAgent(r.Context(), leader, "member", uuidToString(member.UserID), workspaceID) {
			writeError(w, http.StatusForbidden, "cannot use private leader agent")
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
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireSquadManager(w, r, squad, workspaceID); !ok {
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

func (h *Handler) RestoreSquad(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireSquadManager(w, r, squad, workspaceID); !ok {
		return
	}
	if !squad.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "squad is not archived")
		return
	}

	restored, err := h.Queries.RestoreSquad(r.Context(), squad.ID)
	if err != nil {
		slog.Warn("restore squad failed", append(logger.RequestAttrs(r), "error", err, "squad_id", uuidToString(squad.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to restore squad")
		return
	}
	resp, err := h.squadToResponseWithPreview(r.Context(), restored)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad member preview")
		return
	}
	userID := requestUserID(r)
	h.publish(protocol.EventSquadRestored, workspaceID, "member", userID, map[string]any{"squad": resp})
	writeJSON(w, http.StatusOK, resp)
}

// ── Squad Members ───────────────────────────────────────────────────────────

func (h *Handler) ListSquadMembers(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !h.requireSquadVisible(w, r, squad, workspaceID) {
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
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if !h.requireSquadVisible(w, r, squad, workspaceID) {
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
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	member, ok := h.requireSquadManager(w, r, squad, workspaceID)
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
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: memberUUID, WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "agent not found in this workspace")
			return
		}
		if !h.canAccessPrivateAgent(r.Context(), agent, "member", uuidToString(member.UserID), workspaceID) {
			writeError(w, http.StatusForbidden, "cannot add private agent")
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
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireSquadManager(w, r, squad, workspaceID); !ok {
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
	squad, _, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireSquadManager(w, r, squad, workspaceID); !ok {
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
