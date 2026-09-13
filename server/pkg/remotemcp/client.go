// Package remotemcp 实现 Remote MCP 共享的安全边界——既服务插件发现，
// 也服务 daemon 的按任务作用域划分的 broker。
package remotemcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

// MaxResponseBytes 限制远端 HTTP 响应体（含 SSE 单帧）的最大字节数，
// 挡住恶意 server 灌爆内存。
// ConnectTimeout 是单次拨号建连的超时。
// CallTimeout 是单次 HTTP 调用（含读响应）的总超时。
const (
	MaxResponseBytes = 4 << 20
	ConnectTimeout   = 10 * time.Second
	CallTimeout      = 30 * time.Second
)

// Resolver 抽象「把主机名解析成 IP 地址」这一步，让测试可以注入假 DNS、
// 验证各种 IP 都被正确拦截，而不用真去联网。
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// ValidatePublicHTTPSEndpoint 校验一个远端 MCP 端点：必须是公网 HTTPS、
// 不能带 userinfo/query/fragment，host 必须在 allowedHosts 白名单内（有白名单
// 时），且解析出的每个地址都必须是公网地址（防 SSRF）。
//
// 返回规范化后的 *url.URL 供后续调用使用。
func ValidatePublicHTTPSEndpoint(ctx context.Context, raw string, allowedHosts []string, resolver Resolver) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	// 运维点名的 dev origin 只跳过「必须公网」这一条，其余照旧：
	// 下面 net: 作用域的检查仍然生效，所以插件依然到不了管理员没同意过的目的地。
	if isDevOrigin(endpoint) {
		devHost := strings.ToLower(strings.TrimSuffix(endpoint.Hostname(), "."))
		if len(allowedHosts) > 0 && !hostAllowed(devHost, allowedHosts) {
			return nil, errors.New("endpoint host is outside the Plugin endpoint policy")
		}
		return endpoint, nil
	}
	if endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" || endpoint.RawQuery != "" {
		return nil, errors.New("endpoint must be a public HTTPS URL without userinfo, query, or fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(endpoint.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, errors.New("endpoint host is not public")
	}
	if len(allowedHosts) > 0 && !hostAllowed(host, allowedHosts) {
		return nil, errors.New("endpoint host is outside the Plugin endpoint policy")
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("resolve endpoint host: %w", err)
	}
	for _, address := range addresses {
		if !isPublicAddress(address.Unmap()) {
			return nil, fmt.Errorf("endpoint host resolves to non-public address %s", address)
		}
	}
	return endpoint, nil
}

// hostAllowed 判断 host 是否命中某条策略。策略支持精确匹配（"api.example.com"）
// 与通配符后缀（"*.example.com" 匹配其任意子域，但不匹配裸域自身）。
func hostAllowed(host string, policies []string) bool {
	for _, policy := range policies {
		policy = strings.ToLower(strings.TrimSuffix(policy, "."))
		if host == policy {
			return true
		}
		if strings.HasPrefix(policy, "*.") {
			suffix := strings.TrimPrefix(policy, "*")
			if strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".") {
				return true
			}
		}
	}
	return false
}

// isPublicAddress 判断一个地址算不算「公网可及」：先排除 netip 辅助函数
// 能识别的私网/回环/链路本地/组播/未指定地址，再排除 IANA 特殊用途与
// 云元数据网段。
func isPublicAddress(address netip.Addr) bool {
	if !address.IsValid() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	// IANA 特殊用途与元数据网段——netip 的辅助函数覆盖不到的部分。
	blocked := []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("fe80::/10"),
	}
	for _, prefix := range blocked {
		if prefix.Contains(address) {
			return false
		}
	}
	return address.IsGlobalUnicast()
}

// NewSecureHTTPClient 为某个已通过校验的端点构造一个安全 HTTP client：
// 禁用代理、禁止跨 host 重定向、拨号时再次钉住目标 host 且复查「解析结果必须
// 是公网地址」，防止 DNS 重绑定把请求带进内网。
func NewSecureHTTPClient(endpoint *url.URL) *http.Client {
	// dev 与否在这里一次性定死，而不是放到 dialer 里每次现查：端点对这只
	// client 的整个生命周期是固定的，每次拨号都查只会在任务中途环境变化时
	// 产生差异——那等于给「连接被批准后还能偷偷放宽守卫」留了口子。
	devOrigin := isDevOrigin(endpoint)
	dialer := &net.Dialer{Timeout: ConnectTimeout, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if devOrigin {
		// 只为此 origin 多信任一份额外 CA，绝不跳过校验：插件作者本地的
		// MCP server 依然要出示证书。
		if config := devTLSConfig(); config != nil {
			transport.TLSClientConfig = config
		}
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(endpoint.Hostname(), ".")) {
			return nil, errors.New("remote MCP redirect changed endpoint host")
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("resolve endpoint host: %w", err)
		}
		for _, candidate := range addresses {
			candidate = candidate.Unmap()
			// 上面那个 host 钉住对 dev origin 同样生效，所以重定向到别的
			// 地方两种情形都会被拒。这里跳过的仅仅是「地址必须落在公网」。
			if !devOrigin && !isPublicAddress(candidate) {
				return nil, errors.New("remote MCP endpoint resolved to a non-public address")
			}
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
			if dialErr == nil {
				return connection, nil
			}
		}
		return nil, errors.New("connect to remote MCP endpoint failed")
	}
	return &http.Client{
		Timeout:   CallTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("remote MCP redirects are not allowed")
		},
	}
}

// rpcResponse 是 MCP JSON-RPC 响应的最小线格式：id 保持 RawMessage，
// 以便原样回显，Error 是 MCP 错误对象（规范里 code 为 -32700~-32603 等）。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// discoveredTool 是远端 tools/list 返回的工具条目，inputSchema 与 MCP 规范
// 一致用驼峰 inputSchema 命名（写入本地 Tool 时统一存成 input_schema）。
type discoveredTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// SupportedProtocolVersions 返回本构建支持的 MCP 协议修订版，
// 最优先的排最前。Discover 会首选第一个、接受其中任意一个。
func SupportedProtocolVersions() []string {
	return []string{"2025-03-26", "2024-11-05"}
}

// Discover 连接一个远端 MCP server 完成发现握手：initialize → 确认协议版本 →
// notifications/initialized → tools/list，把所有工具的 input schema 归一化后
// 逐个摘要，最后返回整个工具集的集合摘要 digest。
//
// 这是把「管理员要批准的是一组什么工具」变成可审计指纹的地方——调用方只需
// 保存 digest，之后任何 schema 漂移都会被它抓出来。
func Discover(ctx context.Context, rawEndpoint string, allowedHosts, protocolVersions []string, headers http.Header) ([]Tool, string, error) {
	endpoint, err := ValidatePublicHTTPSEndpoint(ctx, rawEndpoint, allowedHosts, nil)
	if err != nil {
		return nil, "", err
	}
	client := NewSecureHTTPClient(endpoint)
	sessionID := ""
	// 空列表的意思是「本构建支持啥就用啥」，不是「一个都不接受」。响应会拿
	// 这同一个切片去比对——如果这里留空，等于拒绝所有正确应答的 server，
	// 第一个走这条路径的插件正是这么栽的。
	if len(protocolVersions) == 0 {
		protocolVersions = SupportedProtocolVersions()
	}
	protocolVersion := protocolVersions[0]
	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "multica-plugin-review", "version": "1"},
		},
	}
	initializeResponse, responseHeaders, err := call(ctx, client, endpoint, headers, sessionID, initialize)
	if err != nil {
		return nil, "", fmt.Errorf("initialize remote MCP: %w", err)
	}
	sessionID = responseHeaders.Get("Mcp-Session-Id")
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(initializeResponse.Result, &initialized); err != nil || !containsString(protocolVersions, initialized.ProtocolVersion) {
		return nil, "", fmt.Errorf("remote MCP negotiated unsupported protocol version %q", initialized.ProtocolVersion)
	}
	if err := notify(ctx, client, endpoint, headers, sessionID, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{},
	}); err != nil {
		return nil, "", fmt.Errorf("confirm remote MCP initialization: %w", err)
	}
	toolsResponse, _, err := call(ctx, client, endpoint, headers, sessionID, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	})
	if err != nil {
		return nil, "", fmt.Errorf("list remote MCP tools: %w", err)
	}
	var result struct {
		Tools []discoveredTool `json:"tools"`
	}
	if err := json.Unmarshal(toolsResponse.Result, &result); err != nil {
		return nil, "", fmt.Errorf("decode tools/list result: %w", err)
	}
	tools := make([]Tool, 0, len(result.Tools))
	seen := map[string]bool{}
	for _, tool := range result.Tools {
		if strings.TrimSpace(tool.Name) == "" || seen[tool.Name] {
			return nil, "", errors.New("remote MCP returned an invalid or duplicate tool name")
		}
		seen[tool.Name] = true
		canonical, err := canonicalJSON(tool.InputSchema)
		if err != nil {
			return nil, "", fmt.Errorf("tool %q input schema: %w", tool.Name, err)
		}
		tools = append(tools, Tool{
			Name: tool.Name, Description: tool.Description, InputSchema: canonical,
			SchemaDigest: DigestBytes(canonical),
		})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	digest, err := ToolSetDigest(tools)
	if err != nil {
		return nil, "", err
	}
	return tools, digest, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// notify 发送一条不需要应答的 MCP notification（如 notifications/initialized），
// 顺便把响应体读掉并按最大字节数限额做保护。
func notify(ctx context.Context, client *http.Client, endpoint *url.URL, headers http.Header, sessionID string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("remote MCP returned HTTP %d", response.StatusCode)
	}
	written, err := io.Copy(io.Discard, io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		return err
	}
	if written > MaxResponseBytes {
		return errors.New("remote MCP response exceeds size limit")
	}
	return nil
}

// call 发送一条需要应答的 JSON-RPC 请求，读回并解码响应。HTTP 层失败、非 2xx、
// 响应体超限或 JSON-RPC error 都会以错误返回；成功后返回响应头（调用方取
// Mcp-Session-Id）与解码后的 rpcResponse。
func call(ctx context.Context, client *http.Client, endpoint *url.URL, headers http.Header, sessionID string, payload any) (rpcResponse, http.Header, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return rpcResponse{}, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return rpcResponse{}, nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	response, err := client.Do(request)
	if err != nil {
		return rpcResponse{}, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return rpcResponse{}, response.Header, fmt.Errorf("remote MCP returned HTTP %d", response.StatusCode)
	}
	raw, err := readResponse(response)
	if err != nil {
		return rpcResponse{}, response.Header, err
	}
	var decoded rpcResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return rpcResponse{}, response.Header, fmt.Errorf("decode JSON-RPC response: %w", err)
	}
	if decoded.Error != nil {
		return rpcResponse{}, response.Header, fmt.Errorf("remote MCP error %d: %s", decoded.Error.Code, decoded.Error.Message)
	}
	return decoded, response.Header, nil
}

// readResponse 读取一个 HTTP 响应并限制大小。对 text/event-stream 内容
// （MCP streamable HTTP 会这样应答）逐行找 "data:" 帧取第一条数据；
// 普通 JSON 则整包读回。
func readResponse(response *http.Response) ([]byte, error) {
	limited := io.LimitReader(response.Body, MaxResponseBytes+1)
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		scanner := bufio.NewScanner(limited)
		scanner.Buffer(make([]byte, 4096), MaxResponseBytes+1)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				data := bytes.TrimSpace([]byte(strings.TrimPrefix(line, "data:")))
				if len(data) > 0 {
					return data, nil
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("remote MCP SSE response contained no data")
	}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxResponseBytes {
		return nil, errors.New("remote MCP response exceeds size limit")
	}
	return raw, nil
}

// canonicalJSON 把一段 JSON 重新序列化一遍，得到语义等价、字节确定的
// 规范形式——这样同样的 schema 无论键序如何都产出同一份摘要。
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{"type":"object"}`)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	return json.RawMessage(canonical), err
}

// ToolSetDigest 把整组工具归一化后算出一个集合级摘要：每个 input schema 先
// 规范化，工具按名字排序后再整体哈希。任何工具的添加/删除/schema 漂移都会
// 改变这个值，是任务认领时比对「批准过的工具集」的可靠指纹。
func ToolSetDigest(tools []Tool) (string, error) {
	copyTools := append([]Tool(nil), tools...)
	for i := range copyTools {
		canonical, err := canonicalJSON(copyTools[i].InputSchema)
		if err != nil {
			return "", fmt.Errorf("tool %q input schema: %w", copyTools[i].Name, err)
		}
		copyTools[i].InputSchema = canonical
	}
	sort.Slice(copyTools, func(i, j int) bool { return copyTools[i].Name < copyTools[j].Name })
	raw, err := json.Marshal(copyTools)
	if err != nil {
		return "", err
	}
	return DigestBytes(raw), nil
}
