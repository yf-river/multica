// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（life.go）实现"人生记忆/陪伴档案"核心读写接口：companion 配置、
// 人生记忆（memory）的创建/纠正/确认/降级/归档/永久删除，以及证据链与
// 修订历史的维护。DeleteLifeMemory 会沿溯源图级联清理所有派生记录，并
// 写入"遗忘墓碑"，保证删除彻底、可追溯。
package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// createLifeMemoryRevision 为一条记忆写一条修订记录：先取下一修订号，
// 再整行快照当前记忆内容（含变更类型/原因与操作者），供后续追溯。
func createLifeMemoryRevision(ctx context.Context, q *db.Queries, memory db.LifeMemory, changeType, reason, actorType string, actorID pgtype.UUID) error {
	revision, err := q.GetNextLifeMemoryRevision(ctx, memory.ID)
	if err != nil {
		return err
	}
	_, err = q.CreateLifeMemoryRevision(ctx, db.CreateLifeMemoryRevisionParams{
		MemoryID: memory.ID, Revision: revision, Kind: memory.Kind, Status: memory.Status,
		Content: memory.Content, Confidence: memory.Confidence, Urgency: memory.Urgency,
		Uncertainty: memory.Uncertainty, Scope: memory.Scope, ChangeType: changeType,
		ChangeReason: reason, ChangedByType: actorType, ChangedByID: actorID,
	})
	return err
}

// lifeMemoryEvidenceExcerptMaxRunes 是证据摘录允许的最大长度（按 rune 计）。
const lifeMemoryEvidenceExcerptMaxRunes = 2_000

// lifeMemoryKindRank 给出各类记忆的"可信等级"排序，数值越大越接近事实、
// 越不易被降级；弱信号等低等级记忆不能降级到更高等级。
var lifeMemoryKindRank = map[string]int{
	"current_expression": 0,
	"weak_signal":        1,
	"understanding":      2,
	"fact":               3,
	"plan":               3,
	"commitment":         4,
}

// lifeRequestScope 是"人生"相关接口的公共请求范围：请求者（成员）的用户
// 与所在 workspace。
type lifeRequestScope struct {
	workspaceID pgtype.UUID
	userID      pgtype.UUID
}

// requireLifeRequestScope 解析并校验请求范围：请求必须来自人类成员（life 场景仅对成员开放），否则写错误并返回 ok=false。
func (h *Handler) requireLifeRequestScope(w http.ResponseWriter, r *http.Request) (lifeRequestScope, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return lifeRequestScope{}, false
	}
	workspaceIDText := h.resolveWorkspaceID(r)
	actorType, _ := h.resolveActor(r, userID, workspaceIDText)
	if actorType != "member" {
		writeError(w, http.StatusForbidden, "life settings require a member")
		return lifeRequestScope{}, false
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDText, "workspace id")
	if !ok {
		return lifeRequestScope{}, false
	}
	parsedUserID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return lifeRequestScope{}, false
	}
	return lifeRequestScope{workspaceID: workspaceID, userID: parsedUserID}, true
}

// companionProfileResponse 是 companion 配置的对外 JSON 结构（ID 均转字符串）。
type companionProfileResponse struct {
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	AgentID     string `json:"agent_id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// companionProfileToResponse 把数据库 companion profile 转成对外结构。
func companionProfileToResponse(profile db.CompanionProfile) companionProfileResponse {
	return companionProfileResponse{
		WorkspaceID: uuidToString(profile.WorkspaceID),
		UserID:      uuidToString(profile.UserID),
		AgentID:     uuidToString(profile.AgentID),
		CreatedAt:   timestampToString(profile.CreatedAt),
		UpdatedAt:   timestampToString(profile.UpdatedAt),
	}
}

// GetCompanionProfile 返回用户当前的 companion（人生伴侣 AI）配置；
// 从未配置时 profile 为 null。
func (h *Handler) GetCompanionProfile(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	profile, err := h.Queries.GetCompanionProfile(r.Context(), db.GetCompanionProfileParams{
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"profile": nil})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load companion")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": companionProfileToResponse(profile)})
}

// UpsertCompanionProfile 设置/更换用户的 companion agent：校验 agent 存在、未归档、用户可访问，且不影响已有观察者；事务内应用 agent 相关运行时选择，并确保 companion 所需的人生能力默认项已初始化。
func (h *Handler) UpsertCompanionProfile(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), agentID)
	if err != nil || agent.WorkspaceID != scope.workspaceID || agent.ArchivedAt.Valid {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if !h.requirePersonalAgentAccess(w, r, agent, "member", uuidToString(scope.userID), uuidToString(scope.workspaceID), "you do not have access to this agent") {
		return
	}
	observers, err := h.Queries.ListLifeObservers(r.Context(), db.ListLifeObserversParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate life runtimes")
		return
	}
	agentIDs := []pgtype.UUID{agentID}
	for _, observer := range observers {
		observerAgent, loadErr := h.Queries.GetAgent(r.Context(), observer.AgentID)
		if loadErr != nil || observerAgent.ArchivedAt.Valid {
			writeError(w, http.StatusConflict, "observer agent is unavailable")
			return
		}
		agentIDs = append(agentIDs, observerAgent.ID)
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start companion update")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := h.Queries.WithTx(tx)
	if _, err := h.applyLifeAgentSelections(r.Context(), q, scope.workspaceID, agentIDs); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	profile, err := q.UpsertCompanionProfile(r.Context(), db.UpsertCompanionProfileParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, AgentID: agentID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save companion")
		return
	}
	if err := ensureCompanionLifeDefaultsWithQueries(r.Context(), q, scope); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to initialize companion life capabilities")
		return
	}
	event, err := recordLifeChangedTx(r.Context(), q, scope, "member", scope.userID, "companion_profile", uuidToString(scope.userID), "updated", map[string]any{"agent_id": uuidToString(profile.AgentID)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record companion update event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit companion update")
		return
	}
	h.publishLifeEvents(event)
	writeJSON(w, http.StatusOK, map[string]any{"profile": companionProfileToResponse(profile)})
}

// lifeMemoryEvidenceResponse 是记忆证据的对外 JSON 结构。
type lifeMemoryEvidenceResponse struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Excerpt    string `json:"excerpt"`
	Stance     string `json:"stance"`
	ObservedAt string `json:"observed_at"`
}

// lifeMemoryResponse 是记忆（含证据列表）的对外 JSON 结构。
type lifeMemoryResponse struct {
	ID            string                       `json:"id"`
	Kind          string                       `json:"kind"`
	Status        string                       `json:"status"`
	Content       string                       `json:"content"`
	Confidence    float64                      `json:"confidence"`
	Urgency       float64                      `json:"urgency"`
	Uncertainty   string                       `json:"uncertainty"`
	ValidFrom     *string                      `json:"valid_from"`
	ValidTo       *string                      `json:"valid_to"`
	ConfirmedAt   *string                      `json:"confirmed_at"`
	CreatedByType string                       `json:"created_by_type"`
	CreatedByID   string                       `json:"created_by_id"`
	CreatedAt     string                       `json:"created_at"`
	UpdatedAt     string                       `json:"updated_at"`
	Evidence      []lifeMemoryEvidenceResponse `json:"evidence"`
}

// lifeMemoryToResponse 加载某条记忆的全部证据，并把记忆整理成对外结构。
func (h *Handler) lifeMemoryToResponse(r *http.Request, memory db.LifeMemory) (lifeMemoryResponse, error) {
	evidence, err := h.Queries.ListLifeMemoryEvidence(r.Context(), memory.ID)
	if err != nil {
		return lifeMemoryResponse{}, err
	}
	items := make([]lifeMemoryEvidenceResponse, 0, len(evidence))
	for _, item := range evidence {
		items = append(items, lifeMemoryEvidenceResponse{
			SourceType: item.SourceType,
			SourceID:   uuidToString(item.SourceID),
			Excerpt:    item.Excerpt,
			Stance:     item.Stance,
			ObservedAt: timestampToString(item.ObservedAt),
		})
	}
	return lifeMemoryResponse{
		ID:            uuidToString(memory.ID),
		Kind:          memory.Kind,
		Status:        memory.Status,
		Content:       memory.Content,
		Confidence:    memory.Confidence,
		Urgency:       memory.Urgency,
		Uncertainty:   memory.Uncertainty,
		ValidFrom:     timestampToPtr(memory.ValidFrom),
		ValidTo:       timestampToPtr(memory.ValidTo),
		ConfirmedAt:   timestampToPtr(memory.ConfirmedAt),
		CreatedByType: memory.CreatedByType,
		CreatedByID:   uuidToString(memory.CreatedByID),
		CreatedAt:     timestampToString(memory.CreatedAt),
		UpdatedAt:     timestampToString(memory.UpdatedAt),
		Evidence:      items,
	}, nil
}

// validLifeMemoryKind 判断记忆类型是否在允许范围内。
func validLifeMemoryKind(kind string) bool {
	_, ok := lifeMemoryKindRank[kind]
	return ok
}

// validLifeMemoryStatus 判断记忆状态是否为合法取值之一。
func validLifeMemoryStatus(status string) bool {
	switch status {
	case "candidate", "confirmed", "archived":
		return true
	default:
		return false
	}
}

// validateLifeMemoryContent 校验记忆的公共字段（类型/内容非空/置信度与紧迫度取值在 0~1 之间）；不合法时写 400 并返回 false。
func validateLifeMemoryContent(w http.ResponseWriter, kind, content string, confidence, urgency float64) bool {
	if !validLifeMemoryKind(kind) {
		writeError(w, http.StatusBadRequest, "invalid memory kind")
		return false
	}
	if strings.TrimSpace(content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return false
	}
	if confidence < 0 || confidence > 1 {
		writeError(w, http.StatusBadRequest, "confidence must be between 0 and 1")
		return false
	}
	if urgency < 0 || urgency > 1 {
		writeError(w, http.StatusBadRequest, "urgency must be between 0 and 1")
		return false
	}
	return true
}

// parseLifeOptionalTime 解析可选的 RFC3339 时间字段：空串返回零值（视为未填），非空则必须是合法 RFC3339，否则写 400。
func parseLifeOptionalTime(w http.ResponseWriter, raw, field string) (pgtype.Timestamptz, bool) {
	if strings.TrimSpace(raw) == "" {
		return pgtype.Timestamptz{}, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, field+" must be RFC3339")
		return pgtype.Timestamptz{}, false
	}
	return pgtype.Timestamptz{Time: parsed, Valid: true}, true
}

// ListLifeMemories 列出记忆，可用 ?status= 过滤；每条都附带其证据。
func (h *Handler) ListLifeMemories(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !validLifeMemoryStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	var memories []db.LifeMemory
	var err error
	if status == "" {
		memories, err = h.Queries.ListLifeMemories(r.Context(), db.ListLifeMemoriesParams{
			WorkspaceID: scope.workspaceID,
			UserID:      scope.userID,
		})
	} else {
		memories, err = h.Queries.ListLifeMemoriesByStatus(r.Context(), db.ListLifeMemoriesByStatusParams{
			WorkspaceID: scope.workspaceID,
			UserID:      scope.userID,
			Status:      status,
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list memories")
		return
	}
	items := make([]lifeMemoryResponse, 0, len(memories))
	for _, memory := range memories {
		item, err := h.lifeMemoryToResponse(r, memory)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load memory evidence")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": items})
}

// lifeMemoryEvidenceRequest 是创建记忆时单条证据的请求体。
type lifeMemoryEvidenceRequest struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Excerpt    string `json:"excerpt"`
	Stance     string `json:"stance"`
}

// createLifeMemoryRequest 是创建/更新记忆的请求体。
type createLifeMemoryRequest struct {
	Kind        string                      `json:"kind"`
	Content     string                      `json:"content"`
	Confidence  float64                     `json:"confidence"`
	Urgency     float64                     `json:"urgency"`
	Uncertainty string                      `json:"uncertainty"`
	ValidFrom   string                      `json:"valid_from"`
	ValidTo     string                      `json:"valid_to"`
	Evidence    []lifeMemoryEvidenceRequest `json:"evidence"`
}

// resolvedLifeMemoryEvidence 是经过校验/落库前解析好的证据（source_id 已是
// UUID，且带观察时间与来源记忆 ID 等派生信息）。
type resolvedLifeMemoryEvidence struct {
	sourceType     string
	sourceID       pgtype.UUID
	excerpt        string
	stance         string
	observedAt     pgtype.Timestamptz
	sourceMemoryID pgtype.UUID
}

// resolveLifeMemoryEvidence 解析并校验一条证据：source_id 必须是当前用户workspace 内真实存在的聊天消息或记忆（错误即 400），摘录为空时自动回退到原内容；记忆作为证据时同时记录来源记忆 ID，供后面建立派生依赖。
func (h *Handler) resolveLifeMemoryEvidence(w http.ResponseWriter, r *http.Request, q *db.Queries, scope lifeRequestScope, req lifeMemoryEvidenceRequest) (resolvedLifeMemoryEvidence, bool) {
	sourceID, ok := parseUUIDOrBadRequest(w, req.SourceID, "evidence.source_id")
	if !ok {
		return resolvedLifeMemoryEvidence{}, false
	}
	excerpt := strings.TrimSpace(req.Excerpt)
	stance := strings.TrimSpace(req.Stance)
	if stance == "" {
		stance = "supports"
	}
	if stance != "supports" && stance != "contradicts" && stance != "context" {
		writeError(w, http.StatusBadRequest, "evidence.stance must be supports, contradicts or context")
		return resolvedLifeMemoryEvidence{}, false
	}
	if len([]rune(excerpt)) > lifeMemoryEvidenceExcerptMaxRunes {
		writeError(w, http.StatusBadRequest, "evidence excerpt is too long")
		return resolvedLifeMemoryEvidence{}, false
	}
	switch req.SourceType {
	case "chat_message":
		row, err := q.GetChatMessageForLifeEvidence(r.Context(), db.GetChatMessageForLifeEvidenceParams{
			ID:          sourceID,
			WorkspaceID: scope.workspaceID,
			CreatorID:   scope.userID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "chat evidence not found")
			return resolvedLifeMemoryEvidence{}, false
		}
		if excerpt == "" {
			excerpt = row.ChatMessage.Content
		}
		return resolvedLifeMemoryEvidence{
			sourceType: req.SourceType,
			sourceID:   sourceID,
			excerpt:    excerpt,
			observedAt: row.ChatMessage.CreatedAt,
			stance:     stance,
		}, true
	case "memory":
		memory, err := q.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
			ID:          sourceID,
			WorkspaceID: scope.workspaceID,
			UserID:      scope.userID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "memory evidence not found")
			return resolvedLifeMemoryEvidence{}, false
		}
		if excerpt == "" {
			excerpt = memory.Content
		}
		return resolvedLifeMemoryEvidence{
			sourceType:     req.SourceType,
			sourceID:       sourceID,
			excerpt:        excerpt,
			observedAt:     memory.UpdatedAt,
			stance:         stance,
			sourceMemoryID: sourceID,
		}, true
	default:
		writeError(w, http.StatusBadRequest, "unsupported evidence source_type")
		return resolvedLifeMemoryEvidence{}, false
	}
}

// CreateLifeMemory 以成员身份创建一条记忆（由 createLifeMemory 承载）。
func (h *Handler) CreateLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	h.createLifeMemory(w, r, scope, "member", scope.userID)
}

// createLifeMemory 创建一条记忆：校验内容与每条证据后，在事务里写入记忆、
// 首版修订、证据（及记忆间依赖），再记录变更事件。createdByType/ID 说明
// 创建者（成员或 agent）。
func (h *Handler) createLifeMemory(w http.ResponseWriter, r *http.Request, scope lifeRequestScope, createdByType string, createdByID pgtype.UUID) {
	var req createLifeMemoryRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if !validateLifeMemoryContent(w, req.Kind, req.Content, req.Confidence, req.Urgency) {
		return
	}
	if len(req.Evidence) == 0 {
		writeError(w, http.StatusBadRequest, "at least one evidence item is required")
		return
	}
	validFrom, ok := parseLifeOptionalTime(w, req.ValidFrom, "valid_from")
	if !ok {
		return
	}
	validTo, ok := parseLifeOptionalTime(w, req.ValidTo, "valid_to")
	if !ok {
		return
	}
	if validFrom.Valid && validTo.Valid && validTo.Time.Before(validFrom.Time) {
		writeError(w, http.StatusBadRequest, "valid_to must not be before valid_from")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	resolved := make([]resolvedLifeMemoryEvidence, 0, len(req.Evidence))
	for _, item := range req.Evidence {
		evidence, ok := h.resolveLifeMemoryEvidence(w, r, qtx, scope, item)
		if !ok {
			return
		}
		resolved = append(resolved, evidence)
	}
	memory, err := qtx.CreateLifeMemory(r.Context(), db.CreateLifeMemoryParams{
		WorkspaceID:   scope.workspaceID,
		UserID:        scope.userID,
		CreatedByType: createdByType,
		CreatedByID:   createdByID,
		Kind:          req.Kind,
		Content:       strings.TrimSpace(req.Content),
		Confidence:    req.Confidence,
		Urgency:       req.Urgency,
		Uncertainty:   strings.TrimSpace(req.Uncertainty),
		ValidFrom:     validFrom,
		ValidTo:       validTo,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, memory, "created", "由用户建立", createdByType, createdByID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create memory revision")
		return
	}
	for _, item := range resolved {
		if _, err := qtx.CreateLifeMemoryEvidence(r.Context(), db.CreateLifeMemoryEvidenceParams{
			MemoryID:   memory.ID,
			SourceType: item.sourceType,
			SourceID:   item.sourceID,
			Excerpt:    item.excerpt,
			ObservedAt: item.observedAt,
			Stance:     item.stance,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create memory evidence")
			return
		}
		if item.sourceMemoryID.Valid {
			if err := qtx.CreateLifeMemoryDependency(r.Context(), db.CreateLifeMemoryDependencyParams{
				SourceMemoryID:  item.sourceMemoryID,
				DerivedMemoryID: memory.ID,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to link memory dependency")
				return
			}
		}
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, createdByType, createdByID,
		"memory", uuidToString(memory.ID), "created", map[string]any{"kind": memory.Kind})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory")
		return
	}
	h.publishLifeEvents(event)
	response, err := h.lifeMemoryToResponse(r, memory)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

// loadLifeMemory 按 URL 里的 memoryId 加载属于当前用户的一条记忆。
func (h *Handler) loadLifeMemory(w http.ResponseWriter, r *http.Request, scope lifeRequestScope) (db.LifeMemory, bool) {
	memoryID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memoryId"), "memory id")
	if !ok {
		return db.LifeMemory{}, false
	}
	memory, err := h.Queries.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
		ID:          memoryID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeEntityLoadError(w, err, "memory", "memory_id", chi.URLParam(r, "memoryId"))
		return db.LifeMemory{}, false
	}
	return memory, true
}

// UpdateLifeMemory 纠正一条记忆的内容/类型/置信度等，并写一条 corrected 修订。
func (h *Handler) UpdateLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memory, ok := h.loadLifeMemory(w, r, scope)
	if !ok {
		return
	}
	var req createLifeMemoryRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if !validateLifeMemoryContent(w, req.Kind, req.Content, req.Confidence, req.Urgency) {
		return
	}
	validFrom, ok := parseLifeOptionalTime(w, req.ValidFrom, "valid_from")
	if !ok {
		return
	}
	validTo, ok := parseLifeOptionalTime(w, req.ValidTo, "valid_to")
	if !ok {
		return
	}
	if validFrom.Valid && validTo.Valid && validTo.Time.Before(validFrom.Time) {
		writeError(w, http.StatusBadRequest, "valid_to must not be before valid_from")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory correction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.UpdateLifeMemoryContent(r.Context(), db.UpdateLifeMemoryContentParams{
		ID:          memory.ID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
		Kind:        req.Kind,
		Content:     strings.TrimSpace(req.Content),
		Confidence:  req.Confidence,
		Urgency:     req.Urgency,
		Uncertainty: strings.TrimSpace(req.Uncertainty),
		ValidFrom:   validFrom,
		ValidTo:     validTo,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, updated, "corrected", "由用户纠正", "member", scope.userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory correction")
		return
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, "member", scope.userID,
		"memory", uuidToString(updated.ID), "corrected", map[string]any{"kind": updated.Kind})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory correction")
		return
	}
	h.publishLifeEvents(event)
	response, err := h.lifeMemoryToResponse(r, updated)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// ConfirmLifeMemory 确认一条候选记忆：仅 candidate 可确认；确认后写修订并记录事件。
func (h *Handler) ConfirmLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memory, ok := h.loadLifeMemory(w, r, scope)
	if !ok {
		return
	}
	if memory.Status != "candidate" {
		writeError(w, http.StatusConflict, "only candidate memories can be confirmed")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory confirmation")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	confirmed, err := qtx.ConfirmLifeMemory(r.Context(), db.ConfirmLifeMemoryParams{
		ID:            memory.ID,
		WorkspaceID:   scope.workspaceID,
		UserID:        scope.userID,
		ConfirmedByID: scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to confirm memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, confirmed, "confirmed", "由用户确认", "member", scope.userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory confirmation")
		return
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, "member", scope.userID,
		"memory", uuidToString(confirmed.ID), "confirmed", map[string]any{"kind": confirmed.Kind})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory confirmation")
		return
	}
	h.publishLifeEvents(event)
	response, err := h.lifeMemoryToResponse(r, confirmed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// DowngradeLifeMemory 把记忆的可信等级降级（kind 必须低于当前等级，如把 fact 降成 understanding）。
func (h *Handler) DowngradeLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memory, ok := h.loadLifeMemory(w, r, scope)
	if !ok {
		return
	}
	var req struct {
		Kind string `json:"kind"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	targetRank, valid := lifeMemoryKindRank[req.Kind]
	if !valid || targetRank >= lifeMemoryKindRank[memory.Kind] {
		writeError(w, http.StatusBadRequest, "kind must be lower confidence than the current kind")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory downgrade")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	downgraded, err := qtx.DowngradeLifeMemory(r.Context(), db.DowngradeLifeMemoryParams{
		ID:          memory.ID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
		Kind:        req.Kind,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to downgrade memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, downgraded, "downgraded", "由用户降低可信等级", "member", scope.userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory downgrade")
		return
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, "member", scope.userID,
		"memory", uuidToString(downgraded.ID), "downgraded", map[string]any{"kind": downgraded.Kind})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory downgrade")
		return
	}
	h.publishLifeEvents(event)
	response, err := h.lifeMemoryToResponse(r, downgraded)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// ArchiveLifeMemory 归档一条记忆（不再参与日常但保留追溯）。
func (h *Handler) ArchiveLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memory, ok := h.loadLifeMemory(w, r, scope)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start memory archive")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	archived, err := qtx.ArchiveLifeMemory(r.Context(), db.ArchiveLifeMemoryParams{
		ID:          memory.ID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive memory")
		return
	}
	if err := createLifeMemoryRevision(r.Context(), qtx, archived, "archived", "由用户归档", "member", scope.userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory archive")
		return
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, "member", scope.userID,
		"memory", uuidToString(archived.ID), "archived", map[string]any{"kind": archived.Kind})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory archive")
		return
	}
	h.publishLifeEvents(event)
	response, err := h.lifeMemoryToResponse(r, archived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load memory")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// DeleteLifeMemory 永久删除一条记忆及其整条派生链，实现"被遗忘权"。
// 这是本文件最重的一个接口，工作分几大步：
func (h *Handler) DeleteLifeMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireLifeRequestScope(w, r)
	if !ok {
		return
	}
	memoryID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memoryId"), "memory id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start deletion transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	rootMemory, err := qtx.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
		ID:          memoryID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeEntityLoadError(w, err, "memory", "memory_id", chi.URLParam(r, "memoryId"))
		return
	}
	memoryIDs, err := qtx.ListDerivedLifeMemoryIDs(r.Context(), db.ListDerivedLifeMemoryIDsParams{
		ID:          memoryID,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve memory dependencies")
		return
	}
	materials, err := qtx.ListLifeMaterialsByEvidenceSources(r.Context(), db.ListLifeMaterialsByEvidenceSourcesParams{
		WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: memoryIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve memory source materials")
		return
	}
	materialIDs := make([]pgtype.UUID, 0, len(materials))
	sourceIDs := make([]string, 0, len(materials)+len(memoryIDs))
	sourceTokens := make([]string, 0, len(materials)*2+len(memoryIDs))
	for _, material := range materials {
		digest := sha256.Sum256([]byte(material.Content))
		if err := qtx.CreateLifeForgetTombstone(r.Context(), db.CreateLifeForgetTombstoneParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, SourceType: material.SourceType,
			SourceKey: material.SourceKey, ContentHash: fmt.Sprintf("%x", digest[:]),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record permanent forgetting")
			return
		}
		materialIDs = append(materialIDs, material.ID)
		sourceIDs = append(sourceIDs, material.SourceKey)
		sourceTokens = append(sourceTokens,
			material.SourceType+":"+material.SourceKey,
			material.SourceType+":"+uuidToString(material.ID),
			"material:"+uuidToString(material.ID),
		)
	}
	memorySet := make(map[string]pgtype.UUID, len(memoryIDs))
	for _, id := range memoryIDs {
		key := uuidToString(id)
		memorySet[key] = id
		sourceIDs = append(sourceIDs, key)
		sourceTokens = append(sourceTokens, "memory:"+key)
	}
	// 同一来源可能喂给多条模型生成的记录，而它们之间并没有显式的
	// 记忆到记忆的边。因此要反复走溯源图，直到所有派生记忆都被收进同一次
	// 原子删除。
	for {
		derivations, err := qtx.ListLifeDerivationsBySources(r.Context(), db.ListLifeDerivationsBySourcesParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: sourceTokens,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to resolve derived life records")
			return
		}
		changed := false
		for _, derivation := range derivations {
			if derivation.TargetType != "memory" {
				continue
			}
			key := uuidToString(derivation.TargetID)
			if _, exists := memorySet[key]; exists {
				continue
			}
			derivedIDs, err := qtx.ListDerivedLifeMemoryIDs(r.Context(), db.ListDerivedLifeMemoryIDsParams{
				ID: derivation.TargetID, WorkspaceID: scope.workspaceID, UserID: scope.userID,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to resolve derived memory dependencies")
				return
			}
			for _, derivedID := range derivedIDs {
				derivedKey := uuidToString(derivedID)
				if _, exists := memorySet[derivedKey]; exists {
					continue
				}
				memorySet[derivedKey] = derivedID
				memoryIDs = append(memoryIDs, derivedID)
				sourceIDs = append(sourceIDs, derivedKey)
				sourceTokens = append(sourceTokens, "memory:"+derivedKey)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	// 删除前先为每条记忆记下内容哈希。墓碑让"延迟的证据校验"即使在该行
	// 已被删除后，仍能认出这是一个被永久遗忘的来源。
	for _, id := range memoryIDs {
		memory := rootMemory
		if id != rootMemory.ID {
			memory, err = qtx.GetLifeMemory(r.Context(), db.GetLifeMemoryParams{
				ID: id, WorkspaceID: scope.workspaceID, UserID: scope.userID,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load derived memory")
				return
			}
		}
		digest := sha256.Sum256([]byte(memory.Content))
		if err := qtx.CreateLifeForgetTombstone(r.Context(), db.CreateLifeForgetTombstoneParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID,
			SourceType: "memory", SourceKey: uuidToString(id), ContentHash: fmt.Sprintf("%x", digest[:]),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record permanent memory forgetting")
			return
		}
	}
	if len(materialIDs) > 0 {
		materialIDStrings := make([]string, 0, len(materialIDs))
		for _, id := range materialIDs {
			materialIDStrings = append(materialIDStrings, uuidToString(id))
		}
		if err := qtx.ScrubLifeCognitionTasksByMaterialIDs(r.Context(), db.ScrubLifeCognitionTasksByMaterialIDsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: materialIDStrings}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scrub background task copies")
			return
		}
	}
	if len(memoryIDs) > 0 {
		memoryIDStrings := make([]string, 0, len(memoryIDs))
		for _, id := range memoryIDs {
			memoryIDStrings = append(memoryIDStrings, uuidToString(id))
		}
		if err := qtx.ScrubLifeCognitionTasksByMemoryIDs(r.Context(), db.ScrubLifeCognitionTasksByMemoryIDsParams{
			WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: memoryIDStrings,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scrub memory cognition copies")
			return
		}
	}
	if len(sourceTokens) > 0 {
		if err := qtx.DeleteLifeDerivedRecordsByTargets(r.Context(), db.DeleteLifeDerivedRecordsByTargetsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: sourceTokens}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete derived life records")
			return
		}
	}
	roundIDs, err := qtx.ListLifeExperimentRoundIDsByMemoryIDs(r.Context(), memoryIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve experiment dependencies")
		return
	}
	if len(roundIDs) > 0 {
		if err := qtx.DeleteLifeActionProposalsByRoundIDs(r.Context(), roundIDs); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete proposal dependencies")
			return
		}
	}
	for _, id := range roundIDs {
		sourceIDs = append(sourceIDs, uuidToString(id))
	}
	if len(sourceTokens) > 0 || len(sourceIDs) > 0 {
		eventSourceIDs := append([]string(nil), sourceIDs...)
		for _, token := range sourceTokens {
			eventSourceIDs = append(eventSourceIDs, token)
		}
		if err := qtx.RedactLifeDomainEventsBySourceIDs(r.Context(), db.RedactLifeDomainEventsBySourceIDsParams{
			WorkspaceID: scope.workspaceID, Column2: eventSourceIDs,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to redact forgotten life events")
			return
		}
	}
	if err := qtx.DeleteLifeCommitmentsByMemoryIDs(r.Context(), db.DeleteLifeCommitmentsByMemoryIDsParams{Column1: memoryIDs, WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete commitment dependencies")
		return
	}
	if len(sourceIDs) > 0 {
		if err := qtx.DeleteLifeObserverJudgementsBySources(r.Context(), db.DeleteLifeObserverJudgementsBySourcesParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: sourceIDs}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete observer dependencies")
			return
		}
	}
	if err := qtx.DeleteLifeChronicleEntriesBySources(r.Context(), db.DeleteLifeChronicleEntriesBySourcesParams{
		Column1: memoryIDs,
		Column2: roundIDs,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete chronicle dependencies")
		return
	}
	if len(roundIDs) > 0 {
		if err := qtx.DeleteLifeExperimentRoundsByIDs(r.Context(), roundIDs); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete experiment dependencies")
			return
		}
	}
	if err := qtx.DeleteLifeMemoriesByIDs(r.Context(), db.DeleteLifeMemoriesByIDsParams{
		Column1:     memoryIDs,
		WorkspaceID: scope.workspaceID,
		UserID:      scope.userID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete memory")
		return
	}
	if err := qtx.DeleteLifeDerivationsByMemoryIDs(r.Context(), db.DeleteLifeDerivationsByMemoryIDsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID, Column3: memoryIDs}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete memory provenance")
		return
	}
	if len(materialIDs) > 0 {
		if err := qtx.DeleteLifeMaterialsByIDs(r.Context(), db.DeleteLifeMaterialsByIDsParams{Column1: materialIDs, WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete source materials")
			return
		}
	}
	if err := qtx.DeleteEmptyLifeTopics(r.Context(), db.DeleteEmptyLifeTopicsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, 500, "failed to delete empty topics")
		return
	}
	if err := qtx.DeleteEmptyLifeObservationTopics(r.Context(), db.DeleteEmptyLifeObservationTopicsParams{WorkspaceID: scope.workspaceID, UserID: scope.userID}); err != nil {
		writeError(w, 500, "failed to delete empty observation topics")
		return
	}
	event, err := recordLifeChangedTx(r.Context(), qtx, scope, "member", scope.userID,
		"memory", uuidToString(memoryID), "deleted", map[string]any{"derived_memory_count": len(memoryIDs), "material_count": len(materialIDs)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record memory deletion event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit memory deletion")
		return
	}
	h.publishLifeEvents(event)
	w.WriteHeader(http.StatusNoContent)
}
