// 本文件：runtime 的「模型清单请求」存储与接口。服务器不能直接调用 daemon（daemon 在
// 用户 NAT 后面，只能轮询服务器），所以「列出这台 runtime 支持哪些模型」走 pending 请求
// 模式：前端 POST 建一个 pending 请求 → daemon 下次心跳取走并本地执行 → 回报结果。
// 本文件是这条路线的跨节点共享状态：POST、心跳、轮询可能落在不同副本，都必须看到同一份
// 请求生命周期。头部长注释即设计说明（内存/Redis 两种实现的取舍）。
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// 模型清单请求存储
// ---------------------------------------------------------------------------
//
// 服务器不能直接调用 daemon（daemon 在用户 NAT 后面，只会轮询服务器）。所以「列出这台
// runtime 的模型」用 pending 请求模式：前端 POST 创建一个 pending 请求，daemon 在下一个
// 心跳取走它、在本地执行、把结果回报回来。
//
// 存储是这条流程的横切状态，必须在所有 API 副本间保持一致——POST、心跳、轮询可能落在
// 不同节点，它们都要看到同一个请求生命周期。单机内存实现适合自托管开发；多节点部署
// （Multica Cloud）必须用 Redis 实现，否则 pending 请求对接下来收到调用的那个副本不可见，
// 选择器会显示 "No models available"（回归：见 multica-ai/multica#2009 评审）。

// ModelListStatus 表示一次模型清单请求的生命周期状态。
type ModelListStatus string

const (
	ModelListPending   ModelListStatus = "pending"
	ModelListRunning   ModelListStatus = "running"
	ModelListCompleted ModelListStatus = "completed"
	ModelListFailed    ModelListStatus = "failed"
	ModelListTimeout   ModelListStatus = "timeout"
)

// ModelListRequest 表示一次 pending 或完成的模型清单请求。
// Supported 为 false 表示该 provider 完全不理会「按 Agent 选模型」——agent.ModelSelectionSupported
// 是唯一事实来源，它只点名 qwenpaw 与 mcode。Hermes 不在其中：它通过 ACP `session/set_model`
// RPC 尊重 opts.Model，所以别为了「对齐」把 hermes 加进这份清单（MUL-6606——本注释早期版本
// 提到过 hermes，真要加了会直接让 Hermes 失去模型选择器）。UI 据此禁用它的下拉框，而不是
// 默默接受一个后端会丢弃的值。
//
// RunStartedAt 在 PopPending 认领请求时被设置。它标 `json:"-"`，因为是服务端记账字段——
// UI 只需要 Status / UpdatedAt 就能驱动轮询。
type ModelListRequest struct {
	ID           string          `json:"id"`
	RuntimeID    string          `json:"runtime_id"`
	Status       ModelListStatus `json:"status"`
	Models       []ModelEntry    `json:"models,omitempty"`
	Supported    bool            `json:"supported"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	RunStartedAt *time.Time      `json:"-"`
	// Cached 标记该响应来自服务端目录缓存而非一次实时的 daemon 往返（MUL-5444）。纯信息性：
	// Status 已是 "completed"、Models 已填好，忽略此字段的客户端行为与以前完全一致。CachedAt
	// 携带快照采集时间，给想展示新鲜度的客户端用。这两个字段从不写入请求存储，只存在于
	// 合成的缓存命中响应上。
	Cached   bool       `json:"cached,omitempty"`
	CachedAt *time.Time `json:"cached_at,omitempty"`
}

// ModelEntry 与 agent.Model 一一对应，用于线上传输。`Default` 标记该 runtime 宣传的
// 首选模型（如 Claude Code 出厂默认，或 hermes 的 currentModelId），UI 用它打徽标——
// 序列化时别丢。
//
// `Thinking` 携带 daemon 为支持按模型调推理档位的 runtime（claude、codex——见 MUL-2339）
// 发现的思考档位目录。nil 表示「该模型没有档位选择器」，UI 隐藏 thinking_level 下拉。
// 老 daemon（2026-05 之前）不会发这个字段也没关系：UI 隐藏选择器，Agent 用 runtime 默认档位跑。
type ModelEntry struct {
	ID                                  string             `json:"id"`
	Label                               string             `json:"label"`
	Provider                            string             `json:"provider,omitempty"`
	Default                             bool               `json:"default,omitempty"`
	Thinking                            *ModelThinking     `json:"thinking,omitempty"`
	ServiceTiers                        []ModelServiceTier `json:"service_tiers,omitempty"`
	SupportsExplicitStandardServiceTier bool               `json:"supports_explicit_standard_service_tier,omitempty"`
}

// ModelServiceTier 是模型服务等级（tier）的线上结构：服务商给模型分档定价/限速，
// 每一档一个可选项。
type ModelServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ModelThinking 是「每模型思考目录」的线上结构，镜像 agent.ModelThinking，daemon 的上报
// 无需重映射即可透传。
type ModelThinking struct {
	SupportedLevels []ThinkingLevel `json:"supported_levels"`
	DefaultLevel    string          `json:"default_level,omitempty"`
}

// ThinkingLevel 是模型思考目录里单个档位的线上结构。`Value` 是 daemon 原样传给 CLI 的字面
// token；`Label` 是人可读的展示名；`Description` 是可选说明文案（Codex 的 debug-models
// 输出每个档位都带一句）。
type ThinkingLevel struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

const (
	// modelListPendingTimeout 限制 pending 请求能在存储里等多久，超时就告诉 UI
	// 「daemon 没取走这个请求」。
	modelListPendingTimeout = 30 * time.Second
	// modelListRunningTimeout 限制一个被认领（running）的请求能保持认领状态多久，超时就
	// 告诉 UI「daemon 取走了但一直没回报结果」。当携带 `pending_model_list` 的心跳响应在
	// 途中丢失时（例如 PopPending 已改存储状态、HTTP 客户端却超时）这很重要：没有这个
	// 转换，UI 会一直轮询一条卡死在 `running`、直到被保留期清扫的记录。
	modelListRunningTimeout = 60 * time.Second
	// modelListStoreRetention 限制任何已存请求在后端存储里的存活时长。Redis 后端把它当 TTL，
	// 内存后端在 Create 时做 GC。该窗口刻意比 pending/running 超时更宽，这样 UI 最后一轮
	// 轮询到达时，终态记录仍可读。
	modelListStoreRetention = 2 * time.Minute
)

// ModelListStore 是每个后端（内存单节点、Redis 多节点）都必须满足的契约。方法带 context，
// 让 Redis 实现能尊重心跳侧的超时，避免慢的共享存储拖住心跳的其它部分。
type ModelListStore interface {
	Create(ctx context.Context, runtimeID string) (*ModelListRequest, error)
	Get(ctx context.Context, id string) (*ModelListRequest, error)
	// HasPending 是廉价只读探测，心跳热路径用它给有副作用的 PopPending 做闸门。偶尔误报
	// "true" 没关系——PopPending 探测后若队列已空就返回 nil。
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	PopPending(ctx context.Context, runtimeID string) (*ModelListRequest, error)
	Complete(ctx context.Context, id string, models []ModelEntry, supported bool) error
	Fail(ctx context.Context, id string, errMsg string) error
}

// applyModelListTimeout 请求卡在某个非终态超过阈值时转成 ModelListTimeout，返回是否改动过
// 以便调用方持久化。pending 阈值兜「daemon 从未取走」；running 阈值兜「daemon 取走了但结果
// 回报丢了」——没有 running 这条逃生通道，只有保留期清扫能结束轮询循环。
func applyModelListTimeout(req *ModelListRequest, now time.Time) bool {
	switch req.Status {
	case ModelListPending:
		if now.Sub(req.CreatedAt) > modelListPendingTimeout {
			req.Status = ModelListTimeout
			req.Error = "daemon did not respond within 30 seconds"
			req.UpdatedAt = now
			return true
		}
	case ModelListRunning:
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > modelListRunningTimeout {
			req.Status = ModelListTimeout
			req.Error = "daemon did not finish within 60 seconds"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

// InMemoryModelListStore 是单节点实现。自托管开发与测试套件够用，但多节点部署下不安全
// （每个副本各持一张 map，没收到 POST 的副本看不到那个 pending 请求）。
type InMemoryModelListStore struct {
	mu       sync.Mutex
	requests map[string]*ModelListRequest
}

func NewInMemoryModelListStore() *InMemoryModelListStore {
	return &InMemoryModelListStore{requests: make(map[string]*ModelListRequest)}
}

func (s *InMemoryModelListStore) Create(_ context.Context, runtimeID string) (*ModelListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 顺手 GC 过期条目，map 不会无限膨胀。
	for id, req := range s.requests {
		if time.Since(req.CreatedAt) > modelListStoreRetention {
			delete(s.requests, id)
		}
	}

	now := time.Now()
	req := &ModelListRequest{
		ID:        randomID(),
		RuntimeID: runtimeID,
		Status:    ModelListPending,
		// 默认 true；不支持按 Agent 选模型的 provider，daemon 会在上报时覆盖它。
		Supported: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.requests[req.ID] = req
	return req, nil
}

func (s *InMemoryModelListStore) Get(_ context.Context, id string) (*ModelListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, ok := s.requests[id]
	if !ok {
		return nil, nil
	}
	applyModelListTimeout(req, time.Now())
	return req, nil
}

func (s *InMemoryModelListStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, req := range s.requests {
		applyModelListTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == ModelListPending {
			return true, nil
		}
	}
	return false, nil
}

func (s *InMemoryModelListStore) PopPending(_ context.Context, runtimeID string) (*ModelListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var oldest *ModelListRequest
	now := time.Now()
	for _, req := range s.requests {
		applyModelListTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == ModelListPending {
			if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
				oldest = req
			}
		}
	}
	if oldest != nil {
		oldest.Status = ModelListRunning
		startedAt := now
		oldest.RunStartedAt = &startedAt
		oldest.UpdatedAt = now
	}
	return oldest, nil
}

func (s *InMemoryModelListStore) Complete(_ context.Context, id string, models []ModelEntry, supported bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = ModelListCompleted
		req.Models = models
		req.Supported = supported
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryModelListStore) Fail(_ context.Context, id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = ModelListFailed
		req.Error = errMsg
		req.UpdatedAt = time.Now()
	}
	return nil
}

func modelListRequestTerminal(status ModelListStatus) bool {
	return status == ModelListCompleted || status == ModelListFailed || status == ModelListTimeout
}

// ---------------------------------------------------------------------------
// HTTP Handlers
// ---------------------------------------------------------------------------

// InitiateListModels 处理 GET /api/runtimes/:id/models——应答「列出这台 runtime 的模型」。
//
// 快路径：返回一份比 modelCatalogServeWindow 更年轻的缓存目录，直接组装成一条已完成请求，
// 选择器立刻渲染而不用等 daemon（stale-while-revalidate）。应答比 modelCatalogRevalidateAfter
// 旧的快照时，会顺带排一个后台刷新——没人轮询它，它唯一的任务是把缓存热给下一次打开。
//
// 慢路径：入队一条 pending 请求，由 daemon 在下一个心跳认领，并推一个唤醒提示，让「下一
// 个心跳」是现在而不是最多一个 HeartbeatInterval 之后。
func (h *Handler) InitiateListModels(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if rt.Status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}
	resolvedRuntimeID := uuidToString(rt.ID)

	if cached := h.cachedModelCatalog(r.Context(), resolvedRuntimeID); cached != nil {
		age := cached.Age(time.Now())
		if age >= modelCatalogRevalidateAfter {
			h.revalidateModelCatalog(r.Context(), resolvedRuntimeID)
		}
		storedAt := cached.StoredAt
		writeJSON(w, http.StatusOK, &ModelListRequest{
			// 合成 ID：缓存命中背后没有存储记录。客户端只在 status 为 pending/running 时才
			// 轮询 GET /models/{id}，而这个响应永远不会是那两种状态。
			ID:        randomID(),
			RuntimeID: resolvedRuntimeID,
			Status:    ModelListCompleted,
			Models:    cached.Models,
			Supported: cached.Supported,
			CreatedAt: storedAt,
			UpdatedAt: storedAt,
			Cached:    true,
			CachedAt:  &storedAt,
		})
		return
	}

	req, err := h.ModelListStore.Create(r.Context(), resolvedRuntimeID)
	if err != nil {
		slog.Warn("enqueue model list request failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to enqueue model list request")
		return
	}
	h.requestDaemonPendingWork(resolvedRuntimeID, protocol.PendingWorkKindModelList)
	writeJSON(w, http.StatusOK, req)
}

// cachedModelCatalog 返回一份可用的缓存目录；缓存缺失、变冷或读不了都返回 nil。缓存出问题
// 绝不能连累请求失败——调用方回退到 daemon 往返即可。
func (h *Handler) cachedModelCatalog(ctx context.Context, runtimeID string) *ModelCatalogSnapshot {
	if h.ModelCatalogCache == nil {
		return nil
	}
	snapshot, err := h.ModelCatalogCache.Get(ctx, runtimeID)
	if err != nil {
		slog.Warn("model catalog cache read failed", "error", err, "runtime_id", runtimeID)
		return nil
	}
	// fallback=false：能存进缓存的快照按构造必然是真实发现结果——fallback 目录从不进缓存。
	if snapshot == nil || !cacheableModelCatalog(snapshot.Models, snapshot.Supported, false) {
		return nil
	}
	return snapshot
}

// revalidateModelCatalog 排一个后台发现请求，其结果只用于更新缓存。没有客户端轮询它；
// daemon 一直不应答的话，存储自身的超时会清扫这条记录。
//
// 惊群控制：该 runtime 已有一条在队列里的请求就跳过。已经被认领的 running 请求对
// HasPending 不可见，所以同一秒内的两次打开可能排两条刷新——有界、便宜（daemon 对发现
// 结果有 60s 记忆），并且严格好于「从不刷新」这个备选。
func (h *Handler) revalidateModelCatalog(ctx context.Context, runtimeID string) {
	if h.ModelListStore == nil {
		return
	}
	pending, err := h.ModelListStore.HasPending(ctx, runtimeID)
	if err != nil {
		slog.Debug("model catalog revalidate probe failed", "error", err, "runtime_id", runtimeID)
		return
	}
	if pending {
		return
	}
	if _, err := h.ModelListStore.Create(ctx, runtimeID); err != nil {
		slog.Debug("model catalog revalidate enqueue failed", "error", err, "runtime_id", runtimeID)
		return
	}
	h.requestDaemonPendingWork(runtimeID, protocol.PendingWorkKindModelList)
}

// requestDaemonPendingWork 戳一下 daemon，让它现在就来心跳。设计上就是尽力而为：daemon
// 的定时心跳始终是正确性路径，通知器缺失或 daemon 离线只损失延迟。优先走 relay 通知器
// （能触达持有 daemon socket 的 API 节点），回退到本地 hub——单节点部署下就是整个集群。
func (h *Handler) requestDaemonPendingWork(runtimeID, kind string) {
	if runtimeID == "" {
		return
	}
	if h.DaemonPendingWork != nil {
		h.DaemonPendingWork.NotifyPendingWork(runtimeID, kind)
		return
	}
	if h.DaemonHub != nil {
		h.DaemonHub.NotifyPendingWork(runtimeID, kind)
	}
}

// GetModelListRequest 处理 GET /api/runtimes/:id/models/requests/:requestId——返回一次
// 模型清单请求的状态（前端轮询）。请求不存在或不属于该 runtime 回 404。
func (h *Handler) GetModelListRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeModelPoll, runtimeID)
	if !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")

	req, err := h.ModelListStore.Get(r.Context(), requestID)
	if err != nil {
		slog.Warn("load request failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load request")
		return
	}
	if req == nil || req.RuntimeID != uuidToString(rt.ID) {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// ReportModelListResult 处理 GET daemon 的清单结果上报（须以 daemon 身份访问）：
// completed 时把结果写入请求记录，并顺带维护模型目录缓存；非 completed 记 Fail。
func (h *Handler) ReportModelListResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")

	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")

	// 先取记录，才能忽略已终态请求的过期回报（例如触发这次 daemon 运行的心跳响应是重试，
	// 而原始回报早已落库）。
	existing, err := h.ModelListStore.Get(r.Context(), requestID)
	if err != nil {
		slog.Warn("load request failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load request")
		return
	}
	if existing == nil || existing.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if modelListRequestTerminal(existing.Status) {
		slog.Debug("ignoring stale model list report", "runtime_id", runtimeID, "request_id", requestID, "status", existing.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status    string       `json:"status"` // "completed" or "failed"
		Models    []ModelEntry `json:"models"`
		Supported *bool        `json:"supported"`
		Error     string       `json:"error"`
		// Fallback 标记一次 completed 上报的模型其实是 provider 在发现失败后替换的静态
		// 占位目录，不是 runtime 的真实目录。老 daemon 省略它；缺省表示「这个 daemon 说不
		// 清」，维持 MUL-5549 之前的行为。
		Fallback bool `json:"fallback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Status == "completed" {
		// 老 daemon 可能省略 `supported`；默认 true，让它们尚未重部署期间 UI 仍可用。
		supported := true
		if body.Supported != nil {
			supported = *body.Supported
		}
		if err := h.ModelListStore.Complete(r.Context(), requestID, body.Models, supported); err != nil {
			// 存储失败要回 5xx 让 daemon 重试，而不是吞掉这份上报（那会让请求卡在 running
			// 直到服务端超时——正是我们要避免的「看着正常其实什么都没发生」那类 bug）。
			slog.Error("ModelListStore Complete failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist completion")
			return
		}
		// 给目录缓存加热，让下一次打开选择器即时渲染（MUL-5444）。缓存写失败不是 daemon
		// 的事——上报本身已成功——所以只记日志、不向上抛。
		//
		// 完成但不可缓存的（空目录，或 runtime 不理会按 Agent 选模型）是我们手头最新鲜的
		// 事实：丢弃任何旧快照，别让快路径继续给一份 runtime 已不再宣告的目录。failed 上报
		// 刻意不动缓存——用 last-known-good 撑过一次瞬态发现失败正是这个缓存存在的意义。
		//
		// fallback 上报是披着 completed 外衣的失败：模型是静态占位，既不算值得存的「新鲜
		// 事实」，也不构成丢弃手头真实目录的理由。照失败处理、缓存原样不动（MUL-5549）。
		if h.ModelCatalogCache != nil {
			switch modelCatalogCacheDecision(body.Models, supported, body.Fallback) {
			case modelCatalogCacheStore:
				if err := h.ModelCatalogCache.Put(r.Context(), runtimeID, body.Models, supported); err != nil {
					slog.Warn("model catalog cache write failed", "error", err, "runtime_id", runtimeID)
				}
			case modelCatalogCacheDrop:
				if err := h.ModelCatalogCache.Invalidate(r.Context(), runtimeID); err != nil {
					slog.Warn("model catalog cache invalidate failed", "error", err, "runtime_id", runtimeID)
				}
			case modelCatalogCacheKeep:
				slog.Debug("model discovery reported a fallback catalog; leaving cached catalog untouched",
					"runtime_id", runtimeID, "count", len(body.Models))
			}
		}
	} else {
		if err := h.ModelListStore.Fail(r.Context(), requestID, body.Error); err != nil {
			slog.Error("ModelListStore Fail failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist failure")
			return
		}
	}

	slog.Debug("model list report", "runtime_id", runtimeID, "request_id", requestID, "status", body.Status, "count", len(body.Models))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
