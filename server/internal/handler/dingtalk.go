// 职责定位：钉钉（DingTalk）接入相关的 HTTP 处理器。覆盖面：钉钉安装（installation）清单、
// 群里机器人观测信息（groups/群组发现）、忘掉某群、自助接入机器人（BYO）、吊销安装，以及
// 「钉钉账号 ↔ 站内用户」的绑定 token 兑换。真正的钉钉 API 调用与凭据加密在
// internal/integrations/dingtalk，这里只做 HTTP 编排与鉴权。
//
// 关键可见性规则：workspace 的 owner/admin 能看到全部 agent 的安装/群信息；普通成员只能
// 看到自己「有权限打开」的 agent 相关的信息，且不能看到别人账号的绑定明细。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// DingTalkInstallationResponse 是钉钉安装记录的接口形态。配置里的加密 AppSecret 故意
// 不出现——它属于服务端内部机密（只有出站消息发送方才解密）。WebSocket 租约列是运行时
// 内部状态、不是 API 表面，也一并省略。
type DingTalkInstallationResponse struct {
	ID                   string   `json:"id"`
	WorkspaceID          string   `json:"workspace_id"`
	AgentID              string   `json:"agent_id"`
	InstallerUserID      string   `json:"installer_user_id"`
	Status               string   `json:"status"`
	InstalledAt          string   `json:"installed_at"`
	CreatedAt            string   `json:"created_at"`
	UpdatedAt            string   `json:"updated_at"`
	AgentAvailable       bool     `json:"agent_available"`
	BoundDingTalkUserIDs []string `json:"bound_dingtalk_user_ids,omitempty"`
}

// DingTalkGroupBotResponse 描述在某个钉钉群里观测到的一个已连接的 Multica bot。
// AgentID 是产品侧身份；BotName 是钉钉可读的身份名（应用拥有 qyapi_chat_manage
// 权限时才有）。
type DingTalkGroupBotResponse struct {
	InstallationID   string `json:"installation_id"`
	AgentID          string `json:"agent_id"`
	BotName          string `json:"bot_name"`
	BotIdentityIssue string `json:"bot_identity_issue"`
	LastActiveAt     string `json:"last_active_at"`
	MentionCount     int64  `json:"mention_count"`
}

// DingTalkGroupResponse 把同一个钉钉 openConversationId 下观测到的所有 bot 归到一组。
type DingTalkGroupResponse struct {
	ConversationID    string                     `json:"conversation_id"`
	ConversationTitle string                     `json:"conversation_title"`
	Bots              []DingTalkGroupBotResponse `json:"bots"`
}

// ListDingTalkGroupsResponse 是群清单接口的聚合响应：
//   - Groups：按会话分组的群及其 bot；
//   - InactiveGroupCounts：每个安装下「超窗未活跃」的群数量（键是 installation_id）；
//   - BotIdentities：按 installation_id 索引的 bot 身份（供前端画连线）；
//   - NextOffset：翻页游标，没有下一页时为 nil。
type ListDingTalkGroupsResponse struct {
	Groups                  []DingTalkGroupResponse             `json:"groups"`
	GroupDiscoverySupported bool                                `json:"group_discovery_supported"`
	InactiveGroupCounts     map[string]int64                    `json:"inactive_group_counts"`
	BotIdentities           map[string]DingTalkGroupBotResponse `json:"bot_identities"`
	NextOffset              *int                                `json:"next_offset,omitempty"`
}

// 钉钉群活跃度相关参数：90 天没动静就算「不活跃群」；不活跃清单分页每页默认 20、上限 100。
const (
	dingTalkActiveGroupWindow = 90 * 24 * time.Hour
	dingTalkInactivePageSize  = 20
	dingTalkInactiveMaxPage   = 100
)

// dingtalkInstallationToResponse 把数据库里的安装行转成接口响应结构。AgentAvailable
// 恒为 true，真正的「agent 是否还存在/可用」由调用方查询后覆盖。
func dingtalkInstallationToResponse(row db.ChannelInstallation) DingTalkInstallationResponse {
	return DingTalkInstallationResponse{
		ID:              uuidToString(row.ID),
		WorkspaceID:     uuidToString(row.WorkspaceID),
		AgentID:         uuidToString(row.AgentID),
		InstallerUserID: uuidToString(row.InstallerUserID),
		Status:          row.Status,
		InstalledAt:     row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:       row.UpdatedAt.Time.UTC().Format(time.RFC3339),
		AgentAvailable:  true,
	}
}

// dingtalkAgentVisibility 一次性算出「Settings 清单里 agent 侧的可见性」两个集合：
//   - available：当前 workspace 里真实存在的 agent。管理员靠它区分「孤儿安装（agent 已删）」
//     和「只是当前用户无权访问」；成员清单里用它填 AgentAvailable 字段。
//   - visible：当前用户（按 owner/admin 角色，或与普通成员同样的「归属者/角色/可调用目标」
//     规则，与 agent 列表一致）有权打开的 agent。
func (h *Handler) dingtalkAgentVisibility(
	ctx context.Context,
	workspaceID pgtype.UUID,
	userID string,
	member db.Member,
) (available map[string]struct{}, visible map[string]struct{}, ok bool) {
	agents, err := h.Queries.ListAllAgents(ctx, workspaceID)
	if err != nil {
		return nil, nil, false
	}
	available = make(map[string]struct{}, len(agents))
	visible = make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		agentID := uuidToString(agent.ID)
		available[agentID] = struct{}{}
		if roleAllowed(member.Role, "owner", "admin") {
			visible[agentID] = struct{}{}
		}
	}
	if roleAllowed(member.Role, "owner", "admin") {
		return available, visible, true
	}
	targetsByAgent, loaded := h.loadInvocationTargetsByAgent(ctx, agents)
	if !loaded {
		return nil, nil, false
	}
	for _, agent := range agents {
		agentID := uuidToString(agent.ID)
		if memberAllowedToViewAgent(agent, targetsByAgent[agentID], userID, member.Role) {
			visible[agentID] = struct{}{}
		}
	}
	return available, visible, true
}

// ListDingTalkInstallations (GET /api/workspaces/{id}/dingtalk/installations)
// 普通成员也能看（这样非管理员的 Integrations 页能正常渲染）。响应里的两个标志位与
// Slack 对齐：
//   - configured：静态加密密钥已配置（即 DingTalkInstall != nil）；
//   - install_supported：给管理界面用；只要 configured 就为 true——BYO 安装只依赖
//     那把静态密钥。
func (h *Handler) ListDingTalkInstallations(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkInstall == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []DingTalkInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, uuidToString(wsUUID))
	if !ok {
		return
	}
	availableAgentIDs, visibleAgentIDs, loaded := h.dingtalkAgentVisibility(
		r.Context(), wsUUID, userID, member,
	)
	if !loaded {
		writeError(w, http.StatusInternalServerError, "failed to resolve dingtalk installation visibility")
		return
	}
	rows, err := h.DingTalkInstall.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dingtalk installations")
		return
	}
	bindingsByInstallation := map[pgtype.UUID][]string{}
	// 「已绑定的钉钉用户」属于账号隐私，只有 owner/admin 能看；普通成员不加载。
	canViewAll := roleAllowed(member.Role, "owner", "admin")
	canViewAccountBindings := canViewAll
	if canViewAccountBindings {
		userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
		if !ok {
			return
		}
		bindings, err := h.Queries.ListDingTalkUserBindingsForMember(r.Context(), db.ListDingTalkUserBindingsForMemberParams{
			WorkspaceID:   wsUUID,
			MulticaUserID: userUUID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list dingtalk user bindings")
			return
		}
		for _, binding := range bindings {
			bindingsByInstallation[binding.InstallationID] = append(
				bindingsByInstallation[binding.InstallationID],
				binding.ChannelUserID,
			)
		}
	}
	out := make([]DingTalkInstallationResponse, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		// 非管理员：只返回自己有权限打开的 agent 的安装。
		if !canViewAll {
			if _, canView := visibleAgentIDs[agentID]; !canView {
				continue
			}
		}
		response := dingtalkInstallationToResponse(row)
		// AgentAvailable 反映「这个 agent 现在还在不在」——已删/孤儿安装会变 false，
		// 与「agent 还在但当前用户无权看」区分开。
		_, response.AgentAvailable = availableAgentIDs[agentID]
		response.BoundDingTalkUserIDs = bindingsByInstallation[row.ID]
		out = append(out, response)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": true,
	})
}

// ListDingTalkGroups (GET /api/workspaces/{id}/dingtalk/groups) 返回 Settings 里观测到的
// 群与已连接 bot 的清单。owner/admin 看到完整清单；普通成员只拿到自己「能打开的 agent」
// 的 bot（与 ListAgents / Agent 详情口径一致）。
func (h *Handler) ListDingTalkGroups(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkInstall == nil {
		writeJSON(w, http.StatusOK, ListDingTalkGroupsResponse{
			Groups:                  []DingTalkGroupResponse{},
			GroupDiscoverySupported: true,
			InactiveGroupCounts:     map[string]int64{},
			BotIdentities:           map[string]DingTalkGroupBotResponse{},
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, uuidToString(wsUUID))
	if !ok {
		return
	}
	var visibleAgentIDs map[string]struct{}
	if !roleAllowed(member.Role, "owner", "admin") {
		_, visible, loaded := h.dingtalkAgentVisibility(r.Context(), wsUUID, userID, member)
		if !loaded {
			writeError(w, http.StatusInternalServerError, "failed to resolve dingtalk group visibility")
			return
		}
		visibleAgentIDs = visible
	}
	h.listDingTalkGroups(w, r, wsUUID, "", visibleAgentIDs)
}

// ListDingTalkGroupsForAgent (GET /api/agents/{id}/dingtalk/groups) 只暴露指定 agent 的
// 1:1 bot 及其观测到的群。这里刻意复用「打开 agent 详情」这道闸：调用方打不开这个 agent，
// 就不该能推断它的钉钉群活动。
func (h *Handler) ListDingTalkGroupsForAgent(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, agentID)
	if !ok {
		return
	}
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}
	if h.DingTalkInstall == nil {
		writeJSON(w, http.StatusOK, ListDingTalkGroupsResponse{
			Groups:                  []DingTalkGroupResponse{},
			GroupDiscoverySupported: true,
			InactiveGroupCounts:     map[string]int64{},
			BotIdentities:           map[string]DingTalkGroupBotResponse{},
		})
		return
	}
	h.listDingTalkGroups(w, r, agent.WorkspaceID, agentID, nil)
}

// listDingTalkGroups 是群清单的核心实现，被 workspace 级与 agent 级两个公开接口共用。
//
// 参数语义：agentID 非空表示「只看这一个 agent」；visibleAgentIDs 非空表示「只能看到集合内
// agent 的数据」（普通成员），为空表示 owner/admin 不限。activity=inactive 时走分页并需要
// installation_id（按单个安装翻「不活跃群」）。
//
// 结果按「群」聚合：同一 conversation 下的多个 bot 合成一个组；组的排序是「有标题的排在
// 无标题的前面，其余按标题、再按 conversation_id 稳定排序」。
func (h *Handler) listDingTalkGroups(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	agentID string,
	visibleAgentIDs map[string]struct{},
) {
	filterByAgent := agentID != ""
	var agentUUID pgtype.UUID
	if filterByAgent {
		agentUUID = parseUUID(agentID)
	}
	activeSince := pgtype.Timestamptz{Time: time.Now().UTC().Add(-dingTalkActiveGroupWindow), Valid: true}
	// 只允许 activity 缺省（活跃群视图）或显式 inactive。
	activity := strings.TrimSpace(r.URL.Query().Get("activity"))
	if activity != "" && activity != "inactive" {
		writeError(w, http.StatusBadRequest, "activity must be inactive when provided")
		return
	}
	includeInactive := activity == "inactive"
	installationID := strings.TrimSpace(r.URL.Query().Get("installation_id"))
	var installationUUID pgtype.UUID
	if includeInactive {
		// 不活跃视图必须指定 installation_id：否则无法判断这个群属于哪个机器人安装。
		if installationID == "" {
			writeError(w, http.StatusBadRequest, "installation_id is required for inactive groups")
			return
		}
		var ok bool
		installationUUID, ok = parseUUIDOrBadRequest(w, installationID, "installation_id")
		if !ok {
			return
		}
	}
	pageOffset := 0
	pageLimit := 0
	if includeInactive {
		pageLimit = dingTalkInactivePageSize
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > dingTalkInactiveMaxPage {
				writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
				return
			}
			pageLimit = parsed
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 0 {
				writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
				return
			}
			pageOffset = parsed
		}
	}
	// 不活跃清单属于「敏感存在性数据」（能暴露某个安装曾出现在哪些群里），先做边界鉴权。
	mayListPresence := true
	if includeInactive {
		var err error
		mayListPresence, err = h.mayListInactiveDingTalkInstallation(
			r.Context(), workspaceID, installationUUID, filterByAgent, agentUUID, visibleAgentIDs,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize dingtalk group installation")
			return
		}
	}
	rows := []db.ListDingTalkGroupPresencesByWorkspaceRow{}
	if mayListPresence {
		var err error
		rows, err = h.Queries.ListDingTalkGroupPresencesByWorkspace(r.Context(), db.ListDingTalkGroupPresencesByWorkspaceParams{
			WorkspaceID:          workspaceID,
			FilterByAgent:        filterByAgent,
			AgentID:              agentUUID,
			FilterByInstallation: includeInactive,
			FilterInstallationID: installationUUID,
			IncludeInactive:      includeInactive,
			ActiveSince:          activeSince,
			PageOffset:           int32(pageOffset),
			// 多取一行当「探针」：若拿回来的行数超过 pageLimit，说明后面还有下一页。
			PageLimit: int32(pageLimit + boolToInt(includeInactive)),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list dingtalk groups")
			return
		}
	}
	var nextOffset *int
	if includeInactive && len(rows) > pageLimit {
		// 把探针行裁掉，并给出下一页游标。
		rows = rows[:pageLimit]
		next := pageOffset + pageLimit
		nextOffset = &next
	}
	inactiveRows, err := h.Queries.CountInactiveDingTalkGroupPresencesByWorkspace(r.Context(), db.CountInactiveDingTalkGroupPresencesByWorkspaceParams{
		WorkspaceID:   workspaceID,
		ActiveSince:   activeSince,
		FilterByAgent: filterByAgent,
		AgentID:       agentUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count inactive dingtalk groups")
		return
	}
	inactiveCounts := make(map[string]int64, len(inactiveRows))
	for _, row := range inactiveRows {
		rowAgentID := uuidToString(row.AgentID)
		if visibleAgentIDs != nil {
			if _, visible := visibleAgentIDs[rowAgentID]; !visible {
				continue
			}
		}
		inactiveCounts[uuidToString(row.InstallationID)] = row.GroupCount
	}
	identityRows, err := h.Queries.ListDingTalkBotIdentitiesByWorkspace(r.Context(), db.ListDingTalkBotIdentitiesByWorkspaceParams{
		WorkspaceID:   workspaceID,
		FilterByAgent: filterByAgent,
		AgentID:       agentUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dingtalk bot identities")
		return
	}
	botIdentities := make(map[string]DingTalkGroupBotResponse, len(identityRows))
	for _, row := range identityRows {
		rowAgentID := uuidToString(row.AgentID)
		if visibleAgentIDs != nil {
			if _, visible := visibleAgentIDs[rowAgentID]; !visible {
				continue
			}
		}
		installationID := uuidToString(row.InstallationID)
		botIdentities[installationID] = DingTalkGroupBotResponse{
			InstallationID:   installationID,
			AgentID:          rowAgentID,
			BotName:          row.BotName,
			BotIdentityIssue: row.BotIdentityIssue,
		}
	}

	// 按 conversation_id 把同一群的多行记录聚合成一组；groupIndexByID 记录每个群当前在
	// groups 里的下标，方便后续行直接往对应组的 Bots 里追加。
	groupIndexByID := make(map[string]int, len(rows))
	groups := make([]DingTalkGroupResponse, 0, len(rows))
	for _, row := range rows {
		rowAgentID := uuidToString(row.AgentID)
		if agentID != "" && rowAgentID != agentID {
			continue
		}
		if visibleAgentIDs != nil {
			if _, visible := visibleAgentIDs[rowAgentID]; !visible {
				continue
			}
		}
		groupIndex, exists := groupIndexByID[row.ConversationID]
		if !exists {
			groups = append(groups, DingTalkGroupResponse{
				ConversationID:    row.ConversationID,
				ConversationTitle: row.ConversationTitle,
				Bots:              []DingTalkGroupBotResponse{},
			})
			groupIndex = len(groups) - 1
			groupIndexByID[row.ConversationID] = groupIndex
		} else if groups[groupIndex].ConversationTitle == "" && row.ConversationTitle != "" {
			// 群标题只在个别行上有值：组内只要有任一行带标题就补上，避免列表里出现空标题。
			groups[groupIndex].ConversationTitle = row.ConversationTitle
		}
		lastActiveAt := ""
		if row.LastActiveAt.Valid {
			lastActiveAt = row.LastActiveAt.Time.UTC().Format(time.RFC3339)
		}
		groups[groupIndex].Bots = append(groups[groupIndex].Bots, DingTalkGroupBotResponse{
			InstallationID:   uuidToString(row.InstallationID),
			AgentID:          rowAgentID,
			BotName:          row.BotName,
			BotIdentityIssue: row.BotIdentityIssue,
			LastActiveAt:     lastActiveAt,
			MentionCount:     row.MentionCount,
		})
	}
	// 排序规则：先排「组内 bot 的稳定性」（按 installation_id），再排「组本身」——
	// 有标题的组排在没有标题的组前面，其余依次比标题、比 conversation_id。
	for index := range groups {
		sort.SliceStable(groups[index].Bots, func(i, j int) bool {
			return groups[index].Bots[i].InstallationID < groups[index].Bots[j].InstallationID
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left := groups[i].ConversationTitle
		right := groups[j].ConversationTitle
		if left == right {
			return groups[i].ConversationID < groups[j].ConversationID
		}
		if left == "" {
			return false
		}
		if right == "" {
			return true
		}
		return left < right
	})
	writeJSON(w, http.StatusOK, ListDingTalkGroupsResponse{
		Groups:                  groups,
		GroupDiscoverySupported: true,
		InactiveGroupCounts:     inactiveCounts,
		BotIdentities:           botIdentities,
		NextOffset:              nextOffset,
	})
}

// mayListInactiveDingTalkInstallation 在「分页能从行数据推出游标」之前，先在服务端边界上
// 校验目标安装是否可查。安装不存在、非 active、跨 workspace、或调用方无权访问其 agent，
// 一律返回 false——这样调用方无法靠群数据或 next_offset 区分这几种「查不到」的原因。
func (h *Handler) mayListInactiveDingTalkInstallation(
	ctx context.Context,
	workspaceID pgtype.UUID,
	installationID pgtype.UUID,
	filterByAgent bool,
	agentID pgtype.UUID,
	visibleAgentIDs map[string]struct{},
) (bool, error) {
	installation, err := h.Queries.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          installationID,
		WorkspaceID: workspaceID,
		ChannelType: string(dingtalk.TypeDingTalk),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if installation.Status != "active" {
		return false, nil
	}
	if filterByAgent {
		return installation.AgentID == agentID, nil
	}
	if visibleAgentIDs == nil {
		return true, nil
	}
	_, visible := visibleAgentIDs[uuidToString(installation.AgentID)]
	return visible, nil
}

// boolToInt 把布尔换算成 0/1，用来给「分页探针多取一行」这类条件加一。
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// ForgetDingTalkGroup 从 Settings 清单里移除「某安装曾在某群出现过」的一条观测记录。
// 会话与消息历史不受影响；之后同一群里再来一条可正常触达的消息，会自动把它重新观测进来。
func (h *Handler) ForgetDingTalkGroup(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, uuidToString(wsUUID), "dingtalk group not found", "owner", "admin"); !ok {
		return
	}
	installationUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	conversationID, err := url.PathUnescape(chi.URLParam(r, "conversationId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		writeError(w, http.StatusBadRequest, "conversation id is required")
		return
	}
	if _, err = h.Queries.ForgetDingTalkGroupPresence(r.Context(), db.ForgetDingTalkGroupPresenceParams{
		WorkspaceID:    wsUUID,
		InstallationID: installationUUID,
		ConversationID: conversationID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "dingtalk group not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to forget dingtalk group")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RegisterDingTalkBYORequest 是「自带应用（BYO）安装」的请求体：用户从自己的钉钉
// Stream 模式机器人里复制来的两个凭据。
type RegisterDingTalkBYORequest struct {
	ClientID     string `json:"client_id"`     // AppKey
	ClientSecret string `json:"client_secret"` // AppSecret
}

// RegisterDingTalkBYO (POST /api/workspaces/{id}/dingtalk/install/byo?agent_id=…)
// 把用户自带的钉钉机器人装到某个 agent 上，从而让多个 agent 能在「同一个钉钉组织」里各有
// 各的 bot 身份。路由层要求是 workspace 成员；这里再校验调用方是目标 agent 的归属人或
// workspace 的 owner/admin。和 Slack 的 BYO 路径一样，只需要静态密钥已配置
// （DingTalkInstall != nil）。
func (h *Handler) RegisterDingTalkBYO(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkInstall == nil {
		writeFeatureDisabled(w, "dingtalk_not_configured", "dingtalk integration not enabled")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentIDStr := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	// 先在边界上把目标 agent 解析并授权掉：agent_id 写错了给清晰的 404，无关成员则无法把
	// bot 挂到这个 agent 上。
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var body RegisterDingTalkBYORequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, err := h.DingTalkInstall.RegisterBYO(r.Context(), dingtalk.RegisterBYOParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
		AppKey:      body.ClientID,
		AppSecret:   body.ClientSecret,
	})
	if err != nil {
		switch {
		case errors.Is(err, dingtalk.ErrInvalidAppKey), errors.Is(err, dingtalk.ErrInvalidAppSecret):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, dingtalk.ErrRobotOwnedBySameWorkspace):
			writeError(w, http.StatusConflict, "this DingTalk robot is already connected to another agent in this workspace — disconnect it there first, then connect it here")
		case errors.Is(err, dingtalk.ErrRobotOwnedByArchivedAgent):
			writeError(w, http.StatusConflict, "this DingTalk robot is connected to an archived agent in this workspace — restore that agent, or disconnect its robot, before connecting it here")
		case errors.Is(err, dingtalk.ErrRobotOwnedByAnotherWorkspace):
			writeError(w, http.StatusConflict, "this DingTalk robot is already connected to a different Multica workspace — disconnect it there before connecting it here")
		case errors.Is(err, dingtalk.ErrCredentialValidation):
			// 换取 access-token 时拒绝了用户粘贴的凭据（用户侧错误），引导用户复查。
			writeError(w, http.StatusBadRequest, "could not verify the DingTalk credentials — check the AppKey (client id) and AppSecret (client secret), and that the robot is a Stream-mode robot in your organization")
		default:
			// 加密/持久化等未预期的失败是服务端问题，不是用户凭据问题——回 500，别把
			// 它们误报成「凭据不对」。
			writeError(w, http.StatusInternalServerError, "could not connect the DingTalk robot")
		}
		return
	}
	// 广播 dingtalk_installation:created，让所有打开的客户端（Settings、Agent Integrations、
	// 其他标签页）都失效自己的安装查询、刷出新 bot——与 revoke 事件和 Slack 的安装语义一致。
	h.publishDingTalkInstallationCreated(row, userID)
	writeJSON(w, http.StatusOK, dingtalkInstallationToResponse(row))
}

// publishDingTalkInstallationCreated 为「新连上的 bot」发出 dingtalk_installation:created。
// realtime 层把它扇出给整个 workspace；web 端监听 dingtalk_installation:* 来失效安装查询。
func (h *Handler) publishDingTalkInstallationCreated(row db.ChannelInstallation, actorID string) {
	h.publish(protocol.EventDingTalkInstallationCreated, uuidToString(row.WorkspaceID), "user", actorID, map[string]any{
		"id": uuidToString(row.ID),
	})
}

// RevokeDingTalkInstallation (DELETE /api/workspaces/{id}/dingtalk/installations/{installationId})
// 把安装状态翻成 'revoked'。路由层要求 workspace 成员；这里再校验调用方是绑定 agent 的
// 归属人或 workspace 的 owner/admin。行记录保留以便审计；重新安装（再次粘贴机器人凭据）
// 会把状态翻回 'active'。如果安装已「孤儿化」（绑定 agent 已删、找不到归属人），则回退为
// 仅 workspace owner/admin 可清理。
func (h *Handler) RevokeDingTalkInstallation(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkInstall == nil {
		writeFeatureDisabled(w, "dingtalk_not_configured", "dingtalk integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	instUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	// 按 workspace 范围查，防止某个 workspace 靠猜 UUID 去吊销别家空间的安装。
	inst, err := h.DingTalkInstall.GetInWorkspace(r.Context(), instUUID, wsUUID)
	if err != nil {
		if errors.Is(err, dingtalk.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "dingtalk installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	// 绑定的 agent 还能查到：按「能否管理该 agent」授权；查不到（agent 已删的孤儿安装）：
	// 回退成 workspace owner/admin 才能清理。
	agent, agentErr := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          inst.AgentID,
		WorkspaceID: wsUUID,
	})
	if agentErr != nil {
		if _, ok := h.requireWorkspaceRole(w, r, uuidToString(wsUUID), "dingtalk installation not found", "owner", "admin"); !ok {
			return
		}
	} else if !h.canManageAgent(w, r, agent) {
		return
	}
	if err := h.DingTalkInstall.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventDingTalkInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RedeemDingTalkBindingTokenRequest 携带用户从 bot 的「绑定账号」提示里点过来的原始 token。
type RedeemDingTalkBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemDingTalkBindingTokenResponse 回显绑定好的 workspace/安装/钉钉用户，前端无需再
// 发一次查询即可确认结果。
type RedeemDingTalkBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	DingTalkUserID string `json:"dingtalk_user_id"`
}

// RedeemDingTalkBindingToken (POST /api/dingtalk/binding/redeem) 把 bearer token 携带的
// 钉钉用户 id 绑到当前登录的站内用户身上。兑换者身份来自登录会话，而持有 token 证明了对
// 投递到那个钉钉账号的链接的控制权。失败映射成不同状态码：
//   - 410 Gone:      token 未知 / 已消费 / 已过期
//   - 409 Conflict:  该钉钉 id 已绑到别的站内用户
//   - 403 Forbidden: 兑换者不是 workspace 成员
func (h *Handler) RedeemDingTalkBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkBindingTokens == nil {
		writeFeatureDisabled(w, "dingtalk_not_configured", "dingtalk integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemDingTalkBindingTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	redeemed, err := h.DingTalkBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, dingtalk.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, dingtalk.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this DingTalk account is already bound to a different Multica user")
		case errors.Is(err, dingtalk.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}
	h.publish(protocol.EventDingTalkAccountBindingUpdated, uuidToString(redeemed.WorkspaceID), "user", userID, map[string]any{
		"id": uuidToString(redeemed.InstallationID),
	})
	writeJSON(w, http.StatusOK, RedeemDingTalkBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		DingTalkUserID: redeemed.DingTalkUserID,
	})
}
