package publicapiv1

import (
	"encoding/json"
	"net/http"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// ProblemContentType 是 RFC 9457 问题（problem）响应的媒体类型。
const ProblemContentType = "application/problem+json"

// FieldError 描述一个非法请求字段。它可选，端点可按需采用字段级校验而无需改动
// 顶层信封结构。
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Problem 是稳定的 v1 错误信封。Error 是兼容旧 Plugin 客户端的别名；新客户端应
// 按 Code 分支并渲染 Detail。
type Problem struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Code      string       `json:"code"`
	Detail    string       `json:"detail"`
	RequestID string       `json:"request_id"`
	Errors    []FieldError `json:"errors,omitempty"`
	Error     string       `json:"error"`
}

// CodeForStatus 把 HTTP 状态码映射到 Multica 稳定的机器码；未知状态一律
// internal_error。
func CodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusPaymentRequired:
		return "payment_required"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusUnprocessableEntity:
		return "incompatible"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusInsufficientStorage:
		return "quota_exceeded"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	case http.StatusBadGateway:
		return "upstream_unavailable"
	default:
		return "internal_error"
	}
}

// requestID 从请求上下文或头里取请求 id；都没有则新造一个，保证响应总能带回
// request_id。
func requestID(r *http.Request) string {
	if r != nil {
		if id := strings.TrimSpace(chimw.GetReqID(r.Context())); id != "" {
			return id
		}
		if id := strings.TrimSpace(r.Header.Get(chimw.RequestIDHeader)); id != "" {
			return id
		}
	}
	return uuid.NewString()
}

// WriteProblem 写一个 RFC 9457 风格的问题响应，带 Multica 的稳定 code 与旧的
// `error` 别名。auth 中间件与各 handler 共用它，保证"路由前失败"与"资源内失败"
// 契约一致。
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	if code == "" {
		code = CodeForStatus(status)
	}
	title := http.StatusText(status)
	if title == "" {
		title = "Request failed"
	}
	id := requestID(r)
	payload := Problem{
		Type:      "urn:multica:problem:" + code,
		Title:     title,
		Status:    status,
		Code:      code,
		Detail:    detail,
		RequestID: id,
		Error:     detail,
	}

	w.Header().Set("Content-Type", ProblemContentType)
	w.Header().Set(chimw.RequestIDHeader, id)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, http.StatusNotFound, "not_found", "resource not found")
}

func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}
