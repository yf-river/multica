package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// 本文件实现一个本地 MCP server，把当前 workspace 的插件钩子（plugin hook）以工具的
// 形态呈现给 agent。
//
// 与旁边的 Remote MCP broker 不同，这里没有上游 MCP server 可代理：插件作者写的是
// 一个普通 HTTP 端点，根本不懂 MCP。因此这里按清单（manifest）自行合成协议——钩子的
// 描述变成工具描述，钩子的 input_schema 变成工具的 inputSchema。
//
// 工具调用并不会从这里直达插件：而是回传 Multica，由服务端发出带签名的请求。理由：
// daemon 跑在某个人的笔记本上，把签名密钥放在这里意味着每台跑 agent 的机器都持有
// 一份可以冒充服务端对任何插件后端发请求的凭据。绕道服务端还意味着限流、熔断、
// `net:` 检查与调用记录对全部四种触发方式共用同一份代码。

const (
	pluginHookMCPProtocolVersion = "2024-11-05"
	pluginHookMCPMaxRequestBytes = 1 << 20
	pluginHookMCPCallTimeout     = 60 * time.Second
)

// pluginHookInvoker 针对 Multica 服务端发起一次钩子调用，返回钩子的原始输出。
type pluginHookInvoker func(ctx context.Context, taskID, installationID, hookKey string, input json.RawMessage) (json.RawMessage, error)

// pluginHookMCPServer 是单任务的插件钩子 MCP 服务实例：按工具名索引钩子、以 HTTP
// 处理 MCP JSON-RPC 请求，路径上带随机令牌做防护。
type pluginHookMCPServer struct {
	taskID string
	tools  []PluginHookTool
	byName map[string]PluginHookTool
	invoke pluginHookInvoker
	path   string
	logger *slog.Logger
}

// pluginHookMCPSet 聚合一个任务对应的 HTTP server 与其生命周期（Close 幂等，可被
// 多次调用）。
type pluginHookMCPSet struct {
	server   *http.Server
	listener net.Listener
	once     sync.Once
}

// Close 优雅关停 HTTP server 并关闭监听；once 保证重复调用只执行一次。
func (set *pluginHookMCPSet) Close() {
	if set == nil {
		return
	}
	set.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if set.server != nil {
			_ = set.server.Shutdown(ctx)
		}
		if set.listener != nil {
			_ = set.listener.Close()
		}
	})
}

// startTaskPluginHookMCP 在任务带有钩子工具时为该任务启动本地工具 server。
//
// 返回要并入 agent MCP 配置的 JSON 片段——与 Remote MCP broker 的返回形态一致，
// 使两者能沿同一条路径到达 agent。
func startTaskPluginHookMCP(lifetimeCtx context.Context, taskID string, tools []PluginHookTool, invoke pluginHookInvoker, logger *slog.Logger) (json.RawMessage, *pluginHookMCPSet, error) {
	if len(tools) == 0 || invoke == nil {
		return nil, nil, nil
	}

	byName := make(map[string]PluginHookTool, len(tools))
	acceptedTools := make([]PluginHookTool, 0, len(tools))
	for _, tool := range tools {
		// 服务端会做命名空间隔离，但万一还是有重名到达，必须解析到恰好一个钩子，
		// 而不是“最后写入者胜”。
		if _, clash := byName[tool.Name]; clash {
			if logger != nil {
				logger.Warn("plugin hook tool name collided; ignoring the duplicate", "task_id", taskID, "tool", tool.Name)
			}
			continue
		}
		byName[tool.Name] = tool
		acceptedTools = append(acceptedTools, tool)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen for plugin hook MCP server: %w", err)
	}
	pathToken, err := randomBrokerToken()
	if err != nil {
		_ = listener.Close()
		return nil, nil, err
	}

	handler := &pluginHookMCPServer{
		taskID: taskID, tools: acceptedTools, byName: byName,
		invoke: invoke, path: "/" + pathToken, logger: logger,
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	set := &pluginHookMCPSet{server: server, listener: listener}

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && logger != nil {
			logger.Warn("plugin hook MCP server stopped unexpectedly", "task_id", taskID, "error", serveErr)
		}
	}()
	// lifetimeCtx 结束（任务退出/daemon 关闭）即关停本 server。
	go func() {
		<-lifetimeCtx.Done()
		set.Close()
	}()

	raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"multica-plugins": map[string]any{
			"type": "http",
			"url":  "http://" + listener.Addr().String() + handler.path,
		},
	}})
	if err != nil {
		set.Close()
		return nil, nil, err
	}
	return raw, set, nil
}

// pluginHookMCPRequest 是 MCP JSON-RPC 请求的线格式。
type pluginHookMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *pluginHookMCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 路径是每个任务随机的令牌：只知道端口、不知道令牌的进程够不到这些工具。
	if r.URL.Path != s.path || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, pluginHookMCPMaxRequestBytes))
	if err != nil {
		writePluginHookMCPError(w, nil, -32700, "could not read the request")
		return
	}
	var request pluginHookMCPRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writePluginHookMCPError(w, nil, -32700, "request is not valid JSON-RPC")
		return
	}

	switch request.Method {
	case "initialize":
		writePluginHookMCPResult(w, request.ID, map[string]any{
			"protocolVersion": pluginHookMCPProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "multica-plugins", "version": "1"},
		})
	case "notifications/initialized":
		// 通知没有 id，也不需要回复，202 即可。
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		writePluginHookMCPResult(w, request.ID, map[string]any{"tools": s.toolDescriptors()})
	case "tools/call":
		s.handleCall(w, r, request)
	default:
		writePluginHookMCPError(w, request.ID, -32601, "unsupported method "+request.Method)
	}
}

// toolDescriptors 把任务钩子清单翻译成 MCP tools/list 的描述。
func (s *pluginHookMCPServer) toolDescriptors() []map[string]any {
	descriptors := make([]map[string]any, 0, len(s.tools))
	for _, tool := range s.tools {
		schema := tool.InputSchema
		if len(schema) == 0 {
			// 没有声明入参的工具也需要一个 schema，否则 provider 会整体拒绝列表。
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		descriptors = append(descriptors, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": schema,
		})
	}
	return descriptors
}

// handleCall 处理一次 tools/call：按名字找到钩子，转发给服务端执行并把结果回给 agent。
func (s *pluginHookMCPServer) handleCall(w http.ResponseWriter, r *http.Request, request pluginHookMCPRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		writePluginHookMCPError(w, request.ID, -32602, "invalid tool call parameters")
		return
	}
	tool, ok := s.byName[params.Name]
	if !ok {
		writePluginHookMCPError(w, request.ID, -32602, "unknown tool "+params.Name)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), pluginHookMCPCallTimeout)
	defer cancel()
	output, err := s.invoke(ctx, s.taskID, tool.InstallationID, tool.HookKey, params.Arguments)
	if err != nil {
		// 这是“工具出错”而非“协议出错”。agent 读到 isError 会判定工具没生效然后
		// 继续干任务——这正是设计意图：插件端点不可达不能把某人的 issue 搞挂。
		if s.logger != nil {
			s.logger.Info("plugin hook tool call failed", "task_id", s.taskID, "tool", tool.Name, "error", err)
		}
		writePluginHookMCPResult(w, request.ID, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
		})
		return
	}

	text := string(output)
	if len(output) == 0 {
		text = "The hook completed and returned nothing."
	}
	writePluginHookMCPResult(w, request.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

// writePluginHookMCPResult 以 JSON-RPC result 应答。
func writePluginHookMCPResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// writePluginHookMCPError 以 JSON-RPC error 应答。
func writePluginHookMCPError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}
