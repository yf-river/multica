// 本文件是把请求代理给 multica-cloud HTTP 服务（cloud runtime）的转发层：涵盖 cloud 侧
// 服务信息/健康检查、以及云节点（node）的生命周期管理端点，统一经 proxyCloudRuntime 出站，
// 可选注入 X-User-ID / 查询串 / JSON body。文件末尾是请求体读取、请求 id 透传、以及把 cloud
// 响应/错误转写回客户端的辅助函数。
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
	"github.com/multica-ai/multica/server/internal/logger"
)

// maxCloudRuntimeRequestBodySize 限制发给 cloud 的请求体大小（1 MiB）。
const maxCloudRuntimeRequestBodySize = 1 << 20

// cloudRuntimeProxyOptions 是转发选项：withUserID 注入认证用户，withQuery 透传查询串，
// withBody 读取并校验 JSON 请求体。
type cloudRuntimeProxyOptions struct {
	withUserID bool
	withQuery  bool
	withBody   bool
}

// GetCloudRuntimeService 转发 cloud 侧的服务信息根端点（GET /api/v1/）。
func (h *Handler) GetCloudRuntimeService(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/api/v1/", cloudRuntimeProxyOptions{
		withUserID: true,
	})
}

// GetCloudRuntimeHealth 转发 cloud 侧存活探针（/healthz）。
func (h *Handler) GetCloudRuntimeHealth(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/healthz", cloudRuntimeProxyOptions{})
}

// GetCloudRuntimeReady 转发 cloud 侧就绪探针（/readyz）。
func (h *Handler) GetCloudRuntimeReady(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/readyz", cloudRuntimeProxyOptions{})
}

// ListCloudRuntimeNodes 转发 cloud 侧的节点列表（GET /api/v1/nodes），带用户注入与查询串。
func (h *Handler) ListCloudRuntimeNodes(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodGet, "/api/v1/nodes", cloudRuntimeProxyOptions{
		withUserID: true,
		withQuery:  true,
	})
}

// CreateCloudRuntimeNode 转发云节点的创建请求（POST /api/v1/nodes）。
func (h *Handler) CreateCloudRuntimeNode(w http.ResponseWriter, r *http.Request) {
	// cloud 现在会在 POST /api/v1/nodes 期间自行铸一枚节点维度的 mcn_ PAT，并经 SSM
	// bootstrap 注入 EC2 实例（见 multica-cloud docs/api/node-pat.md）。我们不再转发调用方的
	// mul_ PAT——Fleet 用不到它，而把长寿命的用户 PAT 带进远端机器会扩大任何节点失守的
	// 爆炸半径。因此这个 handler 现在与其余写端点同形：只转发 body，不做 PAT 管道。
	h.proxyCloudRuntime(w, r, http.MethodPost, "/api/v1/nodes", cloudRuntimeProxyOptions{
		withUserID: true,
		withBody:   true,
	})
}

// DeleteCloudRuntimeNode 转发云节点的删除请求（DELETE /api/v1/nodes）。
func (h *Handler) DeleteCloudRuntimeNode(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodDelete, "/api/v1/nodes", cloudRuntimeProxyOptions{
		withUserID: true,
		withBody:   true,
	})
}

// StartCloudRuntimeNode 转发云节点的启动请求（POST /api/v1/nodes/start）。
func (h *Handler) StartCloudRuntimeNode(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodPost, "/api/v1/nodes/start", cloudRuntimeProxyOptions{
		withUserID: true,
		withBody:   true,
	})
}

// StopCloudRuntimeNode 转发云节点的停止请求（POST /api/v1/nodes/stop）。
func (h *Handler) StopCloudRuntimeNode(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodPost, "/api/v1/nodes/stop", cloudRuntimeProxyOptions{
		withUserID: true,
		withBody:   true,
	})
}

// RebootCloudRuntimeNode 转发云节点的重启请求（POST /api/v1/nodes/reboot）。
func (h *Handler) RebootCloudRuntimeNode(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodPost, "/api/v1/nodes/reboot", cloudRuntimeProxyOptions{
		withUserID: true,
		withBody:   true,
	})
}

// GetCloudRuntimeNodeStatus 转发云节点的状态查询（POST /api/v1/nodes/status）。
func (h *Handler) GetCloudRuntimeNodeStatus(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodPost, "/api/v1/nodes/status", cloudRuntimeProxyOptions{
		withUserID: true,
		withBody:   true,
	})
}

// ExecCloudRuntimeNode 转发在云节点上执行命令的请求（POST /api/v1/nodes/exec）。
func (h *Handler) ExecCloudRuntimeNode(w http.ResponseWriter, r *http.Request) {
	h.proxyCloudRuntime(w, r, http.MethodPost, "/api/v1/nodes/exec", cloudRuntimeProxyOptions{
		withUserID: true,
		withBody:   true,
	})
}

// proxyCloudRuntime 是 cloud 代理的统一实现：要求 cloud runtime 已启用，按 opts 逐项
// 解析用户/请求体/查询串，经 h.CloudRuntime.Do 出站并把响应或错误转写回客户端。
func (h *Handler) proxyCloudRuntime(w http.ResponseWriter, r *http.Request, method, path string, opts cloudRuntimeProxyOptions) {
	if h.CloudRuntime == nil || !h.CloudRuntime.Enabled() {
		writeFeatureDisabled(w, "cloud_runtime_not_configured", "cloud runtime is not configured")
		return
	}

	var userID string
	if opts.withUserID {
		var ok bool
		userID, ok = requireUserID(w, r)
		if !ok {
			return
		}
	}

	var body []byte
	if opts.withBody {
		var ok bool
		body, ok = readCloudRuntimeJSONBody(w, r)
		if !ok {
			return
		}
	}

	var query url.Values
	if opts.withQuery {
		query = r.URL.Query()
	}

	resp, err := h.CloudRuntime.Do(r.Context(), cloudruntime.Request{
		Method:    method,
		Path:      path,
		Query:     query,
		Body:      body,
		UserID:    userID,
		RequestID: cloudRuntimeRequestID(r),
	})
	if err != nil {
		writeCloudRuntimeError(w, r, err)
		return
	}
	writeCloudRuntimeResponse(w, resp)
}

// readCloudRuntimeJSONBody 按上限读取请求体并校验它是非空合法 JSON（逐字节保留原样，
// 供原样转发）。不合法时写好错误并返回 ok=false。
func readCloudRuntimeJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCloudRuntimeRequestBodySize)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return nil, false
	}
	if len(bytes.TrimSpace(data)) == 0 {
		writeError(w, http.StatusBadRequest, "request body is required")
		return nil, false
	}
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return nil, false
	}
	return data, true
}

// cloudRuntimeRequestID 透传调用方带来的 X-Request-ID，缺省退回 chi 为该请求生成的内部
// 请求 id，便于跨服务链路追踪。
func cloudRuntimeRequestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-ID"); id != "" {
		return id
	}
	return chimw.GetReqID(r.Context())
}

// writeCloudRuntimeResponse 把 cloud 的响应转写回客户端：透传 X-Request-ID；body 为合法
// JSON 时按原样返回，否则（非 JSON，例如纯文本错误）包成 {"error": ...} 结构。
func writeCloudRuntimeResponse(w http.ResponseWriter, resp *cloudruntime.Response) {
	if requestID := resp.Header.Get("X-Request-ID"); requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	body := bytes.TrimSpace(resp.Body)
	if len(body) == 0 {
		w.WriteHeader(resp.StatusCode)
		return
	}
	if json.Valid(body) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}
	writeJSON(w, resp.StatusCode, map[string]string{"error": string(body)})
}

// writeCloudRuntimeError 把出站失败映射成恰当的 HTTP 状态：未启用/未配置 → 503，
// 超时 → 504，其余一律按 502 兜底并记 warn 日志。
func writeCloudRuntimeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, cloudruntime.ErrDisabled):
		writeFeatureDisabled(w, "cloud_runtime_not_configured", "cloud runtime is not configured")
	case errors.Is(err, cloudruntime.ErrInvalidBaseURL):
		writeErrorCode(w, http.StatusInternalServerError, "cloud_runtime_misconfigured", "cloud runtime is misconfigured")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "cloud runtime request timed out")
	default:
		slog.Warn("cloud runtime request failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusBadGateway, "cloud runtime request failed")
	}
}
