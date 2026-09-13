// 本文件处理「runtime 本地技能/资源」的清单与导入请求：前端（或 CLI）让某台 runtime 的
// daemon 去扫描它自己机器上装的技能（Skills）与 MCP 服务清单，或把本地技能文件导入到
// 工作区。设计要点：
//   - 服务器不落地任何待办状态，全部放在 LocalSkillListStore / LocalSkillImportStore
//     （共享存储，见 runtime_local_skills_redis_store.go），保证多节点部署下「发起请求、
//     心跳取活、轮询结果」落在不同节点也一致；
//   - 请求走三态机 pending → running → completed/failed/timeout/conflict，daemon 通过
//     心跳取走请求、执行完再回报（Report* 端点）；
//   - 导入分 create 与 overwrite 两种动作，同名冲突按客户端是否支持结构化 conflict
//     契约分别返回 conflict 或旧的 failed（MUL-2800）。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RuntimeLocalSkillRequestStatus 描述一次「运行时本地技能」请求（清单或导入）的状态。
type RuntimeLocalSkillRequestStatus string

const (
	RuntimeLocalSkillPending   RuntimeLocalSkillRequestStatus = "pending"
	RuntimeLocalSkillRunning   RuntimeLocalSkillRequestStatus = "running"
	RuntimeLocalSkillCompleted RuntimeLocalSkillRequestStatus = "completed"
	RuntimeLocalSkillFailed    RuntimeLocalSkillRequestStatus = "failed"
	RuntimeLocalSkillTimeout   RuntimeLocalSkillRequestStatus = "timeout"
	// RuntimeLocalSkillConflict 是终态：新导入撞上了同名技能时置为它。这不是错误——请求上
	// 会附带结构化的 Conflict 元信息，让调用方（Desktop UI / CLI）可以给出覆盖/改名/跳过
	// 的选项，而不是静默失败。见 MUL-2800。
	RuntimeLocalSkillConflict RuntimeLocalSkillRequestStatus = "conflict"
)

// LocalSkillImportAction 决定同名技能已存在时，本地技能导入如何解决冲突。
type LocalSkillImportAction string

const (
	// LocalSkillImportActionCreate 是默认动作：新建技能；若名字已被占用则以结构化的
	// `conflict` 终态返回。
	LocalSkillImportActionCreate LocalSkillImportAction = ""
	// LocalSkillImportActionOverwrite 把内容重新导入到已有技能（由 TargetSkillID 指定）。
	// 只有该技能的创建者可以覆盖。
	LocalSkillImportActionOverwrite LocalSkillImportAction = "overwrite"
)

// LocalSkillImportConflict 是请求以 RuntimeLocalSkillConflict 结束时附带的结构化结果。
// CanOverwrite 反映「仅创建者可覆盖」的重新导入策略（canOverwriteSkillByLocalImport）。
type LocalSkillImportConflict struct {
	ExistingSkillID   string `json:"existing_skill_id"`
	ExistingCreatedBy string `json:"existing_created_by,omitempty"`
	CanOverwrite      bool   `json:"can_overwrite"`
}

const (
	// runtimeLocalSkillPendingTimeout 限制请求在 pending 状态最多待多久，超时由服务器标记
	// 为 timeout。取值必须兼容不支持批量导入的老 daemon：它们每个心跳周期（约 15s）只取一个
	// 导入请求。maxLocalSkillImportBatch=10 时，排到第 10 个的导入要等最多 10×15s = 150s
	// 才会被取走，3 分钟是舒适余量。
	//
	// 超时不变式：IMPORT_CONCURRENCY（views/.../runtime-local-skill-import-panel.tsx）
	// × 心跳周期（约 15s）≤ runtimeLocalSkillPendingTimeout，且 IMPORT_POLL_TIMEOUT_MS
	// （core/runtimes/local-skills.ts）必须大于 runtimeLocalSkillPendingTimeout +
	// runtimeLocalSkillRunningTimeout。另见 daemon.go 的 maxLocalSkillImportBatch。
	runtimeLocalSkillPendingTimeout = 3 * time.Minute
	runtimeLocalSkillRunningTimeout = 60 * time.Second
	runtimeLocalSkillStoreRetention = 5 * time.Minute
)

// LocalSkillListStore 跟踪 pending / running / completed 的「runtime 本地技能清单」请求。
// 服务器必须保持无状态——任何需要活过单个请求的状态都得放进共享存储，这样多节点部署下
// POST、心跳与轮询落在不同节点也能对请求状态达成一致。
type LocalSkillListStore interface {
	Create(ctx context.Context, runtimeID string) (*RuntimeLocalSkillListRequest, error)
	Get(ctx context.Context, id string) (*RuntimeLocalSkillListRequest, error)
	// HasPending 是廉价的只读探测：报告该 runtime 是否至少有一个 pending 请求。热路径调用方
	// （如心跳 handler）用它在调用有副作用的 PopPending 前做闸门，避免启动一个可能还得
	// 中途放弃的认领。
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	PopPending(ctx context.Context, runtimeID string) (*RuntimeLocalSkillListRequest, error)
	Complete(ctx context.Context, id string, skills []RuntimeLocalSkillSummary, supported bool, mcpServers []RuntimeLocalMcpServerSummary, mcpSupported bool) error
	Fail(ctx context.Context, id string, errMsg string) error
}

// LocalSkillImportRequestInput 携带入队一次「runtime 本地技能导入」所需的字段。
// SupportsConflict 开启结构化 conflict 契约：只有选择加入的客户端会收到 `conflict` 终态；
// 老客户端维持旧 `failed`（"同名技能已存在"）行为，已装的 Desktop 版本与升级后的后端通信
// 也不会回归。见 MUL-2800。
type LocalSkillImportRequestInput struct {
	RuntimeID        string
	CreatorID        string
	SkillKey         string
	Name             *string
	Description      *string
	Action           LocalSkillImportAction
	TargetSkillID    string
	SupportsConflict bool
}

// LocalSkillImportStore 与 LocalSkillListStore 契约一致，只是面向导入请求。单独拆成接口
// 是因为 Create 签名带了导入专属字段（skill_key、可选改名）。
type LocalSkillImportStore interface {
	Create(ctx context.Context, input LocalSkillImportRequestInput) (*RuntimeLocalSkillImportRequest, error)
	Get(ctx context.Context, id string) (*RuntimeLocalSkillImportRequest, error)
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	PopPending(ctx context.Context, runtimeID string) (*RuntimeLocalSkillImportRequest, error)
	// PopPendingBatch 原子地认领最多 limit 个 pending 请求并置为 running。心跳 handler
	// 用它让每个心跳周期交付多个导入。
	PopPendingBatch(ctx context.Context, runtimeID string, limit int) ([]*RuntimeLocalSkillImportRequest, error)
	Complete(ctx context.Context, id string, skill SkillWithFilesResponse) error
	// Conflict 把请求置为终态 RuntimeLocalSkillConflict，并附上结构化冲突元信息供调用方决策。
	Conflict(ctx context.Context, id string, info LocalSkillImportConflict) error
	Fail(ctx context.Context, id string, errMsg string) error
}

// applyLocalSkillListTimeout 若请求在 pending / running 超过了配置阈值，就把它推进 timeout
// 终态。返回 true 表示记录被改动，调用方可据此持久化变更。
func applyLocalSkillListTimeout(req *RuntimeLocalSkillListRequest, now time.Time) bool {
	switch req.Status {
	case RuntimeLocalSkillPending:
		if now.Sub(req.CreatedAt) > runtimeLocalSkillPendingTimeout {
			req.Status = RuntimeLocalSkillTimeout
			req.Error = "daemon did not respond within 3 minutes"
			req.UpdatedAt = now
			return true
		}
	case RuntimeLocalSkillRunning:
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > runtimeLocalSkillRunningTimeout {
			req.Status = RuntimeLocalSkillTimeout
			req.Error = "daemon did not finish within 60 seconds"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

// applyLocalSkillImportTimeout 与 applyLocalSkillListTimeout 同逻辑，面向导入请求
// （pending/running 超时置为 timeout，返回是否改动过）。
func applyLocalSkillImportTimeout(req *RuntimeLocalSkillImportRequest, now time.Time) bool {
	switch req.Status {
	case RuntimeLocalSkillPending:
		if now.Sub(req.CreatedAt) > runtimeLocalSkillPendingTimeout {
			req.Status = RuntimeLocalSkillTimeout
			req.Error = "daemon did not respond within 3 minutes"
			req.UpdatedAt = now
			return true
		}
	case RuntimeLocalSkillRunning:
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > runtimeLocalSkillRunningTimeout {
			req.Status = RuntimeLocalSkillTimeout
			req.Error = "daemon did not finish within 60 seconds"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

// RuntimeLocalSkillSummary 是 daemon 上报的单个本地技能摘要，不含技能内容本身。
type RuntimeLocalSkillSummary struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SourcePath  string `json:"source_path"`
	Provider    string `json:"provider"`
	// Root 标注 daemon 发现该技能时的发现根目录类型："provider"（runtime 自己的技能目录，
	// 如 ~/.claude/skills）、"universal"（跨工具兜底目录 ~/.agents/skills）、或 "plugin"
	// （来自某个已启用的 runtime 插件贡献）。早于多根发现机制的 daemon 不上报此字段；
	// 空值表示 "unknown"，UI 不应断言它来自哪个源头。
	Root       string `json:"root,omitempty"`
	Plugin     string `json:"plugin,omitempty"`
	CanDisable bool   `json:"can_disable,omitempty"`
	FileCount  int    `json:"file_count"`
}

// RuntimeLocalMcpServerSummary 刻意不包含机密信息。daemon 只上报已配置的服务器名、传输
// 方式与配置作用域；命令参数、URL、header、环境变量值永远不离开本机。
type RuntimeLocalMcpServerSummary struct {
	Name      string `json:"name"`
	Transport string `json:"transport,omitempty"`
	Source    string `json:"source,omitempty"`
	Enabled   bool   `json:"enabled"`
}

// RuntimeLocalSkillListRequest 是「列出本地技能」请求及其进度的对外结构，也是 daemon 心跳
// 取活后回报结果的载体（RunStartedAt 只在内部用，不出 JSON）。
type RuntimeLocalSkillListRequest struct {
	ID           string                         `json:"id"`
	RuntimeID    string                         `json:"runtime_id"`
	Status       RuntimeLocalSkillRequestStatus `json:"status"`
	Skills       []RuntimeLocalSkillSummary     `json:"skills,omitempty"`
	Supported    bool                           `json:"supported"`
	McpServers   []RuntimeLocalMcpServerSummary `json:"mcp_servers,omitempty"`
	McpSupported bool                           `json:"mcp_supported"`
	Error        string                         `json:"error,omitempty"`
	CreatedAt    time.Time                      `json:"created_at"`
	UpdatedAt    time.Time                      `json:"updated_at"`
	RunStartedAt *time.Time                     `json:"-"`
}

// RuntimeLocalSkillImportRequest 是「导入本地技能」请求的对外结构；Skill 为成功后的导入
// 结果，Conflict 为同名冲突终态的元信息。
type RuntimeLocalSkillImportRequest struct {
	ID            string                 `json:"id"`
	RuntimeID     string                 `json:"runtime_id"`
	SkillKey      string                 `json:"skill_key"`
	Name          *string                `json:"name,omitempty"`
	Description   *string                `json:"description,omitempty"`
	Action        LocalSkillImportAction `json:"action,omitempty"`
	TargetSkillID string                 `json:"target_skill_id,omitempty"`
	// SupportsConflict 记录发起客户端是否选择加入结构化 conflict 契约；上报结果时据此决定
	// 返回新的 `conflict` 状态还是旧的 `failed` 行为。
	SupportsConflict bool                           `json:"supports_conflict,omitempty"`
	Status           RuntimeLocalSkillRequestStatus `json:"status"`
	Skill            *SkillWithFilesResponse        `json:"skill,omitempty"`
	Conflict         *LocalSkillImportConflict      `json:"conflict,omitempty"`
	Error            string                         `json:"error,omitempty"`
	CreatedAt        time.Time                      `json:"created_at"`
	UpdatedAt        time.Time                      `json:"updated_at"`
	CreatorID        string                         `json:"-"`
	RunStartedAt     *time.Time                     `json:"-"`
}

// InMemoryLocalSkillListStore 是单节点实现——本地开发和进程内测试够用。生产（多节点）
// 必须用 RedisLocalSkillListStore，让所有 API 节点看到同一个 pending 集合。
// 方法体都是持锁读写内存 map 的直白实现，语义见 LocalSkillListStore 接口注释。
type InMemoryLocalSkillListStore struct {
	mu       sync.Mutex
	requests map[string]*RuntimeLocalSkillListRequest
}

func NewInMemoryLocalSkillListStore() *InMemoryLocalSkillListStore {
	return &InMemoryLocalSkillListStore{requests: make(map[string]*RuntimeLocalSkillListRequest)}
}

func (s *InMemoryLocalSkillListStore) Create(_ context.Context, runtimeID string) (*RuntimeLocalSkillListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, req := range s.requests {
		if time.Since(req.CreatedAt) > runtimeLocalSkillStoreRetention {
			delete(s.requests, id)
		}
	}

	req := &RuntimeLocalSkillListRequest{
		ID:        randomID(),
		RuntimeID: runtimeID,
		Status:    RuntimeLocalSkillPending,
		Supported: true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.requests[req.ID] = req
	return req, nil
}

func (s *InMemoryLocalSkillListStore) Get(_ context.Context, id string) (*RuntimeLocalSkillListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, ok := s.requests[id]
	if !ok {
		return nil, nil
	}
	applyLocalSkillListTimeout(req, time.Now())
	return req, nil
}

func (s *InMemoryLocalSkillListStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, req := range s.requests {
		applyLocalSkillListTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeLocalSkillPending {
			return true, nil
		}
	}
	return false, nil
}

func (s *InMemoryLocalSkillListStore) PopPending(_ context.Context, runtimeID string) (*RuntimeLocalSkillListRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var oldest *RuntimeLocalSkillListRequest
	now := time.Now()
	for _, req := range s.requests {
		applyLocalSkillListTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeLocalSkillPending {
			if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
				oldest = req
			}
		}
	}
	if oldest != nil {
		oldest.Status = RuntimeLocalSkillRunning
		startedAt := now
		oldest.RunStartedAt = &startedAt
		oldest.UpdatedAt = now
	}
	return oldest, nil
}

func (s *InMemoryLocalSkillListStore) Complete(_ context.Context, id string, skills []RuntimeLocalSkillSummary, supported bool, mcpServers []RuntimeLocalMcpServerSummary, mcpSupported bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeLocalSkillCompleted
		req.Skills = skills
		req.Supported = supported
		req.McpServers = mcpServers
		req.McpSupported = mcpSupported
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryLocalSkillListStore) Fail(_ context.Context, id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeLocalSkillFailed
		req.Error = errMsg
		req.UpdatedAt = time.Now()
	}
	return nil
}

// InMemoryLocalSkillImportStore 与 InMemoryLocalSkillListStore 同思路，面向导入请求。
// 单节点与多节点的取舍也一样。方法语义见 LocalSkillImportStore 接口注释。
type InMemoryLocalSkillImportStore struct {
	mu       sync.Mutex
	requests map[string]*RuntimeLocalSkillImportRequest
}

func NewInMemoryLocalSkillImportStore() *InMemoryLocalSkillImportStore {
	return &InMemoryLocalSkillImportStore{requests: make(map[string]*RuntimeLocalSkillImportRequest)}
}

func (s *InMemoryLocalSkillImportStore) Create(_ context.Context, input LocalSkillImportRequestInput) (*RuntimeLocalSkillImportRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, req := range s.requests {
		if time.Since(req.CreatedAt) > runtimeLocalSkillStoreRetention {
			delete(s.requests, id)
		}
	}

	req := &RuntimeLocalSkillImportRequest{
		ID:               randomID(),
		RuntimeID:        input.RuntimeID,
		SkillKey:         input.SkillKey,
		Name:             input.Name,
		Description:      input.Description,
		Action:           input.Action,
		TargetSkillID:    input.TargetSkillID,
		SupportsConflict: input.SupportsConflict,
		Status:           RuntimeLocalSkillPending,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
		CreatorID:        input.CreatorID,
	}
	s.requests[req.ID] = req
	return req, nil
}

func (s *InMemoryLocalSkillImportStore) Get(_ context.Context, id string) (*RuntimeLocalSkillImportRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, ok := s.requests[id]
	if !ok {
		return nil, nil
	}
	applyLocalSkillImportTimeout(req, time.Now())
	return req, nil
}

func (s *InMemoryLocalSkillImportStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, req := range s.requests {
		applyLocalSkillImportTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeLocalSkillPending {
			return true, nil
		}
	}
	return false, nil
}

func (s *InMemoryLocalSkillImportStore) PopPending(_ context.Context, runtimeID string) (*RuntimeLocalSkillImportRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var oldest *RuntimeLocalSkillImportRequest
	now := time.Now()
	for _, req := range s.requests {
		applyLocalSkillImportTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeLocalSkillPending {
			if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
				oldest = req
			}
		}
	}
	if oldest != nil {
		oldest.Status = RuntimeLocalSkillRunning
		startedAt := now
		oldest.RunStartedAt = &startedAt
		oldest.UpdatedAt = now
	}
	return oldest, nil
}

func (s *InMemoryLocalSkillImportStore) PopPendingBatch(_ context.Context, runtimeID string, limit int) ([]*RuntimeLocalSkillImportRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// 收集这台 runtime 的所有 pending 请求，按创建时间排序（先进先出）。
	var pending []*RuntimeLocalSkillImportRequest
	for _, req := range s.requests {
		applyLocalSkillImportTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == RuntimeLocalSkillPending {
			pending = append(pending, req)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})

	if limit > len(pending) {
		limit = len(pending)
	}

	result := make([]*RuntimeLocalSkillImportRequest, 0, limit)
	for _, req := range pending[:limit] {
		req.Status = RuntimeLocalSkillRunning
		startedAt := now
		req.RunStartedAt = &startedAt
		req.UpdatedAt = now
		result = append(result, req)
	}
	return result, nil
}

func (s *InMemoryLocalSkillImportStore) Complete(_ context.Context, id string, skill SkillWithFilesResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeLocalSkillCompleted
		req.Skill = &skill
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryLocalSkillImportStore) Conflict(_ context.Context, id string, info LocalSkillImportConflict) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeLocalSkillConflict
		conflict := info
		req.Conflict = &conflict
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryLocalSkillImportStore) Fail(_ context.Context, id string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = RuntimeLocalSkillFailed
		req.Error = errMsg
		req.UpdatedAt = time.Now()
	}
	return nil
}

// CreateRuntimeLocalSkillImportRequest 是 POST 发起导入接口的请求体。
type CreateRuntimeLocalSkillImportRequest struct {
	SkillKey    string  `json:"skill_key"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	// Action 选择 create（默认）还是 overwrite。overwrite 时 TargetSkillID 必须指向已存在的
	// 同名技能。
	Action        LocalSkillImportAction `json:"action,omitempty"`
	TargetSkillID string                 `json:"target_skill_id,omitempty"`
	// SupportsConflict 让客户端选择加入结构化 conflict 契约。不传（老客户端）则在同名碰撞时
	// 保持旧的 `failed` 行为。overwrite 请求隐含选择加入新契约。
	SupportsConflict bool `json:"supports_conflict,omitempty"`
}

// reportedRuntimeLocalSkill 是 daemon 在导入完成上报里带回的技能包（含内容与文件列表）。
type reportedRuntimeLocalSkill struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Content     string                   `json:"content"`
	SourcePath  string                   `json:"source_path"`
	Provider    string                   `json:"provider"`
	Files       []CreateSkillFileRequest `json:"files,omitempty"`
}

// cleanOptionalString 去掉可选字符串首尾空白；空结果统一变 nil，让「没传」和「传了空白」
// 走同一条处理路径。
func cleanOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// runtimeLocalSkillRequestTerminal 判断状态是否为终态（completed/failed/timeout/conflict），
// 终态后的上报一律视为过期、直接忽略。
func runtimeLocalSkillRequestTerminal(status RuntimeLocalSkillRequestStatus) bool {
	return status == RuntimeLocalSkillCompleted || status == RuntimeLocalSkillFailed ||
		status == RuntimeLocalSkillTimeout || status == RuntimeLocalSkillConflict
}

// requireRuntimeCapabilityReadAccess 把 runtime 读门槛应用到「能力发现」类请求上
// （本地技能清单 + 脱敏后的 MCP 清单）。私有机器即使对 admin 也只允许 owner 访问；
// public 的 runtime 工作区成员可读。会把技能文件从 owner 机器拷走的流程要在
// requireRuntimeLocalSkillAccess 里再加一层仅限 owner 的检查。
func (h *Handler) requireRuntimeCapabilityReadAccess(w http.ResponseWriter, r *http.Request, source, runtimeID string) (runtimeIDAndWorkspace, db.Member, bool) {
	rt, member, ok := h.requireRuntimeReadAccess(w, r, source, runtimeID)
	if !ok {
		return runtimeIDAndWorkspace{}, db.Member{}, false
	}

	wsID := uuidToString(rt.WorkspaceID)

	return runtimeIDAndWorkspace{
		runtimeID:   uuidToString(rt.ID),
		workspaceID: wsID,
		provider:    rt.Provider,
		status:      rt.Status,
		ownerID:     uuidToString(rt.OwnerID),
	}, member, true
}

// requireRuntimeLocalSkillAccess 在能力读门槛之上还要求调用者是该 runtime 的 owner。
// 导入要读取 owner 机器上的完整技能文件内容，所以即便工作区 owner/admin 也不放行，
// 仅限机器主人本人。
func (h *Handler) requireRuntimeLocalSkillAccess(w http.ResponseWriter, r *http.Request, source, runtimeID string) (runtimeIDAndWorkspace, bool) {
	rt, member, ok := h.requireRuntimeCapabilityReadAccess(w, r, source, runtimeID)
	if !ok {
		return runtimeIDAndWorkspace{}, false
	}

	if rt.ownerID != "" && rt.ownerID == uuidToString(member.UserID) {
		return rt, true
	}

	writeError(w, http.StatusForbidden, "insufficient permissions")
	return runtimeIDAndWorkspace{}, false
}

// runtimeIDAndWorkspace 是从 db.AgentRuntime 行里挑出的、传给后续「面向 runtime 的操作」
// 所需的少量上下文（ID、工作区、provider、状态、owner），避免层层透传整行。
type runtimeIDAndWorkspace struct {
	runtimeID   string
	workspaceID string
	provider    string
	status      string
	ownerID     string
}

// InitiateListLocalSkills 处理 POST /api/runtimes/:id/local-skills/list——给该 runtime
// 入队一次「列出本地技能与 MCP 服务」请求并返回请求体。调用方需具备能力读权限；runtime
// 不在线则 503。入队后通过心跳把 pending 活通知 daemon（PendingWorkKindLocalSkills）。
func (h *Handler) InitiateListLocalSkills(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeCapabilityReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if rt.status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}

	req, err := h.LocalSkillListStore.Create(r.Context(), rt.runtimeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue local skills request: "+err.Error())
		return
	}
	h.requestDaemonPendingWork(rt.runtimeID, protocol.PendingWorkKindLocalSkills)
	writeJSON(w, http.StatusOK, req)
}

// GetLocalSkillListRequest 处理 GET /api/runtimes/:id/local-skills/list/:requestId——
// 前端轮询查询清单请求的进度。请求不存在或不属于该 runtime 回 404。轮询指标来源用
// RuntimeLookupSourceRuntimeLocalSkillPoll。
func (h *Handler) GetLocalSkillListRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeCapabilityReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeLocalSkillPoll, runtimeID)
	if !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	req, err := h.LocalSkillListStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if req == nil || req.RuntimeID != rt.runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}

	writeJSON(w, http.StatusOK, req)
}

// InitiateImportLocalSkill 处理 POST /api/runtimes/:id/local-skills/import——把某台 runtime
// 本地的一个技能导入工作区：入队请求（需 owner 权限、runtime 在线），请求体校验
// skill_key 必填、overwrite 时必须给合法 target_skill_id。入队后通过心跳通知 daemon 取活。
func (h *Handler) InitiateImportLocalSkill(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if rt.status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}

	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreateRuntimeLocalSkillImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.SkillKey) == "" {
		writeError(w, http.StatusBadRequest, "skill_key is required")
		return
	}

	targetSkillID := ""
	switch req.Action {
	case LocalSkillImportActionCreate:
		// 无需额外处理
	case LocalSkillImportActionOverwrite:
		// 存在性与创建者权限要到上报落库那一刻才做权威复核（从确认到写入之间技能可能
		// 变化）；这里只要求目标 ID 格式合法，避免给注定写不进去的请求排上队。
		uuid, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.TargetSkillID), "target_skill_id")
		if !ok {
			return
		}
		targetSkillID = uuidToString(uuid)
	default:
		writeError(w, http.StatusBadRequest, "invalid action")
		return
	}

	importReq, err := h.LocalSkillImportStore.Create(r.Context(), LocalSkillImportRequestInput{
		RuntimeID:     rt.runtimeID,
		CreatorID:     creatorID,
		SkillKey:      strings.TrimSpace(req.SkillKey),
		Name:          cleanOptionalString(req.Name),
		Description:   cleanOptionalString(req.Description),
		Action:        req.Action,
		TargetSkillID: targetSkillID,
		// overwrite 请求本质上是新客户端动作，即使没带标志也隐含选择加入结构化 conflict 契约。
		SupportsConflict: req.SupportsConflict || req.Action == LocalSkillImportActionOverwrite,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue local skill import: "+err.Error())
		return
	}
	h.requestDaemonPendingWork(rt.runtimeID, protocol.PendingWorkKindLocalSkillImport)
	writeJSON(w, http.StatusOK, importReq)
}

// GetLocalSkillImportRequest 处理 GET /api/runtimes/:id/local-skills/import/:requestId——
// 前端轮询查询导入请求的进度。请求不存在或不属于该 runtime 回 404。轮询来源用
// RuntimeLookupSourceRuntimeLocalSkillImportPoll。
func (h *Handler) GetLocalSkillImportRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireRuntimeLocalSkillAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeLocalSkillImportPoll, runtimeID)
	if !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	req, err := h.LocalSkillImportStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if req == nil || req.RuntimeID != rt.runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}

	writeJSON(w, http.StatusOK, req)
}

// ReportLocalSkillListResult 处理 daemon 侧对清单请求的回报（须以 daemon 身份访问）：
// 请求已终态则忽略过期回报；completed 保存技能/MCP 清单，否则记 Fail。存储层失败回 5xx
// 以便 daemon 重试。
func (h *Handler) ReportLocalSkillListResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	req, err := h.LocalSkillListStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if req == nil || req.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if runtimeLocalSkillRequestTerminal(req.Status) {
		slog.Debug("ignoring stale runtime local skills report", "runtime_id", runtimeID, "request_id", requestID, "status", req.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status       string                         `json:"status"`
		Skills       []RuntimeLocalSkillSummary     `json:"skills"`
		Supported    *bool                          `json:"supported"`
		McpServers   []RuntimeLocalMcpServerSummary `json:"mcp_servers"`
		McpSupported *bool                          `json:"mcp_supported"`
		Error        string                         `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Status == "completed" {
		supported := true
		if body.Supported != nil {
			supported = *body.Supported
		}
		mcpSupported := false
		if body.McpSupported != nil {
			mcpSupported = *body.McpSupported
		}
		if err := h.LocalSkillListStore.Complete(r.Context(), requestID, body.Skills, supported, body.McpServers, mcpSupported); err != nil {
			// 存储失败要回 5xx 让 daemon 重试，而不是把这份上报吞掉（那会让请求一直卡在
			// running 直到服务端超时——正是我们要避免的「看着正常其实什么都没发生」那类 bug）。
			slog.Error("local skills Complete failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist completion")
			return
		}
	} else {
		if err := h.LocalSkillListStore.Fail(r.Context(), requestID, body.Error); err != nil {
			slog.Error("local skills Fail failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist failure")
			return
		}
	}

	slog.Debug("runtime local skills report", "runtime_id", runtimeID, "request_id", requestID, "status", body.Status, "count", len(body.Skills))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ReportLocalSkillImportResult 处理 daemon 侧对导入请求的回报（须以 daemon 身份访问）：
// completed 时根据动作落库——overwrite 走 overwriteSkillWithFiles，create 先做同名冲突
// 检查再 createSkillWithFiles；失败路径统一转 failLocalSkillImport。若技能已经写进库而
// 存储侧 Complete 失败，create 路径会回滚刚建的技能、overwrite 路径不回滚（详见两处注释）。
func (h *Handler) ReportLocalSkillImportResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}

	requestID := chi.URLParam(r, "requestId")
	req, err := h.LocalSkillImportStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if req == nil || req.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if runtimeLocalSkillRequestTerminal(req.Status) {
		slog.Debug("ignoring stale runtime local skill import report", "runtime_id", runtimeID, "request_id", requestID, "status", req.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status string                     `json:"status"`
		Skill  *reportedRuntimeLocalSkill `json:"skill"`
		Error  string                     `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Status != "completed" {
		h.failLocalSkillImport(w, r, requestID, body.Error)
		return
	}
	if body.Skill == nil {
		h.failLocalSkillImport(w, r, requestID, "daemon returned an empty skill bundle")
		return
	}
	creatorUUID, err := util.ParseUUID(req.CreatorID)
	if err != nil {
		failMsg := "stored local skill import creator_id is invalid"
		if ferr := h.LocalSkillImportStore.Fail(r.Context(), requestID, failMsg); ferr != nil {
			slog.Error("local skill import Fail failed", "error", ferr, "request_id", requestID)
		}
		writeError(w, http.StatusInternalServerError, failMsg)
		return
	}

	name := body.Skill.Name
	if req.Name != nil {
		name = *req.Name
	}
	description := body.Skill.Description
	if req.Description != nil {
		description = *req.Description
	}

	files := make([]CreateSkillFileRequest, 0, len(body.Skill.Files))
	for _, f := range body.Skill.Files {
		if !validateFilePath(f.Path) {
			continue
		}
		files = append(files, f)
	}

	config := map[string]any{
		"origin": map[string]any{
			"type":        "runtime_local",
			"runtime_id":  runtimeID,
			"provider":    body.Skill.Provider,
			"source_path": body.Skill.SourcePath,
		},
	}

	// overwrite 路径：重新导入到已有技能。存在性与创建者权限在 overwriteSkillWithFiles 内部、
	// 与写入同一事务里复核，所以用户确认到这次上报之间目标被删（或换主）会干净地失败，
	// 不会偷偷回退成 create。
	if req.Action == LocalSkillImportActionOverwrite {
		targetUUID, perr := util.ParseUUID(req.TargetSkillID)
		if perr != nil {
			failMsg := "stored target_skill_id is invalid"
			if ferr := h.LocalSkillImportStore.Fail(r.Context(), requestID, failMsg); ferr != nil {
				slog.Error("local skill import Fail failed", "error", ferr, "request_id", requestID)
			}
			writeError(w, http.StatusInternalServerError, failMsg)
			return
		}
		resp, oerr := h.overwriteSkillWithFiles(r.Context(), skillOverwriteInput{
			WorkspaceID:   rt.WorkspaceID,
			TargetSkillID: targetUUID,
			UserID:        req.CreatorID,
			ExpectedName:  sanitizeNullBytes(name),
			Description:   description,
			Content:       body.Skill.Content,
			Config:        config,
			Files:         files,
		})
		if oerr != nil {
			failMsg := oerr.Error()
			switch {
			case errors.Is(oerr, errSkillOverwriteNotFound):
				failMsg = "target skill no longer exists"
			case errors.Is(oerr, errSkillOverwriteForbidden):
				failMsg = "you no longer have permission to overwrite this skill"
			case errors.Is(oerr, errSkillOverwriteNameMismatch):
				failMsg = "target skill name no longer matches the imported skill"
			}
			h.failLocalSkillImport(w, r, requestID, failMsg)
			return
		}
		if err := h.LocalSkillImportStore.Complete(r.Context(), requestID, resp); err != nil {
			// overwrite 已经提交；和 create 路径不同，这里绝不能靠删技能来「回滚」（那会毁掉
			// 一个早就存在的技能及其 Agent 绑定）。回 5xx 让 daemon 重试即可——重试对同一
			// UPDATE 是幂等的。
			slog.Error("local skill import overwrite Complete failed",
				"error", err, "request_id", requestID, "skill_id", resp.ID)
			writeError(w, http.StatusInternalServerError, "failed to persist import completion")
			return
		}
		slog.Debug("runtime local skill overwritten", "runtime_id", runtimeID, "request_id", requestID, "skill_id", resp.ID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	// create 路径：写库前先探测同名冲突。选择加入契约的客户端收到的是结构化终态（不算
	// 失败），可以给出覆盖/改名/跳过选项；老客户端保持旧 `failed` 行为（见
	// resolveLocalSkillConflict）。
	if existing, found, lerr := h.lookupSkillByName(r.Context(), rt.WorkspaceID, sanitizeNullBytes(name)); lerr != nil {
		h.failLocalSkillImport(w, r, requestID, "failed to check for existing skill: "+lerr.Error())
		return
	} else if found {
		h.resolveLocalSkillConflict(w, r, req, existing)
		return
	}

	resp, err := h.createSkillWithFiles(r.Context(), skillCreateInput{
		WorkspaceID: rt.WorkspaceID,
		CreatorID:   creatorUUID,
		Name:        name,
		Description: description,
		Content:     body.Skill.Content,
		Config:      config,
		Files:       files,
	})
	if err != nil {
		// 这里的唯一键冲突说明我们的查询和插入之间另一次导入抢先成功了——按冲突处理，
		// 而不是硬失败。
		if isUniqueViolation(err) {
			if existing, found, lerr := h.lookupSkillByName(r.Context(), rt.WorkspaceID, sanitizeNullBytes(name)); lerr == nil && found {
				h.resolveLocalSkillConflict(w, r, req, existing)
				return
			}
			// 重查时行又没了（插入失败与重查之间被删）：落到旧的 unique-violation 报错。
			h.failLocalSkillImport(w, r, requestID, "a skill with this name already exists")
			return
		}
		h.failLocalSkillImport(w, r, requestID, err.Error())
		return
	}

	if err := h.LocalSkillImportStore.Complete(r.Context(), requestID, resp); err != nil {
		// 技能已经写进 Postgres。若存储侧 Complete 失败，不能把这个技能丢成孤儿：daemon
		// 遇到 5xx 会重试并重建它，结果撞上同名唯一约束，用户看到的就是「导入一直失败」。
		// 所以把已写的副作用回滚掉，让重试落在干净起点上。
		slog.Error("local skill import Complete failed — rolling back created skill",
			"error", err, "request_id", requestID, "skill_id", resp.ID)
		if delErr := h.Queries.DeleteSkill(r.Context(), db.DeleteSkillParams{
			ID:          parseUUID(resp.ID),
			WorkspaceID: rt.WorkspaceID,
		}); delErr != nil {
			slog.Warn("orphan skill rollback failed", "error", delErr, "skill_id", resp.ID)
		}
		writeError(w, http.StatusInternalServerError, "failed to persist import completion")
		return
	}
	slog.Debug("runtime local skill imported", "runtime_id", runtimeID, "request_id", requestID, "skill_id", resp.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// failLocalSkillImport 把请求置为 failed 并回标准的 daemon 成功响应（200 ok）。若存储写入
// 本身失败则回 500，让 daemon 重试。
func (h *Handler) failLocalSkillImport(w http.ResponseWriter, r *http.Request, requestID, failMsg string) {
	if err := h.LocalSkillImportStore.Fail(r.Context(), requestID, failMsg); err != nil {
		slog.Error("local skill import Fail failed", "error", err, "request_id", requestID)
		writeError(w, http.StatusInternalServerError, "failed to persist failure")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// resolveLocalSkillConflict 终结一次「create 撞同名」的导入。选择加入结构化 conflict 契约
// （SupportsConflict）的客户端收到 `conflict` 状态及元信息，可给出覆盖/改名/跳过；老客户端
// 维持旧 `failed`（"同名技能已存在"）行为，早于该契约的已装 Desktop 构建在连接升级后的
// 后端时不会回归。
func (h *Handler) resolveLocalSkillConflict(w http.ResponseWriter, r *http.Request, req *RuntimeLocalSkillImportRequest, existing db.Skill) {
	if req.SupportsConflict {
		h.reportLocalSkillConflict(w, r, req.ID, req.CreatorID, existing)
		return
	}
	h.failLocalSkillImport(w, r, req.ID, "a skill with this name already exists")
}

// reportLocalSkillConflict 把同名冲突记录为终态 RuntimeLocalSkillConflict，附上结构化元信息，
// 调用方据此给出覆盖/改名/跳过选项。
func (h *Handler) reportLocalSkillConflict(w http.ResponseWriter, r *http.Request, requestID, creatorID string, existing db.Skill) {
	info := LocalSkillImportConflict{
		ExistingSkillID: uuidToString(existing.ID),
		CanOverwrite:    canOverwriteSkillByLocalImport(creatorID, existing),
	}
	if existing.CreatedBy.Valid {
		info.ExistingCreatedBy = uuidToString(existing.CreatedBy)
	}
	if err := h.LocalSkillImportStore.Conflict(r.Context(), requestID, info); err != nil {
		slog.Error("local skill import Conflict failed", "error", err, "request_id", requestID)
		writeError(w, http.StatusInternalServerError, "failed to persist conflict")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// lookupSkillByName 按 (workspace, name) 查技能。found=false 且 error 为 nil 表示不存在
// 该技能——也就是没有冲突。
func (h *Handler) lookupSkillByName(ctx context.Context, workspaceID pgtype.UUID, name string) (db.Skill, bool, error) {
	skill, err := h.Queries.GetSkillByWorkspaceAndName(ctx, db.GetSkillByWorkspaceAndNameParams{
		WorkspaceID: workspaceID,
		Name:        name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Skill{}, false, nil
		}
		return db.Skill{}, false, err
	}
	return skill, true, nil
}
