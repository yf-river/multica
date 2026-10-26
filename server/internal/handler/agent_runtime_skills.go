// 本文件实现「禁用某个 runtime 本地技能」的 per-agent 覆盖：
// agent 会从当前分配的本地 runtime 继承一批运行时技能，这里按 (runtime, provider,
// root, key) 精确标识单个技能并落一条禁用以外的白名单状态（disabled_runtime_skills）。
// 处理逻辑含身份归一化/越界校验、事务内加锁复查 runtime 归属、落库后广播
// agent:status 让其它客户端刷新缓存。
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// maxRuntimeSkillKeyLength 是 runtime 技能 key 的长度上界（也复用于 name）。
const maxRuntimeSkillKeyLength = 512

// DisabledRuntimeSkill 标识一个 agent 不得继承的 runtime-local 技能。RuntimeID
// 把选择限定到单台机器/单个 runtime，另一台 runtime 上的同名技能不受影响。
type DisabledRuntimeSkill struct {
	RuntimeID string `json:"runtime_id"`
	Provider  string `json:"provider"`
	Root      string `json:"root"`
	Key       string `json:"key"`
	Name      string `json:"name,omitempty"`
	Plugin    string `json:"plugin,omitempty"`
}

// decodeDisabledRuntimeSkills 把存储的 disabled_runtime_skills 字节解码为切片，
// 空/坏数据一律回落为空切片。
func decodeDisabledRuntimeSkills(raw []byte) []DisabledRuntimeSkill {
	if len(raw) == 0 {
		return []DisabledRuntimeSkill{}
	}
	var skills []DisabledRuntimeSkill
	if err := json.Unmarshal(raw, &skills); err != nil || skills == nil {
		return []DisabledRuntimeSkill{}
	}
	return skills
}

// disabledRuntimeSkillsFor 从全部禁用列表中过滤出指定 (runtime, provider) 的项。
func disabledRuntimeSkillsFor(raw []byte, runtimeID, provider string) []DisabledRuntimeSkill {
	all := decodeDisabledRuntimeSkills(raw)
	result := make([]DisabledRuntimeSkill, 0, len(all))
	for _, skill := range all {
		if skill.RuntimeID == runtimeID && skill.Provider == provider {
			result = append(result, skill)
		}
	}
	return result
}

// normalizeRuntimeSkillIdentity 校验并归一化 runtime 技能的 (root, key, plugin)
// 三元组：key 非空且不越界、清除路径穿越（..、绝对路径）；root 为 provider/
// universal 时 plugin 置空，为 plugin 时必须有 plugin 名。返回 ok=false 表示
// 身份非法。
func normalizeRuntimeSkillIdentity(root, key, plugin string) (string, string, string, bool) {
	root = strings.TrimSpace(root)
	key = strings.TrimSpace(key)
	plugin = strings.TrimSpace(plugin)
	if len(key) == 0 || len(key) > maxRuntimeSkillKeyLength {
		return "", "", "", false
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(key)))
	if cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", "", "", false
	}
	switch root {
	case "provider", "universal":
		plugin = ""
	case "plugin":
		if plugin == "" {
			return "", "", "", false
		}
	default:
		return "", "", "", false
	}
	return root, cleaned, plugin, true
}

// sameDisabledRuntimeSkill 判断两条禁用项是否指同一技能（去重比较用）。
func sameDisabledRuntimeSkill(a, b DisabledRuntimeSkill) bool {
	return a.RuntimeID == b.RuntimeID && a.Provider == b.Provider &&
		a.Root == b.Root && a.Key == b.Key && a.Plugin == b.Plugin
}

// SetAgentRuntimeSkillEnabled 为「从 agent 当前分配的本地 runtime 发现的某个
// 技能」持久化一条 per-agent 覆盖：enabled=false 把该技能加入禁用列表，
// enabled=true 则从列表移除（重新继承）。
func (h *Handler) SetAgentRuntimeSkillEnabled(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, agentID)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	var req struct {
		RuntimeID string `json:"runtime_id"`
		Root      string `json:"root"`
		Key       string `json:"key"`
		Name      string `json:"name"`
		Plugin    string `json:"plugin"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "runtime_id, root, key, and enabled are required")
		return
	}
	runtimeID, ok := parseUUIDOrBadRequest(w, req.RuntimeID, "runtime_id")
	if !ok {
		return
	}
	if !agent.RuntimeID.Valid || agent.RuntimeID != runtimeID {
		writeError(w, http.StatusConflict, "agent is no longer assigned to this runtime")
		return
	}
	rt, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if err != nil || rt.WorkspaceID != agent.WorkspaceID {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	if rt.RuntimeMode != "local" || (rt.Provider != "codex" && rt.Provider != "claude" && rt.Provider != "codebuddy") {
		writeError(w, http.StatusBadRequest, "runtime skill controls are only supported for codex, claude and codebuddy")
		return
	}
	root, key, plugin, valid := normalizeRuntimeSkillIdentity(req.Root, req.Key, req.Plugin)
	if !valid || (root == "plugin" && rt.Provider != "claude" && rt.Provider != "codebuddy") {
		writeError(w, http.StatusBadRequest, "invalid runtime skill identity")
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > maxRuntimeSkillKeyLength {
		writeError(w, http.StatusBadRequest, "invalid runtime skill name")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	locked, err := qtx.GetAgentForUpdate(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent")
		return
	}
	if !locked.RuntimeID.Valid || locked.RuntimeID != runtimeID {
		writeError(w, http.StatusConflict, "agent is no longer assigned to this runtime")
		return
	}

	target := DisabledRuntimeSkill{
		RuntimeID: req.RuntimeID,
		Provider:  rt.Provider,
		Root:      root,
		Key:       key,
		Name:      name,
		Plugin:    plugin,
	}
	current := decodeDisabledRuntimeSkills(locked.DisabledRuntimeSkills)
	next := make([]DisabledRuntimeSkill, 0, len(current)+1)
	for _, skill := range current {
		if sameDisabledRuntimeSkill(skill, target) {
			continue
		}
		next = append(next, skill)
	}
	if !*req.Enabled {
		next = append(next, target)
	}
	payload, err := json.Marshal(next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode runtime skill settings")
		return
	}
	if _, err := qtx.UpdateAgentDisabledRuntimeSkills(r.Context(), db.UpdateAgentDisabledRuntimeSkillsParams{
		ID:                    locked.ID,
		DisabledRuntimeSkills: payload,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update runtime skill")
		return
	}
	locked.DisabledRuntimeSkills = payload
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(locked.WorkspaceID))
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, h.buildAgentDomainEvent(protocol.EventAgentStatus, locked, actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record agent event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}

	// 广播更新后的 agent，让每个其它打开的 web/desktop/mobile 客户端作废缓存并
	// 拿到新的 disabled_runtime_skills 状态。工作区技能开关在 skill.go 里经
	// writeUpdatedAgentSkills 做同样的事；实时层靠 "agent:status" 事件作废
	// workspaceKeys.agents。没有这一步就只有发起标签页刷新，其它客户端会一直
	// 显示过期的开关状态。
	locked.DisabledRuntimeSkills = payload
	resp := h.agentToResponse(locked)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, locked.ID); err != nil {
		slog.Warn("runtime skill toggle: load invocation targets for broadcast failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", agentID)...)
	}
	// agentToResponse 把 Skills 初始化为 []；重载 junction-table 绑定，让广播如实
	// 反映现状，而不是向其它客户端暗示该 agent 的技能被清空了（#3459）。
	if err := h.attachAgentSkills(r.Context(), &resp, locked.ID); err != nil {
		slog.Warn("runtime skill toggle: load agent skills for broadcast failed",
			append(logger.RequestAttrs(r), "error", err, "agent_id", agentID)...)
	}
	h.publishEvent(updatedEvent)

	w.WriteHeader(http.StatusNoContent)
}
