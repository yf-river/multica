// client.go 定义 multica 命令行客户端与服务器 API 通信用的 REST 客户端 APIClient，
// 以及配套的鉴权/上下文头注入、错误归一化（把 >=400 的响应折叠成 *HTTPError）和超时
// 预算管理。ctrl 系列子命令（agent、runtime、status 等）都通过它发请求。
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ClientVersion 是每次请求以 X-Client-Version 头发送的 CLI 版本号。由 multica 可执行
// 文件在 init() 里设置，这样本包就不依赖具体的 cmd 包。没设置时（例如测试里）默认
// 为 "dev"。
var ClientVersion = "dev"

// ClientPlatform 向服务器标识客户端类型。测试或其它入口可以覆盖；默认是 "cli"。
var ClientPlatform = "cli"

// ClientOS 是以 X-Client-OS 头发送的、规整后的操作系统字符串。从 runtime.GOOS 算
// 一次，这样服务器不用把 Go 的 os 名（"darwin"/"windows"/"linux"）反查成协议词汇。
var ClientOS = normalizeGOOS(runtime.GOOS)

// normalizeGOOS 把 Go 的 runtime.GOOS 名映射成协议里用的客户端 OS 名。
func normalizeGOOS(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return goos
	}
}

// APIClient 是访问 Multica 服务器 API 的 REST 客户端，供 ctrl 子命令（agent、runtime、
// status 等）使用。请求会自动带上鉴权与执行上下文相关的头（配置了的情况下）。
type APIClient struct {
	BaseURL            string
	WorkspaceID        string
	Token              string
	AgentID            string // 设置后，请求归因到该 agent 而不是用户
	TaskID             string // 设置后，作为 X-Task-ID 发送，供 agent-task 校验
	DeliveryToken      string
	DeliveryGeneration int64
	HTTPClient         *http.Client

	// 身份覆盖项。留空则回退到包级 ClientPlatform / ClientVersion / ClientOS。
	Platform string
	Version  string
	OS       string
}

// TaskTokenPrefix 标记 task 作用域的 agent 令牌，由 internal/auth.NewAgentTaskToken
// 铸造。task 令牌收到 401 不是「登录过期」那种 401——这个令牌只属于某一次运行，没有
// 「重新登录就能恢复」的路。它为什么被拒，CLI 是看不见的：task 到达终态是常见原因，
// 但畸形令牌、发错服务器、被无关清理逻辑删除等，从这里看完全一样。
const TaskTokenPrefix = "mat_"

// HTTPError 描述一次失败的 HTTP 响应（状态码 >= 400）。
type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
	// TaskScoped 记录「这次失败的请求确实带的是 task 作用域的 `mat_` 令牌」。它不影响
	// 请求本身，只是让 FormatError 能区分「该重新登录的 401」和「正确动作是停止的
	// 401」（GH #7522）。
	TaskScoped bool
}

// Error 拼出 HTTP 错误的描述文本。
func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

// newHTTPError 从一次错误响应（状态码 >= 400）构建 *HTTPError，只读 body 的前若干
// 字节。所有 Multica API 辅助函数都把 >= 400 的响应汇集到这里，这样顶层 FormatError /
// ExitCodeFor 无论命令用了哪个 HTTP 动词，都能统一用 errors.As(err, **HTTPError)
// 归类失败。
func newHTTPError(method, path string, resp *http.Response) *HTTPError {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &HTTPError{
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(data)),
		TaskScoped: requestUsedTaskToken(resp),
	}
}

// requestUsedTaskToken 报告产生 resp 的请求是否真的发了 task 令牌。
//
// 直接读客户端自己的 Token 字段会错两次。DownloadFile 对「绝对签名 URL」刻意不发
// Authorization 头，所以对象存储返回的 401 会因为客户端碰巧持有 `mat_` 令牌而被误报
// 成「task 令牌被拒」；而且 Go 在跨主机重定向时会剥掉 Authorization，实际发出去的
// 请求不总是调用方构造的那个。resp.Request 是重定向之后真正出去的请求，这个论断只
// 能诚实地建立在它之上。
func requestUsedTaskToken(resp *http.Response) bool {
	if resp == nil || resp.Request == nil {
		return false
	}
	return strings.HasPrefix(resp.Request.Header.Get("Authorization"), "Bearer "+TaskTokenPrefix)
}

// defaultHTTPTimeout 是 CLI HTTP 客户端的单请求超时。可用 MULTICA_HTTP_TIMEOUT 环境
// 变量覆盖（见 httpTimeout）。选 30s 而不是历史上的 15s，是因为复杂网络（尤其大陆）
// 完成 TLS 握手加一轮请求往返经常要超过 15s——那会以一句晦涩的 "context deadline
// exceeded" 砸给用户。
const defaultHTTPTimeout = 30 * time.Second

// httpTimeout 返回 HTTP 客户端超时，遵循 MULTICA_HTTP_TIMEOUT。值可以是 Go duration
// 字符串（"45s"、"2m"）或纯数字秒数（"45"）。非法或非正值回退到默认值。
func httpTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv("MULTICA_HTTP_TIMEOUT"))
	if v == "" {
		return defaultHTTPTimeout
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultHTTPTimeout
}

// apiContextGrace 是推导「命令级 context 截止时间」时，在 HTTP 传输超时之上加的余量，
// 这样先触发的是传输层超时（产出一个干净、可分类的 "request timed out" 错误），而不
// 是外层 context 先被取消。
const apiContextGrace = 5 * time.Second

// APITimeout 返回单条 CLI API 命令的截止预算。它至少是「配置的 HTTP 传输超时（见
// httpTimeout，遵循 MULTICA_HTTP_TIMEOUT）加一小段余量」，保证命令级 context 绝不会
// 在用户配置的超时之前把在途请求截断。这是对过去某些命令「硬编码 15s、比 30s/环境
// 变量传输超时还短」的修复。
func APITimeout() time.Duration {
	return AtLeastAPITimeout(0)
}

// AtLeastAPITimeout 返回 max(min, APITimeout())。用于需要比平时更大底线的命令
// （例如历史上用 60s 预算的文件上传）。
func AtLeastAPITimeout(min time.Duration) time.Duration {
	budget := httpTimeout() + apiContextGrace
	if min > budget {
		return min
	}
	return budget
}

// APIContext 派生一个命令作用域的 context，其截止时间是 APITimeout()。返回的 cancel
// 函数必须被调用（通常 defer），以释放资源。命令应该用它而不是硬编码时长的
// context.WithTimeout，这样截止时间永远尊重 MULTICA_HTTP_TIMEOUT。
func APIContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, APITimeout())
}

// NewAPIClient 创建一个给 ctrl 命令用的 API 客户端。
func NewAPIClient(baseURL, workspaceID, token string) *APIClient {
	return &APIClient{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		WorkspaceID: workspaceID,
		Token:       token,
		HTTPClient:  &http.Client{Timeout: httpTimeout()},
	}
}

// clientCapabilities 是每次 CLI 请求都会声明的 X-Client-Capabilities 值。它是协议
// 细节，不是用户可见开关：CLI 永远知道怎么处理这些响应形态，所以调用方无需显式参与。
//
// stable_attachment_urls 要求批量响应返回稳定的 /api/attachments/{id}/download 路径，
// 而不是每次重新铸造的约 800 字符的 CloudFront 签名（MUL-5372 / GitHub #5999）。CLI
// 从不会把附件 URL 交给原生加载器——`multica attachment download <id>` 会从单附件
// 端点另取新签名（不受该能力影响）——所以列表 payload 里的签名纯属开销：原始字节、
// 每次附件一次的 RSA 签名，而且这些字节每次读取都不同，会击穿 agent 的提示词缓存。
const clientCapabilities = "stable_attachment_urls"

// setHeaders 给请求统一设置能力声明、鉴权与执行上下文相关的头。
func (c *APIClient) setHeaders(req *http.Request) {
	req.Header.Set("X-Client-Capabilities", clientCapabilities)
	if c.DeliveryToken != "" {
		req.Header.Set("X-Delivery-Token", c.DeliveryToken)
		req.Header.Set("X-Delivery-Generation", strconv.FormatInt(c.DeliveryGeneration, 10))
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", c.WorkspaceID)
	}
	if c.AgentID != "" {
		req.Header.Set("X-Agent-ID", c.AgentID)
	}
	if c.TaskID != "" {
		req.Header.Set("X-Task-ID", c.TaskID)
	}

	platform := c.Platform
	if platform == "" {
		platform = ClientPlatform
	}
	if platform != "" {
		req.Header.Set("X-Client-Platform", platform)
	}
	version := c.Version
	if version == "" {
		version = ClientVersion
	}
	if version != "" {
		req.Header.Set("X-Client-Version", version)
	}
	osName := c.OS
	if osName == "" {
		osName = ClientOS
	}
	if osName != "" {
		req.Header.Set("X-Client-OS", osName)
	}
}

// GetJSON 发一个 GET 请求并把 JSON 响应解码进 out。
//
// 遇到 HTTP 错误响应（状态码 >= 400）时返回 *HTTPError，调用方可以用 errors.As 查看
// 状态码（例如识别出「服务器没提供某端点」的 404 并优雅降级）。错误文本格式
// （"GET <path> returned <code>: <body>"）由 HTTPError.Error() 保留。
func (c *APIClient) GetJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodGet, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// GetJSONWithHeaders 发 GET 请求、解码 JSON 响应，并返回响应头。当调用方需要
// X-Total-Count 之类的头做分页时很有用。
func (c *APIClient) GetJSONWithHeaders(ctx context.Context, path string, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, newHTTPError(http.MethodGet, path, resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, wrapBodyRead(req, err)
		}
	}
	return resp.Header, nil
}

// DeleteJSON 发一个 DELETE 请求。
func (c *APIClient) DeleteJSON(ctx context.Context, path string) error {
	return c.DeleteJSONResponse(ctx, path, nil)
}

// DeleteJSONResponse 发 DELETE 请求，并可选地把 JSON 响应解码到 out。
func (c *APIClient) DeleteJSONResponse(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodDelete, path, resp)
	}
	if out != nil {
		return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
	}
	return nil
}

// DeleteJSONWithBody 发一个带 JSON body 的 DELETE 请求。
func (c *APIClient) DeleteJSONWithBody(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodDelete, path, resp)
	}
	return nil
}

// PostJSON 发一个带 JSON body 的 POST 请求。
func (c *APIClient) PostJSON(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPost, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// PostJSONWithIdempotencyKey 发一个带 JSON body 与幂等键的 POST 请求，便于调用方安全
// 重试。
func (c *APIClient) PostJSONWithIdempotencyKey(ctx context.Context, path string, body any, key string, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err = wrapTransport(req, err); err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPost, path, resp)
	}
	if out != nil {
		return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
	}
	return nil
}

// PutJSON 发一个带 JSON body 的 PUT 请求。
func (c *APIClient) PutJSON(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPut, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// PatchJSON 发一个带 JSON body 的 PATCH 请求。
func (c *APIClient) PatchJSON(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPatch, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// AttachmentResponse 镜像服务器「上传文件」接口的响应。
type AttachmentResponse struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
	// MarkdownURL 是可在 markdown 正文（聊天回复、评论）里嵌入的持久 URL。与
	// DownloadURL 不同，它永远不带 TTL。
	MarkdownURL string `json:"markdown_url"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAt   string `json:"created_at"`
}

// UploadFile 通过 multipart 表单把文件上传到 /api/upload-file。返回服务器响应里的
// 附件 ID。
func (c *APIClient) UploadFile(ctx context.Context, fileData []byte, filename string, issueID string) (string, error) {
	att, err := c.UploadIssueAttachment(ctx, fileData, filename, issueID)
	if err != nil {
		return "", err
	}
	return att.ID, nil
}

// UploadIssueAttachment uploads a file via multipart form to /api/upload-file
// and returns the full AttachmentResponse. An empty issueID leaves the row
// unbound, which is what `issue create` needs: the file is uploaded before the
// issue exists, its `markdown_url` goes into the description, and the create
// call binds it via `attachment_ids`.
func (c *APIClient) UploadIssueAttachment(ctx context.Context, fileData []byte, filename string, issueID string) (AttachmentResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return AttachmentResponse{}, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return AttachmentResponse{}, fmt.Errorf("write file data: %w", err)
	}

	if issueID != "" {
		if err := writer.WriteField("issue_id", issueID); err != nil {
			return AttachmentResponse{}, fmt.Errorf("write issue_id field: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return AttachmentResponse{}, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/upload-file", &body)
	if err != nil {
		return AttachmentResponse{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	// Honor a longer context deadline for large files: callers widen the
	// context for uploads, which the default client timeout would shadow.
	// Same shape as UploadChatAttachment / UploadFileWithURL.
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}

	resp, err := httpClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return AttachmentResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return AttachmentResponse{}, newHTTPError(http.MethodPost, "/api/upload-file", resp)
	}

	var result AttachmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return AttachmentResponse{}, fmt.Errorf("decode upload response: %w", wrapBodyRead(req, err))
	}
	if result.ID == "" {
		return AttachmentResponse{}, fmt.Errorf("upload response missing attachment id")
	}
	return result, nil
}

// UploadChatAttachment 通过 multipart 表单上传文件到 /api/upload-file，并打上聊天
// task 的标记（task_id）。服务器会把这条记录绑定到该 task 完成时产生的助手中。返回
// 完整的 AttachmentResponse（id + markdown_url），方便 agent 把图片内联嵌进回复。
func (c *APIClient) UploadChatAttachment(ctx context.Context, fileData []byte, filename, taskID string) (AttachmentResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return AttachmentResponse{}, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return AttachmentResponse{}, fmt.Errorf("write file data: %w", err)
	}
	if taskID != "" {
		if err := writer.WriteField("task_id", taskID); err != nil {
			return AttachmentResponse{}, fmt.Errorf("write task_id field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return AttachmentResponse{}, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/upload-file", &body)
	if err != nil {
		return AttachmentResponse{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	// 大图片要给更长的 context 截止时间，与 UploadFileWithURL 一致。
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}

	resp, err := httpClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return AttachmentResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return AttachmentResponse{}, newHTTPError(http.MethodPost, "/api/upload-file", resp)
	}

	var result AttachmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return AttachmentResponse{}, fmt.Errorf("decode upload response: %w", wrapBodyRead(req, err))
	}
	if result.ID == "" {
		return AttachmentResponse{}, fmt.Errorf("upload response missing attachment id")
	}
	return result, nil
}

// UploadFileWithURL 通过 multipart 表单把文件上传到 /api/upload-file，不与 issue 或
// comment 关联。它解码完整 AttachmentResponse，返回附件 ID 和 URL。
func (c *APIClient) UploadFileWithURL(ctx context.Context, fileData []byte, filename string) (string, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return "", "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return "", "", fmt.Errorf("write file data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", "", fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/upload-file", &body)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	// 用一个尊重 context 截止时间的客户端处理慢上传（例如 5MB 的头像）：默认的 HTTP
	// 客户端超时会把更长的 context 截止时间遮蔽掉。
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}

	resp, err := httpClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", "", newHTTPError(http.MethodPost, "/api/upload-file", resp)
	}

	var result AttachmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode upload response: %w", wrapBodyRead(req, err))
	}
	if result.URL == "" {
		return "", "", fmt.Errorf("upload response missing attachment url")
	}
	// ID 允许为空：在「S3 上传成功但附件 DB 记录失败」的回退路径里，服务器会返回
	// id=""。文件仍可通过 URL 使用。
	return result.ID, result.URL, nil
}

// UploadKnowledgeResource posts one file to the workspace knowledge base.
// The server forwards it to OpenViking and returns the ingestion result.
func (c *APIClient) UploadKnowledgeResource(ctx context.Context, fileData []byte, filename string) (json.RawMessage, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return nil, fmt.Errorf("write file data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}
	path := "/api/workspaces/" + c.WorkspaceID + "/knowledge/resources"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, newHTTPError(http.MethodPost, path, resp)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode upload response: %w", wrapBodyRead(req, err))
	}
	return out.Result, nil
}

// ImportSkillFile 把本地 skill 包（.skill / .zip）以 multipart/form-data POST 到
// /api/skills/import，同时带上 on_conflict 策略。结构化的导入结果解码进 out。
func (c *APIClient) ImportSkillFile(ctx context.Context, fileData []byte, filename, onConflict string, out any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return fmt.Errorf("write file data: %w", err)
	}
	if onConflict != "" {
		if err := writer.WriteField("on_conflict", onConflict); err != nil {
			return fmt.Errorf("write on_conflict field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/skills/import", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	// 给慢上传更长的 context 截止时间，与 UploadFileWithURL 保持一致：否则默认客户端
	// 超时会把它遮蔽掉。
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}

	resp, err := httpClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPost, "/api/skills/import", resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// UploadPrivatePlugin 上传 workspace 私有的 Plugin 压缩包字节。文件名只是传输元数据；
// 服务器从校验过的 manifest 内容与摘要推导来源身份。
func (c *APIClient) UploadPrivatePlugin(ctx context.Context, path string, archive []byte, filename string, out any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("artifact", filepath.Base(filename))
	if err != nil {
		return fmt.Errorf("create Plugin artifact form file: %w", err)
	}
	if _, err := part.Write(archive); err != nil {
		return fmt.Errorf("write Plugin artifact: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close Plugin artifact upload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPost, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// DownloadFile 从给定 URL 下载文件并返回响应体。用于经签名 download_url 下载附件。
// 下载限制为 100 MB，与上传大小上限对齐。
//
// URL 既可以是绝对地址（签名的 CloudFront/S3 URL），也可以是相对路径（服务器相对
// 路径如 "/api/attachments/{id}/download" 或 "/uploads/..."），取决于服务器如何配置。
// 相对 URL 会基于客户端的 BaseURL 解析，并带上标准鉴权头；绝对 URL 原样使用，以免
// 破坏其 query 串里的签名。
func (c *APIClient) DownloadFile(ctx context.Context, downloadURL string) ([]byte, error) {
	isRelative := !strings.HasPrefix(downloadURL, "http://") && !strings.HasPrefix(downloadURL, "https://")
	if isRelative {
		if c.BaseURL == "" {
			return nil, fmt.Errorf("download URL %q is relative but client has no BaseURL", downloadURL)
		}
		downloadURL = c.BaseURL + downloadURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if isRelative {
		c.setHeaders(req)
	}

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, newHTTPError(http.MethodGet, downloadURL, resp)
	}

	const maxDownloadSize = 100 << 20 // 100 MB
	return io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
}

// HealthCheck 请求 /health 端点并返回响应体。
func (c *APIClient) HealthCheck(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return "", &HTTPError{
			Method:     http.MethodGet,
			Path:       "/health",
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(data)),
			TaskScoped: requestUsedTaskToken(resp),
		}
	}
	return strings.TrimSpace(string(data)), nil
}
