// 职责定位：用户「意见反馈」（feedback）提交接口的 HTTP 处理器。前端埋的「提建议/报 bug」
// 入口最终都会打到这里的 CreateFeedback：校验内容 → 按用户限频 → 把反馈连同运行时上下文
// （平台/版本/UA/所在页面 URL）落库 → 上报一条埋点指标。
//
// 安全与限频约定：反馈内容会连同元数据一起落进 metadata 的 JSONB 列，为防已登录用户借
// 这个口子灌大量垃圾，请求体用 MaxBytesReader 顶在 64 KiB，单条消息再单独限 1 万字
// （见常量注释）。限频按用户计：每小时最多 feedbackHourlyRateLimit 条，计数直接查数据库，
// 因此进程重启、多实例部署下依然准确，代价只是每次提交多一次带索引的廉价 COUNT。
//
// 依赖与数据流：依赖 middleware.ClientMetadataFromContext 取客户端元数据，依赖
// analytics.FeedbackSubmitted + obsmetrics.RecordEvent 上报埋点，最终写入 feedback 表。
// 本文件没有对应的删除/查询入口——反馈只进不出，供客服侧在别处读取。
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// feedbackImageRegex 是「消息里是否贴了图片」的粗略判据：匹配 Markdown 图片语法
// ![alt](url)。用途只是给埋点打 has_images 标记，不需要完整 Markdown 解析器；
// 正文里恰好出现字面量 "![" 时误判成有图也完全可以接受——它只是个客服分级信号。
var feedbackImageRegex = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)

const (
	// 单条反馈正文的最大长度（按字符串长度计，不是字节数）。
	feedbackMaxMessageLen = 10000
	// 每个用户每小时最多可提交的反馈条数，超过则返回 429。
	feedbackHourlyRateLimit = 10
	// feedbackBodyLimit 把整个请求体顶在 64 KiB。正文上限在它之外单独按 1 万字
	// 另算；多出来的预算用来容纳 JSON 包裹开销、可选的 workspace_id 等附加字段和
	// 诊断上下文——确保已登录客户端顶多塞进 64 KiB，而不是往 metadata 这个 JSONB
	// 列里灌数 MB 垃圾。
	feedbackBodyLimit = 64 * 1024
)

// CreateFeedbackRequest 是提交反馈的请求体。WorkspaceID 可空：反馈不一定发生在某个
// 工作区内（例如登录前的页面），此时该字段为 nil，落库为 NULL。
type CreateFeedbackRequest struct {
	Message string `json:"message"`
	URL     string `json:"url"`
	// Kind 是反馈入口选的粗分类别（前端反馈选择器打上的标签）。它最终成为埋点指标
	// `multica_feedback_submitted_total{kind=...}` 的标签取值；该标签只认
	// metrics.NormalizeFeedbackKind 里的固定白名单（"bug"/"feature"/"general"/
	// "praise"），白名单之外一律归一化成 "other"。为空或缺失时落回 "general"，
	// 这样老客户端不传该字段也不至于让指标凭空丢数据。
	Kind        string  `json:"kind"`
	WorkspaceID *string `json:"workspace_id,omitempty"`
}

// FeedbackResponse 是成功提交后返回给前端的响应：只有新反馈的 ID 和创建时间。
type FeedbackResponse struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

// CreateFeedback 处理 POST /feedback 意见提交：校验消息 → 按用户查限频 → 组装运行时
// 元数据 → 落库 → 上报埋点。任一前置校验失败即返回对应错误；只有真正入库成功才回 201。
func (h *Handler) CreateFeedback(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 给请求体设读取上限：超限时 json.Decode 会读失败，落到下面的 400 分支。
	r.Body = http.MaxBytesReader(w, r.Body, feedbackBodyLimit)
	var req CreateFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	if len(message) > feedbackMaxMessageLen {
		writeError(w, http.StatusBadRequest, "message too long")
		return
	}
	// 按用户限频：数一下该用户最近一小时已提交的条数。计数直接查库，进程重启、
	// 多实例部署下都不会失准，也无需共享缓存；代价只是每次提交多一次带索引的廉价 COUNT。
	count, err := h.Queries.CountRecentFeedbackByUser(r.Context(), parseUUID(userID))
	if err != nil {
		slog.Warn("count recent feedback failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return
	}
	if count >= feedbackHourlyRateLimit {
		writeError(w, http.StatusTooManyRequests, "too many feedback submissions, please try again later")
		return
	}

	platform, version, clientOS := middleware.ClientMetadataFromContext(r.Context())
	metadata := map[string]any{
		"url":        req.URL,
		"platform":   platform,
		"version":    version,
		"os":         clientOS,
		"user_agent": r.UserAgent(),
	}
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		// 理论上 map 里全是已知可 JSON 化的值，几乎不会失败；真失败也不值得为这点
		// 非关键元数据把整个请求 500 掉，退化成空对象 {} 继续流程即可。
		metaBytes = []byte("{}")
	}

	var workspaceID pgtype.UUID
	if req.WorkspaceID != nil && *req.WorkspaceID != "" {
		ws, ok := parseUUIDOrBadRequest(w, *req.WorkspaceID, "workspace_id")
		if !ok {
			return
		}
		workspaceID = ws
	}

	fb, err := h.Queries.CreateFeedback(r.Context(), db.CreateFeedbackParams{
		UserID:      parseUUID(userID),
		Message:     message,
		Metadata:    metaBytes,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		slog.Warn("create feedback failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to submit feedback")
		return
	}

	slog.Info("feedback submitted", append(logger.RequestAttrs(r), "feedback_id", uuidToString(fb.ID))...)

	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "general"
	}

	// 上报埋点：Message 里是否含图片用上面那个正则粗略判断；埋点标签用的 kind 在
	// 指标库里还会再过一次白名单归一化（见 Kind 字段注释）。
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.FeedbackSubmitted(
		userID,
		uuidToString(fb.WorkspaceID),
		kind,
		len(message),
		feedbackImageRegex.MatchString(message),
		platform,
		version,
	))

	writeJSON(w, http.StatusCreated, FeedbackResponse{
		ID:        uuidToString(fb.ID),
		CreatedAt: timestampToString(fb.CreatedAt),
	})
}
