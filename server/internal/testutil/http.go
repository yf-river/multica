package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"
)

// JSONRequest 构造一个 body 为"把 body 按 JSON 编码后"的请求。
//
// 当 body 本身已是 string、[]byte 或 io.Reader 时原样透传，这样测试可以 POST 畸形
// 载荷——端点检查清单要求的畸形响应测试否则得绕过本辅助手动重建请求头。
func JSONRequest(method, path string, body any) *http.Request {
	var reader io.Reader
	switch v := body.(type) {
	case nil:
		reader = &bytes.Buffer{}
	case string:
		reader = strings.NewReader(v)
	case []byte:
		reader = bytes.NewReader(v)
	case io.Reader:
		reader = v
	default:
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(v); err != nil {
			// 在测试里构造的字面量只有值本身无法编码（channel、NaN）时才会失败；
			// 这里 panic 能把它报告在调用点，而不是稍后变成让人困惑的空 body。
			panic(fmt.Sprintf("testutil: encode request body for %s %s: %v", method, path, err))
		}
		reader = &buf
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// WithHeaders 以 key/value 交替形式设置请求头并返回 req。
func WithHeaders(req *http.Request, kv ...string) *http.Request {
	if len(kv)%2 != 0 {
		panic("testutil: WithHeaders needs an even number of key/value arguments")
	}
	for i := 0; i < len(kv); i += 2 {
		req.Header.Set(kv[i], kv[i+1])
	}
	return req
}

// WithURLParams 以 key/value 交替形式附加 chi 的 URL 参数。绕过 router 直接调
// handler 时，路径段就是这样读取的。
func WithURLParams(req *http.Request, kv ...string) *http.Request {
	if len(kv)%2 != 0 {
		panic("testutil: WithURLParams needs an even number of key/value arguments")
	}
	rctx := chi.NewRouteContext()
	for i := 0; i < len(kv); i += 2 {
		rctx.URLParams.Add(kv[i], kv[i+1])
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TB 是本包辅助用到的 *testing.T 的那部分能力。取接口而非具体类型，正是为了让本包
// 能对自己的失败消息做断言——消息本身就是共享断言值得存在的原因，所以它们需要
// 属于自己的测试。
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// Response 是某次 handler 调用的记录结果。其断言是致命的：读错状态的测试，随后
// 从 body 解码出来的一切也都读错了。
type Response struct {
	*httptest.ResponseRecorder

	t   TB
	req *http.Request
}

// Call 用 req 驱动 h 并记录它写出的内容。
func Call(t TB, h http.HandlerFunc, req *http.Request) *Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, req)
	return &Response{ResponseRecorder: rec, t: t, req: req}
}

// Want 在 handler 写出的状态码不是 want 时让测试失败。失败消息带请求行与响应体，
// 因为 handler 是靠 body 表达它触发了哪一路拒绝的。
func (r *Response) Want(want int) *Response {
	r.t.Helper()
	if r.Code != want {
		r.t.Fatalf("%s %s: expected %d, got %d: %s",
			r.req.Method, r.req.URL.RequestURI(), want, r.Code, r.Body.String())
	}
	return r
}

// WantOneOf 在状态码不属于 want 集合中的任何一个时让测试失败。仅在确实可能有多个
// 正确状态码时使用；用"断言一个集合"来规避 flake 反而会掩盖 flake。
func (r *Response) WantOneOf(want ...int) *Response {
	r.t.Helper()
	for _, code := range want {
		if r.Code == code {
			return r
		}
	}
	r.t.Fatalf("%s %s: expected one of %v, got %d: %s",
		r.req.Method, r.req.URL.RequestURI(), want, r.Code, r.Body.String())
	return r
}

// JSON 把响应体解码到 dest。
func (r *Response) JSON(dest any) *Response {
	r.t.Helper()
	if err := json.Unmarshal(r.Body.Bytes(), dest); err != nil {
		r.t.Fatalf("%s %s: decode response: %v: %s",
			r.req.Method, r.req.URL.RequestURI(), err, r.Body.String())
	}
	return r
}

// Map 把响应体解码成通用对象，适合只断言一两个字段、不值得建命名结构体的场景。
func (r *Response) Map() map[string]any {
	r.t.Helper()
	out := map[string]any{}
	r.JSON(&out)
	return out
}

// Text 返回原始响应体。
func (r *Response) Text() string { return r.Body.String() }

// Decode 在一个表达式里完成"跑 handler、断言状态、解码 body"，适合调用方想要
// 类型化载荷、对响应别无他用的情况。
func Decode[T any](t TB, h http.HandlerFunc, req *http.Request, want int) T {
	t.Helper()
	var out T
	Call(t, h, req).Want(want).JSON(&out)
	return out
}
