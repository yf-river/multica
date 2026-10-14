// 本文件实现用户的 onboarding（新用户引导）相关端点：标记完成引导、补记问卷答案、
// 以及「加入云端等待名单」。这些端点的共同点是把用户表上的 onboarding 字段翻转，
// 并沿「未完成 → 完成 / 未提交 → 已提交」的边界只发一次漏斗事件。
//
// 关键事件约定：onboarding_completed 与 onboarding_questionnaire_submitted 每个用户
// 只发一次（漏斗统计的是用户，不是编辑次数）；服务端补发 onboarding_started 作为
// 前端页面打开事件的对账信号。
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 自由文本字段的上限。cloudWaitlistReasonMaxLen 是产品层面的封顶（「等待名单不需要
// 一篇论文」）；下面还有一层 body 体积上限，作为对「拿 JSON body 任意占存储」的
// 纵深防御。
const (
	cloudWaitlistReasonMaxLen = 500

	// PatchOnboarding 的 body 是很小的 JSON，至多承载一份 3 问问卷。
	// 16 KiB 约是现实体积的 10 倍——这是既能给未来字段留余地、又不让恶意用户
	// 把 JSONB 列当仓库塞的最低值。
	patchOnboardingBodyLimit = 16 * 1024
)

// completeOnboardingRequest 携带客户端对「用户从引导流程哪条出口离开」的判断。
// 纯粹作为分析维度使用——服务端状态（onboarded_at）无论路径如何都以同样方式翻转。
// 未知 / 缺失 → OnboardingPathUnknown，让老客户端照样干净完成，只是没有漏斗标签。
//
// workspace_id 保留给分析增强用。handler 本身不创建 agent：当前与 runtime 连接的
// 客户端会先创建他们的 Mika onboarding 聊天再调本端点；显式无 runtime 的路径仍由
// 前端先种下一份 setup guide。
type completeOnboardingRequest struct {
	CompletionPath string `json:"completion_path,omitempty"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
}

// validCompletionPaths 是合法的完成路径集合，用于把乱填的路径归一成 Unknown。
var validCompletionPaths = map[string]struct{}{
	analytics.OnboardingPathFull:           {},
	analytics.OnboardingPathRuntimeSkipped: {},
	analytics.OnboardingPathCloudWaitlist:  {},
	analytics.OnboardingPathSkipExisting:   {},
	analytics.OnboardingPathInviteAccept:   {},
}

// CompleteOnboarding 把已认证用户标记为完成 onboarding。幂等：底层查询用 COALESCE，
// 多次调用保留最初的时间戳。
//
// onboarding_completed 事件只在首次真正把 onboarded_at 从 NULL 翻成非空的那次调用
// 发出。后续调用仍返回 200 OK（供客户端重试），但不再发事件，让漏斗数的是真实的
// 首次完成。
//
// 现客户端在此 handler 里没有创建 agent 的副作用。runtime 连接流程会先创建 Mika 并
// 启动它的 onboarding 聊天；显式无 runtime 路径可能导航后在别处创建 setup-guide 议题。
// 本 handler 自己只做一件事：翻转字段。
func (h *Handler) CompleteOnboarding(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// body 可选——空 body 是合法的老客户端调用。
	var req completeOnboardingRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	// 若传了 workspace_id 就校验它：我们不用它写库，但畸形值应快速失败，而不是
	// 作为垃圾维度静默落进 PostHog。
	if req.WorkspaceID != "" {
		wsUUID, ok := parseUUIDOrBadRequest(w, req.WorkspaceID, "workspace_id")
		if !ok {
			return
		}
		req.WorkspaceID = uuidToString(wsUUID)
	}

	before, err := h.Queries.GetUser(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete onboarding")
		return
	}
	firstCompletion := !before.OnboardedAt.Valid

	user, err := h.Queries.MarkUserOnboarded(r.Context(), parseUUID(userID))
	if err != nil {
		slog.Warn("complete onboarding: mark user onboarded failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to complete onboarding")
		return
	}

	if firstCompletion {
		path := req.CompletionPath
		if _, ok := validCompletionPaths[path]; !ok {
			path = analytics.OnboardingPathUnknown
		}
		onboardedAt := ""
		if user.OnboardedAt.Valid {
			onboardedAt = user.OnboardedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.OnboardingCompleted(
			userID,
			req.WorkspaceID,
			path,
			onboardedAt,
			user.CloudWaitlistEmail.Valid,
		))
	}

	writeJSON(w, http.StatusOK, h.userToResponse(user))
}

// patchOnboardingRequest 是 PATCH 的请求体，questionnaire 字段可选。
type patchOnboardingRequest struct {
	Questionnaire *json.RawMessage `json:"questionnaire,omitempty"`
}

// stringOrSlice 兼容用途字段的数组与历史字符串形式。
type stringOrSlice []string

func (s *stringOrSlice) UnmarshalJSON(data []byte) error {
	// 空 / null 都解成 nil 切片。
	if len(data) == 0 || string(data) == "null" {
		*s = nil
		return nil
	}
	// 先试数组（当前形态）。
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = arr
		return nil
	}
	// 退回单个字符串（该列改存切片之前的旧形态）。空串表示「未作答」——保持 nil。
	var single string
	if err := json.Unmarshal(data, &single); err != nil {
		return err
	}
	if single == "" {
		*s = nil
		return nil
	}
	*s = []string{single}
	return nil
}

// questionnaireAnswers 是一次问卷答案的落库结构。
type questionnaireAnswers struct {
	Role           string        `json:"role"`
	RoleOther      string        `json:"role_other"`
	RoleSkipped    bool          `json:"role_skipped"`
	UseCase        stringOrSlice `json:"use_case"`
	UseCaseOther   string        `json:"use_case_other"`
	UseCaseSkipped bool          `json:"use_case_skipped"`
	Version        int           `json:"version"`
}

// 检查题目是否已作答或明确跳过。
func (q questionnaireAnswers) roleResolved() bool {
	return q.Role != "" || q.RoleSkipped
}
func (q questionnaireAnswers) useCaseResolved() bool {
	return len(q.UseCase) > 0 || q.UseCaseSkipped
}

// questionnaireSchemaVersion 是本 handler 认识的问卷 schema 版本。complete() 与漏斗
// 事件都限定在该版本上，避免未来的 v3 行被悄悄按 v2 语义误统计。
const questionnaireSchemaVersion = 2

// complete 检查角色与用途是否已回答或跳过。
func (q questionnaireAnswers) complete() bool {
	if q.Version != questionnaireSchemaVersion {
		return false
	}
	return q.roleResolved() && q.useCaseResolved()
}

// PatchOnboarding 持久化用户的问卷答案。字段可选；未传则保留原问卷。用户在哪个
// 步骤故意不落库——每次进入引导都从 Welcome 开始。
//
// 每个用户只发一次 onboarding_questionnaire_submitted：首次 PATCH 把 role + use_case
// 从「至少一个槽空着」推进到「两者都已作答」时触发。
func (h *Handler) PatchOnboarding(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	// 给 body 封顶，防止 JSONB 列被当成批量仓库——否则每次 /api/me 读取都要回传
	// 这份膨胀。
	r.Body = http.MaxBytesReader(w, r.Body, patchOnboardingBodyLimit)
	var req patchOnboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 先读旧答案，才能在更新后探测 NULL/部分 → 完成 的转换。旧行解不出错按
	// 「未完成」处理——最坏也就是比该发的多发一次，绝不会对同一次转换发两次。
	var before questionnaireAnswers
	beforeRaw := []byte("{}")
	if beforeUser, err := h.Queries.GetUser(r.Context(), parseUUID(userID)); err == nil {
		beforeRaw = beforeUser.OnboardingQuestionnaire
		_ = json.Unmarshal(beforeRaw, &before)
	}
	// firstTouch 表示这次 PATCH 之前，用户从未在服务端写过任何 onboarding 状态。
	// 用来让服务端每个用户只发一次 onboarding_started。
	firstTouch := len(beforeRaw) == 0 || string(beforeRaw) == "null" || string(beforeRaw) == "{}"

	params := db.PatchUserOnboardingParams{ID: parseUUID(userID)}
	if req.Questionnaire != nil {
		params.Questionnaire = []byte(*req.Questionnaire)
	}
	user, err := h.Queries.PatchUserOnboarding(r.Context(), params)
	if err != nil {
		slog.Warn("patch onboarding failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update onboarding")
		return
	}

	// 服务端补发的 onboarding_started：在首个真正携带问卷载荷的 PATCH 时触发。
	// 前端打开页面时也会发自己的 onboarding_started；两者配合，让 Grafana 能对
	// PostHog 交叉核对漏斗。
	if firstTouch && req.Questionnaire != nil && len(*req.Questionnaire) > 0 && string(*req.Questionnaire) != "{}" {
		platform, _, _ := middleware.ClientMetadataFromContext(r.Context())
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.OnboardingStarted(userID, platform))
	}

	var after questionnaireAnswers
	_ = json.Unmarshal(user.OnboardingQuestionnaire, &after)
	if after.complete() && !before.complete() {
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.OnboardingQuestionnaireSubmitted(
			userID,
			after.Role,
			[]string(after.UseCase),
			after.RoleSkipped,
			after.UseCaseSkipped,
			after.RoleOther != "",
			after.UseCaseOther != "",
		))
	}

	writeJSON(w, http.StatusOK, h.userToResponse(user))
}

// joinCloudWaitlistRequest 是加入云端等待名单的请求体。
type joinCloudWaitlistRequest struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

// JoinCloudWaitlist 记录用户对云 runtime 的兴趣。纯副作用——不会完成 onboarding。
// 用户仍须选真实的第 3 步路径（带检测到 runtime 的 CLI）或跳过才能继续。重复调用会
// 覆盖 email + reason。
func (h *Handler) JoinCloudWaitlist(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req joinCloudWaitlistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// RFC 5321 把邮箱上限定为 254 字符；列类型是 VARCHAR(254)，下面的格式检查也会
	// 拒绝任何 net/mail 解析不了的值。
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if len(email) > 254 {
		writeError(w, http.StatusBadRequest, "email is too long")
		return
	}
	if _, err := mail.ParseAddress(email); err != nil {
		writeError(w, http.StatusBadRequest, "email is invalid")
		return
	}

	reason := strings.TrimSpace(req.Reason)
	if len(reason) > cloudWaitlistReasonMaxLen {
		writeError(w, http.StatusBadRequest, "reason is too long")
		return
	}

	reasonParam := pgtype.Text{}
	if reason != "" {
		reasonParam = pgtype.Text{String: reason, Valid: true}
	}

	user, err := h.Queries.JoinCloudWaitlist(r.Context(), db.JoinCloudWaitlistParams{
		ID:                  parseUUID(userID),
		CloudWaitlistEmail:  pgtype.Text{String: email, Valid: true},
		CloudWaitlistReason: reasonParam,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to join waitlist")
		return
	}

	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.CloudWaitlistJoined(userID, reason != ""))

	writeJSON(w, http.StatusOK, h.userToResponse(user))
}
