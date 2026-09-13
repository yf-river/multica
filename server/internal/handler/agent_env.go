// 本文件实现 agent custom_env（自定义环境变量）的专属管理端点：
// GET / PUT /api/agents/{id}/env。密钥值刻意不塞进通用 agent 资源；这里提供带
// 审计的读取（写 activity_log，只记键名绝不记值，审计写失败则拒绝吐明文）与
// 整体替换式更新（支持 **** 哨兵保留原值）。鉴权要求 actor 必须是人（拒绝任何
// agent 令牌，MUL-2600）且是 agent 所有者或工作区 owner/admin（MUL-5438）。
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// envSentinel 是 UI / 客户端在真实值位置上看到的掩码标记。PUT body 中某个键的
// 值等于它即表示「不要覆盖该键的现有值」——纵深防御：客户端把一份部分脱敏的
// map 原样往返回来时，不会把掩码占位符当真值保存而悄悄销毁真实密钥。
const envSentinel = "****"

// agentEnvActivityRevealed / agentEnvActivityUpdated 是两个 env 管理端点写入
// activity_log 的 `action` 常量。存在 `issue_id IS NULL` 的行上（env 访问不挂在
// 任何 issue 下）。所有者日后可查询——可查询的审计 UI 超出本 PR 范围，但现在就
// 落行，数据从第一天起就被捕获。工作区活动历史最终会浮出它们；目前仅供取证。
const (
	agentEnvActivityRevealed = "agent_env_revealed"
	agentEnvActivityUpdated  = "agent_env_updated"
)

// AgentEnvResponse 是专属 env 管理端点的线格式。与 `AgentResponse` 保持区分，
// 让密钥不会意外漏回通用 agent 资源——未来给 AgentResponse 加字段的重构不会
// 顺带把 env 值拖进去。
type AgentEnvResponse struct {
	AgentID   string            `json:"agent_id"`
	CustomEnv map[string]string `json:"custom_env"`
}

// UpdateAgentEnvRequest 是 `PUT /api/agents/{id}/env` 的线格式。只接受
// `custom_env`——面越少，越不易被滥用。
type UpdateAgentEnvRequest struct {
	CustomEnv map[string]string `json:"custom_env"`
}

// authorizeAgentEnv 强制 env 端点按请求级的鉴权契约：
//
//  1. actor 必须解析为成员（人）。任何由 agent 令牌署名的请求——即使其背后的
//     成员是工作区 owner，或正是目标 agent 所属的那个人——都被拒绝。这是修复
//     MUL-2600 所针对的冒充/横移风险的关键：在工作区里运行的 agent 不能借宿主
//     的 owner 凭据去读另一个 agent 的密钥。
//  2. 成员必须是工作区 owner/admin，或该 agent 的本人所有者（MUL-5438）。
//
// 规则 2 过去只认工作区角色，这让 env 成为 agent 权限模型里唯一无视 agent
// 所有权的端点：canManageAgent 已允许所有者更新/归档/恢复，canViewAgentSecrets
// 已允许所有者读 mcp_config。最不对称的是创建路径——POST /api/agents 允许任何
// 成员提交 custom_env——于是成员可以把密钥写进自己的 agent，之后却再也读不出
// 或轮换不了。
//
// 成功时返回加载的 agent 与鉴权过的成员。所有非 2xx 分支自己写好响应并返回
// ok=false。
func (h *Handler) authorizeAgentEnv(w http.ResponseWriter, r *http.Request) (db.Agent, db.Member, bool) {
	agentID := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, agentID)
	if !ok {
		return db.Agent{}, db.Member{}, false
	}

	workspaceID := uuidToString(agent.WorkspaceID)
	userID := requestUserID(r)

	// 先于一切拒绝 agent actor。resolveActor 只有在 X-Agent-ID 与合法 X-Task-ID
	// 同时存在、且该任务确实属于该 agent 时才返回 "agent"——所以这道守卫是精确
	// 的，成员自己伪造请求头骗不过去。
	actorType, _ := h.resolveActor(r, userID, workspaceID)
	if actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not access env management endpoints")
		return db.Agent{}, db.Member{}, false
	}

	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "agent not found", "owner", "admin", "member")
	if !ok {
		return db.Agent{}, db.Member{}, false
	}
	if !canManageAgentEnv(agent, member) {
		writeError(w, http.StatusForbidden, "only the agent owner or a workspace owner/admin can manage this agent's env")
		return db.Agent{}, db.Member{}, false
	}

	return agent, member, true
}

// canManageAgentEnv 是 env 鉴权规则的纯函数半部：工作区 owner/admin，或拥有该
// agent 的人。镜像 canManageAgent（update/archive），让能管理 agent 的成员也
// 能轮换它的密钥。
//
// owner 比较刻意针对 member.UserID 而非 requestUserID(r)。agent.owner_id 可空
// （迁移 001），uuidToString 会把 NULL UUID 渲染成 ""，所以拿它跟同样可能为空的
// 裸请求头比较，会让每个 NULL-owner 的 agent 对任何人都可读。member.UserID 在
// 成员查询成功后才存在，下面的空 owner 守卫再从另一侧把这个洞补上。
func canManageAgentEnv(agent db.Agent, member db.Member) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	ownerID := uuidToString(agent.OwnerID)
	return ownerID != "" && ownerID == uuidToString(member.UserID)
}

// GetAgentEnv 在 authorizeAgentEnv 门控后返回单个 agent 的明文 custom_env map。
// 每次成功读取都往 activity_log 写一条 `agent_env_revealed`（只记键名，绝不记
// 值），让工作区 owner 有「谁看过哪些键」的轨迹。
//
// 审计语义失败关闭：审计行持久化不了就绝不吐明文。记录不了的 reveal 与未审计的
// reveal 无法区分，会悄悄破坏 MUL-2600 承诺的「每次 reveal 都留下可查询轨迹」。
// 在这里撞上 500 的运维能看到审计日志故障并去修；另一条路——悄悄把密钥交出去
// ——是不可见的。
func (h *Handler) GetAgentEnv(w http.ResponseWriter, r *http.Request) {
	agent, member, ok := h.authorizeAgentEnv(w, r)
	if !ok {
		return
	}

	customEnv := unmarshalCustomEnv(agent)

	revealedKeys := sortedKeys(customEnv)
	details, _ := json.Marshal(map[string]any{
		"agent_id":      uuidToString(agent.ID),
		"agent_name":    agent.Name,
		"revealed_keys": revealedKeys,
		"key_count":     len(revealedKeys),
	})
	if _, err := h.Queries.CreateActivity(r.Context(), db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: agent.WorkspaceID,
		IssueID:     pgtype.UUID{}, // env access is not tied to an issue
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     parseUUID(uuidToString(member.UserID)),
		Action:      agentEnvActivityRevealed,
		Details:     details,
	}); err != nil {
		slog.Error("agent_env_revealed audit write failed; refusing to serve plaintext",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "audit log write failed; refusing to serve env without a recorded reveal")
		return
	}

	writeJSON(w, http.StatusOK, AgentEnvResponse{
		AgentID:   uuidToString(agent.ID),
		CustomEnv: customEnv,
	})
}

// UpdateAgentEnv 整体替换 agent 的 custom_env。**** 掩码按键生效：任何等于
// envSentinel 的值都当作「该键保留现有值」，防止这种场景——UI 抓取 env、只对部分
// 值脱敏，然后草率地把整张 map PUT 回来。直接写入的话，真实密钥的位置会被存成
// 字面量 `****`。审计日志记录新旧键集合之间的对称差，但永不记值。
//
// 持久化 + 审计跑在同一个数据库事务里，同生共死。审计写故障不会在磁盘上留下
// 未审计的 env 变更；持久化失败也不会留下一条声称发生却未发生的幽灵审计行。
func (h *Handler) UpdateAgentEnv(w http.ResponseWriter, r *http.Request) {
	agent, member, ok := h.authorizeAgentEnv(w, r)
	if !ok {
		return
	}

	var req UpdateAgentEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CustomEnv == nil {
		req.CustomEnv = map[string]string{}
	}

	existing := unmarshalCustomEnv(agent)
	merged, audit := mergeAgentEnv(existing, req.CustomEnv)

	envBytes, err := json.Marshal(merged)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode env")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Error("agent_env update: begin tx failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to update env")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	updated, err := qtx.UpdateAgentCustomEnv(r.Context(), db.UpdateAgentCustomEnvParams{
		ID:        agent.ID,
		CustomEnv: envBytes,
	})
	if err != nil {
		slog.Warn("update agent custom_env failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to update env")
		return
	}

	auditDetails := map[string]any{
		"agent_id":       uuidToString(agent.ID),
		"agent_name":     agent.Name,
		"added_keys":     audit.added,
		"removed_keys":   audit.removed,
		"changed_keys":   audit.changed,
		"preserved_keys": audit.preserved,
	}
	details, _ := json.Marshal(auditDetails)
	if _, err := qtx.CreateActivity(r.Context(), db.CreateActivityParams{
		ID:          dbid.NewV7(),
		WorkspaceID: agent.WorkspaceID,
		IssueID:     pgtype.UUID{},
		ActorType:   pgtype.Text{String: "member", Valid: true},
		ActorID:     parseUUID(uuidToString(member.UserID)),
		Action:      agentEnvActivityUpdated,
		Details:     details,
	}); err != nil {
		slog.Error("agent_env_updated audit write failed; rolling back update",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "audit log write failed; env update rolled back")
		return
	}
	actorType, actorID := h.resolveActor(r, uuidToString(member.UserID), uuidToString(agent.WorkspaceID))
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, h.buildAgentDomainEvent(protocol.EventAgentStatus, updated, actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent event")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("agent_env update: tx commit failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to update env")
		return
	}

	// 广播 agent:status 更新，让已连接的客户端刷新「已配置 N 个变量」指示器。
	// 负载是脱敏后的 AgentResponse——绝不发送 env 值。技能也要重载，否则广播会
	// 让订阅者误以为该 agent 没有技能（#3459）。
	resp := h.agentToResponse(updated)
	if err := h.attachAgentSkills(r.Context(), &resp, updated.ID); err != nil {
		slog.Warn("load agent skills after env update failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(updated.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	h.publishEvent(updatedEvent)

	writeJSON(w, http.StatusOK, AgentEnvResponse{
		AgentID:   uuidToString(updated.ID),
		CustomEnv: merged,
	})
}

// envAudit 总结 agent 既有 env 与新的 env 之间的差异，按类别拆开，让审计者能
// 精确重建一次操作碰过哪些键，又不泄漏值。所有切片都排序，让 activity 行内容
// 对测试与下游工具确定性可复现。
type envAudit struct {
	added     []string
	removed   []string
	changed   []string
	preserved []string
}

// mergeAgentEnv 应用 **** 哨兵规则，返回最终要持久化的 map 以及哪些键变化过的
// 审计摘要。行为：
//   - 请求含该键、值为 "****"、键在 `existing` 中 → 保留原值，计入 preserved
//   - 请求含该键、值为 "****"、键不在 `existing` 中 → 丢弃该键（字面量 "****"
//     永远不是合法存储值）
//   - 请求含该键、值 != "****"、existing 已有同值 → no-op（不计）
//   - 请求含该键、值 != "****"、与 existing 不同 → 写入新值，计入 changed
//   - 请求含该键、值 != "****"、键不在 existing 中 → 写入新值，计入 added
//   - 键在 existing 但请求没带 → removed
func mergeAgentEnv(existing, request map[string]string) (map[string]string, envAudit) {
	merged := make(map[string]string, len(request))
	audit := envAudit{}

	for k, v := range request {
		if v == envSentinel {
			if old, ok := existing[k]; ok {
				merged[k] = old
				audit.preserved = append(audit.preserved, k)
			}
			// else（existing 无此键）：丢弃。字面量 "****" 永远不作为存储值落库。
			continue
		}
		if old, ok := existing[k]; ok {
			if old == v {
				merged[k] = v
				continue
			}
			merged[k] = v
			audit.changed = append(audit.changed, k)
			continue
		}
		merged[k] = v
		audit.added = append(audit.added, k)
	}

	for k := range existing {
		if _, ok := request[k]; !ok {
			audit.removed = append(audit.removed, k)
		}
	}

	sort.Strings(audit.added)
	sort.Strings(audit.removed)
	sort.Strings(audit.changed)
	sort.Strings(audit.preserved)
	return merged, audit
}

// unmarshalCustomEnv 把 agent 存储的 custom_env 字节解码成 map，出错时返回空
// （绝不返回 nil）map，让调用方可以安全迭代。
func unmarshalCustomEnv(a db.Agent) map[string]string {
	out := map[string]string{}
	if len(a.CustomEnv) == 0 {
		return out
	}
	if err := json.Unmarshal(a.CustomEnv, &out); err != nil {
		slog.Warn("failed to unmarshal agent custom_env", "agent_id", uuidToString(a.ID), "error", err)
		return map[string]string{}
	}
	if out == nil {
		return map[string]string{}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
