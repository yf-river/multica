// 本文件：Mika——工作区内置的「Chief of Staff」系统 Agent——的创建（首次启用/幂等复用）
// 与会话引导端点。Mika 的产品身份在这里是服务端常量，见下。
package handler

import (
	"context"
	"errors"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testagents"
	"strings"

	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"log/slog"
	"net/http"
)

// Mika 的产品级身份。刻意做成服务端常量：公开的 CreateAgent API 既不接受 `kind` 也不接受
// `system_key`，客户端无法铸造一个能通过引导门槛的内置 Agent。若可以，既
// 能伪造一个「Mika」，又会打破「每工作区一个」的不变式。
const (
	mikaAgentMaxConcurrency = 3
	mikaAgentVisibility     = "workspace"
	mikaAgentPermissionMode = "public_to"
	// 在 Mika 有正式形象前的占位。用的是所有 Agent 头像都在用的 `emoji:` 标记，ActorAvatar
	// 会把它当文本渲染，任何 surface 都不用为她特判。旧值是一段手搓的 data-URI SVG，只有
	// 那个常量自己知道怎么产出。
	mikaAgentAvatarURL = agentEmojiAvatarPrefix + "🦄"
)

// mikaAgentDescription 是 Mika 简介，创建 Agent 行时写入 Description。
const mikaAgentDescription = "你的工作区 Chief of Staff。Mika 会把目标转化为任务、协调智能体，并帮你建立可复用的工作流。"

type createMikaAgentRequest struct {
	RuntimeID string `json:"runtime_id"`
	// Model 是 Mika 应该跑的 runtime 模型。可选：空表示「用 runtime 默认」，没有按 Agent
	// 选模型能力的部署本来也就只能这样。
	Model string `json:"model"`
	// SessionTitle 在这次调用就是创建引导会话的那次时，给会话起的名。它永远只是个标签：
	// 会话的身份是 (workspace, member, Mika)，所以之后传不同标题也只是复用它而不是另开一条。
	SessionTitle string `json:"session_title"`
}

// mikaAgentResponse 是「Agent + 调用者的引导会话」一起返回。
//
// 两者要一起返回，因为每个调用方两个都需要，而且这对组合必须原子产出。客户端以前是先列
// 会话、看不到匹配就创建一个——那是一处没有保护的 check-then-insert：LockWorkspaceForChatSessionCreate
// 特意取 FOR KEY SHARE，让并发创建会话彼此不阻塞，于是两个标签页各建了一条会话、各拿到
// 自己的普通会话。
type mikaAgentResponse struct {
	AgentResponse
	OnboardingSession *ChatSessionResponse `json:"onboarding_session,omitempty"`
}

// CreateMikaAgent 处理 POST /api/... 的 Mika 启用请求——在给定 runtime 上置备工作区内置的
// Chief of Staff，或返回已有的那一个（已存在时返回 200，新建返回 201）。
//
// 幂等键是工作区内的 system_key，而不是名字：名字可被 owner 改，migration 172 的唯一索引
// 覆盖 (workspace_id, owner_id, runtime_id, system_key)，不同 runtime 或不同 owner 下仍可能
// 出现第二个 Mika。下面这次查找才是真正守住「每工作区一个 Mika」的不变式。
func (h *Handler) CreateMikaAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	var req createMikaAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	agent, created, ok := h.resolveMikaAgent(w, r, workspaceID, userID, req)
	if !ok {
		return
	}
	// 刻意放在 resolveMikaAgent 的事务之外：会话的 get-or-create 自己开事务，若在工作区
	// 置备锁还持有时跑它，会把每个成员的会话都排在一条与会话无关的锁后面。
	h.writeMikaAgentResponse(w, r, agent, workspaceID, userID, req.SessionTitle, created)
}

// resolveMikaAgent 返回工作区的 Mika——已有则直接返回，否则新置备一个。bool 对是
// (created, ok)；ok 为 false 时它已经写好了错误响应。
func (h *Handler) resolveMikaAgent(w http.ResponseWriter, r *http.Request, workspaceID, userID string, req createMikaAgentRequest) (db.Agent, bool, bool) {
	runtimeID, ok := parseUUIDOrBadRequest(w, req.RuntimeID, "runtime_id")
	if !ok {
		return db.Agent{}, false, false
	}
	wsUUID := parseUUID(workspaceID)

	// 已置备：直接交回同一个 Agent，重试、第二个标签页或重跑引导都不会产生重复。
	if existing, err := h.Queries.GetAgentBySystemKey(r.Context(), db.GetAgentBySystemKeyParams{
		WorkspaceID: wsUUID,
		SystemKey:   pgtype.Text{String: service.MikaSystemKey, Valid: true},
	}); err == nil {
		return existing, false, true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to look up the workspace agent")
		return db.Agent{}, false, false
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.Agent{}, false, false
	}
	runtime, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if err != nil || uuidToString(runtime.WorkspaceID) != workspaceID {
		writeError(w, http.StatusBadRequest, "runtime not found in this workspace")
		return db.Agent{}, false, false
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "you cannot bind an agent to this runtime")
		return db.Agent{}, false, false
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent create transaction")
		return db.Agent{}, false, false
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 按工作区串行化置备。上面的检查只是快路径：没有它，两个成员同时点「和 Mika 开始」会
	// 双双没查到、双双插入。migration 172 的唯一索引也拦不住——它覆盖 (workspace_id,
	// owner_id, runtime_id, system_key)，不同 owner 或不同 runtime 就是不同的元组，结果工作区
	// 里会有两个活着的 Mika。
	if _, err := tx.Exec(r.Context(),
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"mika:"+workspaceID,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock the workspace agent")
		return db.Agent{}, false, false
	}
	// 在锁下复查：我们启动时还在途中的请求此刻可能已提交。
	if existing, err := qtx.GetAgentBySystemKey(r.Context(), db.GetAgentBySystemKeyParams{
		WorkspaceID: wsUUID,
		SystemKey:   pgtype.Text{String: service.MikaSystemKey, Valid: true},
	}); err == nil {
		return existing, false, true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to look up the workspace agent")
		return db.Agent{}, false, false
	}

	created, err := qtx.CreateSystemUserAgent(r.Context(), db.CreateSystemUserAgentParams{
		WorkspaceID:        wsUUID,
		Name:               service.MikaDefaultName,
		Instructions:       service.MikaDefaultInstructions(service.MikaDefaultName),
		Description:        mikaAgentDescription,
		AvatarUrl:          pgtype.Text{String: mikaAgentAvatarURL, Valid: true},
		RuntimeMode:        runtime.RuntimeMode,
		RuntimeID:          runtime.ID,
		Model:              pgtype.Text{String: strings.TrimSpace(req.Model), Valid: strings.TrimSpace(req.Model) != ""},
		Visibility:         mikaAgentVisibility,
		PermissionMode:     mikaAgentPermissionMode,
		MaxConcurrentTasks: mikaAgentMaxConcurrency,
		OwnerID:            parseUUID(userID),
		SystemKey:          pgtype.Text{String: service.MikaSystemKey, Valid: true},
	})
	if err != nil {
		slog.Warn("create mika agent failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create the workspace agent")
		return db.Agent{}, false, false
	}
	if testagents.Environment(auth.EnvironmentID()) {
		if err := testagents.Apply(r.Context(), tx, workspaceID, uuidToString(created.ID)); err != nil {
			writeError(w, 503, err.Error())
			return db.Agent{}, false, false
		}
		created, err = qtx.GetAgent(r.Context(), created.ID)
		if err != nil {
			writeError(w, 500, "读取内置测试 Agent 失败")
			return db.Agent{}, false, false
		}
	}
	// 工作区可调用：每个成员都能和 Mika 聊天、给它派活。
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, created.ID, parseUUID(userID), []targetSpec{
		{targetType: invocationTargetWorkspace, targetID: wsUUID},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent access")
		return db.Agent{}, false, false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	createdEvent, err := service.RecordDurableEventTx(r.Context(), qtx, h.buildAgentDomainEvent(protocol.EventAgentCreated, created, actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent event")
		return db.Agent{}, false, false
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent create")
		return db.Agent{}, false, false
	}
	h.publishEvent(createdEvent)
	slog.Info("mika agent created", append(logger.RequestAttrs(r), "agent_id", uuidToString(created.ID), "workspace_id", workspaceID)...)

	if runtime.Status == "online" {
		h.TaskService.ReconcileAgentStatus(r.Context(), created.ID)
		created, _ = h.Queries.GetAgent(r.Context(), created.ID)
	}
	return created, true, true
}

// writeMikaAgentResponse 组装并写出 mikaAgentResponse：Agent 详情 + 调用者的引导会话。
// 会话 get-or-create 失败时整体报错（见内联注释），已创建（201）与已存在（200）状态码分开。
func (h *Handler) writeMikaAgentResponse(w http.ResponseWriter, r *http.Request, agent db.Agent, workspaceID, userID, sessionTitle string, created bool) {
	resp := h.agentToResponse(agent)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, agent.ID); err != nil {
		slog.Warn("mika agent: load invocation targets failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
	}

	// 在会话步骤可能失败前就广播 Agent 创建事件，因为 Agent 本身已经提交，其它客户端的
	// Agent 列表在听到这个消息之前一直是旧的。
	session, err := h.getOrCreateMikaSession(r.Context(), agent, workspaceID, userID, sessionTitle)
	if err != nil {
		// 把会话省略掉报成功比失败更糟：调用方分不清「引导只做了一半」和「全做完了」，而
		// 那些提供「补齐引导」的 surface 都以 Agent 为键，它现在已存在。这里的每一步都幂等，
		// 重试会收敛——所以用错误明说，而不是交回一条半成品的流程。
		slog.Warn("mika agent: get-or-create onboarding session failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to open the Mika conversation")
		return
	}
	sessionResp := chatSessionToResponse(session)
	out := mikaAgentResponse{AgentResponse: resp, OnboardingSession: &sessionResp}

	if created {
		writeJSON(w, http.StatusCreated, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// getOrCreateMikaSession 返回调用者的 Mika 会话；还没有才创建。
//
// 按 (workspace, member) 用咨询锁串行化，而不是靠唯一索引——chat_session 没有可依仗的
// 唯一性约束，而本仓库不轻易加数据库外键/索引。查找按 (workspace, creator, agent)，刻意
// 不看标题：展示文案不能定义会话身份。
func (h *Handler) getOrCreateMikaSession(ctx context.Context, agent db.Agent, workspaceID, userID, title string) (db.ChatSession, error) {
	wsUUID := parseUUID(workspaceID)
	creatorUUID := parseUUID(userID)

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.ChatSession{}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	if _, err := tx.Exec(ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"mika-session:"+workspaceID+":"+userID,
	); err != nil {
		return db.ChatSession{}, err
	}
	// 与其它会话创建者相同的「工作区删除/创建协议」（#5219）：这里 FOR KEY SHARE 会与
	// DeleteWorkspace 的 FOR UPDATE 互斥。
	if _, err := qtx.LockWorkspaceForChatSessionCreate(ctx, wsUUID); err != nil {
		return db.ChatSession{}, err
	}

	existing, err := qtx.GetOldestActiveChatSessionForCreatorAgent(ctx, db.GetOldestActiveChatSessionForCreatorAgentParams{
		WorkspaceID: wsUUID,
		CreatorID:   creatorUUID,
		AgentID:     agent.ID,
	})
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return db.ChatSession{}, err
		}
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.ChatSession{}, err
	}

	created, err := qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
		ID:          dbid.NewV7(),
		WorkspaceID: wsUUID,
		AgentID:     agent.ID,
		CreatorID:   creatorUUID,
		Title:       strings.TrimSpace(title),
	})
	if err != nil {
		return db.ChatSession{}, err
	}
	created, err = qtx.MarkChatSessionExplicitlyCreated(ctx, created.ID)
	if err != nil {
		return db.ChatSession{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.ChatSession{}, err
	}
	return created, nil
}
