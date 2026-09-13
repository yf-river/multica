// 本文件是「Autopilot（自动驾驶规则）」的 HTTP 处理器集合：规则 CRUD、执行者
// （agent/squad）校验、订阅者、协作者（显式授权）管理、触发器（schedule/webhook）
// 管理与 webhook 签名密钥设置/轮换、运行历史查询、手动触发与配额查询。
// 贯穿全文件的模型：
//   - 写权限：创建者 / 工作区 owner-admin / 被显式授权的协作者；管理访问列表
//     的权利更窄（仅创建者与 owner-admin，防止提权）（MUL-3807）；
//   - 责任归属（MUL-4302 §3.4）：创建/删除/实质修改（目标/启用态/执行模式/描述/
//     触发器）都会以本次操作成员为发布者追加 rule-version；
//   - webhook token 与签名密钥是「可触发」级秘密，读路径对非写者一律剥除；
//   - 保存时的归属校验会加锁与 Runtime 拆除串行化，避免存档竞态。
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// computeNextRun 委托给 service 包的共享 cron 助手。
func computeNextRun(cronExpr, timezone string) (time.Time, error) {
	return service.ComputeNextRun(cronExpr, timezone)
}

// ── 响应类型 ───────────────────────────────────────────────────────────────

// AutopilotResponse 是规则的线格式（列表/详情/变更后通用）。
type AutopilotResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	ProjectID   *string `json:"project_id"`
	// AssigneeType 是 "agent" 或 "squad"。MUL-2429 的路径 A：为 "squad" 时
	// AssigneeID 指向 squad(id) 而非 agent(id)，派发时解析为 squad.leader_id。
	AssigneeType       string  `json:"assignee_type"`
	AssigneeID         string  `json:"assignee_id"`
	Status             string  `json:"status"`
	PauseReason        *string `json:"pause_reason"`
	ExecutionMode      string  `json:"execution_mode"`
	IssueTitleTemplate *string `json:"issue_title_template"`
	CreatedByType      string  `json:"created_by_type"`
	CreatedByID        string  `json:"created_by_id"`
	LastRunAt          *string `json:"last_run_at"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`

	// 仅列表端点派生的字段（detail/create/update 响应及旧服务器上缺席——客户端
	// 必须当作可选）。Enabled 仅触发器有；last_run_status 是最近一次运行状态。
	TriggerKinds  []string `json:"trigger_kinds,omitempty"`
	NextRunAt     *string  `json:"next_run_at,omitempty"`
	LastRunStatus *string  `json:"last_run_status,omitempty"`

	// 恒非 nil（未配置订阅者时为空切片），让前端可选链规则可把该字段当权威。
	Subscribers []AutopilotSubscriberEntry `json:"subscribers"`

	// CanWrite 报告请求调用者是否可对本规则执行写/执行操作——编辑、删除、触发、
	// 管理触发器/webhook 密钥（创建者、工作区 owner/admin 或显式协作者）。无调用
	// 者上下文的响应上为 nil（旧服务器省略；客户端把缺席当「未知」并回退为尝试
	// 动作）。见 MUL-3807。
	CanWrite *bool `json:"can_write,omitempty"`

	// CanManageAccess 报告调用者是否可管理协作者（访问）列表——更窄的权利，仅
	// 创建者与工作区 owner/admin 持有，被授权的协作者（能写但不能转授）没有。
	// 无调用者上下文时为 nil。见 MUL-3807。
	CanManageAccess *bool `json:"can_manage_access,omitempty"`
}

// AutopilotQuotaUsageResponse 是配额用量端点响应。
type AutopilotQuotaUsageResponse struct {
	Action        string           `json:"action"`
	Used          *int64           `json:"used"`
	Reserved      *int64           `json:"reserved"`
	Total         *int64           `json:"total"`
	Limit         *int64           `json:"limit"`
	Reached       *bool            `json:"reached"`
	PeriodStart   *string          `json:"period_start"`
	PeriodEnd     *string          `json:"period_end"`
	ResetAt       *string          `json:"reset_at"`
	BlockedCounts map[string]int64 `json:"blocked_counts"`
}

// AutopilotCollaboratorEntry 是被显式授予某规则写权限的成员，出现在详情响应与
// 协作者端点上。
type AutopilotCollaboratorEntry struct {
	UserType  string `json:"user_type"`
	UserID    string `json:"user_id"`
	GrantedBy string `json:"granted_by"`
	CreatedAt string `json:"created_at"`
}

func collaboratorToEntry(c db.AutopilotCollaborator) AutopilotCollaboratorEntry {
	return AutopilotCollaboratorEntry{
		UserType:  c.UserType,
		UserID:    uuidToString(c.UserID),
		GrantedBy: uuidToString(c.GrantedBy),
		CreatedAt: timestampToString(c.CreatedAt),
	}
}

// user_type 在 DB 层限定为 "member"；字段留在线上，让未来扩展到 agent/squad
// 是增量而非破坏性的。
type AutopilotSubscriberEntry struct {
	UserType  string `json:"user_type"`
	UserID    string `json:"user_id"`
	CreatedAt string `json:"created_at"`
}

// AutopilotTriggerResponse 是触发器（schedule/webhook）的线格式。
type AutopilotTriggerResponse struct {
	ID             string  `json:"id"`
	AutopilotID    string  `json:"autopilot_id"`
	Kind           string  `json:"kind"`
	Enabled        bool    `json:"enabled"`
	CronExpression *string `json:"cron_expression"`
	Timezone       *string `json:"timezone"`
	NextRunAt      *string `json:"next_run_at"`
	WebhookToken   *string `json:"webhook_token"`
	// WebhookPath 由 webhook_token 计算而来。webhook 触发器恒有；schedule/api 为
	// nil。不落库——见 triggerToResponse。
	WebhookPath *string `json:"webhook_path"`
	// WebhookURL 是由服务端 MULTICA_PUBLIC_URL 拼出的绝对 URL。服务端未配置公共
	// URL 时为 nil；此时客户端自己用 webhook_path 加 API base/当前 origin 拼。
	WebhookURL *string `json:"webhook_url"`
	// Provider 指明按端点签章/去重约定。目前："generic"（仅 bearer URL，
	// Idempotency-Key 去重）或 "github"（X-Hub-Signature-256 + X-GitHub-Delivery）。
	// 非 webhook 触发器省略。
	Provider *string `json:"provider"`
	// HasSigningSecret 表示该触发器是否已配置签章密钥。密钥本身永不返回——经专用
	// 只写端点设置。非 webhook 触发器恒为 false。
	HasSigningSecret bool `json:"has_signing_secret"`
	// SigningSecretHint 是所配置密钥的最后 4 个字符，帮运维在 UI 里区分两个密钥。
	// 未配置时为 nil。
	SigningSecretHint *string `json:"signing_secret_hint"`
	Label             *string `json:"label"`
	LastFiredAt       *string `json:"last_fired_at"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	// EventFilters 是声明的活动范围。仅 webhook 触发器有；接受所有事件时省略。
	// 序列化为 {event, actions?} 对象数组——绝不是 base64 串（encoding/json 对
	// []byte 会那么做）。
	EventFilters []WebhookEventFilter `json:"event_filters,omitempty"`
}

// AutopilotRunResponse 是 autopilot 一次「运行」（触发尝试）的对外线格式：除常规
// 状态/时间字段外，TriggerPayload 与 Result 随触发来源与执行结果而变，ReasonCode
// 对 skipped/failed 给出稳定的可本地化分类（UI 不直接回显英文原因，避免点名私有
// 指派人 agent）。
type AutopilotRunResponse struct {
	ID            string  `json:"id"`
	AutopilotID   string  `json:"autopilot_id"`
	TriggerID     *string `json:"trigger_id"`
	Source        string  `json:"source"`
	Status        string  `json:"status"`
	IssueID       *string `json:"issue_id"`
	TaskID        *string `json:"task_id"`
	TriggeredAt   string  `json:"triggered_at"`
	CompletedAt   *string `json:"completed_at"`
	FailureReason *string `json:"failure_reason"`
	// ReasonCode 是对非成功运行（skipped/failed）的稳定、可本地化、可枚举的分类，
	// 在决策源持久化。UI 本地化它而不是回声英文原因（可能点名私有指派人 agent）。
	// 增量字段：legacy/成功路径运行为 nil。
	ReasonCode     *string `json:"reason_code,omitempty"`
	TriggerPayload any     `json:"trigger_payload"`
	Result         any     `json:"result"`
	CreatedAt      string  `json:"created_at"`
}

// ── 转换器 ────────────────────────────────────────────────────────────────

// autopilotToResponse 把 DB 行+订阅者转为响应。
func autopilotToResponse(a db.Autopilot, subscribers []db.AutopilotSubscriber) AutopilotResponse {
	assigneeType := a.AssigneeType
	if assigneeType == "" {
		// MUL-2429 前的旧行对过期 schema 视图可能是 ""；默认 "agent" 保持 API
		// 契约非空。
		assigneeType = "agent"
	}
	subResp := make([]AutopilotSubscriberEntry, len(subscribers))
	for i, s := range subscribers {
		subResp[i] = AutopilotSubscriberEntry{
			UserType:  s.UserType,
			UserID:    uuidToString(s.UserID),
			CreatedAt: timestampToString(s.CreatedAt),
		}
	}
	return AutopilotResponse{
		ID:                 uuidToString(a.ID),
		WorkspaceID:        uuidToString(a.WorkspaceID),
		Title:              a.Title,
		Description:        textToPtr(a.Description),
		ProjectID:          uuidToPtr(a.ProjectID),
		AssigneeType:       assigneeType,
		AssigneeID:         uuidToString(a.AssigneeID),
		Status:             a.Status,
		PauseReason:        textToPtr(a.PauseReason),
		ExecutionMode:      a.ExecutionMode,
		IssueTitleTemplate: textToPtr(a.IssueTitleTemplate),
		CreatedByType:      a.CreatedByType,
		CreatedByID:        uuidToString(a.CreatedByID),
		LastRunAt:          timestampToPtr(a.LastRunAt),
		CreatedAt:          timestampToString(a.CreatedAt),
		UpdatedAt:          timestampToString(a.UpdatedAt),
		Subscribers:        subResp,
	}
}

func (h *Handler) triggerToResponse(t db.AutopilotTrigger) AutopilotTriggerResponse {
	resp := AutopilotTriggerResponse{
		ID:             uuidToString(t.ID),
		AutopilotID:    uuidToString(t.AutopilotID),
		Kind:           t.Kind,
		Enabled:        t.Enabled,
		CronExpression: textToPtr(t.CronExpression),
		Timezone:       textToPtr(t.Timezone),
		NextRunAt:      timestampToPtr(t.NextRunAt),
		WebhookToken:   textToPtr(t.WebhookToken),
		Label:          textToPtr(t.Label),
		LastFiredAt:    timestampToPtr(t.LastFiredAt),
		CreatedAt:      timestampToString(t.CreatedAt),
		UpdatedAt:      timestampToString(t.UpdatedAt),
	}
	if t.Kind == "webhook" && t.WebhookToken.Valid && t.WebhookToken.String != "" {
		path := webhookPathForToken(t.WebhookToken.String)
		resp.WebhookPath = &path
		if h.cfg.PublicURL != "" {
			full := h.cfg.PublicURL + path
			resp.WebhookURL = &full
		}
		provider := t.Provider
		if provider == "" {
			provider = "generic"
		}
		resp.Provider = &provider
		if t.SigningSecret.Valid && t.SigningSecret.String != "" {
			resp.HasSigningSecret = true
			hint := signingSecretHint(t.SigningSecret.String)
			resp.SigningSecretHint = &hint
		}
		if len(t.EventFilters) > 0 {
			var filters []WebhookEventFilter
			if err := json.Unmarshal(t.EventFilters, &filters); err == nil {
				resp.EventFilters = filters
			}
			// 解组失败时有意丢弃字段，而不暴露原始字节或 500——严格的写时校验
			// 本应让该分支不可达，而损坏行万一溜进来匹配器也会失败关闭。
		}
	}
	return resp
}

// signingSecretHint 返回签章密钥的最后 4 个字符，让 UI 能看到「已配置 vs 已轮换」
// 而不暴露密钥本身。不足 4 字符（校验器已拒绝）直接返回空串。
func signingSecretHint(secret string) string {
	if len(secret) < 4 {
		return ""
	}
	return secret[len(secret)-4:]
}

// webhookPathForToken 拼出公共入口路由使用的路径。保持为自由函数（无 Handler
// receiver），测试代码构造预期 URL 时无需实例化 Handler 即可调用。
func webhookPathForToken(token string) string {
	return "/api/webhooks/autopilots/" + token
}

func runToResponse(r db.AutopilotRun) AutopilotRunResponse {
	var payload any
	if r.TriggerPayload != nil {
		json.Unmarshal(r.TriggerPayload, &payload)
	}
	var result any
	if r.Result != nil {
		json.Unmarshal(r.Result, &result)
	}
	return AutopilotRunResponse{
		ID:             uuidToString(r.ID),
		AutopilotID:    uuidToString(r.AutopilotID),
		TriggerID:      uuidToPtr(r.TriggerID),
		Source:         r.Source,
		Status:         r.Status,
		IssueID:        uuidToPtr(r.IssueID),
		TaskID:         uuidToPtr(r.TaskID),
		TriggeredAt:    timestampToString(r.TriggeredAt),
		CompletedAt:    timestampToPtr(r.CompletedAt),
		FailureReason:  textToPtr(r.FailureReason),
		ReasonCode:     textToPtr(r.ReasonCode),
		TriggerPayload: payload,
		Result:         result,
		CreatedAt:      timestampToString(r.CreatedAt),
	}
}

// runToResponseSlim 镜像 runToResponse 但省略 TriggerPayload，供列表端点使用——
// 那里原样回声完整 webhook 信封（单条至多 256 KiB × N 行）会主导响应体积。用户
// 打开运行详情对话框时客户端经 GET /api/autopilots/{id}/runs/{runId} 取完整负载。
func runToResponseSlim(r db.AutopilotRun) AutopilotRunResponse {
	resp := runToResponse(r)
	resp.TriggerPayload = nil
	return resp
}

// ── 请求类型 ───────────────────────────────────────────────────────────────

// CreateAutopilotRequest 是创建规则的请求体。
type CreateAutopilotRequest struct {
	Title       string  `json:"title"`
	Description *string `json:"description"`
	ProjectID   *string `json:"project_id"`
	// AssigneeType 可选，旧客户端缺省为 "agent"。
	AssigneeType       *string           `json:"assignee_type"`
	AssigneeID         string            `json:"assignee_id"`
	ExecutionMode      string            `json:"execution_mode"`
	IssueTitleTemplate *string           `json:"issue_title_template"`
	Subscribers        []SubscriberInput `json:"subscribers"`
}

// UpdateAutopilotRequest 是更新规则的请求体（指针字段=缺席/清空/设置三态）。
type UpdateAutopilotRequest struct {
	Title              *string `json:"title"`
	Description        *string `json:"description"`
	ProjectID          *string `json:"project_id"`
	AssigneeType       *string `json:"assignee_type"`
	AssigneeID         *string `json:"assignee_id"`
	Status             *string `json:"status"`
	ExecutionMode      *string `json:"execution_mode"`
	IssueTitleTemplate *string `json:"issue_title_template"`
	// 出现时整体替换；省略则不动订阅者。
	Subscribers []SubscriberInput `json:"subscribers"`
}

// SubscriberInput 是订阅者输入（user_type + user_id）。
type SubscriberInput struct {
	UserType string `json:"user_type"`
	UserID   string `json:"user_id"`
}

// CreateAutopilotTriggerRequest 是创建触发器的请求体。
type CreateAutopilotTriggerRequest struct {
	Kind           string  `json:"kind"`
	CronExpression *string `json:"cron_expression"`
	Timezone       *string `json:"timezone"`
	Label          *string `json:"label"`
	// Provider 目前只对 kind=webhook 有意义。允许值："generic"（默认）或
	// "github"。未设置 → "generic"。
	Provider *string `json:"provider"`
	// EventFilters 是可选的 {event, actions?} 作用域列表。仅 webhook 触发器有
	// 意义。nil/空 = 「接受所有事件」。
	EventFilters []WebhookEventFilter `json:"event_filters,omitempty"`
}

// SetSigningSecretRequest 是 PUT
// /api/autopilots/{id}/triggers/{triggerId}/signing-secret 的请求体。独立类型，
// 让密钥绝不与触发器更新路径上的其他字段同体——记录请求体的调试 handler 不会
// 意外捡到它。
type SetSigningSecretRequest struct {
	// SigningSecret 是新 HMAC 密钥。显式发空串即清除密钥（禁用签名校验）。请传
	// 有足够熵的值——GitHub 文档建议至少 32 个随机字符；非空输入我们强制最低
	// 16 字符。
	SigningSecret string `json:"signing_secret"`
}

// UpdateAutopilotTriggerRequest 是更新触发器的请求体。
type UpdateAutopilotTriggerRequest struct {
	Enabled        *bool   `json:"enabled"`
	CronExpression *string `json:"cron_expression"`
	Timezone       *string `json:"timezone"`
	Label          *string `json:"label"`
	// EventFilters 是带三态 PATCH 语义的事件过滤集合：
	//
	//   - 缺席 / 显式 null（nil 指针）→ 保留现值。
	//   - 显式 []（非 nil、长度 0）→ 清除过滤（触发器回到「接受所有事件」）。
	//   - 显式 [...]→ 整体替换。
	//
	// 这就是指针重要的原因：用裸 []WebhookEventFilter 无法区分「PATCH 体里没有
	// 该字段」与「有字段但为空」，用户一旦设置过过滤就永远清不掉。
	EventFilters *[]WebhookEventFilter `json:"event_filters,omitempty"`
}

// ── 处理器 ────────────────────────────────────────────────────────────────

// ListAutopilots 处理 GET /api/autopilots。工作区成员可见；返回带订阅者、
// can_write、can_manage_access 的列表。
func (h *Handler) ListAutopilots(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	var statusFilter pgtype.Text
	if s := r.URL.Query().Get("status"); s != "" {
		statusFilter = pgtype.Text{String: s, Valid: true}
	}

	autopilots, err := h.Queries.ListAutopilots(r.Context(), db.ListAutopilotsParams{
		WorkspaceID: parseUUID(workspaceID),
		Status:      statusFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list autopilots")
		return
	}

	// 解析调用者写权限，用于逐行 can_write。协作者授权一次性取成集合（按
	// autopilot id 键），该标志无需逐行查询。成员缺失（工作区成员中间件后不应
	// 发生）时一律 can_write=false。
	caller, callerErr := h.getWorkspaceMember(r.Context(), requestUserID(r), workspaceID)
	collabSet := map[string]struct{}{}
	if callerErr == nil {
		if ids, err := h.Queries.ListAutopilotIDsForCollaborator(r.Context(), caller.UserID); err == nil {
			for _, id := range ids {
				collabSet[uuidToString(id)] = struct{}{}
			}
		}
	}

	// 订阅者整页一次批量查询。早期版本为躲 N+1 传 nil，但响应类型把 nil 切片
	// 序列化成 [] 而非省略——于是列表里每个 autopilot 都声称没有订阅者，而详情
	// 端点报出真实的——静默错值比缺失更糟（MUL-6680）。批量按主键首列键控，每页
	// 一次索引查询，而非每行一次。
	subsByAutopilot := map[string][]db.AutopilotSubscriber{}
	autopilotIDs := make([]pgtype.UUID, 0, len(autopilots))
	for _, row := range autopilots {
		autopilotIDs = append(autopilotIDs, row.Autopilot.ID)
	}
	if len(autopilotIDs) > 0 {
		subs, err := h.Queries.ListAutopilotSubscribersForAutopilots(r.Context(), autopilotIDs)
		if err != nil {
			// 失败关闭。这里退化为空集等于重新引入本端点刚修好的 bug：subscribers
			// 是无 omitempty、文档化为权威的字段，读取失败时空值无法与「未配置」区分
			// ——调用者据此行事可能覆盖真实订阅者列表。可见可重试的错误是唯一诚实的
			// 回答。
			writeError(w, http.StatusInternalServerError, "failed to list autopilot subscribers")
			return
		}
		for _, s := range subs {
			id := uuidToString(s.AutopilotID)
			subsByAutopilot[id] = append(subsByAutopilot[id], s)
		}
	}

	resp := make([]AutopilotResponse, len(autopilots))
	for i, row := range autopilots {
		r := autopilotToResponse(row.Autopilot, subsByAutopilot[uuidToString(row.Autopilot.ID)])
		r.TriggerKinds = row.TriggerKinds
		if row.NextRunAt.Valid {
			r.NextRunAt = timestampToPtr(row.NextRunAt)
		}
		if row.LastRunStatus != "" {
			s := row.LastRunStatus
			r.LastRunStatus = &s
		}
		if callerErr == nil {
			_, isCollaborator := collabSet[uuidToString(row.Autopilot.ID)]
			cw := autopilotWriteByOwnership(row.Autopilot, caller) || isCollaborator
			r.CanWrite = &cw
		}
		resp[i] = r
	}
	writeJSON(w, http.StatusOK, map[string]any{"autopilots": resp, "total": len(resp)})
}

// GetAutopilot 返回单条规则详情（规则 + 触发器 + 协作者）。经
// loadAutopilotInWorkspace 限工作区；can_write 由调用者身份与协作者授权决定，
// 并同时门控 webhook token/URL 是否暴露（能读到 token 即等于能外部触发，见
// MUL-3807）；can_manage_access 更窄——只有规则创建者或工作区 owner/admin 才能
// 管理访问列表。
func (h *Handler) GetAutopilot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
		return
	}

	subs, err := h.Queries.ListAutopilotSubscribers(r.Context(), autopilot.ID)
	if err != nil {
		// 与列表端点同样的理由失败关闭：空 subscribers 数组是断言而非缺席，且本
		// 响应会被客户端原样回灌进整体替换的 PATCH。
		writeError(w, http.StatusInternalServerError, "failed to list autopilot subscribers")
		return
	}
	resp := autopilotToResponse(autopilot, subs)

	// 只解析一次调用者写权限：既盖章 can_write，也门控 webhook 密钥暴露。
	// Webhook token 是可触发级密钥（谁能读到谁就能绕过权限体系从外部触发该
	// autopilot），因此只有写者——创建者、工作区 owner/admin 或被授权的协作者——
	// 能拿到实时 token/URL；其他成员只见剥掉密钥字段后的触发器元数据（MUL-3807）。
	canWrite := false
	canManageAccess := false
	if member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), workspaceID); err == nil {
		canWrite = h.memberCanWriteAutopilot(r.Context(), autopilot, member)
		// 管理访问列表比写更窄：协作者能写但不能转授（MUL-3807）。
		canManageAccess = autopilotWriteByOwnership(autopilot, member)
	}
	resp.CanWrite = &canWrite
	resp.CanManageAccess = &canManageAccess

	// 附上触发器。
	triggers, err := h.Queries.ListAutopilotTriggers(r.Context(), autopilot.ID)
	if err != nil {
		triggers = nil
	}
	triggerResp := make([]AutopilotTriggerResponse, len(triggers))
	for i, t := range triggers {
		tr := h.triggerToResponse(t)
		if !canWrite {
			tr.WebhookToken = nil
			tr.WebhookPath = nil
			tr.WebhookURL = nil
		}
		triggerResp[i] = tr
	}

	// 附上显式协作者授权，让「管理访问」UI 无需第二次往返即可渲染当前列表。
	collaborators, err := h.Queries.ListAutopilotCollaborators(r.Context(), autopilot.ID)
	if err != nil {
		collaborators = nil
	}
	collabResp := make([]AutopilotCollaboratorEntry, len(collaborators))
	for i, c := range collaborators {
		collabResp[i] = collaboratorToEntry(c)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"autopilot":     resp,
		"triggers":      triggerResp,
		"collaborators": collabResp,
	})
}

// loadAutopilotInWorkspace 加载工作区内规则，失败时已写好 404。
func (h *Handler) loadAutopilotInWorkspace(w http.ResponseWriter, r *http.Request, autopilotID, workspaceID string) (db.Autopilot, bool) {
	autopilotUUID, ok := parseUUIDOrBadRequest(w, autopilotID, "autopilot id")
	if !ok {
		return db.Autopilot{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Autopilot{}, false
	}

	autopilot, err := h.Queries.GetAutopilotInWorkspace(r.Context(), db.GetAutopilotInWorkspaceParams{
		ID:          autopilotUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "autopilot not found")
		return db.Autopilot{}, false
	}
	return autopilot, true
}

// autopilotWriteByOwnership 是写谓词中免查询的隐式部分：规则创建者与工作区
// owner/admin 恒有写权限。显式协作者授权（memberCanWriteAutopilot）叠在其上
// （MUL-3807）。
func autopilotWriteByOwnership(ap db.Autopilot, member db.Member) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	return ap.CreatedByType == "member" && uuidToString(ap.CreatedByID) == uuidToString(member.UserID)
}

// memberCanWriteAutopilot 判断成员能否对本规则执行写或执行操作——编辑、删除、
// 触发运行、重放投递、管理其触发器/webhook 密钥与访问列表。写权限归创建者、
// 工作区 owner/admin 与显式授权为协作者的成员。同一谓词也门控读路径是否暴露
// webhook 密钥——看到 webhook token 等于能触发。
func (h *Handler) memberCanWriteAutopilot(ctx context.Context, ap db.Autopilot, member db.Member) bool {
	if autopilotWriteByOwnership(ap, member) {
		return true
	}
	granted, err := h.Queries.IsAutopilotCollaborator(ctx, db.IsAutopilotCollaboratorParams{
		AutopilotID: ap.ID,
		UserID:      member.UserID,
	})
	return err == nil && granted
}

// requireAutopilotWrite 为变更/执行请求执行 memberCanWriteAutopilot。失败时已写
// 好响应（调用者非工作区成员为 404，否则 403）并返回 false；调用方须提前 return。
// 成功返回 true。
func (h *Handler) requireAutopilotWrite(w http.ResponseWriter, r *http.Request, ap db.Autopilot, workspaceID string) bool {
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return false
	}
	if !h.memberCanWriteAutopilot(r.Context(), ap, member) {
		writeError(w, http.StatusForbidden, "only the autopilot creator, a workspace admin, or a granted collaborator can manage this autopilot")
		return false
	}
	return true
}

// requireAutopilotAccessManagement 执行协作者（访问列表）端点用的更窄谓词：只有
// 规则创建者或工作区 owner/admin 能授予/撤销访问。被授权的协作者保留自己的写/
// 执行权（编辑、触发、管理触发器/密钥），但不能管理访问列表——防止协作者给他人
// 转授或撤销同伴（提权）。见 MUL-3807。
func (h *Handler) requireAutopilotAccessManagement(w http.ResponseWriter, r *http.Request, ap db.Autopilot, workspaceID string) bool {
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return false
	}
	if !autopilotWriteByOwnership(ap, member) {
		writeError(w, http.StatusForbidden, "only the autopilot creator or a workspace admin can manage access")
		return false
	}
	return true
}

// CreateAutopilot 创建规则（默认 active）。解析校验后开单事务：先取订阅者锁并
// 复查成员身份、再校验指派目标（agent/squad）与 Runtime 就绪，然后 INSERT 规则
// 行，并以创建成员为发布者原子追加 rule-version v1（MUL-4302 §3.4）——创建规则
// 本身即一次实质发布。事务内写入订阅者行、落域事件，提交后广播并记计量。
func (h *Handler) CreateAutopilot(w http.ResponseWriter, r *http.Request) {
	var req CreateAutopilotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.AssigneeID == "" {
		writeError(w, http.StatusBadRequest, "assignee_id is required")
		return
	}
	if req.ExecutionMode == "" {
		writeError(w, http.StatusBadRequest, "execution_mode is required")
		return
	}
	if req.ExecutionMode != "create_issue" && req.ExecutionMode != "run_only" {
		writeError(w, http.StatusBadRequest, "execution_mode must be create_issue or run_only")
		return
	}
	if req.IssueTitleTemplate != nil {
		if err := service.ValidateIssueTitleTemplate(*req.IssueTitleTemplate); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	assigneeUUID, ok := parseUUIDOrBadRequest(w, req.AssigneeID, "assignee_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	assigneeType := "agent"
	if req.AssigneeType != nil && *req.AssigneeType != "" {
		assigneeType = *req.AssigneeType
	}
	if !isValidAutopilotAssigneeType(assigneeType) {
		writeError(w, http.StatusBadRequest, "assignee_type must be agent or squad")
		return
	}
	projectID, ok := h.parseAutopilotProjectID(w, r, req.ProjectID, wsUUID)
	if !ok {
		return
	}

	// 插入前先解析，畸形负载不必开事务。
	subscribers, ok := parseAutopilotSubscribers(w, req.Subscribers)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create autopilot")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 这必须是事务里第一个锁族。成员撤销在清理模板、删除成员行前取同样的逐
	// (workspace, user) 锁。加锁后复查成员身份意味着：要么本保存先提交、撤销随后
	// 清理它；要么撤销先提交、本保存拒绝已离开的订阅者。
	if !h.lockAndValidateAutopilotSubscribers(w, r, qtx, subscribers, wsUUID) {
		return
	}

	// 保存时的就绪校验与插入放同一事务。指派锁让本路径与 Runtime 拆除串行化，
	// 活跃 Autopilot 不会在拆除的暂停清扫之后溜进来。
	if !h.validateAutopilotAssigneeForSave(w, r, qtx, assigneeType, assigneeUUID, wsUUID, true) {
		return
	}

	autopilot, err := qtx.CreateAutopilot(r.Context(), db.CreateAutopilotParams{
		WorkspaceID:        wsUUID,
		Title:              req.Title,
		AssigneeType:       assigneeType,
		AssigneeID:         assigneeUUID,
		Status:             "active",
		ExecutionMode:      req.ExecutionMode,
		CreatedByType:      "member",
		CreatedByID:        parseUUID(userID),
		Description:        ptrToText(req.Description),
		IssueTitleTemplate: ptrToText(req.IssueTitleTemplate),
		ProjectID:          projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create autopilot")
		return
	}

	// 创建规则本身就是一次实质发布：以创建成员为发布者追加 rule-version v1，让
	// 每个 autopilot 在派发时刻都有可追责的人类（MUL-4302 §3.4）。
	if err := h.recordAutopilotRuleVersion(r.Context(), qtx, autopilot, "member", parseUUID(userID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create autopilot")
		return
	}

	for _, subscriber := range subscribers {
		if err := qtx.AddAutopilotSubscriber(r.Context(), db.AddAutopilotSubscriberParams{
			AutopilotID: autopilot.ID,
			UserType:    "member",
			UserID:      subscriber.UserID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to add autopilot subscriber")
			return
		}
	}
	subs, err := qtx.ListAutopilotSubscribers(r.Context(), autopilot.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load autopilot subscribers")
		return
	}
	resp := autopilotToResponse(autopilot, subs)
	createdEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotCreated, autopilot, "member", userID, "created",
		autopilot.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		map[string]any{"autopilot": resp},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create autopilot")
		return
	}
	h.publishEvent(createdEvent)
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.AutopilotCreated(
		userID,
		workspaceID,
		uuidToString(autopilot.ID),
		"manual",
		"manual",
	))
	writeJSON(w, http.StatusCreated, resp)
}

// autopilotSubscriberCandidate 是解析后待校验的订阅者（user id + 在原始输入中
// 的下标，用于错误定位）。
type autopilotSubscriberCandidate struct {
	UserID     pgtype.UUID
	InputIndex int
}

// parseAutopilotSubscribers 只校验线格式，不读可变的成员资格状态。成员资格要等
// 写事务持有与成员撤销相同的串行化锁之后再查。
func parseAutopilotSubscribers(w http.ResponseWriter, raw []SubscriberInput) ([]autopilotSubscriberCandidate, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	out := make([]autopilotSubscriberCandidate, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for i, entry := range raw {
		if entry.UserType != "member" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("subscribers[%d].user_type must be 'member'", i))
			return nil, false
		}
		if entry.UserID == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("subscribers[%d].user_id is required", i))
			return nil, false
		}
		uid, ok := parseUUIDOrBadRequest(w, entry.UserID, fmt.Sprintf("subscribers[%d].user_id", i))
		if !ok {
			return nil, false
		}
		canonicalID := uuidToString(uid)
		if seen[canonicalID] {
			continue
		}
		seen[canonicalID] = true
		out = append(out, autopilotSubscriberCandidate{UserID: uid, InputIndex: i})
	}
	return out, true
}

// lockAndValidateAutopilotSubscribers 让订阅者模板写入与成员撤销串行化。锁与行
// 检查都按规范 UUID 顺序进行，两个含相同成员但请求顺序不同的保存不会互相死锁。
func (h *Handler) lockAndValidateAutopilotSubscribers(
	w http.ResponseWriter,
	r *http.Request,
	qtx *db.Queries,
	subscribers []autopilotSubscriberCandidate,
	workspaceID pgtype.UUID,
) bool {
	ordered := append([]autopilotSubscriberCandidate(nil), subscribers...)
	sort.Slice(ordered, func(i, j int) bool {
		return uuidToString(ordered[i].UserID) < uuidToString(ordered[j].UserID)
	})

	for _, subscriber := range ordered {
		if err := qtx.LockSubscriberWrites(r.Context(), db.LockSubscriberWritesParams{
			WorkspaceID: workspaceID,
			UserID:      subscriber.UserID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to validate autopilot subscribers")
			return false
		}
	}
	for _, subscriber := range ordered {
		if _, err := qtx.LockActiveMember(r.Context(), db.LockActiveMemberParams{
			UserID:      subscriber.UserID,
			WorkspaceID: workspaceID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf(
					"subscribers[%d] is not a member of this workspace", subscriber.InputIndex,
				))
				return false
			}
			writeError(w, http.StatusInternalServerError, "failed to validate autopilot subscribers")
			return false
		}
	}
	return true
}

// UpdateAutopilot 整体替换式 PATCH 更新规则。入口先做写权限校验（创建者 /
// 工作区 owner-admin / 协作者），随后在同一事务内完成：加锁复查乐观并发
// （updated_at 变了就 409）、校验并写回可改字段、锁定订阅者身份、按需重新
// 校验指派目标与 Runtime 就绪。assignee_type 与 assignee_id 必须成对出现，
// subscribers 传了就整体替换。实质变更（目标/启停/执行模式等）会以当前成员
// 为发布者重发 rule-version，让后续运行归因到最后改动规则的人（MUL-4302）。
func (h *Handler) UpdateAutopilot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	prev, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotWrite(w, r, prev, workspaceID) {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var req UpdateAutopilotRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var rawFields map[string]json.RawMessage
	json.Unmarshal(bodyBytes, &rawFields)

	params := db.UpdateAutopilotParams{
		ID:                 prev.ID,
		Description:        prev.Description,
		AssigneeID:         prev.AssigneeID,
		IssueTitleTemplate: prev.IssueTitleTemplate,
		ProjectID:          prev.ProjectID,
	}
	if req.Title != nil {
		params.Title = pgtype.Text{String: *req.Title, Valid: true}
	}
	if req.Status != nil {
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.ExecutionMode != nil {
		params.ExecutionMode = pgtype.Text{String: *req.ExecutionMode, Valid: true}
	}
	if _, ok := rawFields["description"]; ok {
		params.Description = ptrToText(req.Description)
	}
	if _, ok := rawFields["issue_title_template"]; ok {
		if req.IssueTitleTemplate != nil {
			if err := service.ValidateIssueTitleTemplate(*req.IssueTitleTemplate); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		params.IssueTitleTemplate = ptrToText(req.IssueTitleTemplate)
	}
	if _, ok := rawFields["project_id"]; ok {
		projectID, ok := h.parseAutopilotProjectID(w, r, req.ProjectID, prev.WorkspaceID)
		if !ok {
			return
		}
		params.ProjectID = projectID
	}
	// assignee_type 与 assignee_id 成对校验：未带新 id 就在 agent/squad 之间切换，
	// 会让行指向错误表。期望客户端任何变更都同时发两个字段；只改其一的部分更新
	// 被拒绝。
	_, typeSent := rawFields["assignee_type"]
	_, idSent := rawFields["assignee_id"]
	nextType := prev.AssigneeType
	nextID := prev.AssigneeID
	if typeSent || idSent {
		if typeSent && req.AssigneeType != nil && *req.AssigneeType != "" {
			nextType = *req.AssigneeType
		}
		if !isValidAutopilotAssigneeType(nextType) {
			writeError(w, http.StatusBadRequest, "assignee_type must be agent or squad")
			return
		}
		if idSent {
			if req.AssigneeID == nil {
				writeError(w, http.StatusBadRequest, "assignee_id cannot be null")
				return
			}
			parsed, ok := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
			if !ok {
				return
			}
			nextID = parsed
		}
		// 无成对 id 就拒绝 agent↔squad 切换，否则行会在 assignee_type='squad'
		// 下指向 agent(id)，反之亦然。
		if typeSent && !idSent && nextType != prev.AssigneeType {
			writeError(w, http.StatusBadRequest, "assignee_id is required when changing assignee_type")
			return
		}
		if typeSent {
			params.AssigneeType = pgtype.Text{String: nextType, Valid: true}
		}
		if idSent {
			params.AssigneeID = nextID
		}
	}

	// 先解析订阅者线值。可变的成员资格在事务开始后、撤销串行化锁下重新校验。
	var (
		subscribers        []autopilotSubscriberCandidate
		replaceSubscribers bool
	)
	if _, sent := rawFields["subscribers"]; sent {
		replaceSubscribers = true
		validated, vok := parseAutopilotSubscribers(w, req.Subscribers)
		if !vok {
			return
		}
		subscribers = validated
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update autopilot")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 在指派行与 autopilot 行之前锁订阅者身份。这是 CreateAutopilot 与成员撤销
	// 使用的同一全局顺序。
	if !h.lockAndValidateAutopilotSubscribers(w, r, qtx, subscribers, prev.WorkspaceID) {
		return
	}

	// 改派目标必须校验多态引用；恢复 active 还要校验 Runtime 就绪。都放在本事务
	// 里，让 Runtime 拆除要么在其提交后暂停活跃行，要么先赢并让激活带着有用的
	// 恢复提示失败。
	nextStatus := prev.Status
	if req.Status != nil {
		nextStatus = *req.Status
	}
	validateAssignee := typeSent || idSent || (req.Status != nil && *req.Status == "active")
	if validateAssignee && !h.validateAutopilotAssigneeForSave(
		w, r, qtx, nextType, nextID, prev.WorkspaceID, nextStatus == "active",
	) {
		return
	}

	// 指派锁必须先取。Runtime 拆除与 squad leader 变更在改到匹配的 Autopilot 行
	// 之前也锁 Agent/Squad；保持这个全局顺序能防止 Agent↔Autopilot 死锁。
	lockedPrev, err := qtx.LockAutopilotForUpdate(r.Context(), db.LockAutopilotForUpdateParams{
		ID:          prev.ID,
		WorkspaceID: prev.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "autopilot not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update autopilot")
		return
	}
	if lockedPrev.UpdatedAt.Valid != prev.UpdatedAt.Valid ||
		(lockedPrev.UpdatedAt.Valid && !lockedPrev.UpdatedAt.Time.Equal(prev.UpdatedAt.Time)) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "the autopilot changed while it was being edited; reload and try again.",
			"code":  "autopilot_update_conflict",
		})
		return
	}

	autopilot, err := qtx.UpdateAutopilot(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update autopilot")
		return
	}

	// 实质变更（指派目标 / 启停状态 / 执行模式）会重发规则：以「当前成员」为
	// 发布者追加新版本，让之后的运行归因到最后一个改变规则语义的人，而不是
	// 最初创建者。纯外观编辑（title/description/template）不写版本，追责保持
	// 在上一发布者（MUL-4302 §3.4）。
	if autopilotRuleSubstantiveChange(prev, autopilot) {
		if err := h.recordAutopilotRuleVersion(r.Context(), qtx, autopilot, "member", parseUUID(userID)); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update autopilot")
			return
		}
		// autopilot 级实质编辑管辖其所有触发器，因此每个触发器的责任都转移到
		// 本次编辑者（source=trigger_owner）；仅改单个触发器的编辑只重盖自己的
		// 行（见 UpdateAutopilotTrigger）。
		if err := qtx.SetAutopilotTriggerPublishersByAutopilot(r.Context(), db.SetAutopilotTriggerPublishersByAutopilotParams{
			AutopilotID:     autopilot.ID,
			PublishedByType: pgtype.Text{String: "member", Valid: true},
			PublishedByID:   parseUUID(userID),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update autopilot")
			return
		}
	}

	if replaceSubscribers {
		if err := qtx.DeleteAutopilotSubscribersForAutopilot(r.Context(), autopilot.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update subscribers")
			return
		}
		for _, subscriber := range subscribers {
			if err := qtx.AddAutopilotSubscriber(r.Context(), db.AddAutopilotSubscriberParams{
				AutopilotID: autopilot.ID,
				UserType:    "member",
				UserID:      subscriber.UserID,
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to add autopilot subscriber")
				return
			}
		}
	}
	subs, err := qtx.ListAutopilotSubscribers(r.Context(), autopilot.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load autopilot subscribers")
		return
	}
	resp := autopilotToResponse(autopilot, subs)
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotUpdated, autopilot, "member", userID, "updated",
		autopilot.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		map[string]any{"autopilot": resp},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update autopilot")
		return
	}

	h.publishEvent(updatedEvent)
	writeJSON(w, http.StatusOK, resp)
}

// autopilotRuleSubstantiveChange 判断 prev→next 之间 autopilot「行」的某个实质
// （值得发布）字段是否变化——凡是改变「自动化指示 agent 做什么 / 谁来跑 / 是否
// 跑」的字段都算，责任随编辑者转移（MUL-4302 §3.4；边界由 Elon 界定）：
//
//   - assignee_type / assignee_id —— 谁（agent / squad leader）执行；
//   - status —— 启停状态（active / paused / archived）；
//   - execution_mode —— run_only 与 create_issue 之分；
//   - description —— 产品把它当作运行提示词（任务指令本身），改它必须转移
//     责任（Elon 指出的缺口：指令的新发布者才是可追责的人）；
//   - issue_title_template —— create_issue 模式创建的 issue 的标题模板，属于
//     运行要产出的指令 / 输出规格的一部分。
//
// 刻意不当作实质（外观 / 路由性质——既不改指令也不改执行者）：title（显示标签）
// 与 project_id（创建的 issue 归属哪个项目）。比较是忠实的，因为 UpdateAutopilot
// 从 prev 逐个播种参数，未传的字段原样往返。
//
// 触发器表上的编辑（cron / timezone / enabled / event_filters）按触发器计为
// 实质变更，在 UpdateAutopilotTrigger 里处理；归档与系统暂停在自己的路径里
// 各自重发。
func autopilotRuleSubstantiveChange(prev, next db.Autopilot) bool {
	return prev.AssigneeType != next.AssigneeType ||
		prev.AssigneeID != next.AssigneeID ||
		prev.Status != next.Status ||
		prev.ExecutionMode != next.ExecutionMode ||
		prev.Description != next.Description ||
		prev.IssueTitleTemplate != next.IssueTitleTemplate
}

// recordAutopilotRuleVersion 为一次实质发布追加一条 rule-version 快照
// （MUL-4302 §3.4）。service.RecordAutopilotRuleVersion（与失败监控器共用）的
// 薄 handler 封装；调用方传入事务作用域的 Queries，让版本与 autopilot 写入
// 原子生效。
func (h *Handler) recordAutopilotRuleVersion(ctx context.Context, q *db.Queries, ap db.Autopilot, publishedByType string, publishedByID pgtype.UUID) error {
	return service.RecordAutopilotRuleVersion(ctx, q, ap, publishedByType, publishedByID)
}

func (h *Handler) parseAutopilotProjectID(
	w http.ResponseWriter,
	r *http.Request,
	raw *string,
	workspaceID pgtype.UUID,
) (pgtype.UUID, bool) {
	if raw == nil || *raw == "" {
		return pgtype.UUID{}, true
	}
	projectID, ok := parseUUIDOrBadRequest(w, *raw, "project_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID:          projectID,
		WorkspaceID: workspaceID,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "project_id must reference a project in this workspace")
		return pgtype.UUID{}, false
	}
	return projectID, true
}

// DeleteAutopilot「删除」规则。产品语义是归档：停止未来触发、从默认列表隐藏，
// 同时保留运行 / 任务 / webhook 投递 / 订阅者 / 协作者作为执行历史。归档是一
// 次实质状态变更（MUL-4302 §3.4），因此与归档原子地以该成员为发布者重发
// rule-version。入口同样要求 requireAutopilotWrite。
func (h *Handler) DeleteAutopilot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	idUUID, ok := parseUUIDOrBadRequest(w, id, "autopilot id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	ap, err := h.Queries.GetAutopilotInWorkspace(r.Context(), db.GetAutopilotInWorkspaceParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "autopilot not found")
		return
	}
	if !h.requireAutopilotWrite(w, r, ap, workspaceID) {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 产品语义的「删除」是归档：停止未来的触发、从默认列表隐藏，同时把运行 /
	// 任务 / webhook 投递 / 订阅者 / 协作者作为执行历史保留下来。归档是一次实质
	// 状态变更（MUL-4302 §3.4），所以与归档原子地以该成员为发布者重发 rule 版本。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete autopilot")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if err := qtx.ArchiveAutopilot(r.Context(), idUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete autopilot")
		return
	}
	ap.Status = "archived" // 让版本快照反映归档后的状态
	if err := h.recordAutopilotRuleVersion(r.Context(), qtx, ap, "member", parseUUID(userID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete autopilot")
		return
	}
	deletedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotDeleted, ap, "member", userID, "deleted", uuidToString(ap.ID),
		map[string]any{},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete autopilot")
		return
	}

	h.publishEvent(deletedEvent)
	w.WriteHeader(http.StatusNoContent)
}

// ── 协作者（访问授权）管理 ──────────────────────────────────────────────────

// AutopilotCollaboratorRequest 是把成员显式授权为规则协作者的请求体。
type AutopilotCollaboratorRequest struct {
	UserID string `json:"user_id"`
}

func (h *Handler) writeAutopilotCollaborators(w http.ResponseWriter, r *http.Request, autopilotID pgtype.UUID, status int) {
	collaborators, err := h.Queries.ListAutopilotCollaborators(r.Context(), autopilotID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load collaborators")
		return
	}
	resp := make([]AutopilotCollaboratorEntry, len(collaborators))
	for i, c := range collaborators {
		resp[i] = collaboratorToEntry(c)
	}
	writeJSON(w, status, map[string]any{"collaborators": resp})
}

// AddAutopilotCollaborator 把某工作区成员显式授权为规则的写者。只有规则创建者
// 或工作区 owner/admin 能管理访问列表；被授权的协作者不能把访问转授他人
// （防提权）。见 MUL-3807。
func (h *Handler) AddAutopilotCollaborator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotAccessManagement(w, r, ap, workspaceID) {
		return
	}

	var req AutopilotCollaboratorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	targetUUID, ok := parseUUIDOrBadRequest(w, req.UserID, "user_id")
	if !ok {
		return
	}
	// 只有工作区成员能被授权——agent 走自己的派发路径够到 autopilot，不走这张
	// 授权列表。
	if !h.isWorkspaceEntity(r.Context(), "member", req.UserID, workspaceID) {
		writeError(w, http.StatusBadRequest, "user_id must be a member of this workspace")
		return
	}

	grantedBy, ok := requireUserID(w, r)
	if !ok {
		return
	}
	grantedByUUID, ok := parseUUIDOrBadRequest(w, grantedBy, "granted_by")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to grant access")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	collaborator, err := qtx.AddAutopilotCollaborator(r.Context(), db.AddAutopilotCollaboratorParams{
		AutopilotID: ap.ID,
		UserType:    "member",
		UserID:      targetUUID,
		GrantedBy:   grantedByUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to grant access")
		return
	}
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotUpdated, ap, "member", grantedBy, "collaborator_added",
		uuidToString(targetUUID)+":"+collaborator.CreatedAt.Time.UTC().Format("20060102150405.999999999"),
		map[string]any{"collaborator": collaboratorToEntry(collaborator)},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to grant access")
		return
	}

	h.publishEvent(updatedEvent)
	h.writeAutopilotCollaborators(w, r, ap.ID, http.StatusCreated)
}

// RemoveAutopilotCollaborator 撤销某成员被显式授予的写权限。只有规则创建者或
// 工作区 owner/admin 能管理访问列表；协作者不能撤销同伴。隐式写者（创建者 /
// owner / admin）不受影响——他们本来就没有可删的授权行。
func (h *Handler) RemoveAutopilotCollaborator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := chi.URLParam(r, "userId")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotAccessManagement(w, r, ap, workspaceID) {
		return
	}
	targetUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke access")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DeleteAutopilotCollaborator(r.Context(), db.DeleteAutopilotCollaboratorParams{
		AutopilotID: ap.ID,
		UserType:    "member",
		UserID:      targetUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke access")
		return
	}

	actor := requestUserID(r)
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotUpdated, ap, "member", actor, "collaborator_removed", uuidToString(targetUUID),
		map[string]any{"collaborator_user_id": uuidToString(targetUUID)},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke access")
		return
	}
	h.publishEvent(updatedEvent)
	h.writeAutopilotCollaborators(w, r, ap.ID, http.StatusOK)
}

// ── 触发器管理 ──────────────────────────────────────────────────────────────

// CreateAutopilotTrigger 为规则新建一个触发器（schedule 定时 或 webhook 事件）。
// 入口做 requireAutopilotWrite 写权限校验，随后按 kind 走各自路径：
//   - schedule：本事务内写触发器行 + 重发 rule-version + 落事件，原子生效；
//   - webhook：先铸好 bearer token 再 INSERT（避免半写入状态），并用独立事务
//     保证触发器 INSERT 与 rule-version 重发同生共死（MUL-4302 §3.4）。
//
// 新增触发器会改变规则「何时/如何触发」，属实质发布，必须由当前成员作为发布者
// 原子地重发 rule-version——版本写失败必须回滚触发器，绝不让未来的派发归因到
// 上一发布者。
func (h *Handler) CreateAutopilotTrigger(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotWrite(w, r, ap, workspaceID) {
		return
	}
	// 新增触发器会改变规则「何时 / 如何」触发——一次实质发布，因此操作成员必须与
	// 触发器创建「原子地」重发 rule-version（MUL-4302 §3.4）。这里先解析好成员，
	// 让 webhook 与 schedule 两条创建路径都能在同一事务里随 INSERT 写版本——版本
	// 写失败必须把触发器一起回滚，绝不能让未来的派发继续归因到上一发布者。
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	publisherID := parseUUID(userID)

	var req CreateAutopilotTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	if req.Kind != "schedule" && req.Kind != "webhook" {
		// "api" kind 已废弃：它曾是个「预留但无实义」的类型（既无调度器、也无
		// 入口路由），唯一触发方式其实是手动 /trigger 端点——而该端点本来就不
		// 限 trigger kind。对漏网者直接 400，推动调用方改用 schedule 或 webhook。
		writeError(w, http.StatusBadRequest, "kind must be schedule or webhook")
		return
	}
	if req.Kind == "schedule" && (req.CronExpression == nil || *req.CronExpression == "") {
		writeError(w, http.StatusBadRequest, "cron_expression is required for schedule triggers")
		return
	}
	if req.Kind == "webhook" && req.Timezone != nil && *req.Timezone != "" {
		// webhook 触发器靠外部 POST 按需触发——没有 next_run_at 可算，时区对它
		// 毫无意义。大声拒绝而不是悄悄丢掉该字段。
		writeError(w, http.StatusBadRequest, "timezone is not valid for webhook triggers")
		return
	}
	if req.Kind != "webhook" && len(req.EventFilters) > 0 {
		// event_filters 只收窄 webhook 入口——对 schedule 触发器无意义，否则会被
		// 悄悄丢弃。
		writeError(w, http.StatusBadRequest, "event_filters is only valid for webhook triggers")
		return
	}
	if err := validateWebhookEventFilters(req.EventFilters); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Provider 只适用于 webhook 触发器，取值空间是封闭的——尽早拒绝未知值，免得
	// 创建时的笔误悄悄退化成绕过 provider 去重/签名行为的 "generic" 触发器。
	provider := "generic"
	if req.Provider != nil && *req.Provider != "" {
		if req.Kind != "webhook" {
			writeError(w, http.StatusBadRequest, "provider is only valid for webhook triggers")
			return
		}
		if !isAllowedWebhookProvider(*req.Provider) {
			writeError(w, http.StatusBadRequest, "provider must be generic or github")
			return
		}
		provider = *req.Provider
	}

	if req.Timezone != nil && *req.Timezone != "" {
		if err := service.ValidateTimezone(*req.Timezone); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// 按 kind 做归一化。webhook 触发器忽略 cron/timezone/next_run_at——它们是
	// 按需触发的。
	var (
		nextRunAt    pgtype.Timestamptz
		cronText     pgtype.Text
		tzText       pgtype.Text
		webhookToken pgtype.Text
	)
	switch req.Kind {
	case "schedule":
		cronText = ptrToText(req.CronExpression)
		tzText = ptrToText(req.Timezone)
		tz := "UTC"
		if req.Timezone != nil && *req.Timezone != "" {
			tz = *req.Timezone
		}
		t, err := computeNextRun(*req.CronExpression, tz)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		nextRunAt = pgtype.Timestamptz{Time: t, Valid: true}
	case "webhook":
		// 在 INSERT 之前先铸好 token，保证行绝不会处于 kind=webhook 但
		// webhook_token=NULL 的半成品状态。万一随机 token 与既有唯一索引撞了
		// （256 bit 下几乎不可能，但若 RNG 退化，重试能让失败模式一目了然），
		// 就重新生成再 INSERT——绝不 UPDATE。
		eventFiltersBytes, err := encodeWebhookEventFilters(req.EventFilters)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode event_filters")
			return
		}
		trigger, updatedEvent, err := h.createWebhookTriggerWithMintedToken(r, ap, ptrToText(req.Label), provider, eventFiltersBytes, publisherID, userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create trigger")
			return
		}
		resp := h.triggerToResponse(trigger)
		h.publishEvent(updatedEvent)
		writeJSON(w, http.StatusCreated, resp)
		return
	}

	// schedule 创建路径：原子地写触发器并重发 rule-version。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create trigger")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	trigger, err := qtx.CreateAutopilotTrigger(r.Context(), db.CreateAutopilotTriggerParams{
		AutopilotID:    ap.ID,
		Kind:           req.Kind,
		Enabled:        true,
		CronExpression: cronText,
		Timezone:       tzText,
		NextRunAt:      nextRunAt,
		Label:          ptrToText(req.Label),
		WebhookToken:   webhookToken,
		// 初始把负责的发布者定为创建者；之后的实质编辑会把它盖成编辑者，让运行
		// 归因到最后一个塑造该触发器的人（source=trigger_owner, MUL-4302）。
		PublishedByType: pgtype.Text{String: "member", Valid: publisherID.Valid},
		PublishedByID:   publisherID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create trigger")
		return
	}
	if err := h.recordAutopilotRuleVersion(r.Context(), qtx, ap, "member", publisherID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create trigger")
		return
	}
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotUpdated, ap, "member", userID, "trigger_created", uuidToString(trigger.ID),
		map[string]any{"trigger": h.triggerToResponse(trigger)},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create trigger")
		return
	}

	resp := h.triggerToResponse(trigger)
	h.publishEvent(updatedEvent)
	writeJSON(w, http.StatusCreated, resp)
}

// createWebhookTriggerWithMintedToken 在同一次 INSERT 里原子地创建 webhook
// 触发器并铸好全新的 bearer token。取代了旧的「先 INSERT 再 UPDATE
// webhook_token」两步式——那种写法一旦第二步失败，UI 里就可能出现
// kind=webhook 但 webhook_token=NULL 的行。
//
// 每次尝试跑在「自己的」事务里，让触发器 INSERT 与 rule-version 重发（webhook
// 触发器是对「什么会触发」的实质变更，MUL-4302 §3.4，由 publisherID 发布）一起
// 提交——版本写失败会把触发器一并回滚，而不是让未来的派发归因到上一发布者。
// 命中唯一索引冲突时用新 token 重试（冲突那次的 tx 已回滚），让几乎不可能发生的
// RNG 撞车变成干净的重试而非 500。
func (h *Handler) createWebhookTriggerWithMintedToken(
	r *http.Request,
	ap db.Autopilot,
	label pgtype.Text,
	provider string,
	eventFilters []byte,
	publisherID pgtype.UUID,
	actorID string,
) (db.AutopilotTrigger, events.Event, error) {
	ctx := r.Context()
	for attempt := 0; attempt < 3; attempt++ {
		token, err := generateWebhookToken()
		if err != nil {
			return db.AutopilotTrigger{}, events.Event{}, err
		}
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return db.AutopilotTrigger{}, events.Event{}, err
		}
		qtx := h.Queries.WithTx(tx)
		trigger, err := qtx.CreateAutopilotTrigger(ctx, db.CreateAutopilotTriggerParams{
			AutopilotID:  ap.ID,
			Kind:         "webhook",
			Enabled:      true,
			Label:        label,
			WebhookToken: pgtype.Text{String: token, Valid: true},
			Provider:     pgtype.Text{String: provider, Valid: provider != ""},
			EventFilters: eventFilters,
			// 初始把负责的发布者定为创建者；之后的实质编辑会把它盖成编辑者
			// （source=trigger_owner, MUL-4302）。
			PublishedByType: pgtype.Text{String: "member", Valid: publisherID.Valid},
			PublishedByID:   publisherID,
		})
		if err != nil {
			tx.Rollback(ctx)
			if isUniqueViolation(err) {
				continue // token 撞车：换新 token 重试
			}
			return db.AutopilotTrigger{}, events.Event{}, err
		}
		if err := h.recordAutopilotRuleVersion(ctx, qtx, ap, "member", publisherID); err != nil {
			tx.Rollback(ctx)
			return db.AutopilotTrigger{}, events.Event{}, err
		}
		updatedEvent, err := service.RecordDurableEventTx(ctx, qtx, buildAutopilotDomainEvent(
			protocol.EventAutopilotUpdated, ap, "member", actorID, "trigger_created", uuidToString(trigger.ID),
			map[string]any{"trigger": h.triggerToResponse(trigger)},
		))
		if err != nil {
			tx.Rollback(ctx)
			return db.AutopilotTrigger{}, events.Event{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return db.AutopilotTrigger{}, events.Event{}, err
		}
		return trigger, updatedEvent, nil
	}
	return db.AutopilotTrigger{}, events.Event{}, fmt.Errorf("could not mint unique webhook token")
}

func isAllowedWebhookProvider(p string) bool {
	switch p {
	case "generic", "github":
		return true
	default:
		return false
	}
}

func isValidAutopilotAssigneeType(t string) bool {
	switch t {
	case "agent", "squad":
		return true
	default:
		return false
	}
}

// validateAutopilotAssigneeForSave 校验指派目标（agent 或 squad）在该工作区
// 存在，且当 requireRuntime 为 true 时其有效 Agent 已绑定 Runtime。它经 q 取
// 指派锁，让正在进行的保存与调用方的 Autopilot 写入对 Runtime 拆除保持串行。
//
// 派发时刻还会再跑同样的检查（resolveAutopilotLeader + AgentReadiness）——它们
// 在那里处理「保存时 leader 在线、触发时却已下线」的情况。保存时校验的意义在
// 于让用户立刻得到反馈（「先绑定 runtime」），而不是到下一次调度 tick 才发现
// Autopilot 是死的。
func (h *Handler) validateAutopilotAssigneeForSave(
	w http.ResponseWriter,
	r *http.Request,
	q *db.Queries,
	assigneeType string,
	assigneeID, workspaceID pgtype.UUID,
	requireRuntime bool,
) bool {
	switch assigneeType {
	case "agent":
		agent, err := q.LockAgentForAutopilotAssignment(r.Context(), db.LockAgentForAutopilotAssignmentParams{
			ID:          assigneeID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "assignee must be a valid agent in this workspace")
			return false
		}
		if agent.ArchivedAt.Valid {
			writeError(w, http.StatusUnprocessableEntity, "assignee agent is archived; pick a different agent")
			return false
		}
		if requireRuntime && !agent.RuntimeID.Valid {
			writeError(w, http.StatusUnprocessableEntity, "assignee agent needs a runtime before this autopilot can be active")
			return false
		}
		return true
	case "squad":
		squad, err := q.LockSquadForAutopilotAssignment(r.Context(), db.LockSquadForAutopilotAssignmentParams{
			ID:          assigneeID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "assignee must be a valid squad in this workspace")
			return false
		}
		// 已归档的 squad 必须在保存时就拒绝：否则派发器会对一个未经显式取消归档
		// 就永远无法复活的 squad 产生源源不断的 skipped 运行。与 DeleteSquad 上的
		// TransferSquadAutopilotsToLeader 配对——任何在归档中存活的 autopilot 都会
		// 翻转为 assignee_type='agent'（指向 leader），不再引用已死的 squad 行。
		if squad.ArchivedAt.Valid {
			writeError(w, http.StatusUnprocessableEntity, "squad is archived; pick a different squad")
			return false
		}
		leader, err := q.LockAgentForAutopilotAssignment(r.Context(), db.LockAgentForAutopilotAssignmentParams{
			ID:          squad.LeaderID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "squad leader agent not found")
			return false
		}
		if leader.ArchivedAt.Valid {
			writeError(w, http.StatusUnprocessableEntity, "squad leader is archived; pick a different squad or rotate the leader before assigning autopilot")
			return false
		}
		if requireRuntime && !leader.RuntimeID.Valid {
			writeError(w, http.StatusUnprocessableEntity, "squad leader needs a runtime before this autopilot can be active")
			return false
		}
		// private-leader 门控：配置 autopilot 的成员必须有权访问该 private
		// leader，与 validateAssigneePair 一致。
		actorType, actorID := h.resolveActor(r, requestUserID(r), util.UUIDToString(workspaceID))
		if !h.canInvokeAgent(r.Context(), leader, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), util.UUIDToString(workspaceID)) {
			writeError(w, http.StatusForbidden, "cannot assign autopilot to squad with private leader")
			return false
		}
		return true
	default:
		writeError(w, http.StatusBadRequest, "assignee_type must be agent or squad")
		return false
	}
}

// UpdateAutopilotTrigger PATCH 更新单个触发器。入口同样 requireAutopilotWrite；
// cron / timezone 仅对 schedule 触发器有效（按 kind 大声拒绝而非静默落库），
// event_filters 走三态 PATCH（nil 不动、空切片清空、有值替换）。cron 或 timezone
// 变了就重算 next_run_at；只有实质编辑才重发 rule-version 并把该触发器责任转移
// 给编辑者——label-only 或 no-op 的 PATCH 不转移责任（MUL-4302）。
func (h *Handler) UpdateAutopilotTrigger(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	triggerID := chi.URLParam(r, "triggerId")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotWrite(w, r, ap, workspaceID) {
		return
	}

	triggerUUID, ok := parseUUIDOrBadRequest(w, triggerID, "trigger id")
	if !ok {
		return
	}

	prev, err := h.Queries.GetAutopilotTrigger(r.Context(), triggerUUID)
	if err != nil || uuidToString(prev.AutopilotID) != uuidToString(ap.ID) {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}

	var req UpdateAutopilotTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 按 kind 校验。复刻创建路径的纪律：cron 与 timezone 只对 schedule 触发器
	// 有意义，大声拒绝而不是持久化没有任何代码路径会读的字段。enabled 与 label
	// 对所有 kind 都有效。
	if prev.Kind != "schedule" {
		if req.CronExpression != nil {
			writeError(w, http.StatusBadRequest, "cron_expression is only valid for schedule triggers")
			return
		}
		if req.Timezone != nil {
			writeError(w, http.StatusBadRequest, "timezone is only valid for schedule triggers")
			return
		}
	}

	params := db.UpdateAutopilotTriggerParams{
		ID:             prev.ID,
		CronExpression: prev.CronExpression,
		Timezone:       prev.Timezone,
		NextRunAt:      prev.NextRunAt,
		Label:          prev.Label,
	}
	if req.Enabled != nil {
		params.Enabled = pgtype.Bool{Bool: *req.Enabled, Valid: true}
	}
	if req.CronExpression != nil {
		params.CronExpression = pgtype.Text{String: *req.CronExpression, Valid: true}
	}
	if req.Timezone != nil {
		if *req.Timezone != "" {
			if err := service.ValidateTimezone(*req.Timezone); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		params.Timezone = pgtype.Text{String: *req.Timezone, Valid: true}
	}
	if req.Label != nil {
		params.Label = pgtype.Text{String: *req.Label, Valid: true}
	}
	// event_filters 的三态 PATCH。nil 指针（字段省略或 JSON null）保持行原样——
	// params.EventFilters 不设置，UPDATE 里的 COALESCE 保留原值。非 nil 指针是
	// 权威的：空切片清空过滤器（编码成 JSONB 字面量 `[]`，让 COALESCE 走替换
	// 而非保留），有值切片则整体替换。
	if req.EventFilters != nil {
		if prev.Kind != "webhook" {
			writeError(w, http.StatusBadRequest, "event_filters is only valid for webhook triggers")
			return
		}
		if err := validateWebhookEventFilters(*req.EventFilters); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, err := encodeWebhookEventFiltersAlways(*req.EventFilters)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode event_filters")
			return
		}
		params.EventFilters = encoded
	}

	// cron 或 timezone 变化了就重算 next_run_at。
	cronExpr := prev.CronExpression.String
	if req.CronExpression != nil {
		cronExpr = *req.CronExpression
	}
	tz := "UTC"
	if prev.Timezone.Valid {
		tz = prev.Timezone.String
	}
	if req.Timezone != nil {
		tz = *req.Timezone
	}
	if prev.Kind == "schedule" && cronExpr != "" {
		t, err := computeNextRun(cronExpr, tz)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.NextRunAt = pgtype.Timestamptz{Time: t, Valid: true}
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update trigger")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	trigger, err := qtx.UpdateAutopilotTrigger(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update trigger")
		return
	}

	// 只有实质编辑才重发 rule-version 并把该触发器的责任转移给编辑者。cron /
	// timezone / enabled / event_filters 改变触发器「何时 / 如何」触发；label 是
	// 外观展示字段，no-op 的 PATCH 什么也没改——两者都不该转移责任（MUL-4302；
	// 即 Elon 指出的过度转移）。比较持久化的前后行能捕获真实变化、忽略 label-only
	// / no-op PATCH（next_run_at 由 cron/timezone 派生，不是独立信号）。
	triggerSubstantiveChange := prev.Enabled != trigger.Enabled ||
		prev.CronExpression != trigger.CronExpression ||
		prev.Timezone != trigger.Timezone ||
		!bytes.Equal(prev.EventFilters, trigger.EventFilters)
	if triggerSubstantiveChange {
		if err := h.recordAutopilotRuleVersion(r.Context(), qtx, ap, "member", parseUUID(userID)); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update trigger")
			return
		}
		// 本触发器的运行责任转移到编辑者。作用域限定到单行，改一个触发器绝不会
		// 重挂另一个的追责——这是 autopilot 级的 rule_version 给不了的、按触发
		// 触发器的粒度。
		if err := qtx.SetAutopilotTriggerPublisher(r.Context(), db.SetAutopilotTriggerPublisherParams{
			ID:              trigger.ID,
			PublishedByType: pgtype.Text{String: "member", Valid: true},
			PublishedByID:   parseUUID(userID),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update trigger")
			return
		}
	}
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotUpdated, ap, "member", userID, "trigger_updated", fmt.Sprintf("%s:%d", uuidToString(trigger.ID), trigger.Revision),
		map[string]any{"trigger": h.triggerToResponse(trigger)},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update trigger")
		return
	}

	resp := h.triggerToResponse(trigger)
	h.publishEvent(updatedEvent)
	writeJSON(w, http.StatusOK, resp)
}

// DeleteAutopilotTrigger 删除规则的某个触发器。删除改变「什么会触发」——一次实质
// 发布（MUL-4302 §3.4），因此与删除原子地以该成员为发布者重发 rule-version。
// 入口 requireAutopilotWrite，且触发器必须属于该 autopilot（否则 404）。
func (h *Handler) DeleteAutopilotTrigger(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	triggerID := chi.URLParam(r, "triggerId")
	workspaceID := h.resolveWorkspaceID(r)

	autopilotUUID, ok := parseUUIDOrBadRequest(w, autopilotID, "autopilot id")
	if !ok {
		return
	}
	triggerUUID, ok := parseUUIDOrBadRequest(w, triggerID, "trigger id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	ap, err := h.Queries.GetAutopilotInWorkspace(r.Context(), db.GetAutopilotInWorkspaceParams{
		ID:          autopilotUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "autopilot not found")
		return
	}
	if !h.requireAutopilotWrite(w, r, ap, workspaceID) {
		return
	}

	trigger, err := h.Queries.GetAutopilotTrigger(r.Context(), triggerUUID)
	if err != nil || uuidToString(trigger.AutopilotID) != uuidToString(autopilotUUID) {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 删除触发器改变「什么会触发」——一次实质发布（MUL-4302 §3.4）。以该成员
	// 为发布者重发 rule-version，与删除原子生效。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete trigger")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if err := qtx.DeleteAutopilotTrigger(r.Context(), triggerUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete trigger")
		return
	}
	if err := h.recordAutopilotRuleVersion(r.Context(), qtx, ap, "member", parseUUID(userID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete trigger")
		return
	}
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotUpdated, ap, "member", userID, "trigger_deleted", uuidToString(triggerUUID),
		map[string]any{"trigger_id": uuidToString(triggerUUID)},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete trigger")
		return
	}

	h.publishEvent(updatedEvent)
	w.WriteHeader(http.StatusNoContent)
}

// RotateAutopilotTriggerWebhookToken 为既有 webhook 触发器签发全新 bearer token。
// 旧 token 立刻失效——公开入口路由靠当前行值做唯一索引查找。
func (h *Handler) RotateAutopilotTriggerWebhookToken(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	triggerID := chi.URLParam(r, "triggerId")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotWrite(w, r, ap, workspaceID) {
		return
	}

	triggerUUID, ok := parseUUIDOrBadRequest(w, triggerID, "trigger id")
	if !ok {
		return
	}
	prev, err := h.Queries.GetAutopilotTrigger(r.Context(), triggerUUID)
	if err != nil || uuidToString(prev.AutopilotID) != uuidToString(ap.ID) {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	if prev.Kind != "webhook" {
		writeError(w, http.StatusBadRequest, "trigger is not a webhook trigger")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var rotated db.AutopilotTrigger
	var rotatedEvent events.Event
	for attempt := 0; attempt < 3; attempt++ {
		token, terr := generateWebhookToken()
		if terr != nil {
			writeError(w, http.StatusInternalServerError, "failed to generate webhook token")
			return
		}
		tx, txErr := h.TxStarter.Begin(r.Context())
		if txErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to rotate webhook token")
			return
		}
		qtx := h.Queries.WithTx(tx)
		rotated, err = qtx.RotateAutopilotTriggerWebhookToken(r.Context(), db.RotateAutopilotTriggerWebhookTokenParams{
			ID:           triggerUUID,
			WebhookToken: pgtype.Text{String: token, Valid: true},
		})
		if err == nil {
			rotatedEvent, err = service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
				protocol.EventAutopilotUpdated, ap, "member", userID, "trigger_token_rotated", fmt.Sprintf("%s:%d", uuidToString(rotated.ID), rotated.Revision),
				map[string]any{"trigger": h.triggerToResponse(rotated)},
			))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err == nil {
			break
		}
		_ = tx.Rollback(r.Context())
		if !isUniqueViolation(err) {
			writeError(w, http.StatusInternalServerError, "failed to rotate webhook token")
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to rotate webhook token")
		return
	}

	resp := h.triggerToResponse(rotated)
	h.publishEvent(rotatedEvent)
	writeJSON(w, http.StatusOK, resp)
}

// SetAutopilotTriggerSigningSecret 设置（或清空）webhook 触发器的 HMAC 签名
// 密钥。它独占一个端点，让密钥值永不与其他字段共享请求体——从而避开可能带
// patch 负载的通用请求体日志与审计捕获。
//
// 空 body / 空 `signing_secret` 清空密钥，触发器退回纯 bearer-token 鉴权。响应
// 只带 `has_signing_secret` + `signing_secret_hint`，密钥本身永不回显——与
// GitHub / Stripe 的业界惯例一致。
func (h *Handler) SetAutopilotTriggerSigningSecret(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	triggerID := chi.URLParam(r, "triggerId")
	workspaceID := h.resolveWorkspaceID(r)

	ap, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotWrite(w, r, ap, workspaceID) {
		return
	}
	triggerUUID, ok := parseUUIDOrBadRequest(w, triggerID, "trigger id")
	if !ok {
		return
	}
	prev, err := h.Queries.GetAutopilotTrigger(r.Context(), triggerUUID)
	if err != nil || uuidToString(prev.AutopilotID) != uuidToString(ap.ID) {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	if prev.Kind != "webhook" {
		writeError(w, http.StatusBadRequest, "trigger is not a webhook trigger")
		return
	}

	var req SetSigningSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	secret := strings.TrimSpace(req.SigningSecret)
	// 16 字符是下限：足以让 SHA-256 HMAC 的暴力破解不现实，又不至于拒绝签发更短
	// 密钥的 provider（Slack 签名密钥是 32 个 hex 字符；GitHub 推荐 32）。
	if secret != "" && len(secret) < 16 {
		writeError(w, http.StatusBadRequest, "signing_secret must be at least 16 characters")
		return
	}

	param := db.SetAutopilotTriggerSigningSecretParams{ID: triggerUUID}
	if secret != "" {
		param.SigningSecret = pgtype.Text{String: secret, Valid: true}
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update signing secret")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.SetAutopilotTriggerSigningSecret(r.Context(), param)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update signing secret")
		return
	}
	updatedEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildAutopilotDomainEvent(
		protocol.EventAutopilotUpdated, ap, "member", userID, "trigger_signing_secret_updated", fmt.Sprintf("%s:%d", uuidToString(updated.ID), updated.Revision),
		map[string]any{"trigger": h.triggerToResponse(updated)},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record autopilot event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update signing secret")
		return
	}

	resp := h.triggerToResponse(updated)
	// 广播触发器更新，让 UI 实时刷新 has_signing_secret 徽标。事件负载只带响应
	// 形状，不含密钥本身。
	h.publishEvent(updatedEvent)
	writeJSON(w, http.StatusOK, resp)
}

// ── 运行 ────────────────────────────────────────────────────────────────────

// ListAutopilotRuns 分页列出规则的运行记录（默认 20、上限 100，支持 offset）。
// 列表刻意省略 trigger_payload——webhook 信封可达 256 KiB，默认页的极端情况会
// 变成 ~5 MB；详情对话框走 GetAutopilotRun 取完整负载。
func (h *Handler) ListAutopilotRuns(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}

	limit := int32(20)
	offset := int32(0)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = int32(v)
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = int32(v)
		}
	}

	runs, err := h.Queries.ListAutopilotRuns(r.Context(), db.ListAutopilotRunsParams{
		AutopilotID: autopilot.ID,
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}

	resp := make([]AutopilotRunResponse, len(runs))
	for i, run := range runs {
		// 列表响应省略 trigger_payload——webhook 信封可达 256 KiB 而 limit 默认
		// 20，全量最坏 ~5 MB。详情对话框从 GetAutopilotRun 取完整负载。
		resp[i] = runToResponseSlim(run)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": resp, "total": len(resp)})
}

// GetAutopilotRun 返回单次运行详情，含完整 trigger_payload。工作区限定经
// loadAutopilotInWorkspace 达成；随后再复核该 run 确实属于这个 autopilot，
// 防止用别的 autopilot 的 URL 猜 runId 造成跨工作区数据泄漏。
func (h *Handler) GetAutopilotRun(w http.ResponseWriter, r *http.Request) {
	autopilotID := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "runId")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, autopilotID, workspaceID)
	if !ok {
		return
	}

	runUUID, ok := parseUUIDOrBadRequest(w, runID, "run id")
	if !ok {
		return
	}

	run, err := h.Queries.GetAutopilotRun(r.Context(), runUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if uuidToString(run.AutopilotID) != uuidToString(autopilot.ID) {
		// 防止别人经该 autopilot 的 URL 用别的规则的 runId 请求——失败关闭为
		// 404，响应形状与「不存在」一致，不泄漏任何信息。
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	writeJSON(w, http.StatusOK, runToResponse(run))
}

// ── 手动触发 ────────────────────────────────────────────────────────────────

// TriggerAutopilot 手动「立即运行一次」规则。要求规则 active 且有写权限
// （requireAutopilotWrite）。手动运行是一次直接的人类动作，归因 direct_human
// 给触发成员（MUL-4302 §4）；支持 Idempotency-Key 去重。配额超限返回 429 +
// Retry-After；其余内部失败只写日志、对外回统一的 5xx（MUL-6472）。响应把派发
// 源头定好的类型化 reason code（MUL-4525）原样带出，UI 据此渲染「run now」提示。
func (h *Handler) TriggerAutopilot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	autopilot, ok := h.loadAutopilotInWorkspace(w, r, id, workspaceID)
	if !ok {
		return
	}
	if !h.requireAutopilotWrite(w, r, autopilot, workspaceID) {
		return
	}
	if autopilot.Status != "active" {
		writeError(w, http.StatusBadRequest, "autopilot is not active")
		return
	}

	// 手动「立即运行」是一次直接的人类动作，因此运行归因 direct_human 给触发
	// 成员（MUL-4302 §4）。与 assign/promote 同样方式解析 actor；只有 member actor
	// 才是人——agent 走 A2A 触发会得到无效 actor，回退到 rule_owner。
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) > 255 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is too long")
		return
	}
	if idempotencyKey == "" {
		idempotencyKey = service.NewRequestIdempotencyKey()
	}
	run, reasonCode, err := h.AutopilotService.DispatchAutopilotManualWithKey(r.Context(), autopilot, pgtype.UUID{}, nil, memberActorUserID(actorType, actorID), idempotencyKey)
	if err != nil {
		var quotaErr *service.AutopilotQuotaExceededError
		if errors.As(err, &quotaErr) {
			retryAfter := int64(time.Until(quotaErr.ResetAt).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"reason_code": "quota_exceeded", "used": quotaErr.Used,
				"reserved": quotaErr.Reserved, "limit": quotaErr.Limit,
				"reset_at": quotaErr.ResetAt.UTC().Format(time.RFC3339),
			})
			return
		}
		// 配额分支之后的都是未分类的内部失败，错误链带着 pgx 约束/表名与内部 id。
		// 任何工作区成员都能点「立即运行」，所以细节只进日志，响应与文件其余部分
		// 一样是固定的 5xx 字符串（MUL-6472）。
		slog.Error("trigger autopilot failed",
			"error", err,
			"autopilot_id", uuidToString(autopilot.ID),
		)
		writeError(w, http.StatusInternalServerError, "failed to trigger autopilot")
		return
	}

	// 把派发源头定好的类型化 admission reason（MUL-4525）原样带进响应——绝不从
	// failure_reason 文本反推。UI 依据运行状态 + 该 code 分支渲染「立即运行」toast。
	resp := runToResponse(*run)
	if reasonCode != "" {
		c := string(reasonCode)
		resp.ReasonCode = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetAutopilotQuotaUsage 暴露 Cloud 提供的区间事实（used/reserved/limit/周期）
// 加上服务端持有的持久 blocked 计数。门控关闭或畸形时，service 在任何 quota
// 表读取之前就返回。
func (h *Handler) GetAutopilotQuotaUsage(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := util.ParseUUID(h.resolveWorkspaceID(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace")
		return
	}
	usage, err := h.AutopilotService.AutopilotQuotaUsage(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load autopilot quota usage")
		return
	}
	resp := AutopilotQuotaUsageResponse{Action: "off"}
	if usage.Enabled {
		resp.Action = usage.Action
		resp.Used, resp.Reserved, resp.Total = usage.Used, usage.Reserved, usage.Total
		resp.Limit, resp.Reached = usage.Limit, usage.Reached
		resp.BlockedCounts = usage.BlockedCounts
		if usage.PeriodStart != nil {
			v := usage.PeriodStart.UTC().Format(time.RFC3339)
			resp.PeriodStart = &v
		}
		if usage.PeriodEnd != nil {
			v := usage.PeriodEnd.UTC().Format(time.RFC3339)
			resp.PeriodEnd = &v
		}
		if usage.ResetAt != nil {
			v := usage.ResetAt.UTC().Format(time.RFC3339)
			resp.ResetAt = &v
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
