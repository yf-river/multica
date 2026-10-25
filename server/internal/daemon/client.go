package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/codediff"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

// requestError 是 postJSON/getJSON 在服务器返回错误状态时给出的错误。
type requestError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *requestError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

var errInvalidResponseBody = errors.New("invalid response body")

// isWorkspaceNotFoundError returns true if the error is a 404 with "workspace not found" body.
func isWorkspaceNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	if reqErr.StatusCode != http.StatusNotFound {
		return false
	}
	return strings.Contains(strings.ToLower(reqErr.Body), "workspace not found")
}

// isTaskNotFoundError 判断错误是否为带 "task not found" 响应体的 404。daemon 用它来
// 发现“本地 agent 还在跑的时候任务已在服务端被删”（issue 被移除、agent 被改派……），
// 从而可以打断 agent，而不是让它对一个已死的任务继续发工具调用。
func isTaskNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	if reqErr.StatusCode != http.StatusNotFound {
		return false
	}
	return strings.Contains(strings.ToLower(reqErr.Body), "task not found")
}

// isUnauthorizedError 判断错误是否为服务器返回的 401。token 续期循环用它给出明确的
// “需要重新登录”提示，而不是泛泛地当传输层错误重试。
func isUnauthorizedError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == http.StatusUnauthorized
}

// isRuntimeNotFoundError 判断错误是否为带 "runtime not found" 响应体的 404。daemon
// 用它发现“自己还在对死掉的 UUID 发心跳时，runtime 行已被服务端删除”（UI 删除、
// 7 天离线 GC），从而能从本地状态里摘掉这个过期 runtime 并重新注册，而不是永远对着
// 死 ID 打转。
//
// 服务端这里专门把该响应体与 pgx.ErrNoRows 配对（其它 DB 错误返回 500），因此一次
// 瞬时的 DB 抖动不会诱发 daemon 自我清理。
func isRuntimeNotFoundError(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	if reqErr.StatusCode != http.StatusNotFound {
		return false
	}
	return strings.Contains(strings.ToLower(reqErr.Body), "runtime not found")
}

// Client 负责与 Multica 服务器 daemon API 的 HTTP 通信。
type Client struct {
	baseURL string
	token   string
	client  *http.Client

	// bundleClient 负责下载技能包。与 client 不同，它不带固定 Timeout：技能包可能很
	// 大、在抖动链路上很慢，所以调用方用 context 提供“随尺寸缩放”的按请求截止时间，
	// 而不是被适合心跳/claim、却装不下“一次几 MB 的响应体读取”的 30s 控制面超时卡
	// 死。（GitHub #4505）
	bundleClient *http.Client

	// 每条请求以 X-Client-* 发送的身份头。由 SetIdentity() 填充；空值直接省略。
	platform string
	version  string
	os       string

	workspaceMu                    sync.Mutex
	workspaceETag                  string
	workspaceCache                 []WorkspaceInfo
	workspaceCacheValid            bool
	legacyWorkspaceEndpointEnabled bool
	issueGCBatchMu                 sync.Mutex
	legacyIssueGCBatchEnabled      bool
}

// NewClient 创建一个新的 daemon API 客户端。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:      baseURL,
		client:       &http.Client{Timeout: 30 * time.Second, Transport: cloneDefaultTransport()},
		bundleClient: &http.Client{},
		platform:     "daemon",
		os:           normalizeGOOS(runtime.GOOS),
	}
}

// cloneDefaultTransport 克隆 http 默认传输，避免改坏进程级共享的全局传输。
func cloneDefaultTransport() http.RoundTripper {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return http.DefaultTransport
}

// CloseIdleConnections 断开池化的控制面 HTTP 连接。daemon 在心跳传输反复失败后调用
// 它，让“服务器重启留下的陈旧 keep-alive socket”不至于无限期拖延恢复。
func (c *Client) CloseIdleConnections() {
	if c == nil || c.client == nil {
		return
	}
	c.client.CloseIdleConnections()
}

// normalizeGOOS 把 Go 的 runtime.GOOS 映射成 X-Client-OS / client_os 协议词汇
// （"macos" / "windows" / "linux"）。
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

// SetVersion 记录 daemon 的 CLI 版本，随 X-Client-Version 发送。由 Daemon.Run 在
// 配置加载后调用。
func (c *Client) SetVersion(v string) {
	c.version = v
}

// setIdentityHeaders 在已设置时给 req 挂上 X-Client-Platform/Version/OS。
func (c *Client) setIdentityHeaders(req *http.Request) {
	if c.platform != "" {
		req.Header.Set("X-Client-Platform", c.platform)
	}
	if c.version != "" {
		req.Header.Set("X-Client-Version", c.version)
	}
	if c.os != "" {
		req.Header.Set("X-Client-OS", c.os)
	}
	req.Header.Set("X-Client-Capabilities", daemonClientCapabilities())
}

// daemonClientCapabilities 是 daemon 在 HTTP 控制面请求与 WS 握手*两边*都宣告的
// X-Client-Capabilities 值，使经 WS 构建的 claim 与 HTTP 路径拿到一致的能力闸门
// （技能引用、合并评论）。rpc-v1 宣告 WS 请求/响应支持（MUL-4257）。
func daemonClientCapabilities() string {
	return strings.Join([]string{
		"workspace-mcp-check-v1",
		protocol.DaemonCapabilitySkillBundlesV1,
		protocol.DaemonCapabilitySkillAssetsV1,
		protocol.DaemonCapabilityCoalescedCommentsV1,
		protocol.DaemonCapabilityExecutionManifestV1,
		protocol.DaemonCapabilityAgentSkillV1,
		protocol.DaemonCapabilityRemoteMCPV1,
		protocol.DaemonCapabilityLocalWorktreeV1,
		protocol.DaemonCapabilitySourceContextQuickCreateV1,
		protocol.DaemonCapabilityRPCV1,
		protocol.DaemonCapabilityCheckoutKeepsWorkV1,
	}, ",")
}

// SetToken 设置鉴权请求用的认证 token。
func (c *Client) SetToken(token string) {
	c.token = token
}

// Token 返回当前认证 token。
func (c *Client) Token() string {
	return c.token
}

// ClaimTask 为单个 runtime 认领一个任务（旧式按 runtime 认领）。
func (c *Client) ClaimTask(ctx context.Context, runtimeID string) (*Task, error) {
	var resp struct {
		Task *Task `json:"task"`
	}
	if err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/tasks/claim", runtimeID), map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return resp.Task, nil
}

// ResolveRemoteMCPCredential 经“任务级 daemon token”换取 Remote MCP 连接的上游
// 凭据头。
func (c *Client) ResolveRemoteMCPCredential(ctx context.Context, daemonToken, taskID, contributionID string) (http.Header, error) {
	var response struct {
		CredentialHeader string `json:"credential_header"`
		Credential       string `json:"credential"`
	}
	// 插件贡献的连接，其凭据存在插件自己的秘密存储里、由另一条路由提供。标记挂在
	// contribution id 上，因为拨号时 broker 手里只有它。
	route := "remote-mcp"
	if strings.HasPrefix(contributionID, remotemcp.PluginContributionPrefix) {
		route = "plugin-mcp"
	}
	path := fmt.Sprintf("/api/daemon/tasks/%s/%s/%s/credential",
		url.PathEscape(taskID), route, url.PathEscape(contributionID))
	if err := c.getJSONWithToken(ctx, path, daemonToken, &response); err != nil {
		return nil, err
	}
	headers := make(http.Header)
	if response.CredentialHeader != "" {
		headers.Set(response.CredentialHeader, response.Credential)
	}
	return headers, nil
}

// batchClaimRequestTimeout is the short, request-scoped deadline for the
// machine-level batch claim (MUL-4257). Unlike the per-runtime claim — which
// gets the full 30s control-plane timeout because a stall there only blocks
// that one runtime's goroutine — the batch call covers every runtime the
// daemon hosts in a single request, so a slow claim would delay ALL of them
// (the head-of-line coupling the per-runtime pollers were split to avoid,
// MUL-1744). Bounding the batch to a few seconds caps that worst-case
// starvation; a claim that commits server-side after the client gives up is
// recovered by ReclaimStaleDispatchedTasksForRuntimes on the next poll. Kept
// comfortably above p99 claim latency so recovery stays the exception.
const batchClaimRequestTimeout = protocol.DaemonBatchClaimRequestTimeout

// ClaimTasks 是 ClaimTask 的机器级（MUL-4257）batch 对应物：它用一个请求让服务器在
// daemon 承载的每个 runtime 上最多认领 maxTasks 个任务。daemonID 把请求限定到这台
// 机器——服务器会拒绝任何 runtime.daemon_id 不匹配的 runtime_id，因此过期/串台的
// runtime 集合认领不了别的机器的任务。返回的每个 Task 自带 RuntimeID，daemon 据此在
// 本地路由到匹配的 runtime。请求运行在短请求级截止时间（batchClaimRequestTimeout）
// 下而非共享的 30s 控制面超时，一条慢 claim 拖不停整批；该截止时间会传播到服务器、
// 一并取消那边的在途查询。
func (c *Client) ClaimTasks(ctx context.Context, daemonID string, runtimeIDs []string, maxTasks int) ([]*Task, error) {
	reqCtx, cancel := context.WithTimeout(ctx, batchClaimRequestTimeout)
	defer cancel()
	var resp struct {
		Tasks []*Task `json:"tasks"`
	}
	if err := c.postJSON(reqCtx, "/api/daemon/tasks/claim", map[string]any{
		"daemon_id":   daemonID,
		"runtime_ids": runtimeIDs,
		"max_tasks":   maxTasks,
	}, &resp); err != nil {
		return nil, err
	}
	return resp.Tasks, nil
}

// isBatchClaimUnsupported 判断 err 是否为 batch claim 端点的 404——即服务器早于
// /api/daemon/tasks/claim 路由，daemon 必须回退到旧式按 runtime 认领（MUL-4257）。
// batch handler 本身从不返回 404，因此这里的 404 意味着该路由在未升级的服务器上没
// 注册。
func isBatchClaimUnsupported(err error) bool {
	var reqErr *requestError
	if !errors.As(err, &reqErr) {
		return false
	}
	return reqErr.StatusCode == http.StatusNotFound
}

// claimTasksLegacy 是 pre-batch 兼容回退（MUL-4257）：经旧式 POST
// /api/daemon/runtimes/{id}/tasks/claim 逐个 runtime 认领，让新 daemon 对没有 batch
// 路由的服务器仍能工作。最多返回 maxTasks 个任务。只在“一个都还没认领到”时才上抛
// 单 runtime 错误；否则返回部分结果，剩下的交给下一轮轮询重试。
func (c *Client) claimTasksLegacy(ctx context.Context, runtimeIDs []string, maxTasks int) ([]*Task, error) {
	if maxTasks <= 0 {
		return nil, nil
	}
	out := make([]*Task, 0, maxTasks)
	for _, rid := range runtimeIDs {
		if len(out) >= maxTasks {
			break
		}
		task, err := c.ClaimTask(ctx, rid)
		if err != nil {
			if len(out) == 0 {
				return nil, err
			}
			return out, nil
		}
		if task != nil {
			out = append(out, task)
		}
	}
	return out, nil
}

// TransferStats 记录一次逻辑调用读到的成功 HTTP 响应体字节数。对技能包下载而言，
// 它把“链接从未产生成功响应”与“2xx 响应体到了但没读完”分开——这是失败文本过去
// 表达不了的区分：一次连接故障被读成“技能损坏”，把反馈者引向错误的方向
// （GitHub #7386）。
//
// 跨重试时字段保留“高水位”而非“最后一次尝试”：它们要回答的是“这条链接到底有没有
// 走到过”，一次到达响应体的尝试比之后没到达的尝试更有信息量。零值可直接用，每个
// 方法都做 nil 保护，不需要统计的调用方传 nil 即可。
type TransferStats struct {
	// ResponseStarted 表示是否曾收到 2xx 响应头。错误响应体的字节被刻意排除：那些
	// 字节描述的是服务器业务错误，不是下载技能包的进展。
	ResponseStarted bool
	// BytesRead 是任意单次尝试读到的最多响应体字节数。
	BytesRead int64
}

func (t *TransferStats) observeResponseStarted() {
	if t != nil {
		t.ResponseStarted = true
	}
}

// wrap 返回一个包装 reader，把本次尝试读到的字节数反馈进 t。
func (t *TransferStats) wrap(r io.Reader) io.Reader {
	if t == nil {
		return r
	}
	return &countingReader{inner: r, stats: t}
}

// countingReader 统计单次尝试并随读随抬高父级高水位，让“读到一半失败”也能报出该
// 尝试走了多远。
type countingReader struct {
	inner   io.Reader
	stats   *TransferStats
	attempt int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	if n > 0 {
		c.attempt += int64(n)
		if c.attempt > c.stats.BytesRead {
			c.stats.BytesRead = c.attempt
		}
	}
	return n, err
}

// ResolveSkillBundle 下载单个技能包。它走 bundleClient（无固定超时），因此截止时间
// 完全由 ctx 决定——daemon 会把它按包大小缩放——并在 ctx 剩余的预算内重试瞬时传输
// 抖动。一次请求只解析一个技能（而不是把 agent 的整包原子读进体内），让每个下载能配
// 自己的截止时间、能独立缓存，慢链路做的是增量进展，而不是每次派发都让整组失败。
// （GitHub #4505）
func (c *Client) ResolveSkillBundle(ctx context.Context, runtimeID, taskID string, ref SkillRefData) (SkillData, TransferStats, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var resp struct {
			Bundles []SkillData `json:"bundles"`
		}
		var stats TransferStats
		path := fmt.Sprintf("/api/daemon/runtimes/%s/tasks/%s/skill-bundles/resolve", runtimeID, taskID)
		if err := c.postJSONViaWithRetry(ctx, c.bundleClient, path, map[string]any{
			"skills": []SkillRefData{ref},
		}, &resp, skillBundleResolveRetrySchedule, &stats); err != nil {
			return SkillData{}, stats, err
		}
		if len(resp.Bundles) != 1 {
			return SkillData{}, stats, fmt.Errorf("resolve skill bundle: expected 1 bundle, got %d", len(resp.Bundles))
		}
		bundle := resp.Bundles[0]
		if err := c.downloadSkillAssets(ctx, runtimeID, taskID, &bundle); err != nil {
			if errors.Is(err, errSkillAssetVersionChanged) {
				continue
			}
			return SkillData{}, stats, err
		}
		return bundle, stats, nil
	}
	return SkillData{}, TransferStats{}, errSkillAssetVersionChanged
}

// ExtendTaskPrepareLease 延长任务准备阶段的租约，防止准备耗时过长被服务端回收。
func (c *Client) ExtendTaskPrepareLease(ctx context.Context, runtimeID, taskID string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/tasks/%s/prepare-lease", runtimeID, taskID), map[string]any{}, nil)
}

type TaskSupplement struct {
	CommentID  string `json:"comment_id"`
	AuthorName string `json:"author_name"`
	Content    string `json:"content"`
}

func (c *Client) ClaimTaskSupplement(ctx context.Context, taskID string) (*TaskSupplement, error) {
	var supplement TaskSupplement
	if err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/supplements/claim", taskID), map[string]any{}, &supplement); err != nil {
		return nil, err
	}
	if supplement.CommentID == "" {
		return nil, nil
	}
	return &supplement, nil
}

func (c *Client) AckTaskSupplement(ctx context.Context, taskID, commentID string, delivered bool, errText string) error {
	return c.postJSONWithRetry(ctx,
		fmt.Sprintf("/api/daemon/tasks/%s/supplements/%s/ack", taskID, commentID),
		map[string]any{"delivered": delivered, "error": errText}, nil,
		[]time.Duration{0, 100 * time.Millisecond, 300 * time.Millisecond})
}

// startTaskRetrySchedule allows two short reconnects without the terminal
// callbacks' 124s backoff. The whole start has a 30s budget (not 3 x the HTTP
// client's 30s timeout). Preparation keeps renewing its 45s lease every 15s
// throughout requests and backoff; its own deadline can end this sooner.
// runTask calls this once. Task-level retries are separate executions, with
// fresh claims, and cannot reuse this acknowledgement.
var startTaskRetrySchedule = []time.Duration{500 * time.Millisecond, 2 * time.Second}

const (
	startTaskTimeout = 30 * time.Second
	// Start responses can include an issue snapshot alongside the capability.
	maxStartTaskResponseBytes = 1 << 20
)

var errStartClaimRejected = errors.New("task start claim rejected")

func (c *Client) StartTask(ctx context.Context, task Task, capabilities ...string) (bool, error) {
	var negotiated bool
	var decodeResponse responseDecoder = func(r io.Reader) error {
		data, err := io.ReadAll(io.LimitReader(r, maxStartTaskResponseBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxStartTaskResponseBytes {
			return fmt.Errorf("%w: task start exceeds %d bytes", errInvalidResponseBody, maxStartTaskResponseBytes)
		}
		var response struct {
			SupplementCapability string `json:"supplement_capability"`
		}
		// Empty acknowledgements start the task without negotiating supplements.
		// Invalid JSON fails without retrying; transport read failures can retry.
		if len(data) != 0 {
			if err := json.Unmarshal(data, &response); err != nil {
				return fmt.Errorf("%w: task start: %w", errInvalidResponseBody, err)
			}
		}
		negotiated = response.SupplementCapability == protocol.DaemonCapabilityTaskSupplementV1
		return nil
	}
	path := fmt.Sprintf("/api/daemon/tasks/%s/start", task.ID)
	if !task.StartClaimSupported {
		// Old servers have no safe replay contract. Preserve one attempt.
		err := c.postJSON(ctx, path, map[string]any{"capabilities": capabilities}, decodeResponse)
		return err == nil && negotiated, err
	}
	if task.RuntimeID == "" || task.DispatchedAt == "" {
		return false, fmt.Errorf("start task: claim is missing runtime_id or dispatched_at")
	}
	ctx, cancel := context.WithTimeout(ctx, startTaskTimeout)
	defer cancel()
	err := c.postJSONWithRetry(ctx, path, map[string]any{
		"runtime_id":    task.RuntimeID,
		"capabilities":  capabilities,
		"dispatched_at": task.DispatchedAt,
	}, decodeResponse, startTaskRetrySchedule)
	var reqErr *requestError
	if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusConflict {
		return false, fmt.Errorf("%w: %w", errStartClaimRejected, err)
	}
	return err == nil && negotiated, err
}

// MarkTaskWaitingLocalDirectory 把刚派发的任务在服务器上停进 waiting_local_directory
// 状态。daemon 在“已认领一个带 local_directory 资源的任务、但路径互斥锁被另一个在途
// 任务持有”后调用它。reason 是给 UI 的短人类可读提示（如 "<path>"）。daemon 侧幂等——
// 同一 reason 调用两次在行已是 waiting_local_directory 后是无操作（底层 SQL 按
// status='dispatched' 过滤，第二次调用是 daemon 吞掉的 400，然后继续等待）。
func (c *Client) MarkTaskWaitingLocalDirectory(ctx context.Context, taskID, reason string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/wait-local-directory", taskID), map[string]any{
		"reason": reason,
	}, nil)
}

// TaskCancelAck 是 daemon 取消确认的载荷。
type TaskCancelAck struct {
	// BranchName: 被取消的 worktree 任务已完成收尾——它没做完的部分工作已提交到
	// 用户仓库的一个分支。取消路径会丢弃结果其余部分，因此这个 ack 是报告工作去向的
	// 唯一通道。
	BranchName string
	// DurableWorkDir 是一次性任务 worktree 被移除后成为权威的、配置过的
	// local_directory 路径。
	DurableWorkDir string
	// ErrorMessage / FailureReason: 被取消的运行*额外*没成功持久化工作（worktree
	// Finalize 中止）时设置。此时没有分支；携带“保留 worktree 路径”的错误文本是通往
	// agent 工作的唯一指针，没有它取消路径会把它整个吞掉。
	ErrorMessage  string
	FailureReason string
}

// AckTaskCancelled 告诉服务器本 daemon 已观察到任务的取消并已完成 transcript 冲刷
// （runner.run 只在 executeAndDrain 的 drain 等待结束后返回），服务器于是可以现在就
// 落定它推迟的 chat 收尾，而不是等满清理器的宽限周期（#5219）。服务端幂等。
//
// 与 complete/fail 回调一样对瞬时错误重试：当 ack 携带分支或错误时它是终结投递——
// 通往被取消任务工作的唯一指针——丢一次 POST 就永远丢了。服务器从不覆盖已记录的值，
// 因此重放安全。
func (c *Client) AckTaskCancelled(ctx context.Context, taskID string, ack TaskCancelAck) error {
	body := map[string]any{}
	if ack.BranchName != "" {
		body["branch_name"] = ack.BranchName
	}
	if ack.DurableWorkDir != "" {
		body["durable_work_dir"] = ack.DurableWorkDir
	}
	if ack.ErrorMessage != "" {
		body["error_message"] = ack.ErrorMessage
	}
	if ack.FailureReason != "" {
		body["failure_reason"] = ack.FailureReason
	}
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/cancel-ack", taskID), body, nil, defaultTerminalRetrySchedule)
}

// ReportProgress 上报任务进度（摘要 + 步骤计数）。
func (c *Client) ReportProgress(ctx context.Context, taskID, summary string, step, total int) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/progress", taskID), map[string]any{
		"summary": summary,
		"step":    step,
		"total":   total,
	}, nil)
}

// TaskMessageData 表示批量上报中的一条 agent 执行消息。
type TaskMessageData struct {
	// CallID is an opaque tool-call identity scoped to one backend execution.
	CallID  string         `json:"call_id,omitempty"`
	Seq     int            `json:"seq"`
	Type    string         `json:"type"`
	Tool    string         `json:"tool,omitempty"`
	Content string         `json:"content,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Output  string         `json:"output,omitempty"`
	// Daemon 观察到事件的时间，早于批量上报。
	CreatedAt time.Time `json:"created_at"`
}

// ReportTaskMessages 批量上报任务执行过程中的消息。
func (c *Client) ReportTaskMessages(ctx context.Context, taskID string, messages []TaskMessageData) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/messages", taskID), map[string]any{
		"messages": messages,
	}, nil)
}

// CompleteTask 上报任务完成；差异数据随终态一起持久化。
func (c *Client) CompleteTask(ctx context.Context, taskID, output, branchName, sessionID, workDir string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string, codeDiff *codediff.Diff) error {
	return c.completeTaskWithRetrySchedule(ctx, taskID, output, branchName, sessionID, workDir, sessionRolloutMissing, retiredSessionID, durableWorkDir, codeDiff, defaultTerminalRetrySchedule)
}

func (c *Client) completeTaskWithRetrySchedule(ctx context.Context, taskID, output, branchName, sessionID, workDir string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string, codeDiff *codediff.Diff, schedule []time.Duration) error {
	body := map[string]any{"output": output}
	if codeDiff != nil {
		body["code_diff"] = codeDiff
	}
	if branchName != "" {
		body["branch_name"] = branchName
	}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	if durableWorkDir != "" {
		body["durable_work_dir"] = durableWorkDir
	}
	if sessionRolloutMissing {
		body["session_rollout_missing"] = true
	}
	if retiredSessionID != "" {
		body["retired_session_id"] = retiredSessionID
	}
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/complete", taskID), body, nil, schedule)
}

// ReportTaskUsage 上报任务中各模型的 token 用量。
func (c *Client) ReportTaskUsage(ctx context.Context, taskID string, usage []TaskUsageEntry) error {
	if len(usage) == 0 {
		return nil
	}
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/usage", taskID), map[string]any{
		"usage": usage,
	}, nil)
}

// ReportTaskTraces 上报本次执行在外部观测系统里的根请求标识（0..N 条）。
// 与用量上报分开：没有 token 用量时链路依然存在，反过来没发出模型请求时链路为空。
func (c *Client) ReportTaskTraces(ctx context.Context, taskID string, traces []TaskTraceEntry) error {
	if len(traces) == 0 {
		return nil
	}
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/traces", taskID), map[string]any{
		"traces": traces,
	}, nil)
}

func (c *Client) FailTask(ctx context.Context, taskID, errMsg, sessionID, workDir, branchName, failureReason string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string, codeDiff *codediff.Diff) error {
	return c.failTaskWithRetrySchedule(ctx, taskID, errMsg, sessionID, workDir, branchName, failureReason, sessionRolloutMissing, retiredSessionID, durableWorkDir, codeDiff, defaultTerminalRetrySchedule)
}

func (c *Client) failTaskWithRetrySchedule(ctx context.Context, taskID, errMsg, sessionID, workDir, branchName, failureReason string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string, codeDiff *codediff.Diff, schedule []time.Duration) error {
	body := map[string]any{"error": errMsg}
	// 1. 失败的运行也可能已经改过文件：把 diff 一起上报，用户才看得到它停在
	//    哪一步。服务器把它写到失败任务的 result 上。
	if codeDiff != nil {
		body["code_diff"] = codeDiff
	}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	if durableWorkDir != "" {
		body["durable_work_dir"] = durableWorkDir
	}
	// 失败运行仍可能交付了分支：worktree 模式会在移除 worktree 前提交 agent 留下的
	// 一切，部分工作因此得以幸存——但只有它的名字随失败报告一起走才有效。
	if branchName != "" {
		body["branch_name"] = branchName
	}
	if failureReason != "" {
		body["failure_reason"] = failureReason
	}
	if sessionRolloutMissing {
		body["session_rollout_missing"] = true
	}
	if retiredSessionID != "" {
		body["retired_session_id"] = retiredSessionID
	}
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/fail", taskID), body, nil, schedule)
}

// PinTaskSession 在运行中途把 agent 的 session_id 与 work_dir 持久化到任务行上，
// 防止 daemon 崩溃丢掉续接指针。
func (c *Client) PinTaskSession(ctx context.Context, taskID, sessionID, workDir string) error {
	if sessionID == "" && workDir == "" {
		return nil
	}
	body := map[string]any{}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	if workDir != "" {
		body["work_dir"] = workDir
	}
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/session", taskID), body, nil)
}

// RecoverOrphans 让服务器把本 runtime 上一任 daemon 进程遗留下的
// dispatched/running 任务判失败。服务器会按条件自动重试这些任务。
func (c *Client) RecoverOrphans(ctx context.Context, runtimeID string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/recover-orphans", runtimeID), map[string]any{}, nil)
}

// GetTaskStatus 返回任务当前状态。daemon 用它发现任务执行期间的终态/中断信号
// （cancelled、failed、completed，或 404 task-not-found）。
func (c *Client) GetTaskStatus(ctx context.Context, taskID string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/status", taskID), &resp); err != nil {
		return "", err
	}
	return resp.Status, nil
}

// HeartbeatResponse、PendingUpdate 等别名指向线类型，让 HTTP 与 WS 心跳路径共享
// 单一类型与单一解码形态。用别名（而非包装）让调用点无需改动。
type (
	HeartbeatResponse       = protocol.DaemonHeartbeatAckPayload
	PendingUpdate           = protocol.DaemonHeartbeatPendingUpdate
	PendingModelList        = protocol.DaemonHeartbeatPendingModelList
	PendingLocalSkills      = protocol.DaemonHeartbeatPendingLocalSkills
	PendingLocalSkillImport = protocol.DaemonHeartbeatPendingLocalSkillImport
)

// SendHeartbeat 发送单个 runtime 的心跳。
func (c *Client) SendHeartbeat(ctx context.Context, runtimeID string) (*HeartbeatResponse, error) {
	var resp HeartbeatResponse
	if err := c.postJSON(ctx, "/api/daemon/heartbeat", map[string]any{
		"runtime_id":            runtimeID,
		"supports_batch_import": true,
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ReportUpdateResult 把 CLI 更新结果发回服务器。
func (c *Client) ReportUpdateResult(ctx context.Context, runtimeID, updateID string, result map[string]any) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/update/%s/result", runtimeID, updateID), result, nil)
}

// ReportModelListResult 把模型发现结果发回服务器。
func (c *Client) ReportModelListResult(ctx context.Context, runtimeID, requestID string, result map[string]any) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/models/%s/result", runtimeID, requestID), result, nil)
}

// ReportLocalSkillListResult 把 runtime 本地技能清单发回服务器。
func (c *Client) ReportLocalSkillListResult(ctx context.Context, runtimeID, requestID string, result map[string]any) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/local-skills/%s/result", runtimeID, requestID), result, nil)
}

// ReportLocalSkillImportResult 把 runtime 本地技能包发回服务器。
func (c *Client) ReportLocalSkillImportResult(ctx context.Context, runtimeID, requestID string, result map[string]any) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/runtimes/%s/local-skills/import/%s/result", runtimeID, requestID), result, nil)
}

// WorkspaceInfo 持有 API 返回的最小 workspace 元数据。
type WorkspaceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// RenewTokenResponse 镜像 handler.RenewPATResponse——保持松散（string + bool），
// 因为 daemon 从不解析时间戳本身；它只记日志供运维查看。
type RenewTokenResponse struct {
	ExpiresAt string `json:"expires_at"`
	Renewed   bool   `json:"renewed"`
}

// RenewToken 在 daemon 的当前 PAT 进入服务端续期窗口时请服务器就地延长它。阈值以
// 服务器为准——daemon 本地不知道 token 的 expires_at——因此任何节奏调用都安全；多余
// 调用只花一次往返与一次廉价 SELECT。
func (c *Client) RenewToken(ctx context.Context) (*RenewTokenResponse, error) {
	var resp RenewTokenResponse
	if err := c.postJSON(ctx, "/api/tokens/current/renew", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListWorkspaces 拉取 daemon 使用的最小 workspace 成员集。新服务器暴露带 ETag 支持
// 的 daemon 专用端点；当已装的 daemon 连到老服务器时，首个 404 会让本进程永久切到
// 旧版全量 workspace 端点以保持兼容。
func (c *Client) ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	c.workspaceMu.Lock()
	defer c.workspaceMu.Unlock()

	if c.legacyWorkspaceEndpointEnabled {
		return c.listLegacyWorkspaces(ctx)
	}

	const path = "/api/daemon/workspaces"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.setIdentityHeaders(req)
	if c.workspaceETag != "" {
		req.Header.Set("If-None-Match", c.workspaceETag)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		c.legacyWorkspaceEndpointEnabled = true
		c.workspaceETag = ""
		c.workspaceCache = nil
		c.workspaceCacheValid = false
		return c.listLegacyWorkspaces(ctx)
	}
	if resp.StatusCode == http.StatusNotModified {
		if !c.workspaceCacheValid {
			return nil, fmt.Errorf("GET %s returned 304 without a cached workspace set", path)
		}
		return append([]WorkspaceInfo(nil), c.workspaceCache...), nil
	}
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &requestError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}

	var workspaces []WorkspaceInfo
	if err := json.NewDecoder(resp.Body).Decode(&workspaces); err != nil {
		return nil, err
	}
	c.workspaceETag = resp.Header.Get("ETag")
	c.workspaceCache = append([]WorkspaceInfo(nil), workspaces...)
	c.workspaceCacheValid = true
	return append([]WorkspaceInfo(nil), workspaces...), nil
}

// listLegacyWorkspaces 走旧版全量 workspace 端点。
func (c *Client) listLegacyWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	var workspaces []WorkspaceInfo
	if err := c.getJSON(ctx, "/api/workspaces", &workspaces); err != nil {
		return nil, err
	}
	return workspaces, nil
}

func (c *Client) usesLegacyWorkspaceEndpoint() bool {
	c.workspaceMu.Lock()
	defer c.workspaceMu.Unlock()
	return c.legacyWorkspaceEndpointEnabled
}

// IssueGCStatus 持有 GC 检查端点返回的最小 issue 信息。
type IssueGCStatus struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IssueGCCheckResult 是 workspace 批量端点返回的单条显式 issue 结果。Found=false
// 刻意同时覆盖“issue 已删除”与“ID 不在所请求的 workspace 内”，守住服务器防枚举的
// 契约。Err 只由旧版 per-issue 兜底填充。
type IssueGCCheckResult struct {
	ID        string    `json:"id"`
	Found     bool      `json:"found"`
	Status    string    `json:"status,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Err       error     `json:"-"`
}

type issueGCBatchResponse struct {
	Issues []IssueGCCheckResult `json:"issues"`
}

// isIssueGCBatchUnsupported 区分老服务器上 chi 的“路由不匹配”响应与当前服务器对
// “调用方无权访问所请求 workspace”给出的 JSON 404。只有前者是兼容信号；在鉴权 404
// 上做回退会把一次被拒请求放大成几百次旧版探测。
func isIssueGCBatchUnsupported(err error) bool {
	var reqErr *requestError
	return errors.As(err, &reqErr) &&
		reqErr.StatusCode == http.StatusNotFound &&
		strings.TrimSpace(reqErr.Body) == "404 page not found"
}

// GetIssueGCChecks 用一个请求对账某 workspace 的 issue ID。当新 daemon 连到没有
// batch 路由的老服务器时，首个 404 会把这个客户端进程永久切到旧版 per-issue 端点。
// 其它 batch 失败直接返回而不扇出，让瞬时服务器问题不会放大请求量。
func (c *Client) GetIssueGCChecks(ctx context.Context, workspaceID string, issueIDs []string) (map[string]IssueGCCheckResult, error) {
	c.issueGCBatchMu.Lock()
	defer c.issueGCBatchMu.Unlock()

	if c.legacyIssueGCBatchEnabled {
		return c.getLegacyIssueGCChecks(ctx, issueIDs), nil
	}

	path := fmt.Sprintf("/api/daemon/workspaces/%s/issues/gc-check", workspaceID)
	var resp issueGCBatchResponse
	err := c.postJSON(ctx, path, map[string]any{"issue_ids": issueIDs}, &resp)
	if err != nil {
		if !isIssueGCBatchUnsupported(err) {
			return nil, err
		}
		c.legacyIssueGCBatchEnabled = true
		return c.getLegacyIssueGCChecks(ctx, issueIDs), nil
	}

	results := make(map[string]IssueGCCheckResult, len(resp.Issues))
	for _, result := range resp.Issues {
		results[result.ID] = result
	}
	return results, nil
}

// getLegacyIssueGCChecks 逐 issue 调旧版 gc-check 端点做对账。
func (c *Client) getLegacyIssueGCChecks(ctx context.Context, issueIDs []string) map[string]IssueGCCheckResult {
	results := make(map[string]IssueGCCheckResult, len(issueIDs))
	for _, issueID := range issueIDs {
		status, err := c.GetIssueGCCheck(ctx, issueID)
		if err != nil {
			var reqErr *requestError
			if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound {
				results[issueID] = IssueGCCheckResult{ID: issueID, Found: false}
			} else {
				results[issueID] = IssueGCCheckResult{ID: issueID, Err: err}
			}
			continue
		}
		results[issueID] = IssueGCCheckResult{
			ID:        issueID,
			Found:     true,
			Status:    status.Status,
			UpdatedAt: status.UpdatedAt,
		}
	}
	return results
}

// GetIssueGCCheck 返回某个 issue 的状态与 updated_at，供 GC 决策。
func (c *Client) GetIssueGCCheck(ctx context.Context, issueID string) (*IssueGCStatus, error) {
	var resp IssueGCStatus
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/issues/%s/gc-check", issueID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ChatSessionGCStatus 镜像 IssueGCStatus，用于 chat 会话。
type ChatSessionGCStatus struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetChatSessionGCCheck 返回某 chat 会话的状态，供 GC 决策。本端点的 404 表示会话行
// 已被硬删（用户显式移除），调用方把它当作“立即清理”信号。
func (c *Client) GetChatSessionGCCheck(ctx context.Context, sessionID string) (*ChatSessionGCStatus, error) {
	var resp ChatSessionGCStatus
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/chat-sessions/%s/gc-check", sessionID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AutopilotRunGCStatus 携带 autopilot run 的状态。CompletedAt 是 run 的终态时间戳
// （非终态 run 为零）。GC 循环一旦看到终态就回收该 run“从未复用”的 workdir，不再等
// CompletedAt；该字段仅为 API 响应契约与诊断保留。
type AutopilotRunGCStatus struct {
	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completed_at"`
}

// GetAutopilotRunGCCheck 返回某 autopilot run 的状态，供 GC 决策。
func (c *Client) GetAutopilotRunGCCheck(ctx context.Context, runID string) (*AutopilotRunGCStatus, error) {
	var resp AutopilotRunGCStatus
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/autopilot-runs/%s/gc-check", runID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TaskGCStatus 携带 quick-create 清理用的 agent_task_queue 状态。quick-create 任务
// 没有单独的父记录，因此 GC 直接以任务本身为键。
type TaskGCStatus struct {
	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completed_at"`
}

// GetTaskGCCheck 返回某 agent 任务的状态，供 GC 决策。
func (c *Client) GetTaskGCCheck(ctx context.Context, taskID string) (*TaskGCStatus, error) {
	var resp TaskGCStatus
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/gc-check", taskID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RuntimeOfflineCodeNotExecutable 标记“因 OS 拒绝执行其 agent CLI 而离线”的 runtime。
// 它是唯一“等多久也没用”的注销原因——这正是服务器需要知道的：给离线机器的活可以排
// 队等机器回来，但给这台机器的活必须带着解释被拒绝（MUL-6164）。
const RuntimeOfflineCodeNotExecutable = "not_executable"

// RuntimeOfflineReason 是 runtime 离线的原因，形式可被客户端直接行动：一个稳定的、
// 可开关并本地化的 code，加上一条修复安装的命令。说明性文字放 Detail 供日志用——
// 绝不做客户端解析的对象。
type RuntimeOfflineReason struct {
	Code   string                  `json:"code"`
	Detail string                  `json:"detail,omitempty"`
	Repair *agent.ExecFormatRepair `json:"repair,omitempty"`
}

// Deregister 把 runtime 拉下线。reasons 可选、按 runtime id 键控：关停中的 daemon
// 没什么可解释，而判了一个坏 CLI 死刑的 daemon 有。
func (c *Client) Deregister(ctx context.Context, runtimeIDs []string, reasons map[string]RuntimeOfflineReason) error {
	body := map[string]any{"runtime_ids": runtimeIDs}
	if len(reasons) > 0 {
		body["offline_reasons"] = reasons
	}
	return c.postJSON(ctx, "/api/daemon/deregister", body, nil)
}

// RegisterResponse 持有服务器对 daemon 注册的响应。
type RegisterResponse struct {
	Runtimes     []Runtime       `json:"runtimes"`
	Repos        []RepoData      `json:"repos"`
	ReposVersion string          `json:"repos_version"`
	Settings     json.RawMessage `json:"settings,omitempty"`
}

// Register 把 daemon（含 runtime 等）注册到服务器。
func (c *Client) Register(ctx context.Context, req map[string]any) (*RegisterResponse, error) {
	var resp RegisterResponse
	if err := c.postJSON(ctx, "/api/daemon/register", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// WorkspaceReposResponse 是 GET workspace repos 的响应。
type WorkspaceReposResponse struct {
	WorkspaceID  string          `json:"workspace_id"`
	Repos        []RepoData      `json:"repos"`
	ReposVersion string          `json:"repos_version"`
	Settings     json.RawMessage `json:"settings,omitempty"`
}

// GetWorkspaceRepos 拉取某 workspace 的仓库配置。
func (c *Client) GetWorkspaceRepos(ctx context.Context, workspaceID string) (*WorkspaceReposResponse, error) {
	var resp WorkspaceReposResponse
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/workspaces/%s/repos", workspaceID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// RuntimeProfile 镜像服务器的 workspace 自定义 runtime profile（MUL-3284）。
// protocol_family 是任务路由所用的 provider（它选定 agent 后端），command_name 才是
// daemon 在 PATH 上解析并拉起的实际可执行文件。fixed_args 是该 runtime 上每个 agent
// 都继承的启动参数。
type RuntimeProfile struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	DisplayName    string   `json:"display_name"`
	ProtocolFamily string   `json:"protocol_family"`
	CommandName    string   `json:"command_name"`
	Description    *string  `json:"description"`
	FixedArgs      []string `json:"fixed_args"`
	Visibility     string   `json:"visibility"`
	Enabled        bool     `json:"enabled"`
}

// RuntimeProfilesResponse 是
// GET /api/daemon/workspaces/{workspaceID}/runtime-profiles 的响应体。服务器只返回
// 该 workspace 的已启用 profile。
type RuntimeProfilesResponse struct {
	WorkspaceID     string           `json:"workspace_id"`
	RuntimeProfiles []RuntimeProfile `json:"runtime_profiles"`
}

// GetRuntimeProfiles 拉取 workspace 已启用的自定义 runtime profile。镜像
// GetWorkspaceRepos。调用方须把它当尽力而为：没有 profiles 路由的老服务器返回 404，
// daemon 吞掉并只带内置 runtime 继续。
func (c *Client) GetRuntimeProfiles(ctx context.Context, workspaceID string) (*RuntimeProfilesResponse, error) {
	var resp RuntimeProfilesResponse
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/workspaces/%s/runtime-profiles", workspaceID), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// defaultTerminalRetrySchedule 是 postJSONWithRetry 用于终态任务回调
// （CompleteTask / FailTask）的退避。N 个条目 → 最坏 N+1 次尝试（一次立即 + N 次
// 重试）。5 段合计 124s 的退避足够扛过我们见过的短上游抖动（MUL-2780），又不会在
// 故障超过窗口时让任务一直卡着。
var defaultTerminalRetrySchedule = []time.Duration{
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	64 * time.Second,
}

// skillBundleResolveRetrySchedule 扛过单次包下载的短暂传输抖动。刻意保持很短：真正
// 的预算是由 daemon 按技能设置的“随尺寸缩放的 context 截止时间”，仍失败的技能等它的
// 兄弟技能被缓存后会在下次派发重试。N 个条目 → N+1 次尝试。（GitHub #4505）
var skillBundleResolveRetrySchedule = []time.Duration{
	500 * time.Millisecond,
	2 * time.Second,
}

// retrySleep 是重试尝试之间的睡眠。抽成包级变量，测试就能换上一个即时睡眠，而不必
// 改写调用方的计划表。
var retrySleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isTransientError 判断 err 是否像“重试很可能缓解”的抖动：传输层连接 / TLS / I/O
// 错误（包括 http.Client.Do 内表现为 context.DeadlineExceeded 的客户端超时）、5xx
// 服务器响应，以及 408/429 这类限流型 4xx。其它 4xx 视为永久——重试 400（坏 body）
// 或 404（task not found）只烧时间。
//
// 调用方需自行处理父 context 取消；本谓词区分不了“daemon 正在关停”与“HTTP 客户端
// 单次尝试超时”，因为两者到这里都是经 net/http 包装的 context 错误。
func isTransientError(err error) bool {
	if err == nil || errors.Is(err, errInvalidResponseBody) {
		return false
	}
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		if reqErr.StatusCode >= 500 {
			return true
		}
		if reqErr.StatusCode == http.StatusRequestTimeout || reqErr.StatusCode == http.StatusTooManyRequests {
			return true
		}
		return false
	}
	return true
}

// postJSONWithRetry 以有界的指数退避 POST JSON body，供“必须到达服务器”的终态回调
// （CompleteTask / FailTask）使用。它按 isTransientError 重试瞬时错误，并在永久 4xx
// 上立即停止，免得在服务器已经拒绝的请求上烧掉退避。
//
// schedule 控制尝试之间的睡眠。N 个条目时最坏执行 N+1 次尝试（一次初始 + N 次重试）。
// 返回的是服务器最后一次响应带来的错误，调用方仍可用 isTransientError 检查它、决定
// 是否回退到另一个终态调用（例如只在永久错误上 complete → fail）。
//
// 服务端 CompleteTask / FailTask 把“已经是终态”当幂等成功处理（见 service/task.go），
// 因此即使服务器先前的响应在传输中丢失，重试造成的重复重放也安全。
func (c *Client) postJSONWithRetry(ctx context.Context, path string, reqBody any, respBody any, schedule []time.Duration) error {
	return c.postJSONViaWithRetry(ctx, c.client, path, reqBody, respBody, schedule, nil)
}

// postJSONViaWithRetry 是 postJSONWithRetry 跑在显式 http.Client 上的版本，让大体量
// 端点能用 bundleClient（截止时间来自 ctx），而控制面保持其固定 30s 客户端。
func (c *Client) postJSONViaWithRetry(ctx context.Context, httpClient *http.Client, path string, reqBody any, respBody any, schedule []time.Duration, stats *TransferStats) error {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return lastErr
			}
			return err
		}
		err := c.postJSONViaObserved(ctx, httpClient, path, reqBody, respBody, stats)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransientError(err) {
			return err
		}
		if attempt >= len(schedule) {
			return err
		}
		if sleepErr := retrySleep(ctx, schedule[attempt]); sleepErr != nil {
			return err
		}
	}
}

// postJSON 用控制面客户端 POST JSON body。
func (c *Client) postJSON(ctx context.Context, path string, reqBody any, respBody any) error {
	return c.postJSONVia(ctx, c.client, path, reqBody, respBody)
}

// responseDecoder customizes successful-response decoding inside each attempt.
type responseDecoder func(io.Reader) error

// postJSONVia is postJSON over an explicit http.Client. Callers pick the client
// to control the timeout regime: c.client (fixed 30s) for control-plane calls,
// c.bundleClient (deadline from ctx) for large skill-bundle downloads.
// respBody can be a JSON destination or a responseDecoder that decodes a
// successful response inside each attempt, before retry decisions are made.
func (c *Client) postJSONVia(ctx context.Context, httpClient *http.Client, path string, reqBody any, respBody any) error {
	return c.postJSONViaObserved(ctx, httpClient, path, reqBody, respBody, nil)
}

// postJSONViaObserved 是额外把本次尝试搬进 stats 的 postJSONVia（nil 可跳过）。调用
// 方用它区分“链接从未产生响应”与“响应体到得太慢没读完”——见 TransferStats。
func (c *Client) postJSONViaObserved(ctx context.Context, httpClient *http.Client, path string, reqBody any, respBody any, stats *TransferStats) error {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.setIdentityHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	stats.observeResponseStarted()
	respReader := stats.wrap(resp.Body)
	if respBody == nil {
		io.Copy(io.Discard, respReader)
		return nil
	}
	if decode, ok := respBody.(responseDecoder); ok {
		return decode(respReader)
	}
	return json.NewDecoder(respReader).Decode(respBody)
}

// getJSON 用当前 token 做一次 GET。
func (c *Client) getJSON(ctx context.Context, path string, respBody any) error {
	return c.getJSONWithToken(ctx, path, c.token, respBody)
}

// getJSONWithToken 用显式凭据做一次 GET。Remote MCP broker 用它，让它的短命 daemon
// token 不会替换掉或竞态并发控制面请求所用的客户端长命 PAT。
func (c *Client) getJSONWithToken(ctx context.Context, path, token string, respBody any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c.setIdentityHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if respBody == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(respBody)
}

// postJSONWithToken 是 getJSONWithToken 的写对应物，用于 daemon 携带 body 的任务级
// 调用。
func (c *Client) postJSONWithToken(ctx context.Context, path, token string, reqBody, respBody any) error {
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c.setIdentityHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &requestError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if respBody == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(respBody)
}

// InvokeAgentPluginHook 请服务器做一次 agent 触发的钩子调用。
//
// daemon 刻意不自己调插件：签名密钥从部署密钥派生、只留在服务器上，经它绕行还能让
// 限流、熔断、`net:` 目标检查与调用记录对每种触发方式都走同一份代码。
func (c *Client) InvokeAgentPluginHook(ctx context.Context, daemonToken, taskID, installationID, hookKey string, input json.RawMessage) (json.RawMessage, error) {
	var response struct {
		Status string          `json:"status"`
		Output json.RawMessage `json:"output,omitempty"`
		Error  string          `json:"error,omitempty"`
	}
	path := fmt.Sprintf("/api/daemon/tasks/%s/plugin-hooks", url.PathEscape(taskID))
	body := map[string]any{"installation_id": installationID, "hook_key": hookKey}
	if len(input) > 0 {
		body["input"] = input
	}
	if err := c.postJSONWithToken(ctx, path, daemonToken, body, &response); err != nil {
		return nil, err
	}
	// 被拒或失败的钩子以“200 + status”返回，让调用方把它渲染成 TOOL 错误而不是传输
	// 故障——插件端点不可达绝不能把 agent 的任务搞挂。
	if response.Status != "ok" {
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return nil, errors.New("the plugin hook did not succeed")
	}
	return response.Output, nil
}

// AckReleasePause 重试保存执行上下文，发布器据此确认任务已停止。
func (c *Client) AckReleasePause(ctx context.Context, taskID string, result TaskResult) error {
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/release-pause-ack", taskID), map[string]any{"session_id": result.SessionID, "work_dir": result.WorkDir, "root_dir": result.EnvRoot}, nil, defaultTerminalRetrySchedule)
}

// AckReleasePauseFailure 在无法保存暂停检查点时回传原因，让发布窗口能把该任务判为
// skipped 继续收尾，而不是永远等一个不会到来的停止确认。
func (c *Client) AckReleasePauseFailure(ctx context.Context, taskID, reason string) error {
	return c.postJSONWithRetry(ctx, fmt.Sprintf("/api/daemon/tasks/%s/release-pause-ack", taskID), map[string]any{"error": reason}, nil, defaultTerminalRetrySchedule)
}
