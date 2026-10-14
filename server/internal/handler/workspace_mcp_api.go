// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（workspace_mcp_api.go）实现"工作区级 MCP 服务器"的 HTTP 接口：
// workspace 的 MCP 库（增删改查）与 agent 的绑定管理（查看/挂载/启停/解绑）。
// 设计主线是"库条目与 agent 显式绑定分离、凭据只写不回"，写操作权限收在
// owner/admin 人工操作上，agent 自身不可改。
package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// WorkspaceMcpServerResponse 是单个工作区 MCP 服务器对外展示的形态，刻意只含
// 非机密字段。永远别往里加 url/command/args/headers/env：上游 #6062 要求共享
// 密钥"只写不回"，而上述每个字段都常携带 token（Composio 风格的会话 URL 本身
// 就是一种 Bearer 凭据）。与 daemon 侧的 runtimeLocalMcpServerSummary 对齐，
// 两侧对同一问题划同一条线。
//
// Enabled 只在"按 agent 看的列表"里有意义，反映该绑定开关的状态；在工作区库
// 列表中该字段省略。
type WorkspaceMcpServerResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Configured  bool   `json:"configured"`
	Transport   string `json:"transport"`
	Enabled     *bool  `json:"enabled,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// mcpTransportOf 为展示给客户端的形态归类一条服务器条目的传输方式。因为客户端
// 会据此决定"引导式表单能否编辑这条目"，对显式的 type 字段不能有损地推断。
//
// 无法识别的显式 type 原样返回，而不是靠是否带 url 去猜：把
// {"type":"websocket","url":"wss://…"} 报成 "http"，会误导设置 UI 认为表单能表达
// 它；一旦从那表单保存，条目就被改写成 type:"http"，协议被悄悄换掉。只有没有显式
// type 的条目才从 command / url 推断——这是无损的，因为表单回写的就是同一种形状。
func mcpTransportOf(entry json.RawMessage) string {
	var e struct {
		Type    string          `json:"type"`
		Command json.RawMessage `json:"command"`
		URL     json.RawMessage `json:"url"`
	}
	if err := json.Unmarshal(entry, &e); err != nil {
		return "unknown"
	}
	if declared := strings.ToLower(strings.TrimSpace(e.Type)); declared != "" {
		switch declared {
		case "local", "stdio":
			return "stdio"
		case "remote", "http", "streamable-http":
			return "http"
		}
		// `sse` 以及将来新客户端发明的类型都会原样落在这——响应 schema 把
		// transport 设计成自由字符串正是为此。
		return declared
	}
	if len(e.Command) > 0 {
		return "stdio"
	}
	if len(e.URL) > 0 {
		return "http"
	}
	return "unknown"
}

// workspaceMcpServerToResponse 把库表行转成对外响应（库条目不携带 Enabled）。
func workspaceMcpServerToResponse(server db.WorkspaceMcpServer) WorkspaceMcpServerResponse {
	return WorkspaceMcpServerResponse{
		ID:          uuidToString(server.ID),
		WorkspaceID: uuidToString(server.WorkspaceID),
		Name:        server.Name,
		Description: server.Description,
		Configured:  len(server.Config) > 2,
		Transport:   mcpTransportOf(server.Config),
		CreatedAt:   timestampToString(server.CreatedAt),
		UpdatedAt:   timestampToString(server.UpdatedAt),
	}
}

// ListWorkspaceMcpServers 返回 workspace 的 MCP 服务器库。普通成员可见即可：
// 响应里不含任何凭据内容，而 agent 的所有者需要看到"有哪些可用"才好去绑定。
func (h *Handler) ListWorkspaceMcpServers(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	servers, err := h.Queries.ListWorkspaceMcpServers(r.Context(), idUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspace MCP servers")
		return
	}
	resp := make([]WorkspaceMcpServerResponse, 0, len(servers))
	for _, server := range servers {
		resp = append(resp, workspaceMcpServerToResponse(server))
	}
	writeJSON(w, http.StatusOK, resp)
}

// requireWorkspaceMcpWriter 强制写操作的门槛。路由层已把这些接口限制为
// owner/admin，但一个顶着 owner PAT 运行的 agent 执行者也能通过那层门槛；
// 新增共享 MCP 服务器必须是人类管理员的动作，所以这里再加一道 actor 校验。
func (h *Handler) requireWorkspaceMcpWriter(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return false
	}
	if actorType, _ := h.resolveActor(r, requestUserID(r), workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot modify the workspace MCP servers")
		return false
	}
	if !roleAllowed(member.Role, "owner", "admin") {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return false
	}
	return true
}

// WorkspaceMcpServerRequest 携带一条服务器条目。config 是 mcpServers 里某个
// 名字下的那部分对象；它只写不回，任何响应都不会带它。
type WorkspaceMcpServerRequest struct {
	Name        string          `json:"name"`
	Description *string         `json:"description,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
}

// CreateWorkspaceMcpServer 往 workspace 库里加一台服务器。它不会绑定任何
// agent——库条目必须先显式挂到某 agent 上才会起作用，这正是本功能的基本形态。
func (h *Handler) CreateWorkspaceMcpServer(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if !h.requireWorkspaceMcpWriter(w, r, workspaceID) {
		return
	}

	var req WorkspaceMcpServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateWorkspaceMcpServerName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateWorkspaceMcpServerEntry(req.Config); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create the MCP server")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 加入 workspace 拆除围栏（#5219）：本表无外键，不加这把共享锁的话，一条
	// 在 DeleteWorkspace 清扫之后才提交的插入会留下指向已不存在 workspace 的行。
	if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), idUUID); err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}

	server, err := qtx.CreateWorkspaceMcpServer(r.Context(), db.CreateWorkspaceMcpServerParams{
		WorkspaceID: idUUID,
		Name:        name,
		Config:      append([]byte(nil), req.Config...),
		CreatedBy:   parseUUID(requestUserID(r)),
		Description: mcpDescription(req.Description),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an MCP server with this name already exists in the workspace")
			return
		}
		slog.Warn("create workspace mcp server failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create the MCP server")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create the MCP server")
		return
	}
	// 审计日志只记名字、绝不记条目本身——否则审计痕迹会成为 workspace 凭据的
	// 第二份拷贝。
	slog.Info("workspace mcp server created", append(logger.RequestAttrs(r),
		"workspace_id", workspaceID, "server_id", uuidToString(server.ID), "name", server.Name)...)
	writeJSON(w, http.StatusCreated, workspaceMcpServerToResponse(server))
}

// UpdateWorkspaceMcpServer 整体替换库里的某一条目。改名在这里是安全的：绑定
// 是挂在 id 上的，正在用这台服务器的 agent 会继续使用它。
func (h *Handler) UpdateWorkspaceMcpServer(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if !h.requireWorkspaceMcpWriter(w, r, workspaceID) {
		return
	}
	serverUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "serverId"), "server id")
	if !ok {
		return
	}

	var req WorkspaceMcpServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	params := db.UpdateWorkspaceMcpServerParams{ID: serverUUID, WorkspaceID: idUUID}
	if name := strings.TrimSpace(req.Name); name != "" {
		if err := validateWorkspaceMcpServerName(name); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: mcpDescription(req.Description), Valid: true}
	}
	if len(req.Config) > 0 {
		if err := validateWorkspaceMcpServerEntry(req.Config); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Config = append([]byte(nil), req.Config...)
	}

	server, err := h.Queries.UpdateWorkspaceMcpServer(r.Context(), params)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an MCP server with this name already exists in the workspace")
			return
		}
		writeError(w, http.StatusNotFound, "MCP server not found")
		return
	}
	slog.Info("workspace mcp server updated", append(logger.RequestAttrs(r),
		"workspace_id", workspaceID, "server_id", uuidToString(server.ID), "name", server.Name)...)
	writeJSON(w, http.StatusOK, workspaceMcpServerToResponse(server))
}

// DeleteWorkspaceMcpServer 删除库条目以及指向它的全部绑定，两件事在一个事务里
// 完成——绑定表没有外键，不主动清会让孤儿行继续指着已不存在的服务器。
func (h *Handler) DeleteWorkspaceMcpServer(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if !h.requireWorkspaceMcpWriter(w, r, workspaceID) {
		return
	}
	serverUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "serverId"), "server id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete the MCP server")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 先加排他行锁：绑定的写入方在插入期间会对该行持 FOR SHARE 锁，所以这里先
	// 拿排他锁意味着"并发的绑定新增"要么落在这个事务之前（从而被下面的清扫一并
	// 带走），要么等着排到后面、然后发现服务器已经不在了。没有这把锁，新增可能
	// 插在清扫之后，留下指向已不存在服务器的绑定。
	if _, err := qtx.LockWorkspaceMcpServerForUpdate(r.Context(), db.LockWorkspaceMcpServerForUpdateParams{
		ID:          serverUUID,
		WorkspaceID: idUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "MCP server not found")
		return
	}

	rows, err := qtx.DeleteWorkspaceMcpServer(r.Context(), db.DeleteWorkspaceMcpServerParams{
		ID:          serverUUID,
		WorkspaceID: idUUID,
	})
	if err != nil {
		slog.Warn("delete workspace mcp server failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete the MCP server")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "MCP server not found")
		return
	}
	if err := qtx.DeleteWorkspaceMcpChecks(r.Context(), serverUUID); err != nil {
		writeError(w, 500, "failed to delete connection checks")
		return
	}
	if err := qtx.DeleteAgentMcpServersByServer(r.Context(), serverUUID); err != nil {
		slog.Warn("sweep agent mcp bindings failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete the MCP server")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete the MCP server")
		return
	}
	slog.Info("workspace mcp server deleted", append(logger.RequestAttrs(r),
		"workspace_id", workspaceID, "server_id", uuidToString(serverUUID))...)
	w.WriteHeader(http.StatusNoContent)
}

// ListAgentMcpServers 返回绑定到某 agent 的工作区服务器列表，附每条绑定的
// 启停开关状态。
func (h *Handler) ListAgentMcpServers(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListAgentMcpServers(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list the agent's MCP servers")
		return
	}
	resp := make([]WorkspaceMcpServerResponse, 0, len(rows))
	for _, row := range rows {
		enabled := row.Enabled
		resp = append(resp, WorkspaceMcpServerResponse{
			ID:          uuidToString(row.ID),
			WorkspaceID: uuidToString(row.WorkspaceID),
			Name:        row.Name,
			Transport:   mcpTransportOf(row.Config),
			Enabled:     &enabled,
			CreatedAt:   timestampToString(row.CreatedAt),
			UpdatedAt:   timestampToString(row.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// requireAgentMcpWriter 解析目标 agent 并强制"谁能改它的绑定"这道门槛。规则与
// 改 agent 自身 mcp_config 时一致：agent 所有者或 workspace 的 owner/admin，
// agent 执行者一律不行。
func (h *Handler) requireAgentMcpWriter(w http.ResponseWriter, r *http.Request) (db.Agent, bool) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return db.Agent{}, false
	}
	workspaceID := uuidToString(agent.WorkspaceID)
	if actorType, _ := h.resolveActor(r, requestUserID(r), workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot modify MCP server assignments")
		return db.Agent{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.Agent{}, false
	}
	if !canViewAgentSecrets(agent, requestUserID(r), member.Role) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return db.Agent{}, false
	}
	return agent, true
}

// AddAgentMcpServerRequest 指明要挂到 agent 上的库条目。
type AddAgentMcpServerRequest struct {
	ServerID string `json:"server_id"`
}

// AddAgentMcpServer 把工作区里的一台 MCP 服务器授予这个 agent。这是库条目到达
// agent 的唯一途径。
func (h *Handler) AddAgentMcpServer(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgentMcpWriter(w, r)
	if !ok {
		return
	}
	var req AddAgentMcpServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	serverUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.ServerID), "server_id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add the MCP server")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 与其它 workspace 级插入同一道拆除围栏（#5219）：绑定无外键，若写入发生在
	// DeleteWorkspace 清扫之后提交，绑定会比它指向的 agent 与服务器活得更久。
	if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), agent.WorkspaceID); err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	// 一条语句同时做"范围校验"和"删除协议同步"：行必须属于这个 agent 所在的
	// workspace——拿别的工作室的 id 会跨租户绑定；而持 FOR SHARE 锁让本插入与
	// DeleteWorkspaceMcpServer 的清扫串行化。
	if _, err := qtx.LockWorkspaceMcpServerForShare(r.Context(), db.LockWorkspaceMcpServerForShareParams{
		ID:          serverUUID,
		WorkspaceID: agent.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "MCP server not found in this workspace")
		return
	}
	if err := qtx.AddAgentMcpServer(r.Context(), db.AddAgentMcpServerParams{
		AgentID:  agent.ID,
		ServerID: serverUUID,
	}); err != nil {
		slog.Warn("add agent mcp server failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to add the MCP server")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add the MCP server")
		return
	}
	slog.Info("agent mcp server added", append(logger.RequestAttrs(r),
		"agent_id", uuidToString(agent.ID), "server_id", uuidToString(serverUUID))...)
	h.writeAgentMcpServers(w, r, agent.ID)
}

// SetAgentMcpServerEnabledRequest 携带 per-agent 的启停开关。
type SetAgentMcpServerEnabledRequest struct {
	Enabled *bool `json:"enabled"`
}

// SetAgentMcpServerEnabled 翻转某条绑定的开关而不解除绑定，这样再把它打开时
// 不必重新寻找并挂载一遍。
func (h *Handler) SetAgentMcpServerEnabled(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgentMcpWriter(w, r)
	if !ok {
		return
	}
	serverUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "serverId"), "server id")
	if !ok {
		return
	}
	var req SetAgentMcpServerEnabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	rows, err := h.Queries.SetAgentMcpServerEnabled(r.Context(), db.SetAgentMcpServerEnabledParams{
		AgentID:  agent.ID,
		ServerID: serverUUID,
		Enabled:  *req.Enabled,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the MCP server")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "this MCP server is not assigned to the agent")
		return
	}
	slog.Info("agent mcp server toggled", append(logger.RequestAttrs(r),
		"agent_id", uuidToString(agent.ID), "server_id", uuidToString(serverUUID), "enabled", *req.Enabled)...)
	h.writeAgentMcpServers(w, r, agent.ID)
}

// RemoveAgentMcpServer 把工作区里的一台服务器从这个 agent 身上解绑。库里的
// 条目本身不受影响。
func (h *Handler) RemoveAgentMcpServer(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgentMcpWriter(w, r)
	if !ok {
		return
	}
	serverUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "serverId"), "server id")
	if !ok {
		return
	}
	rows, err := h.Queries.RemoveAgentMcpServer(r.Context(), db.RemoveAgentMcpServerParams{
		AgentID:  agent.ID,
		ServerID: serverUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove the MCP server")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "this MCP server is not assigned to the agent")
		return
	}
	slog.Info("agent mcp server removed", append(logger.RequestAttrs(r),
		"agent_id", uuidToString(agent.ID), "server_id", uuidToString(serverUUID))...)
	h.writeAgentMcpServers(w, r, agent.ID)
}

// writeAgentMcpServers 在一次绑定变更后返回该 agent 的当前绑定列表，客户端
// 不必自己猜变更后的结果状态。
func (h *Handler) writeAgentMcpServers(w http.ResponseWriter, r *http.Request, agentID pgtype.UUID) {
	rows, err := h.Queries.ListAgentMcpServers(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list the agent's MCP servers")
		return
	}
	resp := make([]WorkspaceMcpServerResponse, 0, len(rows))
	for _, row := range rows {
		enabled := row.Enabled
		resp = append(resp, WorkspaceMcpServerResponse{
			ID:          uuidToString(row.ID),
			WorkspaceID: uuidToString(row.WorkspaceID),
			Name:        row.Name,
			Transport:   mcpTransportOf(row.Config),
			Enabled:     &enabled,
			CreatedAt:   timestampToString(row.CreatedAt),
			UpdatedAt:   timestampToString(row.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func mcpDescription(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// 元信息更新不会接收连接配置。
func (h *Handler) UpdateWorkspaceMcpMetadata(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, 400, "invalid metadata")
		return
	}
	raw, _ := json.Marshal(WorkspaceMcpServerRequest{Name: body.Name, Description: &body.Description})
	r.Body = io.NopCloser(bytes.NewReader(raw))
	h.UpdateWorkspaceMcpServer(w, r)
}
