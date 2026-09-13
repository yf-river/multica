package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const (
	remoteMCPMaxRequestBytes = 1 << 20
	remoteMCPMaxCalls        = 256
	remoteMCPMaxConcurrency  = 8
)

// remoteMCPBrokerSet 聚合一个任务启动的所有 Remote MCP broker HTTP server 与其
// 生命周期；Close 幂等，可被多次调用。
type remoteMCPBrokerSet struct {
	servers   []*http.Server
	listeners []net.Listener
	once      sync.Once
}

// remoteMCPCredentialResolver 按贡献 ID 解析当前任务该用的上游凭据（返回 HTTP
// 头）；同一任务内可按需多次调用（每次真正转发前都现场解析，凭据被吊销也能感知）。
type remoteMCPCredentialResolver func(context.Context, string) (http.Header, error)

// Close 优雅关停全部 server 并关闭监听。
func (set *remoteMCPBrokerSet) Close() {
	if set == nil {
		return
	}
	set.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, server := range set.servers {
			_ = server.Shutdown(ctx)
		}
		for _, listener := range set.listeners {
			_ = listener.Close()
		}
	})
}

// startTaskRemoteMCPBrokers 为任务的每条 Remote MCP 连接在 127.0.0.1 上启动一个本地
// broker（HTTP → 上游 MCP），并返回可并入 agent MCP 配置的 JSON 片段与诊断信息。
// 任何“必需”连接启动失败都会整体失败（返回错误）；“可选”失败只累积进 diagnostics。
func startTaskRemoteMCPBrokers(setupCtx, lifetimeCtx context.Context, taskID, provider string, connections []remotemcp.Connection, resolveCredential remoteMCPCredentialResolver, logger *slog.Logger) (json.RawMessage, []string, *remoteMCPBrokerSet, error) {
	if len(connections) == 0 {
		return nil, nil, nil, nil
	}
	set := &remoteMCPBrokerSet{}
	servers := map[string]any{}
	diagnostics := make([]string, 0)
	for _, connection := range connections {
		if !providerSupportsRemoteMCPBroker(provider) {
			message := fmt.Sprintf("Remote MCP %s is incompatible with provider %s", connection.ContributionKey, provider)
			if connection.FailurePolicy == "optional" {
				diagnostics = append(diagnostics, message)
				continue
			}
			set.Close()
			return nil, diagnostics, nil, errors.New(message)
		}
		headers := make(http.Header)
		if connection.CredentialHeader != "" && resolveCredential != nil {
			resolvedHeaders, resolveErr := resolveCredential(setupCtx, connection.ContributionID)
			if resolveErr != nil {
				message := fmt.Sprintf("Remote MCP %s credential is unavailable", connection.ContributionKey)
				if connection.FailurePolicy == "optional" {
					diagnostics = append(diagnostics, message)
					continue
				}
				set.Close()
				return nil, diagnostics, nil, fmt.Errorf("%s: %w", message, resolveErr)
			}
			headers = resolvedHeaders
		} else if connection.CredentialHeader != "" {
			message := fmt.Sprintf("Remote MCP %s credential resolver is unavailable", connection.ContributionKey)
			if connection.FailurePolicy == "optional" {
				diagnostics = append(diagnostics, message)
				continue
			}
			set.Close()
			return nil, diagnostics, nil, errors.New(message)
		}
		// 启动前先向上游做一次发现探测 + 对“白名单工具”做 schema 钉扎校验，确认
		// 上游可用且工具未漂移，再把它接进任务。
		discovered, _, err := remotemcp.Discover(setupCtx, connection.Endpoint, connection.EndpointAllowedHosts, connection.ProtocolVersions, headers)
		if err == nil {
			err = validatePinnedRemoteMCPTools(connection.ApprovedTools, discovered)
		}
		if err != nil {
			message := fmt.Sprintf("Remote MCP %s failed startup validation", connection.ContributionKey)
			if connection.FailurePolicy == "optional" {
				diagnostics = append(diagnostics, message)
				continue
			}
			set.Close()
			return nil, diagnostics, nil, fmt.Errorf("%s: %w", message, err)
		}
		endpoint, err := remotemcp.ValidatePublicHTTPSEndpoint(setupCtx, connection.Endpoint, connection.EndpointAllowedHosts, nil)
		if err != nil {
			set.Close()
			return nil, diagnostics, nil, err
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			set.Close()
			return nil, diagnostics, nil, fmt.Errorf("listen for Remote MCP broker: %w", err)
		}
		pathToken, err := randomBrokerToken()
		if err != nil {
			_ = listener.Close()
			set.Close()
			return nil, diagnostics, nil, err
		}
		proxy := &remoteMCPProxy{
			taskID: taskID, connection: connection, endpoint: endpoint,
			client: remotemcp.NewSecureHTTPClient(endpoint), credentialHeaders: headers,
			resolveCredential: resolveCredential, path: "/" + pathToken,
			semaphore: make(chan struct{}, remoteMCPMaxConcurrency), logger: logger,
		}
		server := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}
		set.servers = append(set.servers, server)
		set.listeners = append(set.listeners, listener)
		go func() {
			if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && logger != nil {
				logger.Warn("Remote MCP broker stopped unexpectedly", "task_id", taskID, "contribution", connection.ContributionKey, "error", serveErr)
			}
		}()
		name := remoteMCPServerName(connection)
		servers[name] = map[string]any{
			"type": "http",
			"url":  "http://" + listener.Addr().String() + proxy.path,
		}
	}
	if len(servers) == 0 {
		set.Close()
		return nil, diagnostics, nil, nil
	}
	// lifetimeCtx 结束（任务退出/daemon 关闭）即关停所有 broker。
	go func() {
		<-lifetimeCtx.Done()
		set.Close()
	}()
	raw, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		set.Close()
		return nil, diagnostics, nil, err
	}
	return raw, diagnostics, set, nil
}

// providerSupportsRemoteMCPBroker 判断 provider 是否支持 Remote MCP broker 接入。
func providerSupportsRemoteMCPBroker(provider string) bool {
	switch provider {
	case "codex", "claude", "hermes", "qoder", "mcode":
		return true
	default:
		return false
	}
}

// validatePinnedRemoteMCPTools 校验白名单里每个“已批准的钉扎工具”都能在发现到的
// 工具列表中找到，且 schema 指纹未漂移——白名单工具没了或变了都必须拦下，而不是
// 静默把新旧不一的工具暴露给 agent。
func validatePinnedRemoteMCPTools(approved, discovered []remotemcp.Tool) error {
	available := make(map[string]remotemcp.Tool, len(discovered))
	for _, tool := range discovered {
		available[tool.Name] = tool
	}
	for _, pinned := range approved {
		current, ok := available[pinned.Name]
		if !ok {
			return fmt.Errorf("approved tool %q is missing", pinned.Name)
		}
		if current.SchemaDigest != pinned.SchemaDigest {
			return fmt.Errorf("approved tool %q schema drifted", pinned.Name)
		}
	}
	return nil
}

// randomBrokerToken 生成 24 字节的随机十六进制串，用作 broker 本地路径的访问令牌。
func randomBrokerToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

// remoteMCPServerName 由贡献信息拼出 agent MCP 配置里的 server 名：小写化的
// contribution key（只留字母数字与 - _，其余换 -）加贡献 ID 前 8 位（去横线），
// 形如 plugin-<name>-<id8>。
func remoteMCPServerName(connection remotemcp.Connection) string {
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.ToLower(connection.ContributionKey))
	suffix := strings.ReplaceAll(connection.ContributionID, "-", "")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return "plugin-" + name + "-" + suffix
}

// remoteMCPProxy 是单条 Remote MCP 连接的本地转发代理：接收 agent 的 HTTP MCP
// 请求，补上凭据后原样转给上游，并把返回的响应（tools/list 还会做白名单过滤）回给
// agent。
type remoteMCPProxy struct {
	taskID            string
	connection        remotemcp.Connection
	endpoint          *url.URL
	client            *http.Client
	credentialHeaders http.Header
	resolveCredential remoteMCPCredentialResolver
	path              string
	semaphore         chan struct{}
	calls             atomic.Int64
	logger            *slog.Logger
}

// remoteMCPRequest 是 MCP JSON-RPC 请求的线格式。
type remoteMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ServeHTTP 处理一次来自 agent 的 MCP 调用，把它校验、限流后转发到上游。
func (proxy *remoteMCPProxy) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	started := time.Now()
	toolName := ""
	resultClass := "rejected"
	defer func() {
		if proxy.logger != nil {
			proxy.logger.Info("Remote MCP broker call",
				"task_id", proxy.taskID,
				"installation_id", proxy.connection.InstallationID,
				"contribution", proxy.connection.ContributionKey,
				"tool", toolName,
				"duration_ms", time.Since(started).Milliseconds(),
				"result_class", resultClass,
			)
		}
	}()
	// 路径是每任务随机令牌：只知道端口到不了这里的工具。
	if request.URL.Path != proxy.path || request.Method != http.MethodPost {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// 全任务调用次数硬上限，防止单任务把上游打爆。
	if proxy.calls.Add(1) > remoteMCPMaxCalls {
		writeRemoteMCPError(w, nil, -32002, "Remote MCP task call limit exceeded")
		return
	}
	// 并发上限：满了就立刻拒绝而不是排队，避免坏上游把 broker 线程占满。
	select {
	case proxy.semaphore <- struct{}{}:
		defer func() { <-proxy.semaphore }()
	default:
		writeRemoteMCPError(w, nil, -32003, "Remote MCP concurrency limit exceeded")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, remoteMCPMaxRequestBytes+1))
	if err != nil || len(raw) > remoteMCPMaxRequestBytes {
		writeRemoteMCPError(w, nil, -32600, "Remote MCP request is invalid")
		return
	}
	var rpcRequest remoteMCPRequest
	if err := json.Unmarshal(raw, &rpcRequest); err != nil || rpcRequest.JSONRPC != "2.0" {
		writeRemoteMCPError(w, rpcRequest.ID, -32600, "Remote MCP request is invalid")
		return
	}
	if !allowedRemoteMCPMethod(rpcRequest.Method) {
		writeRemoteMCPError(w, rpcRequest.ID, -32601, "Only approved Remote MCP tools are available")
		return
	}
	if rpcRequest.Method == "tools/call" {
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(rpcRequest.Params, &params); err != nil || !proxy.toolApproved(params.Name) {
			writeRemoteMCPError(w, rpcRequest.ID, -32602, "Remote MCP tool is not approved")
			return
		}
		toolName = params.Name
	}
	upstream, err := http.NewRequestWithContext(request.Context(), http.MethodPost, proxy.endpoint.String(), bytes.NewReader(raw))
	if err != nil {
		writeRemoteMCPError(w, rpcRequest.ID, -32603, "Remote MCP request failed")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Accept", "application/json, text/event-stream")
	// 会话相关头在 agent 与上游之间原样透传，broker 不掺和会话状态。
	for _, header := range []string{"Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-ID"} {
		if value := request.Header.Get(header); value != "" {
			upstream.Header.Set(header, value)
		}
	}
	credentialHeaders := proxy.credentialHeaders
	// 启动时解析的头是“启动校验用的”；每次转发前再现场解析一次，让吊销能被感知。
	if proxy.connection.CredentialHeader != "" && proxy.resolveCredential != nil {
		credentialHeaders, err = proxy.resolveCredential(request.Context(), proxy.connection.ContributionID)
		if err != nil {
			resultClass = "credential_revoked"
			writeRemoteMCPError(w, rpcRequest.ID, -32005, "Remote MCP credential is revoked or unavailable")
			return
		}
	}
	for key, values := range credentialHeaders {
		for _, value := range values {
			upstream.Header.Add(key, value)
		}
	}
	response, err := proxy.client.Do(upstream)
	if err != nil {
		resultClass = "remote_error"
		writeRemoteMCPError(w, rpcRequest.ID, -32000, "Remote MCP service is unavailable")
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, remotemcp.MaxResponseBytes+1))
	if err != nil || len(responseBody) > remotemcp.MaxResponseBytes {
		resultClass = "remote_error"
		writeRemoteMCPError(w, rpcRequest.ID, -32001, "Remote MCP response exceeded the allowed limit")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		resultClass = "remote_error"
		writeRemoteMCPError(w, rpcRequest.ID, -32000, "Remote MCP service returned an error")
		return
	}
	if rpcRequest.Method == "tools/list" {
		// tools/list 要先解开可能的 SSE 包装，再做白名单过滤（只暴露批准的钉扎工具，
		// 并再次核对 schema 指纹），然后以 JSON 回给 agent。
		responseBody, err = decodeRemoteMCPSSEData(response.Header.Get("Content-Type"), responseBody)
		if err != nil {
			resultClass = "remote_error"
			writeRemoteMCPError(w, rpcRequest.ID, -32000, "Remote MCP service returned an invalid response")
			return
		}
		responseBody, err = proxy.filterToolsListResponse(responseBody)
		if err != nil {
			resultClass = "schema_drift"
			writeRemoteMCPError(w, rpcRequest.ID, -32004, "Remote MCP tool schema changed and requires review")
			return
		}
		w.Header().Set("Content-Type", "application/json")
	} else if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	for _, header := range []string{"Mcp-Session-Id", "Mcp-Protocol-Version"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
	resultClass = "success"
}

// decodeRemoteMCPSSEData 当上游以 text/event-stream（SSE）回包时，取第一条 data:
// 行作为真正的 JSON 响应；非 SSE 内容原样返回。
func decodeRemoteMCPSSEData(contentType string, raw []byte) ([]byte, error) {
	if !strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		return raw, nil
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(data) > 0 {
				return data, nil
			}
		}
	}
	return nil, errors.New("Remote MCP SSE response contained no data")
}

// allowedRemoteMCPMethod 只放行 agent 会话需要的最小方法集；其它一律拒绝。
func allowedRemoteMCPMethod(method string) bool {
	switch method {
	case "initialize", "notifications/initialized", "notifications/cancelled", "ping", "tools/list", "tools/call":
		return true
	default:
		return false
	}
}

// toolApproved 判断某工具名是否在这条连接的白名单（ApprovedTools）里。
func (proxy *remoteMCPProxy) toolApproved(name string) bool {
	for _, tool := range proxy.connection.ApprovedTools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// filterToolsListResponse 把上游 tools/list 的响应过滤成只含白名单工具：逐个核对
// schema 与钉扎指纹，任何漂移即整体报错（宁可失败也不把未审的工具放给 agent）。
func (proxy *remoteMCPProxy) filterToolsListResponse(raw []byte) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(response["result"], &result); err != nil {
		return nil, err
	}
	var tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(result["tools"], &tools); err != nil {
		return nil, err
	}
	byName := make(map[string]json.RawMessage, len(tools))
	for _, tool := range tools {
		canonical, err := canonicalRemoteMCPJSON(tool.InputSchema)
		if err != nil {
			return nil, err
		}
		byName[tool.Name] = canonical
	}
	pinned := append([]remotemcp.Tool(nil), proxy.connection.ApprovedTools...)
	sort.Slice(pinned, func(i, j int) bool { return pinned[i].Name < pinned[j].Name })
	filtered := make([]map[string]any, 0, len(pinned))
	for _, tool := range pinned {
		current, ok := byName[tool.Name]
		if !ok || remotemcp.DigestBytes(current) != tool.SchemaDigest {
			return nil, errors.New("tool schema drift")
		}
		filtered = append(filtered, map[string]any{
			"name": tool.Name, "description": tool.Description, "inputSchema": json.RawMessage(current),
		})
	}
	result["tools"], _ = json.Marshal(filtered)
	response["result"], _ = json.Marshal(result)
	return json.Marshal(response)
}

// canonicalRemoteMCPJSON 把一段 JSON 反序列化再序列化，得到“键序/空白规范化”的
// 形态，供指纹比对使用。
func canonicalRemoteMCPJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	return json.RawMessage(canonical), err
}

// writeRemoteMCPError 以 JSON-RPC error 结构应答（HTTP 状态仍是 200——协议层错误
// 由 MCP 的 error 字段表达，agent 靠它区分工具失败与传输失败）。
func writeRemoteMCPError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}

// mergeTaskRemoteMCPConfig 把后加的 overlay 配置里的 mcpServers 逐项并入 base 的
// mcpServers（同名覆盖）；base 为空或 "null" 时直接以 overlay 为准。
func mergeTaskRemoteMCPConfig(base, overlay json.RawMessage) (json.RawMessage, error) {
	if len(overlay) == 0 {
		return base, nil
	}
	var overlayDocument struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(overlay, &overlayDocument); err != nil {
		return nil, err
	}
	baseDocument := struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}{MCPServers: map[string]json.RawMessage{}}
	trimmed := bytes.TrimSpace(base)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &baseDocument); err != nil {
			return nil, err
		}
		if baseDocument.MCPServers == nil {
			baseDocument.MCPServers = map[string]json.RawMessage{}
		}
	}
	for name, server := range overlayDocument.MCPServers {
		baseDocument.MCPServers[name] = server
	}
	return json.Marshal(baseDocument)
}
