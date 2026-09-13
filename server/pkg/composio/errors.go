package composio

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// APIError 是 Composio 返回非 2xx 状态时，SDK 对外抛出的统一错误类型。
//
// Composio v3.1 的错误包络（envelope）形如：
//
//	{
//	  "error": {
//	    "message": "...",
//	    "code":    400,
//	    "slug":    "INVALID_INPUT",
//	    "status":  400,
//	    "request_id":   "req_...",
//	    "suggested_fix":"...",
//	    "errors": ["..."]
//	  }
//	}
//
// HTTPStatus 是本机观察到的传输层状态码；其余字段是上游响应体的镜像
// （前提是 Composio 确实返回了 body）。RawBody 原样保留完整响应，
// 方便调用方排查问题时把上游全文打进日志。
type APIError struct {
	HTTPStatus   int      `json:"-"`
	Message      string   `json:"message,omitempty"`
	Code         int      `json:"code,omitempty"`
	Slug         string   `json:"slug,omitempty"`
	Status       int      `json:"status,omitempty"`
	RequestID    string   `json:"request_id,omitempty"`
	SuggestedFix string   `json:"suggested_fix,omitempty"`
	Errors       []string `json:"errors,omitempty"`
	RawBody      []byte   `json:"-"`
}

// Error 让 APIError 实现 error 接口。输出时把上游的状态码、slug 和消息拼成一行。
func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.HTTPStatus)
	}
	if e.Slug != "" {
		return fmt.Sprintf("composio: %d %s (%s)", e.HTTPStatus, msg, e.Slug)
	}
	return fmt.Sprintf("composio: %d %s", e.HTTPStatus, msg)
}

// IsNotFound 判断该错误是否为 HTTP 404 —— 常用于「删除/撤销要幂等」的流程。
func (e *APIError) IsNotFound() bool { return e != nil && e.HTTPStatus == http.StatusNotFound }

// IsUnauthorized 判断该错误是否为 HTTP 401（凭证无效）。
func (e *APIError) IsUnauthorized() bool {
	return e != nil && e.HTTPStatus == http.StatusUnauthorized
}

// IsRateLimited 判断该错误是否为 HTTP 429（被上游限流）。
func (e *APIError) IsRateLimited() bool {
	return e != nil && e.HTTPStatus == http.StatusTooManyRequests
}

// parseAPIError 解析 Composio 形如 {"error": {...}} 的错误包络。
// 如果 body 不是预期形状，就返回一个只带 HTTPStatus 和 RawBody 的 APIError，
// 保证调用方至少还能拿到点有用的信息。
func parseAPIError(status int, body []byte) *APIError {
	out := &APIError{HTTPStatus: status, RawBody: body}
	if len(body) == 0 {
		return out
	}
	var wire struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		// body 不是预期的错误包络——保留 RawBody、message 留空即可。
		return out
	}
	wire.Error.HTTPStatus = status
	wire.Error.RawBody = body
	return &wire.Error
}
