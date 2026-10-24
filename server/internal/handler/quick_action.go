// 本文件实现「快捷动作」（Quick Action，MUL-5465）：workspace 级的预设
// 「@ 谁 + 说什么」。用户把一个 agent/squad、一段 prompt 和可见范围存成一条
// 快捷动作，之后对任意 issue 点一下就能触发。
//
// 最核心的约定：运行一条快捷动作【不是】一条新的派发路径。它只负责把 prompt
// 渲染成一条带目标 mention 标记的普通评论（quick_action_id 标记来源），然后交给
// 既有的评论触发链路 triggerTasksForComment 处理——权限、归属、squad 路由、
// 执行日志、pending 任务合并全部沿用评论路径。这样避免再次出现 MUL-3375 里
// 「同一套触发决策被复制四份、各改各的」的教训。
//
// 权限模型：可见性（public/private）只是作者声明的意图；真正能否触发由运行时
// 唯一的权限闸门 RunQuickAction 裁决（public 动作绑定的目标构造上就保证人人可触发，
// 但目标之后变私有会怎样，由闸门说了算）。
//
// 正文语义：prompt 原样发出，不做任何插值；{{...}} 模板语法与会@到其他人的
// mention 在写入时就拒绝。
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 议题快捷动作（MUL-5465）：workspace 级预设的「对谁、说什么」。
//
// 契约要点：
//   - 运行一条动作不是新的派发路径。它把 prompt 渲染好、作为一条普通评论发出
//     （携带目标的 mention 标记并标注 quick_action_id），然后交给 triggerTasksForComment。
//     权限、归属、squad 路由、执行日志与 pending 任务合并都从评论路径继承，
//     而不是重新实现——这正是 MUL-3375 的教训：同一份触发决策被复制成四份、各改各的。
//   - 权限只在唯一一处检查：RunQuickAction。列表端点不做任何权限工作，除了
//     `private` 归属外什么都不隐藏。曾试过按触发权限过滤侧边栏，后来移除了：
//     那样会让看同一 issue 的两个人看到不同的侧边栏且毫无解释，比一个会说明
//     拒绝原因的按钮更难排查。
//   - `visibility` 是作者的意图声明，不是鉴权决定。`public` 动作必须绑定所有
//     成员都能触发的目标，所以构造上就能跑；运行时闸门仍有最终决定权——目标
//     事后变私有，会在点击时大声失败，而不是静默地放行任何东西。
//   - prompt 原样发送，不做插值、没有运行时输入：凡是被考虑过的变量
//     （{{issue.title}}、{{issue.identifier}}、{{issue.url}}、{{user.name}}、{{date}}）
//     都是 agent 从 issue 上下文和评论作者身上已经拿得到的；「同一动作、改一处细节」
//     由 `/` 斜杠命令覆盖——它把渲染好的正文放进输入框让用户编辑。写入时仍会
//     拒绝任何 `{{...}}`，免得习惯性模板 token 被逐字送进 agent 的指令里。
const (
	maxActiveQuickActionsPerWorkspace = 30
	maxQuickActionNameLen             = 32
	maxQuickActionDescriptionLen      = 200
	maxQuickActionPromptLen           = 4000
)

// quickActionTemplateTokenRe 匹配 prompt 里任何 `{{...}}`。
//
// 模板语法是刻意不支持的，但「不解释」不等于「安全」：从 autopilot 议题标题模板
// 带着习惯过来的人，可能把 {{issue.title}} 原样写进 agent 的指令里还不自知。
// 写入时拒绝它，代价只是插值引擎的零头，还保留了以后开放变量而不动存量数据的
// 可能性。
var quickActionTemplateTokenRe = regexp.MustCompile(`\{\{[^}]*\}\}`)

// quickActionSideEffectMentionRe 匹配会「@ 到人」的 mention 标记。
//
// prompt 会被原样拼进一条随后走正常 mention 管线的评论，所以 prompt 里的 mention
// 会在每次点击时生效：
//
//   - agent / squad / all -> 会在配置的目标之外再入队第二个目标，破坏权限模型依赖
//     的不变量（public 动作只运行它校验过的那个目标）。侧边栏只上报第一个结果，
//     多跑的那次完全不可见。
//   - member -> notification_listeners.go 会把他加进 mention 收件人并建 inbox 项，
//     于是一条保存的 prompt 每次点击都会 @ 到这个人。最初以「只是渲染一个链接」
//     为由放行过 member，那是错的。
//
// `mention://issue/...` 是唯一例外：它只渲染成链接、不触达任何人，所以 prompt 里
// 合法地指向相关工作。
var quickActionSideEffectMentionRe = regexp.MustCompile(`mention://(agent|squad|member|all)/`)

// ---------------------------------------------------------------------------
// 类型定义
// ---------------------------------------------------------------------------

// QuickActionResponse 是快捷动作接口返回的 JSON 结构，除了存储列之外，还带上
// 解析出的目标展示信息（TargetName / TargetPublic / TargetMissing）。
type QuickActionResponse struct {
	ID           string  `json:"id"`
	WorkspaceID  string  `json:"workspace_id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	AssigneeType string  `json:"assignee_type"`
	AssigneeID   string  `json:"assignee_id"`
	Prompt       string  `json:"prompt"`
	Visibility   string  `json:"visibility"`
	Status       string  `json:"status"`
	LastUsedAt   *string `json:"last_used_at"`
	UseCount     int64   `json:"use_count"`
	CreatedByID  string  `json:"created_by_id"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`

	// TargetName 是绑定的执行目标名字，供展示用。目标能解析到时一定带值：
	// `public` 动作绑的是人人可见可触发的公开 agent；`private` 动作只会返回给
	// 创建者本人——所以没有需要打码的内容。
	TargetName string `json:"target_name,omitempty"`
	// TargetPublic 表示绑定的目标当前是否所有成员都能触发。这只是普通元数据，
	// 不是鉴权结论：设置页把它展示在绑定旁边，让「public 动作指向了一个现在
	// 已经私有化的 agent」一眼可见，而不必再造一个专门的「坏了」状态。
	TargetPublic bool `json:"target_public"`
	// TargetMissing 标记目标已被归档或删除。
	TargetMissing bool `json:"target_missing"`
}

// CreateQuickActionRequest 是创建快捷动作的请求体。
type CreateQuickActionRequest struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	AssigneeType string `json:"assignee_type"`
	AssigneeID   string `json:"assignee_id"`
	Prompt       string `json:"prompt"`
	Visibility   string `json:"visibility"`
}

// UpdateQuickActionRequest 是更新快捷动作的请求体。指针字段区分「未传（保持原值）」
// 与「显式传零值/空串」。
type UpdateQuickActionRequest struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	AssigneeType *string `json:"assignee_type"`
	AssigneeID   *string `json:"assignee_id"`
	Prompt       *string `json:"prompt"`
	Visibility   *string `json:"visibility"`
	Status       *string `json:"status"`
}

// ListQuickActionsResponse 是列表接口的响应。
type ListQuickActionsResponse struct {
	QuickActions []QuickActionResponse `json:"quick_actions"`
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

// validateQuickActionName 校验并规范化动作名：必填、去首尾空白、长度（按字符计）
// 不超过上限。
func validateQuickActionName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(name) > maxQuickActionNameLen {
		return "", fmt.Errorf("name must be at most %d characters", maxQuickActionNameLen)
	}
	return name, nil
}

// validateQuickActionPrompt 对 prompt 做去空白、长度限制，并拒绝模板语法与
// 会@到旁人的 mention。模板语法为何即使暂无此功能也要拒绝，见
// quickActionTemplateTokenRe。
func validateQuickActionPrompt(raw string) (string, error) {
	prompt := strings.TrimSpace(raw)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	if utf8.RuneCountInString(prompt) > maxQuickActionPromptLen {
		return "", fmt.Errorf("prompt must be at most %d characters", maxQuickActionPromptLen)
	}
	if token := quickActionTemplateTokenRe.FindString(prompt); token != "" {
		return "", fmt.Errorf("template variables are not supported yet; remove %s — the agent already reads this issue", token)
	}
	if quickActionSideEffectMentionRe.MatchString(prompt) {
		return "", fmt.Errorf("the prompt cannot @mention an agent, squad, or person; a quick action reaches exactly the one target it is bound to (an issue link is fine)")
	}
	return prompt, nil
}

// validateQuickActionAssignee 校验目标类型与目标 ID：类型只能是 agent 或 squad，
// ID 必填。
func validateQuickActionAssignee(assigneeType, assigneeID string) error {
	if assigneeType != "agent" && assigneeType != "squad" {
		return fmt.Errorf("assignee_type must be \"agent\" or \"squad\"")
	}
	if strings.TrimSpace(assigneeID) == "" {
		return fmt.Errorf("assignee_id is required")
	}
	return nil
}

// normalizeQuickActionVisibility 规范化可见性：缺省视为 public，只接受
// public / private 两个值。
func normalizeQuickActionVisibility(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "public", nil
	}
	if v != "public" && v != "private" {
		return "", fmt.Errorf("visibility must be \"public\" or \"private\"")
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// 目标解析
// ---------------------------------------------------------------------------

// quickActionTarget 是一条动作解析后的执行目标：实际会跑的 agent，加上客户端
// 该展示的身份。squad 绑定时 Agent 取的是 squad 队长、Name 取的是 SQUAD 的名字，
// 因为用户绑定并 @ 的对象是 squad 本身。
type quickActionTarget struct {
	Agent     db.Agent
	Name      string
	AvatarURL string
	// MentionType 是渲染出的评论必须使用的 mention:// 协议类型，让既有触发链路
	// 把它精确路由成手打 mention。
	MentionType string
	// MentionID 是 mention 链接里的 id：agent 绑定时是 agent id；squad 绑定时是
	// SQUAD id（触发链路会自行解析出队长）。
	MentionID string
	Found     bool
}

// resolveQuickActionTarget 加载执行目标。目标 agent 或 squad 缺失/被归档时解析为
// Found=false——调用方把它渲染成「目标不可用」，而不是让整个列表请求失败。
func (h *Handler) resolveQuickActionTarget(ctx context.Context, qa db.QuickAction) quickActionTarget {
	switch qa.AssigneeType {
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          qa.AssigneeID,
			WorkspaceID: qa.WorkspaceID,
		})
		if err != nil || squad.ArchivedAt.Valid {
			return quickActionTarget{}
		}
		leader, err := h.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil || leader.ArchivedAt.Valid {
			return quickActionTarget{}
		}
		avatar := ""
		if squad.AvatarUrl.Valid {
			avatar = squad.AvatarUrl.String
		}
		return quickActionTarget{
			Agent:       leader,
			Name:        squad.Name,
			AvatarURL:   avatar,
			MentionType: "squad",
			MentionID:   uuidToString(squad.ID),
			Found:       true,
		}
	default:
		agent, err := h.Queries.GetAgent(ctx, qa.AssigneeID)
		if err != nil || agent.ArchivedAt.Valid || uuidToString(agent.WorkspaceID) != uuidToString(qa.WorkspaceID) {
			return quickActionTarget{}
		}
		avatar := ""
		if agent.AvatarUrl.Valid {
			avatar = agent.AvatarUrl.String
		}
		return quickActionTarget{
			Agent:       agent,
			Name:        agent.Name,
			AvatarURL:   avatar,
			MentionType: "agent",
			MentionID:   uuidToString(agent.ID),
			Found:       true,
		}
	}
}

// agentInvocableByEveryone 判断该 agent 是否每个 workspace 成员都能触发——
// 即 permission_mode=public_to 且 public_to 的名单里有 workspace 目标。
//
// public_to 名单里只点名了部分成员的 agent 不算数：把它绑到 public 快捷动作上，
// 会让被排除在外的成员的「人人可用」承诺落空。查询出错时失败关闭（返回 false）。
func (h *Handler) agentInvocableByEveryone(ctx context.Context, agent db.Agent) bool {
	if agent.PermissionMode != "public_to" {
		return false
	}
	targets, err := h.Queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false
	}
	for _, t := range targets {
		if t.TargetType == "workspace" {
			return true
		}
	}
	return false
}

// quickActionCatalog 是列表端点用的批量解析器。
//
// 单行路径对每条动作都要发一次 GetAgent（加 GetSquad）和一次
// ListAgentInvocationTargets。按 30 条动作上限算，每次侧边栏与设置页加载要串行
// 打 60~90 次库。这里改成三条查询把 workspace 的 agents、squads、invocation
// targets 全部读进内存再作答。workspace 是小团队规模，全量列 agent 比三十次
// 点查便宜得多。
type quickActionCatalog struct {
	agents map[string]db.Agent
	squads map[string]db.Squad
	// publicAgents 记录「每个 workspace 成员都能触发」的 agent id。
	publicAgents map[string]bool
}

// loadQuickActionCatalog 用三条查询把 workspace 的 agents、squads 与「公开可触发」
// 目标集合装载进内存。
func (h *Handler) loadQuickActionCatalog(ctx context.Context, workspaceID pgtype.UUID) quickActionCatalog {
	cat := quickActionCatalog{
		agents:       map[string]db.Agent{},
		squads:       map[string]db.Squad{},
		publicAgents: map[string]bool{},
	}

	agents, err := h.Queries.ListAgents(ctx, workspaceID)
	if err != nil {
		// 降级成「目标解析不出来」，而不是让整个列表失败：目录只是展示用元数据，
		// 运行路径反正会重新校验一切真正要紧的东西。
		return cat
	}
	agentIDs := make([]pgtype.UUID, 0, len(agents))
	for _, a := range agents {
		cat.agents[uuidToString(a.ID)] = a
		agentIDs = append(agentIDs, a.ID)
	}

	if squads, err := h.Queries.ListSquads(ctx, workspaceID); err == nil {
		for _, sq := range squads {
			cat.squads[uuidToString(sq.ID)] = sq
		}
	}

	if len(agentIDs) > 0 {
		targets, err := h.Queries.ListAgentInvocationTargetsByAgentIDs(ctx, agentIDs)
		if err == nil {
			for _, t := range targets {
				if t.TargetType == "workspace" {
					cat.publicAgents[uuidToString(t.AgentID)] = true
				}
			}
		}
	}
	return cat
}

// resolve 镜像 resolveQuickActionTarget + agentInvocableByEveryone 的判断，但完全
// 从预加载的 map 作答。两条路径必须结论一致；共用 quickActionTarget 这个形状就是
// 防止它们跑偏的约束。
func (c quickActionCatalog) resolve(qa db.QuickAction) (quickActionTarget, bool) {
	if qa.AssigneeType == "squad" {
		squad, ok := c.squads[uuidToString(qa.AssigneeID)]
		if !ok || squad.ArchivedAt.Valid {
			return quickActionTarget{}, false
		}
		leader, ok := c.agents[uuidToString(squad.LeaderID)]
		if !ok {
			return quickActionTarget{}, false
		}
		name := squad.Name
		return quickActionTarget{Agent: leader, Name: name, Found: true},
			c.publicAgents[uuidToString(leader.ID)] && leader.PermissionMode == "public_to"
	}
	agent, ok := c.agents[uuidToString(qa.AssigneeID)]
	if !ok {
		return quickActionTarget{}, false
	}
	return quickActionTarget{Agent: agent, Name: agent.Name, Found: true},
		c.publicAgents[uuidToString(agent.ID)] && agent.PermissionMode == "public_to"
}

// quickActionToResponseFrom 基于批量目录组装一行的响应。
func quickActionToResponseFrom(qa db.QuickAction, cat quickActionCatalog) QuickActionResponse {
	resp := baseQuickActionResponse(qa)
	target, targetPublic := cat.resolve(qa)
	resp.TargetMissing = !target.Found
	if target.Found {
		resp.TargetName = target.Name
		resp.TargetPublic = targetPublic
	}
	return resp
}

// baseQuickActionResponse 直接映射存储列。目标元数据（Target* 字段）由调用方所用
// 的解析器另行叠加上去。
func baseQuickActionResponse(qa db.QuickAction) QuickActionResponse {
	return QuickActionResponse{
		ID:           uuidToString(qa.ID),
		WorkspaceID:  uuidToString(qa.WorkspaceID),
		Name:         qa.Name,
		Description:  qa.Description,
		AssigneeType: qa.AssigneeType,
		AssigneeID:   uuidToString(qa.AssigneeID),
		Prompt:       qa.Prompt,
		Visibility:   qa.Visibility,
		Status:       qa.Status,
		LastUsedAt:   timestampToPtr(qa.LastUsedAt),
		UseCount:     qa.UseCount,
		CreatedByID:  uuidToString(qa.CreatedByID),
		CreatedAt:    timestampToString(qa.CreatedAt),
		UpdatedAt:    timestampToString(qa.UpdatedAt),
	}
}

// quickActionToResponse 是单行路径，供 create/update 使用——它们只返回恰好一条
// 动作，批量目录反而浪费。列表端点改用 quickActionToResponseFrom。
func (h *Handler) quickActionToResponse(ctx context.Context, qa db.QuickAction) QuickActionResponse {
	resp := baseQuickActionResponse(qa)
	target := h.resolveQuickActionTarget(ctx, qa)
	resp.TargetMissing = !target.Found
	if target.Found {
		resp.TargetName = target.Name
		resp.TargetPublic = h.agentInvocableByEveryone(ctx, target.Agent)
	}
	return resp
}

// ---------------------------------------------------------------------------
// 管理闸门
// ---------------------------------------------------------------------------

// requireQuickActionActor 解析出「操作人」。agent 被拒绝：agent 继承其 runtime
// 所有者的凭据，没有这道闸，管理员的 agent 就能批量制造动作、再出现在所有人的
// 侧边栏里。
//
// 角色检查刻意不放在这里——它取决于将要写入的 visibility：`public` 动作是
// workspace 的公共设施，需要 owner/admin；`private` 动作是私人物品，任何成员都
// 可以创建。
func (h *Handler) requireQuickActionActor(w http.ResponseWriter, r *http.Request) (workspaceID, userID string, ok bool) {
	workspaceID = h.resolveWorkspaceID(r)
	userID, ok = requireUserID(w, r)
	if !ok {
		return "", "", false
	}
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot manage quick actions")
		return "", "", false
	}
	if _, memberOK := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !memberOK {
		return "", "", false
	}
	return workspaceID, userID, true
}

// loadReachableQuickAction 取一条动作并执行「谁能碰 private 动作」的唯一规则：它只
// 属于创建者，无论别人怎么打听到它的 id 都不行。
//
// 之所以单独抽出来，是因为这条检查以前内联在 RunQuickAction 里。Update 和 Render
// 只按 (id, workspace) 取行，于是任何成员只要拿到 UUID——一条动作创建的评论就携带
// 一个——就能改写或读回别人的 private prompt。集中到这里后，新端点不可能因为
// 「忘了加」而漏掉这条规则。
//
// 非本人访问 private 动作时报 404 而非 403：某个 UUID 是否存在，本身就不该是调用方
// 有权知道的事。
func (h *Handler) loadReachableQuickAction(
	w http.ResponseWriter,
	r *http.Request,
	id, workspaceID pgtype.UUID,
	userID string,
) (db.QuickAction, bool) {
	qa, err := h.Queries.GetQuickAction(r.Context(), db.GetQuickActionParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "quick action not found")
		return db.QuickAction{}, false
	}
	if qa.Visibility == "private" && uuidToString(qa.CreatedByID) != userID {
		writeError(w, http.StatusNotFound, "quick action not found")
		return db.QuickAction{}, false
	}
	return qa, true
}

// requirePublicQuickActionRole 给会把动作写成 `public` 的写操作把关，要求 owner 或
// admin 角色。返回 false 时错误响应已写出。
func (h *Handler) requirePublicQuickActionRole(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	_, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	return ok
}

// ---------------------------------------------------------------------------
// 端点
// ---------------------------------------------------------------------------

// ListQuickActions 返回 workspace 的快捷动作目录。它不做任何权限过滤：`private`
// 行由查询限定到创建者本人（这就是该字段的含义，不是权限检查），其余行对所有人
// 可见。能不能「运行」某条由 RunQuickAction 回答。
func (h *Handler) ListQuickActions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	viewerUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	includeArchived := r.URL.Query().Get("include_archived") == "true"
	rows, err := h.Queries.ListQuickActions(r.Context(), db.ListQuickActionsParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: includeArchived,
		ViewerID:        viewerUUID,
	})
	if err != nil {
		slog.Warn("list quick actions failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list quick actions")
		return
	}

	catalog := h.loadQuickActionCatalog(r.Context(), wsUUID)
	out := make([]QuickActionResponse, 0, len(rows))
	for _, qa := range rows {
		out = append(out, quickActionToResponseFrom(qa, catalog))
	}
	writeJSON(w, http.StatusOK, ListQuickActionsResponse{QuickActions: out})
}

// CreateQuickAction 创建一条快捷动作：先解析操作人与可见性（public 需要角色），
// 逐项校验名称/目标/prompt/描述，再校验绑定（public 动作必须绑人人可触发的目标），
// 最后检查 workspace 内 active 动作数量上限后落库。
func (h *Handler) CreateQuickAction(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireQuickActionActor(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	var req CreateQuickActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	visibility, err := normalizeQuickActionVisibility(req.Visibility)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if visibility == "public" && !h.requirePublicQuickActionRole(w, r, workspaceID) {
		return
	}
	name, err := validateQuickActionName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateQuickActionAssignee(req.AssigneeType, req.AssigneeID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	assigneeUUID, ok := parseUUIDOrBadRequest(w, req.AssigneeID, "assignee_id")
	if !ok {
		return
	}
	prompt, err := validateQuickActionPrompt(req.Prompt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	description, ok := trimmedWithinLimit(w, req.Description, maxQuickActionDescriptionLen, "description")
	if !ok {
		return
	}
	if !h.validateQuickActionBinding(w, r, req.AssigneeType, assigneeUUID, wsUUID, visibility) {
		return
	}

	count, err := h.Queries.CountActiveQuickActions(r.Context(), wsUUID)
	if err == nil && count >= maxActiveQuickActionsPerWorkspace {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("a workspace can have at most %d active quick actions; archive one first", maxActiveQuickActionsPerWorkspace))
		return
	}

	qa, err := h.Queries.CreateQuickAction(r.Context(), db.CreateQuickActionParams{
		WorkspaceID:   wsUUID,
		Name:          name,
		Description:   description,
		AssigneeType:  req.AssigneeType,
		AssigneeID:    assigneeUUID,
		Prompt:        prompt,
		Visibility:    visibility,
		CreatedByType: "member",
		CreatedByID:   parseUUID(userID),
	})
	if err != nil {
		slog.Warn("create quick action failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create quick action")
		return
	}
	writeJSON(w, http.StatusCreated, h.quickActionToResponse(r.Context(), qa))
}

// UpdateQuickAction 更新一条快捷动作。目标类型与目标 ID 成对替换（不会出现换了
// 类型却残留旧 id），绑定按「更新后的可见性」重新校验；涉及 public 的写操作需要
// owner/admin 角色。
func (h *Handler) UpdateQuickAction(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireQuickActionActor(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "quick action id")
	if !ok {
		return
	}
	existing, ok := h.loadReachableQuickAction(w, r, idUUID, wsUUID, userID)
	if !ok {
		return
	}

	var req UpdateQuickActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 需要什么角色由「更新后的可见性」决定，所以先定它。编辑一条已经是 public 的
	// 动作同样要角色——否则任何成员都能改 workspace 级按钮背后的 prompt。
	visibility := existing.Visibility
	if req.Visibility != nil {
		normalized, err := normalizeQuickActionVisibility(*req.Visibility)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		visibility = normalized
	}
	if (visibility == "public" || existing.Visibility == "public") && !h.requirePublicQuickActionRole(w, r, workspaceID) {
		return
	}

	params := db.UpdateQuickActionParams{ID: idUUID, WorkspaceID: wsUUID}
	if req.Visibility != nil {
		params.Visibility = pgtype.Text{String: visibility, Valid: true}
	}

	if req.Name != nil {
		name, err := validateQuickActionName(*req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Description != nil {
		description, ok := trimmedWithinLimit(w, *req.Description, maxQuickActionDescriptionLen, "description")
		if !ok {
			return
		}
		params.Description = pgtype.Text{String: description, Valid: true}
	}

	// assignee_type 与 assignee_id 必须成对更新，绝不能让类型切换带着不匹配的旧 id
	// 落库（autopilot 执行同一条规则）。绑定按「更新后的可见性」重新校验：把一条
	// 动作改成 public 却仍绑着私有 agent，在这里就会被拦下。
	newType := existing.AssigneeType
	newID := uuidToString(existing.AssigneeID)
	if req.AssigneeType != nil {
		newType = *req.AssigneeType
	}
	if req.AssigneeID != nil {
		newID = *req.AssigneeID
	}
	if err := validateQuickActionAssignee(newType, newID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	newUUID, ok := parseUUIDOrBadRequest(w, newID, "assignee_id")
	if !ok {
		return
	}
	if req.AssigneeType != nil || req.AssigneeID != nil {
		params.AssigneeType = pgtype.Text{String: newType, Valid: true}
		params.AssigneeID = newUUID
	}
	if !h.validateQuickActionBinding(w, r, newType, newUUID, wsUUID, visibility) {
		return
	}

	if req.Prompt != nil {
		prompt, err := validateQuickActionPrompt(*req.Prompt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Prompt = pgtype.Text{String: prompt, Valid: true}
	}
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "archived" {
			writeError(w, http.StatusBadRequest, "status must be \"active\" or \"archived\"")
			return
		}
		if *req.Status == "active" && existing.Status != "active" {
			count, err := h.Queries.CountActiveQuickActions(r.Context(), wsUUID)
			if err == nil && count >= maxActiveQuickActionsPerWorkspace {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("a workspace can have at most %d active quick actions", maxActiveQuickActionsPerWorkspace))
				return
			}
		}
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}

	qa, err := h.Queries.UpdateQuickAction(r.Context(), params)
	if err != nil {
		slog.Warn("update quick action failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update quick action")
		return
	}
	writeJSON(w, http.StatusOK, h.quickActionToResponse(r.Context(), qa))
}

// DeleteQuickAction 删除一条快捷动作。public 动作是 workspace 公共设施（需要角色），
// private 动作只属于创建者、别人无权删除。
func (h *Handler) DeleteQuickAction(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requireQuickActionActor(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "quick action id")
	if !ok {
		return
	}
	existing, ok := h.loadReachableQuickAction(w, r, idUUID, wsUUID, userID)
	if !ok {
		return
	}
	// public 动作是 workspace 的公共设施；private 动作只属于创建者，别人无权删除。
	if existing.Visibility == "public" {
		if !h.requirePublicQuickActionRole(w, r, workspaceID) {
			return
		}
	} else if uuidToString(existing.CreatedByID) != userID {
		writeError(w, http.StatusForbidden, "only the creator can delete a private quick action")
		return
	}

	if err := h.Queries.DeleteQuickAction(r.Context(), db.DeleteQuickActionParams{ID: idUUID, WorkspaceID: wsUUID}); err != nil {
		slog.Warn("delete quick action failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to delete quick action")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// trimmedWithinLimit 对自由文本字段去首尾空白并施加字符数上限，超过时自己写好
// 400 响应并返回 false，让调用方保持线性流程。
func trimmedWithinLimit(w http.ResponseWriter, raw string, limit int, field string) (string, bool) {
	v := strings.TrimSpace(raw)
	if utf8.RuneCountInString(v) > limit {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be at most %d characters", field, limit))
		return "", false
	}
	return v, true
}

// validateQuickActionBinding 校验目标存在于本 workspace，且——对 public 动作——
// 每个成员都能触发它。被拒时自行写好 400 并返回 false。
func (h *Handler) validateQuickActionBinding(w http.ResponseWriter, r *http.Request, assigneeType string, id, workspaceID pgtype.UUID, visibility string) bool {
	qa := db.QuickAction{AssigneeType: assigneeType, AssigneeID: id, WorkspaceID: workspaceID}
	target := h.resolveQuickActionTarget(r.Context(), qa)
	if !target.Found {
		writeError(w, http.StatusBadRequest, "assignee not found in this workspace")
		return false
	}
	// public 动作承诺「人人可跑」。绑一个团队触发不了的目标，第一次点击就会让
	// 承诺落空，所以要在写入时拒绝而不是等跑的时候。
	if visibility == "public" && !h.agentInvocableByEveryone(r.Context(), target.Agent) {
		writeError(w, http.StatusBadRequest, "a public quick action must use an agent every workspace member can trigger; make the agent public or set this action to private")
		return false
	}
	return true
}

// RenderQuickAction 返回该动作「将要发布的内容」，但不真正发布。
//
// 它支撑输入框交接（`/` 斜杠命令）：用户在评论框里拿到完整渲染好的文本，编辑完
// 再发送。即便这是只读路径，渲染仍留在服务器端——这样插值实现只有一份，客户端
// 再抄一份会与运行路径漂移，悄悄发出与预览不同的内容。
func (h *Handler) RenderQuickAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "quickActionId"), "quick action id")
	if !ok {
		return
	}
	qa, ok := h.loadReachableQuickAction(w, r, idUUID, issue.WorkspaceID, userID)
	if !ok {
		return
	}

	target := h.resolveQuickActionTarget(r.Context(), qa)
	if !target.Found {
		h.writeDispatchBlocked(w, http.StatusConflict, ReasonTargetUnavailable)
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	// 与运行路径同一条闸门：不然预览会把触发某 agent 所需的原文直接递给一个
	// 无权触发它的人。
	if !h.canInvokeAgent(r.Context(), target.Agent, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), workspaceID) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"content": buildQuickActionBody(qa, target),
	})
}

// buildQuickActionBody 把真正触发运行的 @ 行放在正文前面。该行与输入框产物走同
// 一份 ParseMentions，所以下游看一条快捷动作和手打的 @mention 没有任何区别。
// prompt 本身原样透传——为什么没有插值步骤见文件头部说明。
func buildQuickActionBody(qa db.QuickAction, target quickActionTarget) string {
	return fmt.Sprintf("[@%s](mention://%s/%s)\n\n%s", target.Name, target.MentionType, target.MentionID, qa.Prompt)
}

// RunQuickAction 渲染动作内容并以一条 quick_action 评论发布，然后交给标准评论
// 触发链路。
//
// 这是唯一检查触发权限的地方。列表端点除 `private` 归属外什么都不隐藏，所以成员
// 完全可能点一条自己无权运行的动作；此时返回结构化 403，客户端渲染成对话框，
// 比一个静默消失的按钮更有信息量。
//
// 成功响应是一条携带 trigger_outcomes 的 CommentResponse，与 POST /comments 的
// 形状一致——客户端因此能复用同一套结果处理，白拿 `coalesced`（并入目标 pending
// 任务）、`deferred`（runtime 离线）、`blocked` 这些结果。
func (h *Handler) RunQuickAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "quickActionId"), "quick action id")
	if !ok {
		return
	}
	qa, ok := h.loadReachableQuickAction(w, r, idUUID, issue.WorkspaceID, userID)
	if !ok {
		return
	}
	if qa.Status != "active" {
		writeError(w, http.StatusBadRequest, "quick action is archived")
		return
	}

	target := h.resolveQuickActionTarget(r.Context(), qa)
	if !target.Found {
		h.writeDispatchBlocked(w, http.StatusConflict, ReasonTargetUnavailable)
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	// 唯一的权限闸门。能看到这个 issue 绝不等于有权去触发别人的私有 agent。
	if !h.canInvokeAgent(r.Context(), target.Agent, actorType, actorID, originatorUserID, workspaceID) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}

	body := sanitizeNullBytes(buildQuickActionBody(qa, target))

	if h.TxStarter == nil {
		writeError(w, http.StatusInternalServerError, "failed to start quick action transaction")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("quick action comment transaction begin failed", append(logger.RequestAttrs(r), "error", err, "quick_action_id", uuidToString(qa.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to run quick action")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateComment(r.Context(), db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  actorType,
		AuthorID:    parseUUID(actorID),
		Content:     body,
		// 刻意存成普通评论。折叠卡片由 quick_action_id 驱动，而它没有任何客户端
		// 能伪造——通用评论端点的 `type` 是客户端可传的，若这里用专用 type 值就
		// 可被伪造，还会让热表上的 CHECK 重建一次。
		Type:          "comment",
		QuickActionID: qa.ID,
	})
	if err != nil {
		slog.Warn("quick action comment create failed", append(logger.RequestAttrs(r), "error", err, "quick_action_id", uuidToString(qa.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to run quick action")
		return
	}
	comment := created.Comment()

	resp := commentToResponse(comment, nil, nil)
	resp.IssueRevision = created.IssueRevision
	createdEvent, err := eventoutbox.Enqueue(r.Context(), qtx, buildCommentCreatedEvent(issue, resp, actorType, actorID))
	if err != nil {
		slog.Warn("quick action comment event enqueue failed", append(logger.RequestAttrs(r), "error", err, "quick_action_id", uuidToString(qa.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to run quick action")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("quick action comment transaction commit failed", append(logger.RequestAttrs(r), "error", err, "quick_action_id", uuidToString(qa.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to run quick action")
		return
	}
	h.publishEvent(createdEvent)

	delegationAuthority := h.autopilotDelegationAuthorityFromRequest(r, issue, actorType, actorID)
	dispatch := h.triggerTasksForComment(r.Context(), issue, comment, nil, actorType, actorID, commentTriggerComputeOptions{
		OriginatorUserID:                   originatorUserID,
		AutopilotDelegationAuthorityUserID: delegationAuthority,
		// 1. quick action 的评论按发起动作入队，任务级继承与普通评论走同一判定（GONG-83）。
		TaskDelegationAuthority: h.requestTaskDelegationResolver(r, scopeExistingIssue(&issue), actorType, actorID, uuidToString(issue.WorkspaceID)),
	}, nil)
	resp.TriggerOutcomes = dispatch.Outcomes
	resp.ReviewReply = dispatch.ReviewReply

	// 用量上报是尽力而为、刻意放在运行成功路径之外：一次计数失败绝不能反过来
	// 让用户的运行泡汤。
	if err := h.Queries.TouchQuickActionUsage(r.Context(), db.TouchQuickActionUsageParams{ID: qa.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
		slog.Debug("quick action usage touch failed", "quick_action_id", uuidToString(qa.ID), "error", err)
	}

	slog.Info("quick action run", append(logger.RequestAttrs(r),
		"quick_action_id", uuidToString(qa.ID),
		"issue_id", uuidToString(issue.ID),
		"comment_id", uuidToString(comment.ID),
	)...)
	writeJSON(w, http.StatusCreated, resp)
}
