// 本文件：CLI 更新请求的存储层与 HTTP 接口。流程与其他 runtime 请求一致——前端建
// pending 请求，daemon 心跳认领并实际执行更新，回报 running/completed/failed，UI 轮询。
// 本文件定义 UpdateStore 接口与单机内存实现（多节点用 runtime_update_redis_store.go 的
// Redis 实现），以及三个 handler：发起更新、查询更新、daemon 回报结果。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
)

// ---------------------------------------------------------------------------
// CLI 更新请求存储
// ---------------------------------------------------------------------------

// UpdateStatus 是一次 CLI 更新请求的状态。
type UpdateStatus string

const (
	UpdatePending   UpdateStatus = "pending"
	UpdateRunning   UpdateStatus = "running"
	UpdateCompleted UpdateStatus = "completed"
	UpdateFailed    UpdateStatus = "failed"
	UpdateTimeout   UpdateStatus = "timeout"
)

// UpdateRequest 表示一次 pending 或已完成的 CLI 更新请求。InitiatorUserID /
// RunStartedAt 标 `json:"-"`，不外发给前端。
type UpdateRequest struct {
	ID              string       `json:"id"`
	RuntimeID       string       `json:"runtime_id"`
	InitiatorUserID string       `json:"-"`
	Status          UpdateStatus `json:"status"`
	TargetVersion   string       `json:"target_version"`
	Output          string       `json:"output,omitempty"`
	Error           string       `json:"error,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	RunStartedAt    *time.Time   `json:"-"`
}

const (
	updatePendingTimeout = 120 * time.Second
	updateRunningTimeout = 150 * time.Second
	updateStoreRetention = 5 * time.Minute
)

// UpdateStore 跟踪 pending/running/completed 的 CLI 更新请求；服务器保持无状态，所有
// 需要跨请求存活的状态都放共享存储（多节点部署用 Redis 实现）。方法语义与 model-list /
// 本地技能两个 store 一致。
type UpdateStore interface {
	Create(ctx context.Context, runtimeID, targetVersion, initiatorUserID string) (*UpdateRequest, error)
	Get(ctx context.Context, id string) (*UpdateRequest, error)
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	PopPending(ctx context.Context, runtimeID string) (*UpdateRequest, error)
	Complete(ctx context.Context, id string, output string) error
	Fail(ctx context.Context, id string, errMsg string) error
}

// updateRequestTerminal 判断更新状态是否为终态。
func updateRequestTerminal(status UpdateStatus) bool {
	return status == UpdateCompleted || status == UpdateFailed || status == UpdateTimeout
}

// applyUpdateTimeout 请求在 pending/running 超过阈值时置为 timeout，返回是否改动过。
func applyUpdateTimeout(req *UpdateRequest, now time.Time) bool {
	switch req.Status {
	case UpdatePending:
		if now.Sub(req.CreatedAt) > updatePendingTimeout {
			req.Status = UpdateTimeout
			req.Error = "daemon did not respond within 120 seconds"
			req.UpdatedAt = now
			return true
		}
	case UpdateRunning:
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > updateRunningTimeout {
			req.Status = UpdateTimeout
			req.Error = "update did not complete within 150 seconds"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

// InMemoryUpdateStore 是单节点实现。多节点部署必须用 RedisUpdateStore，让 Web POST、
// daemon 心跳、daemon 回报、UI 轮询对同一请求生命周期达成一致。方法体语义见
// UpdateStore 接口注释。
type InMemoryUpdateStore struct {
	mu       sync.Mutex
	requests map[string]*UpdateRequest // keyed by update ID
}

func NewInMemoryUpdateStore() *InMemoryUpdateStore {
	return &InMemoryUpdateStore{
		requests: make(map[string]*UpdateRequest),
	}
}

func (s *InMemoryUpdateStore) Create(_ context.Context, runtimeID, targetVersion, initiatorUserID string) (*UpdateRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 顺手清理过期的旧请求。
	for id, req := range s.requests {
		if time.Since(req.CreatedAt) > updateStoreRetention {
			delete(s.requests, id)
		}
	}

	// 这台 runtime 已有 pending/running 的更新就拒绝再建。
	for _, req := range s.requests {
		if req.RuntimeID == runtimeID && (req.Status == UpdatePending || req.Status == UpdateRunning) {
			return nil, errUpdateInProgress
		}
	}

	req := &UpdateRequest{
		ID:              randomID(),
		RuntimeID:       runtimeID,
		InitiatorUserID: initiatorUserID,
		Status:          UpdatePending,
		TargetVersion:   targetVersion,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	s.requests[req.ID] = req
	return req, nil
}

// errUpdateInProgress 表示该 runtime 已有一条更新在进行中（pending/running），不能并发更新。
var errUpdateInProgress = &updateError{msg: "an update is already in progress for this runtime"}

// updateError 是带固定文案的错误，便于调用方用 errors.Is 精确匹配上面的 sentinel。
type updateError struct{ msg string }

func (e *updateError) Error() string { return e.msg }

func (s *InMemoryUpdateStore) Get(_ context.Context, id string) (*UpdateRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, ok := s.requests[id]
	if !ok {
		return nil, nil
	}
	applyUpdateTimeout(req, time.Now())
	return req, nil
}

func (s *InMemoryUpdateStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, req := range s.requests {
		applyUpdateTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == UpdatePending {
			return true, nil
		}
	}
	return false, nil
}

// PopPending 取走某台 runtime 的 pending 更新请求并把它标为 running。
func (s *InMemoryUpdateStore) PopPending(_ context.Context, runtimeID string) (*UpdateRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var oldest *UpdateRequest
	now := time.Now()
	for _, req := range s.requests {
		applyUpdateTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == UpdatePending {
			if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
				oldest = req
			}
		}
	}
	if oldest != nil {
		oldest.Status = UpdateRunning
		startedAt := now
		oldest.RunStartedAt = &startedAt
		oldest.UpdatedAt = now
	}
	return oldest, nil
}

func (s *InMemoryUpdateStore) Complete(_ context.Context, id string, output string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = UpdateCompleted
		req.Output = output
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryUpdateStore) Fail(_ context.Context, id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = UpdateFailed
		req.Error = errMsg
		req.UpdatedAt = time.Now()
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP Handlers
// ---------------------------------------------------------------------------

// InitiateUpdate 处理 POST /api/runtimes/:id/update——创建一个新的 CLI 更新请求
// （受保护路由，前端调用）。需 runtime 读权限 + canEditRuntime（仅 owner / 工作区 admin
// 可发起更新）；body 里 target_version 必填。若该 runtime 已有更新在进行中返回 409。
func (h *Handler) InitiateUpdate(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, member, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if !canEditRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "only runtime owners and workspace admins can update runtimes")
		return
	}

	var req struct {
		TargetVersion string `json:"target_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TargetVersion == "" {
		writeError(w, http.StatusBadRequest, "target_version is required")
		return
	}

	update, err := h.UpdateStore.Create(
		r.Context(),
		uuidToString(rt.ID),
		req.TargetVersion,
		uuidToString(member.UserID),
	)
	if err != nil {
		// 只有「已在更新中」这种拒绝是调用方能处理的冲突。Create 的其它失败都属于基础设施
		// 故障（Redis store 把连接失败包装成 "reserve active update: ..."/"persist update
		// request: ..."）——原样回给用户既泄露内部细节，又把一次宕机标成用户可修复的冲突。
		// CLI 以前隐藏 409 响应体时还扛得住；现在默认展示响应体就不行了。
		if errors.Is(err, errUpdateInProgress) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		slog.Error("UpdateStore Create failed", "error", err, "runtime_id", uuidToString(rt.ID))
		writeError(w, http.StatusInternalServerError, "failed to start the update")
		return
	}

	writeJSON(w, http.StatusOK, update)
}

// GetUpdate 处理 GET /api/runtimes/:id/update/:updateId——返回某个更新请求的状态
// （受保护路由，前端轮询）。需 runtime 读权限；请求不存在或不属于该 runtime 回 404。
func (h *Handler) GetUpdate(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, member, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeUpdatePoll, runtimeID)
	if !ok {
		return
	}
	updateID := chi.URLParam(r, "updateId")

	update, err := h.UpdateStore.Get(r.Context(), updateID)
	if err != nil {
		slog.Warn("load update failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load update")
		return
	}
	if update == nil || update.RuntimeID != uuidToString(rt.ID) {
		writeError(w, http.StatusNotFound, "update not found")
		return
	}
	// 若发起更新后 admin 被降权，也别掐断他在轮询中的请求。这条例外只限定给发起更新时
	// 记录下的不可变发起人；其它普通成员依然读不到别人 runtime 的更新状态。
	if !canEditRuntime(member, rt) && update.InitiatorUserID != uuidToString(member.UserID) {
		writeError(w, http.StatusForbidden, "only runtime owners, workspace admins, and the update initiator can view this update")
		return
	}

	writeJSON(w, http.StatusOK, update)
}

// ReportUpdateResult 接收 daemon 回报的更新结果（running/completed/failed）。
// 调用方必须是该 runtime 所属工作区的 daemon；请求已终态时忽略过期回报。
func (h *Handler) ReportUpdateResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")

	// 校验调用方拥有这台 runtime 的工作区（即 daemon 身份）。
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	updateID := chi.URLParam(r, "updateId")

	existing, err := h.UpdateStore.Get(r.Context(), updateID)
	if err != nil {
		slog.Warn("load update failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load update")
		return
	}
	if existing == nil || existing.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "update not found")
		return
	}
	if updateRequestTerminal(existing.Status) {
		slog.Debug("ignoring stale update report", "runtime_id", runtimeID, "update_id", updateID, "status", existing.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var req struct {
		Status string `json:"status"` // "running", "completed", or "failed"
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	switch req.Status {
	case "completed":
		if err := h.UpdateStore.Complete(r.Context(), updateID, req.Output); err != nil {
			slog.Error("UpdateStore Complete failed", "error", err, "update_id", updateID)
			writeError(w, http.StatusInternalServerError, "failed to persist completion")
			return
		}
	case "failed":
		if err := h.UpdateStore.Fail(r.Context(), updateID, req.Error); err != nil {
			slog.Error("UpdateStore Fail failed", "error", err, "update_id", updateID)
			writeError(w, http.StatusInternalServerError, "failed to persist failure")
			return
		}
	case "running":
		// 空操作：状态在 PopPending 时就已是 "running"。这次调用只是 daemon 发来的进度
		// 信号，确认它收到了更新命令、正在执行。
	default:
		writeError(w, http.StatusBadRequest, "invalid status: "+req.Status)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
