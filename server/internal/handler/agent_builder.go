// 本文件实现「Agent Builder」（MUL-5163）：用户通过一次简短对话来设计 agent 的
// 配置流程。核心机制是「隐藏的 builder 载体 agent」——一个 system agent
// （system_key 前缀 agent_builder:），作为对话/任务的执行载体，永不出现在普通
// agent 列表、也不能被选为指派对象。会话创建、运行中切换 runtime、草稿保存、
// 未完成会话列表等端点都按「会话创建者 + builder 载体」双门控。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const agentBuilderInstructions = `You are Multica Agent Builder. Help the user design one practical AI agent through a short conversation.

Your job is to propose and refine configuration, never to create resources yourself. Ask only questions that materially change behavior. Prefer making a reasonable draft immediately, then ask at most two focused questions per turn.

Every response MUST end with exactly one <agent_draft> JSON block using this shape:
<agent_draft>{"name":"","description":"","instructions":"","conversation_starters":[],"model":"","skill_ids":[],"permission_scope":"private","member_ids":[]}</agent_draft>

Rules:
- The JSON must be valid, compact JSON on one physical line. Do not wrap it in Markdown fences.
- Escape every line break inside instructions as \n. Never place a literal newline inside a JSON string.
- Preserve good existing draft fields supplied in the user's message unless the user asks to change them.
- name is concise and suitable for a workspace list.
- description is one sentence, at most 200 characters.
- instructions are a complete Markdown system prompt describing role, workflow, output, and constraints.
- conversation_starters contains up to three objects with a concise label and a complete prompt. Each should demonstrate a useful first task for this specific agent; never include generic filler.
- model must be empty, preserve current_draft.model, or exactly match an id explicitly listed in AVAILABLE RUNTIME MODELS. Never use a model label as the id.
- When AVAILABLE RUNTIME MODELS is null or empty, preserve current_draft.model and never invent a model id.
- skill_ids may only contain IDs explicitly listed in AVAILABLE WORKSPACE SKILLS.
- permission_scope must be private, workspace, or members. Default to private unless the user explicitly requests sharing.
- member_ids may only contain IDs explicitly listed in AVAILABLE WORKSPACE MEMBERS, and only when permission_scope is members.
- Never request, expose, or place secrets, tokens, passwords, or environment-variable values in the draft.
- Do not claim that the agent has been created. The user must review and confirm the draft in the UI.`

// CreateAgentBuilderSessionRequest 是开启一次 agent 设计对话的请求体：选定要在
// 其上执行的 runtime，model 可选。
type CreateAgentBuilderSessionRequest struct {
	RuntimeID string `json:"runtime_id"`
	Model     string `json:"model,omitempty"`
}

// CreateAgentBuilderSessionResponse 是创建成功后的句柄：会话 id + 隐藏的 builder
// agent id + 承载 runtime。
type CreateAgentBuilderSessionResponse struct {
	SessionID      string `json:"session_id"`
	BuilderAgentID string `json:"builder_agent_id"`
	RuntimeID      string `json:"runtime_id"`
}

// CreateAgentBuilderSession 在既有 runtime 上开启一场私有的配置对话。隐藏的
// system agent 作为执行载体，因为 chat/任务管线有意按 agent 承载；它不出现在
// 普通 agent 列表、也不能被选为指派对象。
func (h *Handler) CreateAgentBuilderSession(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreateAgentBuilderSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}

	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	runtime, ok := h.resolveBuilderRuntime(w, r, workspaceID, workspaceUUID, runtimeID, "start")
	if !ok {
		return
	}

	flowID := uuid.NewString()
	ownerUUID := parseUUID(userID)
	model := strings.TrimSpace(req.Model)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent builder session")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 在创建 builder 的 chat_session 之前，先对工作区行取 FOR KEY SHARE——这是
	// #5219 delete/create 协议的「创建」半边，保证会话不会被创建进一个删除中的
	// 工作区（见 LockWorkspaceForChatSessionCreate）。
	if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), workspaceUUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock workspace")
		return
	}

	builder, err := qtx.CreateAgentBuilder(r.Context(), db.CreateAgentBuilderParams{
		WorkspaceID:  workspaceUUID,
		Name:         fmt.Sprintf(".multica-agent-builder-%s", flowID),
		RuntimeMode:  runtime.RuntimeMode,
		RuntimeID:    runtime.ID,
		OwnerID:      ownerUUID,
		Instructions: agentBuilderInstructions,
		Model:        pgtype.Text{String: model, Valid: model != ""},
		SystemKey: pgtype.Text{
			String: fmt.Sprintf("agent_builder:%s", flowID),
			Valid:  true,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare agent builder")
		return
	}

	session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		ID:          dbid.NewV7(),
		WorkspaceID: workspaceUUID,
		AgentID:     builder.ID,
		CreatorID:   ownerUUID,
		Title:       "Create an agent",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create agent builder session")
		return
	}
	session, err = qtx.MarkChatSessionExplicitlyCreated(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark agent builder session explicit")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent builder session")
		return
	}

	writeJSON(w, http.StatusCreated, CreateAgentBuilderSessionResponse{
		SessionID:      uuidToString(session.ID),
		BuilderAgentID: uuidToString(builder.ID),
		RuntimeID:      runtimeID,
	})
}

// AgentBuilderSessionSummary 是一场尚未完成的 agent 创建对话的摘要行。
type AgentBuilderSessionSummary struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	// RuntimeID 是承载者的 runtime——这场对话实际在哪执行。客户端从它播种自己的
	// runtime 选择器，让选择器永远不会与「下一条消息由谁回答」产生分歧（MUL-5163）。
	RuntimeID string `json:"runtime_id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	// LastMessageContent 是原始存储消息，仍是 builder 的线格式（用户侧是 JSON
	// 信封、assistant 侧带 <agent_draft> 块）。解码是客户端的事：协议由 studio 与
	// 其提示词定义，在这里再实现一遍只会得到一个悄然分叉的第二实现。
	LastMessageContent string `json:"last_message_content"`
	LastMessageRole    string `json:"last_message_role"`
	LastMessageAt      string `json:"last_message_at"`
	// Draft 是已存储的配置，对服务端不透明（见迁移 252）。它随列表下发而不是
	// 单独拉取，因为 studio 把该列表渲染在与所切换的会话旁边——点选那行时配置
	// 必须已在手。当对话从头到尾都由 AI 驱动时为 null，客户端此时重放最后一个
	// <agent_draft> 块。
	Draft json.RawMessage `json:"draft,omitempty"`
}

// ListAgentBuilderSessionsResponse 是未完成 builder 会话列表的容器。
type ListAgentBuilderSessionsResponse struct {
	Sessions []AgentBuilderSessionSummary `json:"sessions"`
}

// ListAgentBuilderSessions 返回调用者未完成的 agent 创建对话，最新活动在前。
//
// 这是回到 builder 会话的唯一途径：它们被 `kind = 'user'` 的 agent 过滤从每个
// chat 界面隐藏，所以在该端点之前，studio 不得不在跳转时删除一个会话或让它永远
// 泄漏。与其它 chat 读取一样按创建者限定——工作区 admin 不能列出别人的草稿，
// 与 loadChatSessionForUser 的规则一致。
func (h *Handler) ListAgentBuilderSessions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	rows, err := h.Queries.ListAgentBuilderSessionsByCreator(r.Context(), db.ListAgentBuilderSessionsByCreatorParams{
		WorkspaceID: workspaceUUID,
		CreatorID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent builder sessions")
		return
	}

	sessions := make([]AgentBuilderSessionSummary, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, AgentBuilderSessionSummary{
			SessionID:          uuidToString(row.ID),
			Title:              row.Title,
			RuntimeID:          uuidToString(row.RuntimeID),
			CreatedAt:          timestampToString(row.CreatedAt),
			UpdatedAt:          timestampToString(row.UpdatedAt),
			LastMessageContent: row.LastMessageContent,
			LastMessageRole:    row.LastMessageRole,
			LastMessageAt:      timestampToString(row.LastMessageAt),
			Draft:              json.RawMessage(row.StoredDraft),
		})
	}
	writeJSON(w, http.StatusOK, ListAgentBuilderSessionsResponse{Sessions: sessions})
}

// maxAgentBuilderDraftBytes 限制单份存储配置的上界。最大的诚实字段是指令
// markdown，而 create API 本身已把它压得远低于此；该上限是为了客户端 bug 不会
// 长出一个无界行。
const maxAgentBuilderDraftBytes = 256 * 1024

// SaveAgentBuilderDraftRequest 是要落库的整份配置草稿（服务端不透明的 JSON）。
type SaveAgentBuilderDraftRequest struct {
	Draft json.RawMessage `json:"draft"`
}

// SaveAgentBuilderDraft 保存一场创建对话当前到达的配置，包括用户已键入但尚未
// 发送的编辑。
//
// 负载不透明（见迁移 252）：其形状是 studio 的 AgentDraft，客户端侧校验，服务端
// 不读任何字段。整对象 last-write-wins 在这里是正确的，因为一场对话只有一个
// 编辑者、一块屏幕——字段级合并只能重建出用户从未见过的状态。
//
// upsert 跑在 LockChatSessionForDraftWrite（删除路径已取的同一行锁）下，并在锁内
// 复查会话。不加锁的话，本 handler 的读与写是两个语句，删除可能插在它们之间
// 提交：客户端按 debounce 自动保存，因此在窗口中间确认的「丢弃」曾会让草稿挂在
// 一个已不存在的会话上——UI 看不见，除了工作区拆除没有 prune 够得着它。
// agent_builder_draft 没有 chat_session 外键来拒绝那次 INSERT，所以正是这把锁把
// 两种次序收敛成仅有的两种：要么保存先提交、删除随后清理它，要么删除先提交、
// 保存发现无行可写。
func (h *Handler) SaveAgentBuilderDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req SaveAgentBuilderDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Draft) == 0 {
		writeError(w, http.StatusBadRequest, "draft is required")
		return
	}
	if len(req.Draft) > maxAgentBuilderDraftBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "draft is too large")
		return
	}
	if !json.Valid(req.Draft) {
		writeError(w, http.StatusBadRequest, "draft must be valid JSON")
		return
	}

	// 仅限创建者，且只能对 builder 载体——与 runtime 切换用的同一对门。少了
	// 载体校验，这就会成为「给调用者拥有的任意 chat 会话挂任意 JSON」的入口。
	// 两个判定都发生在这把未加锁的读上：会话的 workspace / creator / carrier 是
	// 不可变的，下面那把锁能看到的东西不会改变它们。
	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat agent")
		return
	}
	if !isAgentBuilderCarrier(agent) {
		writeError(w, http.StatusNotFound, "agent builder session not found")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent builder draft")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 并发写者还能对这个会话改的一切——它是否存在、是否仍 active——都在锁内对
	// 重读的行判定。若一次保存被删除或归档堵在锁上，恢复后它持着的是阻塞前读到的
	// 值，因此此前的读对这两点都不可信。
	locked, err := qtx.LockChatSessionForDraftWrite(r.Context(), session.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "chat session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}
	if locked.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}

	if _, err := qtx.UpsertAgentBuilderDraft(r.Context(), db.UpsertAgentBuilderDraftParams{
		ChatSessionID: locked.ID,
		WorkspaceID:   locked.WorkspaceID,
		Draft:         req.Draft,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent builder draft")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent builder draft")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveBuilderRuntime 加载调用者被允许在其上执行 builder 对话的 runtime。会话
// 创建与 runtime 切换共用，使两者按同一顺序强制同一组三道门：它存在于本工作区、
// 该成员可用（private runtime 保持 owner/admin-only）、且在线。verb 命名离线错误
// 里的动作，让两个调用点的文案读起来自然。
func (h *Handler) resolveBuilderRuntime(w http.ResponseWriter, r *http.Request, workspaceID string, workspaceUUID pgtype.UUID, runtimeID, verb string) (db.AgentRuntime, bool) {
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return db.AgentRuntime{}, false
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid runtime_id")
		return db.AgentRuntime{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.AgentRuntime{}, false
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "this runtime is private; only its owner can use it")
		return db.AgentRuntime{}, false
	}
	if runtime.Status != "online" {
		writeError(w, http.StatusConflict, fmt.Sprintf("runtime must be online to %s an agent builder session", verb))
		return db.AgentRuntime{}, false
	}
	return runtime, true
}

// SwitchAgentBuilderRuntimeRequest 是运行时切换请求体。
type SwitchAgentBuilderRuntimeRequest struct {
	RuntimeID string `json:"runtime_id"`
}

// SwitchAgentBuilderRuntimeResponse 是切换成功后的新 runtime id。
type SwitchAgentBuilderRuntimeResponse struct {
	RuntimeID string `json:"runtime_id"`
}

// SwitchAgentBuilderRuntime 把一场进行中的 builder 对话改指向另一台 runtime。
// 曾经，live-draft 的 runtime 选择器只改 React 状态，于是 UI 可能显示 runtime B，
// 而每条后续消息仍排给会话创建时冻结在 runtime A 的载体 agent（MUL-5163）。
//
// 重新绑定跑在 LockChatSessionForRuntimeBind 下——与 SendDirectChatMessage 取的
// 是同一把行锁——因此「没有在途回复」与「载体现在指向 B」在一个串行化的步骤里
// 敲定。没有这把锁，一条已经读到 runtime A 的发送可能在本 handler 返回成功后
// 才落它的任务——正好重现本端点要消除的那个不一致。
//
// chat_session.runtime_id 刻意留在旧 runtime 上：daemon 仅当该指针与认领任务的
// runtime 一致时才恢复已存的 provider 会话，因此让它保持过期值，正是让 B 新开
// 一个 provider 会话而不是恢复 A 的那个。Multica 侧的聊天历史与草稿都不受影响。
func (h *Handler) SwitchAgentBuilderRuntime(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req SwitchAgentBuilderRuntimeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}

	// 仅限创建者，与对 chat 会话的任何其它写入一致。
	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	if session.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}

	// 只有 builder 载体允许被重新绑定。用户自建 agent 改 runtime 走 agent 更新
	// 路径，那套有自己的权限模型——本端点不能成为第二条更弱的入口。
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat agent")
		return
	}
	if !isAgentBuilderCarrier(agent) {
		writeError(w, http.StatusNotFound, "agent builder session not found")
		return
	}

	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	runtime, ok := h.resolveBuilderRuntime(w, r, workspaceID, workspaceUUID, runtimeID, "switch")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to switch agent builder runtime")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockChatSessionForRuntimeBind(r.Context(), session.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "chat session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}

	// 在锁内检查，发送无法从它后面溜进来。仍排给离线 runtime 的任务也算 pending
	// ——预期客户端先停掉它（会把消息还回编辑器）。
	if _, err := qtx.GetPendingChatTask(r.Context(), session.ID); err == nil {
		writeError(w, http.StatusConflict, "stop the current reply before switching runtime")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check pending builder task")
		return
	}

	// model id 是按 runtime 的，所以载体 agent 的 model 被清空而不是带过去；新
	// runtime 自己解析默认值。
	updated, err := qtx.RebindAgentBuilderRuntime(r.Context(), db.RebindAgentBuilderRuntimeParams{
		ID:          agent.ID,
		RuntimeID:   runtime.ID,
		RuntimeMode: runtime.RuntimeMode,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent builder session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to switch agent builder runtime")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent builder runtime switch")
		return
	}

	writeJSON(w, http.StatusOK, SwitchAgentBuilderRuntimeResponse{
		RuntimeID: uuidToString(updated.RuntimeID),
	})
}

// isAgentBuilderCarrier 判断某 agent 是否为隐藏的 builder 执行载体。与 builder
// SQL 语句携带的 kind/system_key 守卫镜像，让 handler 在触达数据库之前就拒绝
// 非 builder 会话，而不是依赖一个匹配零行的 UPDATE。
func isAgentBuilderCarrier(agent db.Agent) bool {
	return agent.Kind == "system" &&
		agent.SystemKey.Valid &&
		strings.HasPrefix(agent.SystemKey.String, "agent_builder:")
}
