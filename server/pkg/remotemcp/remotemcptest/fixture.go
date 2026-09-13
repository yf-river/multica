// Package remotemcptest 提供一个确定性的 MCP server，供隔离的 broker 与插件
// 生命周期测试使用。它刻意不是生产级 MCP 端点：httptest server 的生命周期由
// 调用方自己管理，任何网络流量都不会逃出测试进程。
package remotemcptest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Credential 是 fixture server 校验的 Bearer token 值，测试用它来模拟「带了
// 正确凭证」与「凭证不对」两种请求。
const Credential = "fixture-token"

// Server 是一个确定性 MCP fixture server。它实现 initialize / tools/list /
// tools/call 这几个最小协议面：fixture.read 返回固定值，fixture.write 把参数
// 追加进内部日志（可用 Writes 取回断言）。
type Server struct {
	*httptest.Server

	mu     sync.Mutex
	writes []string
}

// request 是 MCP 请求的最小线格式，server 端解析用它。
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// NewServer 启动一个 fixture server。调用方负责在测试结束时 Close 掉它。
func NewServer() *Server {
	fixture := &Server{}
	fixture.Server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	return fixture
}

// Writes 返回历次 fixture.write 收到的值（按调用顺序），返回的是拷贝，
// 调用方可以放心改动。
func (fixture *Server) Writes() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.writes...)
}

// serveHTTP 处理 MCP 的 streamable HTTP 请求：先验 Authorization 头，再按
// JSON-RPC 方法分发。
func (fixture *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+Credential {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "credential rejected"})
		return
	}
	var input request
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	result := map[string]any{}
	switch input.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "multica-remote-mcp-fixture", "version": "1.0.0"},
		}
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/list":
		result = map[string]any{"tools": []any{
			map[string]any{
				"name": "fixture.read", "description": "Read a deterministic fixture value",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
			map[string]any{
				"name": "fixture.write", "description": "Append a value to the fixture log",
				"inputSchema": map[string]any{
					"type": "object", "required": []string{"value"},
					"properties": map[string]any{"value": map[string]any{"type": "string"}},
				},
			},
		}}
	case "tools/call":
		var params struct {
			Name      string `json:"name"`
			Arguments struct {
				Value string `json:"value"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(input.Params, &params)
		switch params.Name {
		case "fixture.read":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "fixture-value"}}}
		case "fixture.write":
			fixture.mu.Lock()
			fixture.writes = append(fixture.writes, params.Arguments.Value)
			fixture.mu.Unlock()
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "written"}}}
		default:
			writeError(w, input.ID, -32602, "unknown tool")
			return
		}
	default:
		writeError(w, input.ID, -32601, "method not found")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": input.ID, "result": result})
}

// writeError 用 MCP 规范错误码写一条 JSON-RPC error 响应。
func writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}
