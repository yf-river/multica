package agent

// 角色定位：本文件是 Codex CLI 的适配层，实现了 Backend 接口：拉起 `codex
// app-server` 子进程，通过 stdio 或独立 Unix WebSocket 上的 JSON-RPC 2.0 与之对话，
// 把 codex 的 thread/turn 协议翻译成本包统一的 Message/Result/Session。

// 职责边界：管「进程生命周期 + JSON-RPC 客户端」这一整条链——参数归一化、MCP 配置
// 落盘、启动握手、线程建立/resume、回合守护（语义静默/首轮无进展/握手三类超时）、
// 产物与用量收集、进程树清理。不解析 AGENTS.md，不做任务持久化（在 daemon）。

// 内部组织：先是常量/标记（含 MUL-5542 起的快速失败 watchdog 族），再是启动参数与
// config.toml 里的受管 MCP block 渲染/剥离（密钥不落 argv），随后 executeOnce 的进程
// 编排与 drainAndWait 两阶段清理，再往下是 codexClient 的 JSON-RPC 传输层、事件归一化
// （patch/MCP/权限自动批准），最后是会话 JSONL 用量扫描。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// codexBlockedArgs 是 daemon 硬编码、绝不允许被用户 custom_args 覆盖的启动参数。
// mcp_servers 相关的配置键写在每个任务自己的 `$CODEX_HOME/config.toml` 里（由
// ensureCodexMcpConfig 写入）；用户再传 `-c mcp_servers.…` 覆盖会被
// filterCodexCustomConfigOverrides 单独剔除——因为 `-c` 还要承载合法的非 MCP 覆盖
// （如 `-c model=…`），不能一刀切禁掉整个 `-c`。
var codexBlockedArgs = map[string]blockedArgMode{
	"--listen": blockedWithValue, // 通信走 stdio:// 传输，禁止改监听方式
}

const (
	codexFastServiceTier     = "priority"
	codexStandardServiceTier = "default"
	codexFastModeFeature     = "fast_mode"
)

// codexStderrTailBytes 限制收录进错误信息里的 stderr 尾部字节数——用于 codex 在
// JSON-RPC 握手之前就退出的场景（例如用户在 custom_args 里传了 `app-server` 子命令
// 不接受的参数）。单独成常量，方便日后 codex 输出更长失败堆栈时独立于其它 agent 调整。
const (
	codexStderrTailBytes                  = 2048
	defaultCodexSemanticInactivityTimeout = 10 * time.Minute
	// defaultCodexFirstTurnNoProgressTimeout 限制第一轮在 app-server 上报 turn/started
	// 之后最多可以保持完全静默多久。它的唯一作用是快速失败，而不是熬到
	// defaultCodexSemanticInactivityTimeout（10 分钟兜底）才收场，所以只需明显小于后者。
	//
	// 为什么取 60s 而不是 30s：两起独立的线上反馈测到 gpt-5.5 的首次进度事件刚好落在
	// 30s 之后，WSL 上的 app-server 则约 39s（GH #5959）——两者都是被误杀的健康轮次。
	// 60s 在这批证据之上留足余量，同时保住快速失败的价值（MUL-5542）。
	defaultCodexFirstTurnNoProgressTimeout = 60 * time.Second
	defaultCodexHandshakeTimeout           = 30 * time.Second
	// 取消后保留两秒接收最终用量；超时终止整棵进程树。
	defaultCodexTurnInterruptTimeout = 2 * time.Second
	// thread/start and thread/resume may refresh the model catalog, initialize
	// MCP integrations, and restore persisted history. Field evidence shows
	// healthy calls crossing the 30s budget used for local initialize, so keep
	// their default separate without slowing failures for lightweight RPCs.
	defaultCodexThreadHandshakeTimeout = 60 * time.Second
	codexVersionDiagnosticTimeout      = 2 * time.Second
	// Keep a raw agent-message tail small enough that a slow daemon does not
	// make the stdout reader retain token-sized channel entries indefinitely.
	// At one sixty-fourth of the scanner's maximum completed-snapshot line, any
	// reconcilable item occupies at most 65 of the 256 message slots (the leading
	// delta plus 64 aggregates), reserving the rest for status and tool events.
	// A single provider event may be larger, in which case that event is flushed
	// as one chunk rather than split at an arbitrary byte boundary.
	codexAgentMessageAggregateBytes = agentStreamMaxLineBytes / 64
	// codexGracefulShutdownTimeout bounds how long the lifecycle goroutine
	// waits for codex to exit on its own after stdin is closed, before forcing
	// a context-cancel kill. A clean exit lets codex run its shutdown path and
	// flush buffered telemetry — OTEL batch exporters only force-flush on
	// graceful shutdown, so killing it immediately (the prior behavior) drops
	// the task's spans/metrics/logs.
	codexGracefulShutdownTimeout = 10 * time.Second
)

// codexGracefulShutdownTimeoutNanos 供测试按纳秒覆盖 codexGracefulShutdownTimeout。
// 零或负值保持生产默认。scanner 溢出清理路径的回归测试（#4520）用它把宽限期从 10s
// 缩到几百毫秒，让回归在常规 `go test` 预算内跑完，而不是每个清理阶段烧掉两个完整
// 宽限期。与 opencodeTerminateGraceNanos 钩子同构。
var codexGracefulShutdownTimeoutNanos atomic.Int64
var codexProcessWaitDelayNanos atomic.Int64
var activeCodexLaunches atomic.Int64
var maxActiveCodexLaunchesObserved atomic.Int64
var codexCleanupConfirmationOverride atomic.Int32

func sanitizeCodexDiagnostic(value string) string {
	return sanitizeAgentDiagnostic(value)
}

func codexProcessExitStatus(state *os.ProcessState) any {
	if state == nil {
		return nil
	}
	return state.String()
}

func codexGracefulShutdown() time.Duration {
	if n := codexGracefulShutdownTimeoutNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return codexGracefulShutdownTimeout
}

func codexProcessWaitDelay() time.Duration {
	if n := codexProcessWaitDelayNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return 10 * time.Second
}

type codexStderrClassification struct {
	// modelRefreshFailure 是「模型目录刷新失败」的宽口径计数，涵盖下面更窄的
	// modelRefreshTimeout，所以两者不可相加。
	modelRefreshFailure int
	modelRefreshTimeout int
	// mcpInitTransport：stderr 某行同时出现 mcp/transport 与 error/failed/closed。
	mcpInitTransport int
	// bareTimeout：确实超时，但没匹配到任何已知故障特征。
	bareTimeout int
}

// classifyCodexStartupStderr 只输出受边界限制的计数器，刻意不做「是否重试」的决策：
// stderr 只是旁证，不能证明 thread/start 到底有没有在服务端建出线程。
func classifyCodexStartupStderr(stderr string, timedOut bool) codexStderrClassification {
	lower := strings.ToLower(sanitizeCodexDiagnostic(stderr))
	classification := codexStderrClassification{
		modelRefreshFailure: strings.Count(lower, codexModelCatalogRefreshFailureSignal),
		modelRefreshTimeout: strings.Count(lower, codexModelCatalogRefreshTimeoutSignal),
	}
	for _, line := range strings.Split(lower, "\n") {
		if strings.Contains(line, "mcp") && strings.Contains(line, "transport") &&
			(strings.Contains(line, "error") || strings.Contains(line, "failed") || strings.Contains(line, "closed")) {
			classification.mcpInitTransport++
		}
	}
	// 只要命中了模型目录刷新失败，超时分类就归它，而不是只归更窄的子进程超时信号。
	// 这是有意为之：让旧版就有的 bare-timeout 桶在各 daemon 版本间口径更严格。
	if timedOut && classification.modelRefreshFailure == 0 && classification.mcpInitTransport == 0 {
		classification.bareTimeout = 1
	}
	return classification
}

// CodexSemanticInactivityMarker 是「Codex 进程还活着但已无语义进展」类超时错误的前缀。
const CodexSemanticInactivityMarker = "codex semantic inactivity timeout"

// CodexFirstTurnNoProgressMarker 标识 app-server 的一种失败形态：Codex 接受了回合后
// 既不产出任何 item/完成事件，也不报错。
const CodexFirstTurnNoProgressMarker = "codex app-server no progress timeout"

// CodexHandshakeTimeoutMarker 标识 Codex app-server 启动类 RPC 在限定的握手窗口内没有应答。
const CodexHandshakeTimeoutMarker = "codex app-server handshake timeout"

// CodexGuardianRateLimitMarker identifies an in-turn approval retry loop that
// the adapter stopped after repeated reviewer failures.
const CodexGuardianRateLimitMarker = "codex Guardian approval rate-limit circuit open"

const codexGuardianRateLimitThreshold = 2
const codexGuardianReviewDedupWindow = 32
const codexGuardianRateLimitError = CodexGuardianRateLimitMarker + ": two consecutive Guardian approval reviews failed with 429 Too Many Requests; check completed work before continuing after the provider limit clears"

type codexGuardianRateLimitCircuit struct {
	consecutive      int
	recentReviewIDs  [codexGuardianReviewDedupWindow]string
	nextReviewIDSlot int
	tripped          bool
}

// observeReview consumes the Guardian-specific app-server notification. A
// synthesized commandExecution item may have no output when approval fails,
// so inspecting tool output would miss the failure we need to stop.
func (c *codexGuardianRateLimitCircuit) observeReview(params map[string]any) bool {
	reviewID, _ := params["reviewId"].(string)
	review, _ := params["review"].(map[string]any)
	if reviewID == "" || review == nil {
		return false
	}
	status, _ := review["status"].(string)
	if status == "" || status == "inProgress" {
		return false
	}
	for _, recentID := range c.recentReviewIDs {
		if reviewID == recentID {
			return false
		}
	}
	c.recentReviewIDs[c.nextReviewIDSlot] = reviewID
	c.nextReviewIDSlot = (c.nextReviewIDSlot + 1) % len(c.recentReviewIDs)
	rationale, _ := review["rationale"].(string)
	lower := strings.ToLower(rationale)
	// A policy denial carries a risk level; a reviewer failure does not. Keep
	// the 429 signal as a narrow gate, without depending on Codex's English
	// failure prefix (the review notification schema is unstable).
	if status != "denied" || review["riskLevel"] != nil ||
		(!strings.Contains(lower, "429") && !strings.Contains(lower, "too many requests")) {
		c.consecutive = 0
		return false
	}
	c.consecutive++
	if c.consecutive < codexGuardianRateLimitThreshold || c.tripped {
		return false
	}
	c.tripped = true
	return true
}

// codexResumeMarker and codexLineOverflowMarker are the two halves of the
// error text a resume-overflow produces: the method that failed (written by
// startOrResumeThread) and bufio's own ErrTooLong wording, which reaches the
// string through the reader goroutine.
const (
	codexResumeMarker       = "thread/resume failed"
	codexLineOverflowMarker = "token too long"
)

// CodexResumeOverflowError 判断某个 agent 错误串是不是「resume 溢出」失败——即
// thread/resume 的响应大到塞不进我们的 stdout 行缓冲，导致该会话根本交接不过来。
//
// 它紧挨着写入这两半错误文本的代码，避免两处漂移；之所以导出，是因为上报时 daemon
// 手里只剩错误字符串：此刻进程内的类型化 bufio.ErrTooLong（isCodexResumeOverflow
// 匹配的就是它）早已不在了。两个标记都必须出现——单凭 "thread/resume failed" 会误伤
// 普通拒绝（普通重试即可解决）；单凭 "token too long" 又会误伤其它 RPC 的溢出
// （那种情况丢掉会话指针毫无意义）。
//
// 这类失败背后的会话在变小之前无法用于 resume——而 codex 的会话是只追加的，永远等不到
// 变小。调用方用这个判断来避免把同一个超大线程继续派给下一个任务（MUL-5722）。
func CodexResumeOverflowError(errText string) bool {
	if errText == "" {
		return false
	}
	lower := strings.ToLower(errText)
	return strings.Contains(lower, codexResumeMarker) && strings.Contains(lower, codexLineOverflowMarker)
}

// codexRetiredCompactPath is the request path only the legacy remote-compaction
// route ever calls. The v2 route compacts on the ordinary /responses stream
// instead, so this segment appears only when the legacy route was selected.
const codexRetiredCompactPath = "responses/compact"

// codexRemoteCompactMarker opens the error Codex writes when a remote
// compaction task fails. Both routes share it — compact_remote_v2.rs labels its
// own failures with the same phrase — so it establishes only that compaction is
// what failed, never which route ran.
const codexRemoteCompactMarker = "remote compact task"

// CodexRetiredCompactionError reports whether an agent error is Codex failing
// to compact a conversation over the route OpenAI has retired, e.g.
//
//	Error running remote compact task: unexpected status 404 Not Found: {"detail":"Not Found"},
//	url: https://chatgpt.com/backend-api/codex/responses/compact, cf-ray: ..., request id: ...
//
// The route is chosen by turning `remote_compaction_v2` off. Older Codex builds
// ignored that setting, so a value added as a workaround for an unrelated,
// since-fixed defect can start failing on upgrade without the user touching
// anything (GH #8000, upstream openai/codex#42468). Nothing in the text names
// the setting, so the failure is unreadable on its own — which is the whole
// reason this predicate exists: callers use it to attach the remedy.
//
// The retired PATH is what has to be present. The compaction marker alone is
// not evidence of anything: the v2 route wraps its own failures in the same
// "Error running remote compact task" prefix, and a v2 stream can return 404
// too — so keying on the marker plus a status code would tell a user whose
// setting is already correct to go turn it off, the exact opposite of the fix.
// The path cannot be produced that way; a compaction request reaching
// /responses/compact at all means the legacy route was selected, whatever
// status came back.
//
// That leaves a deliberate false negative: a Codex build that reports this
// failure without the url tail gets no hint. There is no second signal in the
// text that identifies the route, and the two errors are otherwise
// indistinguishable, so the choice is between missing some cases and
// misdirecting the users whose config was never the problem. A missed hint
// costs what today already costs; a wrong hint spends someone's afternoon.
//
// Deliberately NOT a resume-safety predicate. The stuck thread is recoverable:
// once the setting is right compaction succeeds and the same conversation
// continues, so retiring the session here would throw away exactly the context
// the fix restores. Text only.
//
// Delete once no Codex old enough to select the retired route is still in use.
// Upstream intends to mark the flag Stage::Removed or fall back on the 404,
// and under either fix this predicate simply stops matching: it overrides no
// config and stands between nobody and a setting they may legitimately want
// off, so it costs one substring check on an already-failing path until then
// and comes out in a single commit. That is the difference from forcing
// `--enable remote_compaction_v2`, which was declined for the opposite reason
// (GH #8019) — an override outlives the defect and blocks the legitimate case.
func CodexRetiredCompactionError(errText string) bool {
	if errText == "" {
		return false
	}
	lower := strings.ToLower(errText)
	return strings.Contains(lower, codexRemoteCompactMarker) &&
		strings.Contains(lower, codexRetiredCompactPath)
}

// codexModelCatalogRefreshFailureSignal matches the Codex models-manager error
// emitted when the model catalog could not be refreshed. Codex reports several
// distinct causes under this prefix ("timeout waiting for child process to
// exit", "stream disconnected before completion", ...), so match the shared
// prefix rather than one variant: they are all the same startup-blocking
// failure from the daemon's point of view.
const codexModelCatalogRefreshFailureSignal = "failed to refresh available models"
const codexModelCatalogRefreshTimeoutSignal = "failed to refresh available models: timeout waiting for child process to exit"

var errCodexProcessExited = errors.New("codex process exited")

type codexTimeoutKind int

const (
	codexTimeoutNone codexTimeoutKind = iota
	codexTimeoutSemanticInactivity
	codexTimeoutFirstTurnNoProgress
)

type codexTimeoutDiagnostic struct {
	Kind         codexTimeoutKind // 超时种类：语义静默还是首轮无进展
	Timeout      time.Duration    // 本次实际使用的超时时长
	LastActivity string           // 最后一次语义活动的描述
	ThreadID     string
	TurnID       string
	Model        string
	CodexVersion string
}

// codexFirstItemWaitObservation 记录从 turn/started 到第一个语义进展或最终结局的耗时。
// start 由 stdout 读取方在发布 status:running 前调用，finish 由生命周期 goroutine 调用；
// 互斥锁保证跨 goroutine 的时间戳精确，同时不改动 watchdog 现有的基于 channel 的语义。
type codexFirstItemWaitObservation struct {
	mu         sync.Mutex
	startedAt  time.Time
	finishedAt time.Time
	turnID     string
	outcome    string
	stderr     codexStderrClassification
}

func (o *codexFirstItemWaitObservation) start(now time.Time, turnID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.startedAt.IsZero() {
		o.startedAt = now
		o.turnID = turnID
	}
}

func (o *codexFirstItemWaitObservation) finish(now time.Time, outcome string, stderr codexStderrClassification) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.startedAt.IsZero() || o.outcome != "" {
		return // 尚未 start 或已收过结局，忽略重复的 finish
	}
	o.finishedAt = now
	o.outcome = outcome
	o.stderr = stderr
}

func (o *codexFirstItemWaitObservation) snapshot() (time.Duration, string, string, codexStderrClassification, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.startedAt.IsZero() || o.finishedAt.IsZero() || o.outcome == "" {
		return 0, "", "", codexStderrClassification{}, false
	}
	return o.finishedAt.Sub(o.startedAt), o.turnID, o.outcome, o.stderr, true
}

// codexBackend 实现 Backend 接口：拉起 `codex app-server --listen stdio://` 子进程，
// 通过 stdin/stdout 上的 JSON-RPC 2.0 与它通信。
type codexBackend struct {
	cfg Config
}

func buildCodexArgs(opts ExecOptions, logger *slog.Logger) []string {
	args := []string{"app-server", "--listen", "stdio://"}
	launchArgs := NormalizeCodexLaunchArgs(opts.ExtraArgs, opts.CustomArgs, opts.McpConfig, logger)
	if opts.MCPToolsOnly {
		launchArgs = filterCodexSupervisionArgs(launchArgs, logger)
	}
	if opts.ServiceTier == codexFastServiceTier {
		launchArgs = enforceCodexFastMode(launchArgs, logger)
	}
	if opts.MCPToolsOnly {
		launchArgs = append(launchArgs, codexSupervisionArgs("")...)
	}
	return append(args, launchArgs...)
}

// NormalizeCodexLaunchArgs 返回用户提供的 codex 参数（extra 在前、custom 在后）经过
// 低优先级归一化后的结果：去掉 shell 引号、剔除协议关键参数；并且当受管 mcp_config
// 占据 mcp_servers 命名空间时，丢弃零散的 `-c mcp_servers.*` 覆盖。buildCodexArgs 会
// 在前面拼上固定的 `app-server --listen stdio://` 传输参数，并可能在结果末尾追加一条
// agent 持有的运行时覆盖（目前是 `--enable fast_mode`）。
//
// 导出它是为了让 daemon 在决定 Windows 沙箱模式时能重建 *实际生效* 的启动参数。
// `-c windows.sandbox=…` 这类显式开关可能带 shell 引号出现（用户通常用 shell 语法写
// custom_args，例如 `'-c' 'windows.sandbox=unelevated'`），只有经过同一套归一化后才能
// 变成沙箱探测器要找的 `-c windows.sandbox=…` 形态。用其它方式重建两者会漂移，静默
// 降级掉用户对隔离的显式选择（MUL-4957）。
func NormalizeCodexLaunchArgs(extraArgs, customArgs []string, mcpConfig json.RawMessage, logger *slog.Logger) []string {
	extra := filterCustomArgs(extraArgs, codexBlockedArgs, logger)
	custom := filterCustomArgs(customArgs, codexBlockedArgs, logger)
	// 只有当 agent 在 MCP Tab 里确实有受管 mcp_config 时才接管 `mcp_servers` 命名空间。
	// 否则那些习惯用 `custom_args: ["-c", "mcp_servers.…"]` 配置 MCP 的老用户会静默丢失
	// 这些条目。有了受管 mcp_config 时，daemon 写入的 `$CODEX_HOME/config.toml` 才是唯一
	// 权威来源，零散的 `-c mcp_servers.*` 覆盖要被丢弃，以免 last-wins 又把它盖回去。
	if hasManagedCodexMcpConfig(mcpConfig) {
		extra = filterCodexCustomConfigOverrides(extra, logger)
		custom = filterCodexCustomConfigOverrides(custom, logger)
	}
	out := make([]string, 0, len(extra)+len(custom))
	out = append(out, extra...)
	out = append(out, custom...)
	return out
}

// enforceCodexFastMode 让 agent 显式选择的 service tier 压过 daemon ExtraArgs、各 agent
// 的 CustomArgs 以及继承来的 config.toml。codex 的 `--disable fast_mode` 无论 argv 顺序
// 如何都压过 `--enable fast_mode`，所以要在受管的 enable 追加之前，先去掉这些低优先级的
// 冲突 disable 参数。同理，冲突的 `-c` / `--config features.fast_mode=...` 覆盖也要移除
// （即便当前 codex 版本让 `--enable` 能赢）：最终 argv 对这个开关只能有一个明确的所有者。
//
// 其余被禁用的特性不受影响；该 helper 只用于目录里登记的 `priority` 档位。未来新的
// service tier 不得无意继承 Fast mode 语义。
func enforceCodexFastMode(args []string, logger *slog.Logger) []string {
	return append(stripCodexFastModeConflicts(args, logger), "--enable", codexFastModeFeature)
}

// stripCodexFastModeConflicts 只移除会破坏显式 priority 档位的低优先级覆盖，不追加受管
// enable。从 enforceCodexFastMode 里拆出来，是因为 enable 只需精确出现一次（在最终受管
// 参数上），而移除必须覆盖用户能写入的每一段 argv——包括自定义 runtime profile 的启动
// 前缀。前缀优先不保护这个开关：`--disable fast_mode` 无论 argv 顺序都压过 `--enable
// fast_mode`，而任意位置的 `-c features.fast_mode=false` 都压过 config.toml。若不滤前缀，
// profile 就能覆盖 agent 显式选择的档位，倒置本包承诺的优先级（GH #7046）。
func stripCodexFastModeConflicts(args []string, logger *slog.Logger) []string {
	args = filterCodexConfigOverrides(
		args,
		codexManagedFastModeConfigKeyRe,
		"features.fast_mode",
		logger,
	)
	filtered := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag := arg
		value := ""
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			value = arg[idx+1:]
			hasInlineValue = true
		}
		if flag == "--disable" {
			if !hasInlineValue && i+1 < len(args) {
				value = args[i+1]
			}
			if value == codexFastModeFeature {
				if logger != nil {
					logger.Warn("codex: ignored lower-priority feature disable",
						"feature", codexFastModeFeature)
				}
				if !hasInlineValue {
					i++
				}
				continue
			}
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

// hasManagedCodexMcpConfig 报告 agent 的 mcp_config 字段在 API 三态语义下是否「存在」：
// 非 null 的 JSON 值即为存在。`{}` 与 `{"mcpServers":{}}` 都算存在（管理员存了一组空
// 受管集合——严格模式、无全局回退）；只有 SQL NULL 或字面 JSON `null` 才算不存在
// （CLI 默认）。
func hasManagedCodexMcpConfig(raw json.RawMessage) bool {
	return hasManagedMcpConfig(raw)
}

// codexManagedMcpConfigKeyRe 匹配 daemon 受管的配置命名空间（`mcp_servers.…`）——当它
// 作为 codex `-c` / `--config` 参数的值出现时。filterCodexCustomConfigOverrides 用它丢
// 掉会反噬 MCP Tab 写进 `$CODEX_HOME/config.toml` 内容的用户覆盖。
var codexManagedMcpConfigKeyRe = regexp.MustCompile(`^\s*mcp_servers(?:\s*\.|\s*=|\s*$)`)

var codexManagedFastModeConfigKeyRe = regexp.MustCompile(
	`^\s*features\s*\.\s*fast_mode\s*(?:=|$)`)

// daemon 受管的 shell_environment_policy 同样必须压过 profile 与 custom_args 里的覆盖。
// 这里既匹配根级 policy 键也匹配 profile 级 policy 键，但不会误伤恰好同名同尾的其它
// 无关表字段。
const (
	codexShellEnvPolicyKeyPattern = `(?:shell_environment_policy|"shell_environment_policy"|'shell_environment_policy')`
	codexProfileNameKeyPattern    = `(?:[A-Za-z0-9_-]+|"[^"]+"|'[^']+')`
)

var codexManagedShellEnvConfigKeyRe = regexp.MustCompile(
	`^\s*(?:` + codexShellEnvPolicyKeyPattern + `|profiles\s*\.\s*` + codexProfileNameKeyPattern + `\s*\.\s*` + codexShellEnvPolicyKeyPattern + `)\s*(?:\.|=|$)`)

// filterCodexCustomConfigOverrides 从 custom args 里丢弃 `-c mcp_servers.…=` 与
// `--config mcp_servers.…=` 条目。codex 的 `-c` 是 last-wins（已对 codex-cli 0.132.0
// 验证）：没有这个过滤器的话，用户写在 custom_args 里的 `-c mcp_servers.fetch=…` 会静默
// 盖掉 MCP Tab 存进任务 config.toml 的内容。我们通过受管 block 拥有 `mcp_servers`
// 命名空间，所以用户往里面写的尝试会带警告丢弃，而不是放行让它赢。
// 其它 `-c`/`--config` 键（如 `-c model="o3"`）原样放行。
func filterCodexCustomConfigOverrides(args []string, logger *slog.Logger) []string {
	return filterCodexConfigOverrides(args, codexManagedMcpConfigKeyRe, "mcp_servers", logger)
}

func filterCodexShellEnvConfigOverrides(args []string, logger *slog.Logger) []string {
	return filterCodexConfigOverrides(args, codexManagedShellEnvConfigKeyRe, shellEnvironmentPolicyConfigNamespace, logger)
}

const shellEnvironmentPolicyConfigNamespace = "shell_environment_policy"

func filterCodexConfigOverrides(args []string, managedKeyRe *regexp.Regexp, namespace string, logger *slog.Logger) []string {
	if len(args) == 0 {
		return args
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag := arg
		inlineValue := ""
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			inlineValue = arg[idx+1:]
			hasInlineValue = true
		}
		if flag == "-c" || flag == "--config" {
			value := inlineValue
			if !hasInlineValue && i+1 < len(args) {
				value = args[i+1]
			}
			if managedKeyRe.MatchString(value) {
				if logger != nil {
					// 只记键名绝不记值：受管配置的值可能含密钥，必须远离日志与 argv。
					key := value
					if eqIdx := strings.Index(value, "="); eqIdx >= 0 {
						key = value[:eqIdx]
					}
					logger.Warn("custom_args: blocked managed Codex config override",
						"namespace", namespace, "flag", flag, "key", strings.TrimSpace(key))
				}
				if !hasInlineValue && i+1 < len(args) {
					i++ // 跳过作为独立参数的值，避免它残留下来
				}
				continue
			}
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

// 下面两个标记用于在 `$CODEX_HOME/config.toml` 里圈定 daemon 受管的
// `[mcp_servers.*]` block。沿用现有 sandbox / multi-agent / memory 标记的写法，
// 让运维能统一 grep 所有受管 block。
const (
	multicaCodexMcpBeginMarker = "# BEGIN multica-managed mcp_servers (do not edit; regenerated by daemon)"
	multicaCodexMcpEndMarker   = "# END multica-managed mcp_servers"
)

var codexMcpBlockRe = regexp.MustCompile(
	`(?ms)^` + regexp.QuoteMeta(multicaCodexMcpBeginMarker) +
		`.*?^` + regexp.QuoteMeta(multicaCodexMcpEndMarker) + `\n*`)

// userCodexMcpServersTableHeaderRe 匹配行首的 `[mcp_servers.<name>]`（含带引号的
// `[mcp_servers."<name>"]` 形式）。用于在 agent 自带 mcp_config 时，把用户配置里的
// mcp_servers 表从任务配置中剥掉——对照 Claude 的 `--strict-mcp-config` 模式，
// 即 daemon 的那一份才是权威。
var userCodexMcpServersTableHeaderRe = regexp.MustCompile(
	`^\s*\[\s*mcp_servers\s*\.\s*(?:"[^"]*"|[^\]\s]+)\s*\]\s*(?:#.*)?$`)

// ensureCodexMcpConfig 把 daemon 受管的 `[mcp_servers.*]` block 写进（或清除）
// `$CODEX_HOME/config.toml`。该 block 是本次运行 MCP 服务器的唯一权威来源：当 agent UI
// 里设置了 mcp_config，daemon 还会剥离任务配置中继承来的 `[mcp_servers.*]` 表，避免用户
// 全局 `~/.codex/config.toml` 里的表遮蔽或撞上受管集合。
//
// 文件权限用 0o600：`mcp_servers.<id>.env` 值可能携带密钥（API key、bearer token）；
// 每个任务的家目录归 daemon 用户所有，0o600 在允许 codex 子进程读取的同时，把密钥排除
// 在任何世界可读的副本之外。
//
// mcp_config 格式非法时按错误返回，由调用方决定上报还是告警——与旧 argv 路径的
// fail-soft 契约保持一致。
func ensureCodexMcpConfig(configPath string, mcpConfig json.RawMessage, logger *slog.Logger) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config.toml: %w", err)
	}
	existing := string(data)

	// 每次都先剥掉旧的受管 block，让重跑与清除流程收敛到干净状态。
	stripped := codexMcpBlockRe.ReplaceAllString(existing, "")

	managed := hasManagedCodexMcpConfig(mcpConfig)
	block, _, renderErr := renderCodexMcpServersBlock(mcpConfig)
	if renderErr != nil {
		return renderErr
	}

	var updated string
	if managed {
		// agent 有受管 MCP 集合（可以为空——API 三态语义里 `{}` /
		// `{"mcpServers":{}}` 算「存了一组空集合」，区别于 nil/null 的
		// 「回退 CLI 默认值」）。把从 `~/.codex/config.toml` 继承来的用户
		// `[mcp_servers.*]` 表也剥掉，让受管集合成为唯一严格来源——对照 Claude 的
		// `--strict-mcp-config`。两者不能混用有两个原因：
		//   1. TOML 不允许重复定义同一张表；若 agent 也定义了 fetch，用户表
		//      名 `[mcp_servers.fetch]` 会让 codex 直接崩溃。
		//   2. 管理员在 MCP Tab 存了一份显式清单时，不该看到用户全局的服务器
		//      悄悄混进来——这与 UI 呈现的语义相矛盾。
		stripped = stripCodexUserMcpServerTables(stripped)
		stripped = strings.TrimRight(stripped, "\n")
		// 受管集合为空时仍然写标记 block（表之间没有内容）。这样「受管但为空」
		// 能落盘固定下来，下次运行能找到并剥掉标记，文件意图也可被运维 grep。
		if block == "" {
			block = multicaCodexMcpBeginMarker + "\n" + multicaCodexMcpEndMarker + "\n"
		}
		if stripped == "" {
			updated = block
		} else {
			updated = stripped + "\n\n" + block
		}
	} else {
		// 没有受管配置：只移除可能残留的旧受管 block，继承来的用户表保持原样
		// （回退 CLI 默认）。
		updated = stripped
	}

	if updated == existing {
		return nil
	}
	if err := os.WriteFile(configPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	// os.WriteFile 只在新建文件时应用权限位；如果任务 config.toml 已以 0o644 在磁盘上
	// （execenv.copyFile 从 ~/.codex/config.toml 播种时的默认权限），刚才写入的含密钥
	// 内容会继承这个过宽权限。无条件 Chmod 一次，让密钥始终只落在 daemon 所有者的车道里。
	if err := os.Chmod(configPath, 0o600); err != nil {
		return fmt.Errorf("chmod config.toml to 0600: %w", err)
	}
	if logger != nil {
		logger.Debug("codex: wrote managed mcp_servers block to config.toml",
			"config_path", configPath, "managed", managed)
	}
	return nil
}

// renderCodexMcpServersBlock 把 agent 的 mcp_config JSON（Claude 风格
// `{"mcpServers": {...}}`）渲染成被 BEGIN/END 标记包裹的一批 `[mcp_servers.<name>]`
// TOML 表。返回 (block, hasServers, err)；hasServers=false 表示输入里没有可渲染的
// 服务器（mcp_config 为空/null），调用方此时只需剥掉旧的受管 block。
//
// stdio 服务器键（`args`、`env`、`command`）原样透传——codex 的配置 schema 恰好与
// 这些名字一致。远程 HTTP 服务器用的是 codex 专属键，因此在这里归一化，避免把供应商
// 细节泄漏到 UI/调度层。
func renderCodexMcpServersBlock(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	var parsed struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false, fmt.Errorf("parse mcp_config json: %w", err)
	}
	if len(parsed.McpServers) == 0 {
		return "", false, nil
	}

	names := make([]string, 0, len(parsed.McpServers))
	for name := range parsed.McpServers {
		names = append(names, name)
	}
	sort.Strings(names) // 排序保证输出确定、可复现，diff 时不会乱跳

	var sb strings.Builder
	sb.WriteString(multicaCodexMcpBeginMarker)
	sb.WriteString("\n")
	for i, name := range names {
		if !isCodexBareTomlKey(name) {
			return "", false, fmt.Errorf("mcp server name %q must be ASCII alphanumeric / _ / - to fit Codex's bare-key requirement", name)
		}
		var serverVal map[string]any
		if err := json.Unmarshal(parsed.McpServers[name], &serverVal); err != nil {
			return "", false, fmt.Errorf("mcp_servers.%s: %w", name, err)
		}
		if serverVal == nil {
			return "", false, fmt.Errorf("mcp_servers.%s must be a JSON object", name)
		}
		serverVal = normalizeCodexMcpServerConfig(serverVal)
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[mcp_servers.")
		sb.WriteString(name)
		sb.WriteString("]\n")
		keys := make([]string, 0, len(serverVal))
		for k := range serverVal {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			tomlValue, err := jsonValueToCodexTOMLInline(serverVal[k])
			if err != nil {
				return "", false, fmt.Errorf("mcp_servers.%s.%s: %w", name, k, err)
			}
			sb.WriteString(codexTOMLKey(k))
			sb.WriteString(" = ")
			sb.WriteString(tomlValue)
			sb.WriteString("\n")
		}
	}
	sb.WriteString(multicaCodexMcpEndMarker)
	sb.WriteString("\n")
	return sb.String(), true, nil
}

// normalizeCodexMcpServerConfig 把一套服务器配置归一化：剥掉 UI 用的选择器键
// （tools/prompts/resources），本地 stdio 服务器原样保留其余键；远程 HTTP 服务器则
// 把 Claude 风格键映射为 codex 认识的形式，并强制开启 codex 的实验性远程 MCP 客户端。
func normalizeCodexMcpServerConfig(server map[string]any) map[string]any {
	if !isCodexRemoteMcpServer(server) {
		normalized := make(map[string]any, len(server))
		for k, v := range server {
			if isMulticaMcpSelectorKey(k) {
				continue
			}
			normalized[k] = v
		}
		return normalized
	}

	normalized := make(map[string]any, len(server)+1)
	for k, v := range server {
		switch {
		case isMulticaMcpSelectorKey(k):
			continue
		case k == "type":
			continue
		case k == "headers":
			if _, ok := server["http_headers"]; !ok {
				normalized["http_headers"] = v
			}
		default:
			normalized[k] = v
		}
	}
	normalized["experimental_use_rmcp_client"] = true
	return normalized
}

func isMulticaMcpSelectorKey(k string) bool {
	switch k {
	case "tools", "prompts", "resources":
		return true
	default:
		return false
	}
}

func isCodexRemoteMcpServer(server map[string]any) bool {
	if typ, ok := server["type"].(string); ok && strings.EqualFold(typ, "http") {
		return true
	}
	_, hasURL := server["url"]
	_, hasCommand := server["command"]
	return hasURL && !hasCommand
}

// stripCodexUserMcpServerTables 从 TOML 配置串里移除每一张 `[mcp_servers.*]` 表
// （表头加其下的内容行，直到下一个顶层表头或 EOF）。`[mcp_servers.fetch.env]` 这类
// 子表属于父表的一部分，随父表一并丢弃。
func stripCodexUserMcpServerTables(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		if userCodexMcpServersTableHeaderRe.MatchString(line) {
			skipping = true
			continue
		}
		if skipping {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				// 遇到下一个表头：若仍是 `mcp_servers.*` 表（含子表），继续跳过；
				// 否则结束跳过并放行这一行。
				if userCodexMcpServersTableHeaderRe.MatchString(line) ||
					strings.HasPrefix(trimmed, "[mcp_servers.") ||
					strings.HasPrefix(trimmed, "[ mcp_servers.") {
					continue
				}
				skipping = false
				out = append(out, line)
				continue
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// jsonValueToCodexTOMLInline 把 JSON 值序列化成 TOML 内联值。只支持 codex `-c`
// 接受的那个子集：字符串、数字、布尔、数组、内联表。JSON null 会被拒绝——TOML 没有
// null，静默丢弃会令人困惑。
func jsonValueToCodexTOMLInline(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", fmt.Errorf("null is not a valid TOML value")
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10), nil
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case string:
		return codexTOMLBasicString(x), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			p, err := jsonValueToCodexTOMLInline(e)
			if err != nil {
				return "", err
			}
			parts[i] = p
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			p, err := jsonValueToCodexTOMLInline(x[k])
			if err != nil {
				return "", err
			}
			parts[i] = codexTOMLKey(k) + " = " + p
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", fmt.Errorf("unsupported value type %T", v)
	}
}

func codexTOMLBasicString(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 2)
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			sb.WriteString(`\\`)
		case '"':
			sb.WriteString(`\"`)
		case '\b':
			sb.WriteString(`\b`)
		case '\t':
			sb.WriteString(`\t`)
		case '\n':
			sb.WriteString(`\n`)
		case '\f':
			sb.WriteString(`\f`)
		case '\r':
			sb.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				sb.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func codexTOMLKey(s string) string {
	if isCodexBareTomlKey(s) {
		return s
	}
	return codexTOMLBasicString(s)
}

func isCodexBareTomlKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

func (b *codexBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	if opts.MCPToolsOnly {
		if err := validateCodexSupervision(b.cfg, opts); err != nil {
			return nil, err
		}
		opts.ApprovalPolicy = "never"
	}
	if err := validateNativeControlOnly(opts); err != nil {
		return nil, err
	}
	// Execute 是对外入口：先跑一次完整 attempt，返回的 Session 消息流里，若首轮因
	// 可重试原因失败，则内部再自动补一次 attempt（initialize / model_catalog_refresh）。
	firstSession, err := b.executeOnce(ctx, prompt, opts, 1)
	if err != nil {
		return nil, err
	}
	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	var sessionMu sync.RWMutex
	currentSession := firstSession
	supplement := func(supplementCtx context.Context, instruction string) error {
		sessionMu.RLock()
		session := currentSession
		sessionMu.RUnlock()
		if session == nil || session.Supplement == nil {
			return errors.New("codex turn does not accept additional messages")
		}
		return session.Supplement(supplementCtx, instruction)
	}
	supplementReady := func() bool {
		sessionMu.RLock()
		session := currentSession
		sessionMu.RUnlock()
		return session != nil && session.SupplementReady != nil && session.SupplementReady()
	}
	waitingForInput := func() bool {
		sessionMu.RLock()
		defer sessionMu.RUnlock()
		return currentSession != nil && currentSession.WaitingForInput != nil && currentSession.WaitingForInput()
	}
	runCommand := func(commandCtx context.Context, command string) (string, error) {
		sessionMu.RLock()
		session := currentSession
		sessionMu.RUnlock()
		if session == nil || session.RunCommand == nil {
			return "", errors.New("codex session no longer accepts commands")
		}
		return session.RunCommand(commandCtx, command)
	}
	commandReady := func() bool {
		sessionMu.RLock()
		session := currentSession
		sessionMu.RUnlock()
		return session != nil && session.CommandReady != nil && session.CommandReady()
	}

	go func() {
		defer func() {
			sessionMu.Lock()
			currentSession = nil
			sessionMu.Unlock()
		}()
		defer close(msgCh)
		defer close(resCh)
		session := firstSession
		attemptOpts := opts
		for attempt := 1; attempt <= 2; attempt++ {
			if attempt > 1 {
				var err error
				session, err = b.executeOnce(ctx, prompt, attemptOpts, attempt)
				if err != nil {
					resCh <- Result{Status: "failed", Error: err.Error()}
					return
				}
				sessionMu.Lock()
				currentSession = session
				sessionMu.Unlock()
			}
			// 先把开头的 session-pin 状态消息扣在手里，等本次 attempt 证明自己真的前进了再放行。
			// 重试绝不会接着被丢弃 attempt 的会话（initialize 重试在会话创建前就失败；目录刷新
			// 重试会在下面清掉 ResumeSessionID），提前转发它的 pin 会让 resume 指针指向一个从未
			// 产出回合的会话（MUL-5110）。第一条非 pin 消息意味着 attempt 已活：立即冲刷 pin 并
			// 从该点起持续转发。
			var heldPins []Message
			holdingPins := true
			flushHeldPins := func() {
				for _, held := range heldPins {
					msgCh <- held
				}
				heldPins = nil
				holdingPins = false
			}
			for msg := range session.Messages {
				if holdingPins && msg.Type == MessageStatus && msg.Status == "running" {
					heldPins = append(heldPins, msg)
					continue
				}
				if holdingPins {
					flushHeldPins()
				}
				msgCh <- msg
			}
			result, ok := <-session.Result
			if !ok {
				flushHeldPins()
				resCh <- Result{Status: "failed", Error: "codex attempt closed without result"}
				return
			}
			retryReason := ""
			switch {
			case result.codexInitializeRetrySafe:
				retryReason = "initialize"
			case result.codexStartupRefreshRetrySafe:
				retryReason = "model_catalog_refresh"
			}
			if retryReason == "" || attempt == 2 {
				flushHeldPins()
				resCh <- result
				return
			}
			// 模型目录刷新要访问网络，所以给瞬时故障留的余量比本地 initialize 握手更宽。
			// 两者都远小于任务超时，且下方 ctx.Done() 保证重试不会把任务时间拖超。
			backoff := 75*time.Millisecond + time.Duration(time.Now().UnixNano()%50)*time.Millisecond
			if retryReason == "model_catalog_refresh" {
				backoff = 500*time.Millisecond + time.Duration(time.Now().UnixNano()%1000)*time.Millisecond
				// 卡住的这次 attempt 已到达 turn/started，之前的会话可能握着已提交的输入或
				// 未完成的回合。直接 resume 可能重复投喂该输入；改为新开会话，同时保留
				// ResumeExpected，让 codexTurnInput 在输入前补上「上下文已丢失」的连续性提示。
				if attemptOpts.ResumeSessionID != "" {
					b.cfg.Logger.Warn("codex retry dropping resume pointer after model catalog refresh failure",
						"prior_thread_id", attemptOpts.ResumeSessionID,
					)
					attemptOpts.ResumeSessionID = ""
					attemptOpts.ResumeExpected = true
				}
			}
			b.cfg.Logger.Warn("codex retry scheduled", "reason", retryReason, "attempt", attempt, "next_attempt", attempt+1, "backoff", backoff.String())
			select {
			case <-ctx.Done():
				flushHeldPins()
				resCh <- result
				return
			case <-time.After(backoff):
			}
		}
	}()

	return &Session{RunCommand: runCommand, CommandReady: commandReady, Supplement: supplement, SupplementReady: supplementReady, WaitingForInput: waitingForInput, Messages: msgCh, Result: resCh}, nil
}

// executeOnce 真正拉起一次 codex app-server 进程并跑完一轮（进程生命周期 + JSON-RPC
// 消息流）。attempt 是当前第几次尝试（第 1 次为原始执行，第 2 次为内部重试），用于日志
// 与诊断区分。
func (b *codexBackend) executeOnce(ctx context.Context, prompt string, opts ExecOptions, attempt int) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "codex"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("codex executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	semanticInactivityTimeout := opts.SemanticInactivityTimeout
	if semanticInactivityTimeout == 0 {
		semanticInactivityTimeout = defaultCodexSemanticInactivityTimeout
	}
	handshakeTimeout, threadHandshakeTimeout := resolveCodexHandshakeTimeouts(opts)
	runCtx, cancel := runContext(ctx, timeout)

	// 把 agent 的 MCP 配置落到任务专属的 `$CODEX_HOME/config.toml`。走 argv 本来更简单，
	// 但 `mcp_servers.<id>.env` 允许携带密钥（codex 文档：
	// https://developers.openai.com/codex/mcp#configure-with-configtoml），而 UI 早已把
	// mcp_config 当作「对非管理员脱敏」的字段处理。进程 argv 会出现在 OS 层 `ps` 列表里；
	// daemon 的命令日志对值做脱敏，但日志脱敏保护不了进程列表。经由 config.toml 以 0o600
	// 写入，密钥值彻底不进 argv。
	codexHome := strings.TrimSpace(b.cfg.Env["CODEX_HOME"])
	if codexHome != "" {
		if err := ensureCodexMcpConfig(filepath.Join(codexHome, "config.toml"), opts.McpConfig, b.cfg.Logger); err != nil {
			// 无法落地受管配置就关门失败。告警后继续启动会静默回退到用户全局
			// `~/.codex/config.toml` 的 MCP 服务器，看起来跟「已保存的配置已生效」
			// 一模一样——这正是 MCP Tab 想消除的意外。
			cancel()
			return nil, fmt.Errorf("apply codex mcp_config: %w", err)
		}
	} else if hasManagedCodexMcpConfig(opts.McpConfig) {
		// 保存了受管 mcp_config，却没有 CODEX_HOME 来锚定它。理由同上：静默启动会继承
		// 宿主用户已有的 MCP 配置，那是错误形态的失败。
		cancel()
		return nil, fmt.Errorf("codex: mcp_config is set but CODEX_HOME env var is not configured; cannot apply managed MCP")
	}

	// 自定义 runtime profile 的 fixed_args 会作为启动前缀到达 codex。排在最前面并不能让
	// daemon 的受管配置免受它们影响：`-c key=value` 覆盖从 argv 任意位置都能赢过任务级
	// config.toml，所以前缀同样需要下方 ExtraArgs 与 CustomArgs 要做的那两次剔除。
	runtimeCmd := b.cfg.commandAt(execPath)
	if codexHome != "" {
		// daemon 在任务级配置里持有 shell_environment_policy。codex 的 -c/--config 覆盖
		// 是 last-wins，所以在拼最终 argv 之前，先移除用户提供的根级或 profile 级 policy 覆盖。
		opts.ExtraArgs = filterCodexShellEnvConfigOverrides(opts.ExtraArgs, b.cfg.Logger)
		opts.CustomArgs = filterCodexShellEnvConfigOverrides(opts.CustomArgs, b.cfg.Logger)
		runtimeCmd = runtimeCmd.withFilteredPrefix(func(prefix []string) []string {
			return filterCodexShellEnvConfigOverrides(prefix, b.cfg.Logger)
		})
	}
	if hasManagedCodexMcpConfig(opts.McpConfig) {
		// 与 NormalizeCodexLaunchArgs 对应——它在 agent 有受管 mcp_config 时对
		// ExtraArgs/CustomArgs 做同样处理，这里处理前缀。
		runtimeCmd = runtimeCmd.withFilteredPrefix(func(prefix []string) []string {
			return filterCodexCustomConfigOverrides(prefix, b.cfg.Logger)
		})
	}
	if opts.ServiceTier == codexFastServiceTier {
		// 对应 enforceCodexFastMode——buildCodexArgs 对受管参数做同样处理。enable 本身
		// 在那里只追加一次；前缀只需把冲突的 disable/config 覆盖移除。
		runtimeCmd = runtimeCmd.withFilteredPrefix(func(prefix []string) []string {
			return stripCodexFastModeConflicts(prefix, b.cfg.Logger)
		})
	}
	codexArgs := buildCodexArgs(opts, b.cfg.Logger)
	if opts.MCPToolsOnly {
		profileArgs, err := codexSupervisionProfileArgs(codexHome)
		if err != nil {
			cancel()
			return nil, err
		}
		codexArgs = append(codexArgs, profileArgs...)
	}
	var resident *codexProcessLease
	residentTransferred := false
	if b.cfg.CodexPool != nil {
		var err error
		resident, err = b.cfg.CodexPool.acquire(runCtx, b.cfg, opts, runtimeCmd, codexArgs)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("acquire resident Codex: %w", err)
		}
	}
	defer func() {
		if resident != nil && !residentTransferred {
			resident.release(false)
		}
	}()
	// Keep the app-server alive long enough to handle turn/interrupt when the
	// task context is cancelled. The lifecycle goroutine remains governed by
	// runCtx and explicitly stops this process context during bounded cleanup.
	var cmd *exec.Cmd
	var stdout io.ReadCloser
	var stdin io.WriteCloser
	var stderrBuf *stderrTail
	var stream *codexResidentStream
	var stopProcess func()
	var pid int
	if resident != nil {
		var err error
		stream, err = openCodexResidentStream(runCtx, resident.entry.socket)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("connect resident Codex: %w", err)
		}
		stdout, stdin, stderrBuf = stream.reader, stream, resident.stderr
		pid = resident.entry.cmd.Process.Pid
		stopProcess = func() { _ = stream.Close(); resident.release(false) }
		b.cfg.Logger.Info("codex resident lease", "native_pid", pid, "task_id", b.cfg.TaskID, "reused", resident.reused)
	} else {
		var err error
		processCtx, stop := context.WithCancel(context.WithoutCancel(runCtx))
		stopProcess = stop
		cmd = runtimeCmd.exec(processCtx, codexArgs...)
		hideAgentWindow(cmd)
		// 把 codex 放进独立的进程组，卡死后的清理能覆盖整棵进程树——codex 的 Node 壳加上
		// 它拉起的原生 Rust app-server——而不只是直接子进程。否则杀掉带头进程会留下孤儿孙进程
		// 持续吃内存直到 OS 回收；参见 #4520：thread/resume 期间发生 scanner 溢出时，旧行为会
		// 无限泄漏 codex 进程。configureProcessGroup 在 Windows 上是空操作。
		// 覆盖 exec.CommandContext 默认的取消行为：默认只向 cmd.Process（带头进程）发 SIGKILL，
		// 我们改为给整个进程组发信号，让后代一起死。返回 nil 是避免 exec 记一条虚假错误；
		// 若 kill 后仍有打开的管道，下方 cmd.WaitDelay 仍会兜底 cmd.Wait()。
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				signalProcessGroup(cmd, syscall.SIGKILL)
			}
			return nil
		}
		// 给 context 取消后的等待上界，防止卡死的子进程（或孙进程握着的打开管道）让
		// cmd.Wait() 无限挂起。与其它长驻后端（claude、copilot、cursor 等）保持一致。
		cmd.WaitDelay = codexProcessWaitDelay()
		b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(codexArgs, trustAgentCommandPositional(0, "app-server")))
		if opts.Cwd != "" {
			cmd.Dir = opts.Cwd
		}
		cmd.Env = buildEnv(b.cfg.Env)

		stdout, err = cmd.StdoutPipe()
		if err != nil {
			cancel()
			stopProcess()
			return nil, fmt.Errorf("codex stdout pipe: %w", err)
		}
		stdin, err = cmd.StdinPipe()
		if err != nil {
			cancel()
			stopProcess()
			return nil, fmt.Errorf("codex stdin pipe: %w", err)
		}
		// codex 的 stderr 可能含 auth/provider 诊断。只捕获一段有界尾部，并且只经脱敏器
		// 出现在清理事件里。
		stderrBuf = newStderrTail(io.Discard, codexStderrTailBytes)
		cmd.Stderr = stderrBuf

		// 启动并一步到位接管整棵进程树。Windows 上子进程以挂起方式创建并先放进 Job Object
		// 再运行，让下方清理能覆盖 Node 壳、原生 app-server 及其底下的沙箱辅助进程，而不只是
		// 直接子进程。Unix 上上面配置的进程组已覆盖这些，这里只是普通 Start。接管失败只记日志、
		// 不算致命；无法恢复运行的子进程会被杀掉并在这一步报出来。
		if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
			cancel()
			stopProcess()
			return nil, fmt.Errorf("start codex: %w", err)
		}
		pid = cmd.Process.Pid
	}
	residentTransferred = true
	activeLaunches := activeCodexLaunches.Add(1)
	for {
		maxSeen := maxActiveCodexLaunchesObserved.Load()
		if activeLaunches <= maxSeen || maxActiveCodexLaunchesObserved.CompareAndSwap(maxSeen, activeLaunches) {
			break
		}
	}
	launchStarted := time.Now()
	codexVersion := strings.TrimSpace(b.cfg.CodexVersion)
	if codexVersion == "" {
		codexVersion = "unknown"
	}

	launchPhase := "spawn"
	if resident != nil && resident.reused {
		launchPhase = "lease"
	}
	b.cfg.Logger.Info("codex lifecycle", "phase", launchPhase, "task_id", b.cfg.TaskID, "runtime_id", b.cfg.RuntimeID, "pid", pid, "process_group", pid, "cwd", opts.Cwd, "attempt", attempt, "active_launches", activeLaunches, "codex_version", codexVersion, "daemon_version", b.cfg.DaemonVersion)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	semanticActivityCh := make(chan string, 256)
	guardianRateLimitTrip := make(chan struct{}, 1)
	var guardianRateLimitTripped atomic.Bool

	var outputMu sync.Mutex
	// Result.Output 是「后端选出的最终面向用户输出」（见 agent.go），所以它只装交付物。
	// finalAnswer 是 app-server 标注为 `phase: "final_answer"` 的文本；lastAgentMessage
	// 是旧式 `agent_message` 协议（不带 phase）的回退。每条 agent 消息仍会流到 msgCh，
	// transcript 不变——只是 daemon 转发给聊天/频道回复的内容收窄了（GH #6006）。
	var finalAnswer, lastAgentMessage string
	var semanticObserved atomic.Bool
	turnNotificationGate := &codexTurnNotificationGate{}
	firstItemWait := &codexFirstItemWaitObservation{}

	// turnDone 在启动读取 goroutine 之前就设好，避免生命周期 goroutine 写入与读取方
	// 读取之间产生竞态。
	turnDone := make(chan bool, 1) // true = 被中止

	var c *codexClient
	observeMessage := func(msg Message) {
		logCodexAgentMessage(b.cfg.Logger, msg)
		activity := describeCodexSemanticActivity(msg)
		if activity == "status:running" {
			firstItemWait.start(time.Now(), c.activeTurnID())
		}
		trySendString(semanticActivityCh, activity)
		if activity != "" {
			semanticObserved.Store(true)
		}
	}

	c = &codexClient{
		mcpToolsOnly:             opts.MCPToolsOnly,
		cfg:                      b.cfg,
		approvalPolicy:           opts.ApprovalPolicy,
		stdin:                    stdin,
		pending:                  make(map[int]*pendingRPC),
		processDone:              make(chan struct{}),
		handshakeTimeout:         handshakeTimeout,
		threadHandshakeTimeout:   threadHandshakeTimeout,
		pid:                      pid,
		attempt:                  attempt,
		activeLaunches:           activeLaunches,
		notificationProtocol:     "unknown",
		acceptNotification:       turnNotificationGate.accept,
		guardianRateLimitCircuit: &codexGuardianRateLimitCircuit{},
		onGuardianRateLimit: func() {
			guardianRateLimitTripped.Store(true)
			b.cfg.Logger.Warn("codex Guardian approval rate-limit circuit opened",
				"task_id", b.cfg.TaskID, "runtime_id", b.cfg.RuntimeID,
				"failure_count", codexGuardianRateLimitThreshold)
			guardianRateLimitTrip <- struct{}{}
		},
		onDiscardedNotification: func(string, map[string]any) {
			// 任何 app-server 通知都证明进程做了语义进展——即使它被有意排除在当前回合外。
			// 借此保住 initialize 重试的安全性，又不用重放内容。
			semanticObserved.Store(true)
		},
		onMessage: func(msg Message) {
			observeMessage(msg)
			select {
			case msgCh <- msg:
			case <-runCtx.Done():
				return
			}
		},
		onAgentMessageChunk: func(text string) bool {
			msg := Message{Type: MessageText, Content: text}
			observeMessage(msg)
			// Agent text is the user-visible transcript, so unlike auxiliary
			// progress events it participates in reconciliation. A false return
			// leaves the text pending for completed/terminal/EOF retry. Keep the
			// send non-blocking because Session.Messages is optional; bounded
			// coalescing prevents a token burst from filling the channel while
			// preserving the reader's EOF and cancellation liveness.
			return trySend(msgCh, msg)
		},
		onAgentMessage: func(text string) {
			// Delta events make MessageText incremental. Keep Result.Output's
			// legacy fallback authoritative by updating it from the completed
			// agent-message snapshot, not whichever delta happened to arrive last.
			outputMu.Lock()
			lastAgentMessage = text
			outputMu.Unlock()
		},
		onFinalAnswer: func(text string) {
			outputMu.Lock()
			finalAnswer = text
			outputMu.Unlock()
		},
		onSemanticActivity: func(description string) {
			semanticObserved.Store(true)
			b.cfg.Logger.Debug("codex semantic activity observed", "activity", description)
			trySendString(semanticActivityCh, description)
		},
		onTurnDone: func(aborted bool) {
			// 原生 Goal 的回合边界不是任务边界，续跑由 Codex 自己调度。
			if !aborted && c.goalActive.Load() && c.getTurnError() == "" {
				return
			}
			select {
			case turnDone <- aborted:
			default:
			}
		},
	}

	if resident != nil {
		c.threadConfig = resident.threadConfig
		c.threadClosed = make(chan struct{})
	}
	c.cfg.CodexVersion = codexVersion
	c.interactions = newInteractionBroker("codex", runCtx, runtimeInteractionRequest(opts), c.onMessage)
	// 后台开始读 stdout
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			c.handleLine(line)
		}
		// A cancelled or crashed app-server may close stdout without an
		// item/completed snapshot. Preserve every complete delta JSON event
		// already parsed; an incomplete final JSON token is ignored by
		// handleLine and can never enter this buffer.
		c.flushAgentMessageDeltas()
		if err := scanner.Err(); err != nil {
			// 两个都要 %w：调用方用 errCodexProcessExited 判断进程没了，用
			// bufio.ErrTooLong 区分「读不到响应」和「codex 死了」。startOrResumeThread
			// 需要这个区别，才能把超大 resume 报成「被拒的 resume」而不是崩溃（MUL-5722）。
			c.markProcessExited(fmt.Errorf("%w: %w", errCodexProcessExited, err))
			return
		}
		c.markProcessExited(errCodexProcessExited)
	}()

	// drainAndWait 先关闭 stdin 让 codex 停机，再汇合 cmd.Wait()。
	// cmd.Wait() 是 Go 标准库唯一文档化的、对 os/exec 内部 stderr/stdout 拷贝 goroutine
	// 的同步点——它返回前，stderrBuf 未必观测到 codex 退出前写下的每一个字节，
	// stderrBuf.Tail() 可能返回空或残缺。任何读取 stderrBuf.Tail() 的代码都必须先调用
	// drainAndWait()。sync.Once 让它在两条错误路径与 deferred 清理里都能安全调用。
	//
	// drainAndWait 同时是 scanner 溢出路径（#4520）的清理安全网。当 codex 吐出一条超过
	// scanner MaxScanTokenSize 的 stdout 行时，读取 goroutine 带着 scanner.Err() 退出，
	// 经 markProcessExited 让所有在途 RPC 失败并关闭 readerDone——但 codex 子进程还活着，
	// 正堵着往一条没人读的 stdout 管道写超大行的剩余部分。这时天真的
	// stdin.Close()+cmd.Wait() 会永远挂起：codex 永远到不了读 stdin 的系统调用，也就
	// 看不到 EOF、不会退出，cmd.Wait() 永不返回。于是生命周期 goroutine 永远发不出失败的
	// Result，外层 daemon 卡在它的结果 channel 上，更上层的 fresh-session 兜底也不触发。
	//
	// 为了让干净停机与卡死子进程两种情况都正确，drainAndWait 分两个有界阶段运行：
	//
	//  1. 关闭 stdin 并等读取 goroutine 结束，上限 codexGracefulShutdownTimeout。读取方
	//     在 codex 自行关闭 stdout 时退出（干净停机——给 OTEL 批量导出器 flush 的机会），
	//     或在 scanner 出错时退出（溢出场景——readerDone 早已关闭，select 立即返回）。
	//     按 os/exec 文档，cmd.Wait() 期间若 StdoutPipe 返回的管道上还有读取在途是错误
	//     用法：Wait 会关掉管道，把未完成的读取变成虚假错误，所以必须先等读取方。
	//
	//  2. 等 cmd.Wait() 返回，同样以上限 codexGracefulShutdownTimeout。正常情况进程已退出、
	//     立即返回。卡死子进程场景下进程还活着——取消 runCtx 触发 cmd.Cancel（上面装的
	//     进程组 SIGKILL 助手），cmd.WaitDelay 随后保证即使管道仍开着 cmd.Wait() 也会返回。
	var waitOnce sync.Once
	var cleanupConfirmed bool
	var waitReturned bool
	var cleanupWaitErr error
	var residentCleanupErr error
	drainAndWait := func() {
		waitOnce.Do(func() {
			c.commandReady.Store(false)
			c.commandMu.Lock()
			defer c.commandMu.Unlock()
			if c.goalControlUncertain.Load() && c.getThreadID() != "" {
				c.pauseGoalBeforeStop(c.getThreadID(), b.cfg.Logger)
			}
			if resident != nil {
				residentCleanupErr = c.unloadResidentThread()
				if err := reapCodexResidentExecution(resident.execution); err != nil {
					residentCleanupErr = err
				}
				_ = stream.Close()
				closed := false
				select {
				case <-readerDone:
					closed = true
				case <-time.After(codexGracefulShutdown()):
					residentCleanupErr = errors.New("Codex resident event stream did not close")
				}
				<-stream.done
				retained := residentCleanupErr == nil && closed
				resident.release(retained)
				cleanupConfirmed = !retained && waitProcessGroupGone(resident.entry.cmd, 3*time.Second)
				b.cfg.Logger.Info("codex lifecycle", "phase", "cleanup", "task_id", b.cfg.TaskID, "pid", pid,
					"reaped", cleanupConfirmed, "native_retained", retained, "transport_closed", closed)
				return
			}
			stdin.Close()

			grace := codexGracefulShutdown()
			waitCh := make(chan struct{})
			var startWait sync.Once
			startProcessWait := func() {
				startWait.Do(func() {
					go func() {
						cleanupWaitErr = cmd.Wait()
						close(waitCh)
					}()
				})
			}

			// 阶段 1：调用 cmd.Wait() 之前先让读取方结束。
			select {
			case <-readerDone:
				// 读取方干净排空（codex 停机关闭了 stdout）或提前中止（如 scanner 溢出）。
				// 无论哪种，现在调用 cmd.Wait() 都安全了。
			case <-time.After(grace):
				// codex 没在宽限期内关闭 stdout。用 context 取消强制停机——cmd.Cancel
				// 组杀整棵树，读取方在 stdout EOF 时解除阻塞，进入阶段 2。
				b.cfg.Logger.Warn("codex did not close stdout after stdin EOF; forcing shutdown",
					"pid", pid,
					"grace", grace.String(),
				)
				stopProcess()
				// On Windows, Cancel terminates only the direct child. A
				// descendant may keep inherited stdout open indefinitely. Start
				// Wait now so os/exec's WaitDelay closes the pipe after its
				// bounded deadline and lets the reader finish.
				startProcessWait()
				<-waitCh
				select {
				case <-readerDone:
				case <-time.After(grace):
					b.cfg.Logger.Warn("codex stdout reader remained open after bounded process wait",
						"pid", pid,
						"grace", grace.String(),
					)
				}
			}

			// 阶段 2：给 cmd.Wait() 上界，防止进程还活着时无限等
			// （scanner 溢出场景：读取方自行提前退出，而 codex 仍堵着往塞满的管道写）。
			startProcessWait()
			select {
			case <-waitCh:
				waitReturned = true
				// 干净回收。
			case <-time.After(grace):
				b.cfg.Logger.Warn("codex process still alive after reader exited; forcing shutdown",
					"pid", pid,
					"grace", grace.String(),
				)
				stopProcess()
				// WaitDelay (10s) is the final backstop: even if the
				// group-kill races with an open pipe held by a
				// descendant, cmd.Wait() returns within WaitDelay of the
				// cancel.
				<-waitCh
				waitReturned = true
			}
			// Wait 带着 ProcessState 返回就是 os/exec 的回收边界。Unix 上被 SIGKILL 终止的
			// 进程，即使 Wait 已成功回收，ProcessState.Exited 仍报 false。
			cleanupConfirmed = waitReturned && cmd.ProcessState != nil && waitProcessGroupGone(cmd, grace)
			if codexCleanupConfirmationOverride.Load() < 0 {
				cleanupConfirmed = false
			}
			b.cfg.Logger.Info("codex lifecycle",
				"phase", "cleanup",
				"task_id", b.cfg.TaskID,
				"runtime_id", b.cfg.RuntimeID,
				"pid", pid,
				"process_group", pid,
				"attempt", attempt,
				"latency", time.Since(launchStarted).Round(time.Millisecond).String(),
				"reaped", cleanupConfirmed,
				"exit_status", codexProcessExitStatus(cmd.ProcessState),
				"wait_error", cleanupWaitErr,
				"stderr_bytes", stderrBuf.TotalBytes(),
				"stderr_truncated", stderrBuf.TotalBytes() > codexStderrTailBytes,
			)
			// 整棵树已回收并观测完毕，可以放弃所有权了。Windows 上这是唯一安全的放弃点——
			// 在那里释放会把仍在 job 里的东西杀掉，而这正是对任何活过上面回收的东西该做的事。
			// waitOnce 让每次启动尝试恰好执行一次。
			releaseProcessGroup(cmd)
		})
	}

	// 用 goroutine 驱动会话生命周期。
	// 停机时序：生命周期 goroutine 关闭 stdin + 取消 context → codex 进程退出 → 读取
	// goroutine 的 scanner.Scan() 返回 false → readerDone 关闭 → 生命周期 goroutine
	// 收拢最终输出并发出 Result。
	go func() {
		defer activeCodexLaunches.Add(-1)
		defer cancel()
		defer stopProcess()
		defer close(msgCh)
		defer close(resCh)
		defer func() {
			c.commandReady.Store(false)
			cancel()
			drainAndWait()
			c.interactions.stop()
		}()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string

		// 1. initialize 握手
		initializeStarted := time.Now()
		b.cfg.Logger.Info("codex lifecycle", "phase", "initialize_sent", "task_id", b.cfg.TaskID, "runtime_id", b.cfg.RuntimeID, "pid", pid, "attempt", attempt, "active_launches", activeLaunches)
		initialized, err := c.request(runCtx, "initialize", map[string]any{
			"clientInfo": map[string]any{
				"name":    "multica-agent-sdk",
				"title":   "Multica Agent SDK",
				"version": "0.2.0",
			},
			"capabilities": map[string]any{
				"experimentalApi": true,
			},
		})
		if err == nil {
			var peer struct {
				UserAgent string `json:"userAgent"`
			}
			decodeErr := json.Unmarshal(initialized, &peer)
			actual, versionErr := parseSemver(peer.UserAgent)
			if decodeErr == nil && versionErr == nil {
				c.cfg.CodexVersion = fmt.Sprintf("%d.%d.%d", actual.Major, actual.Minor, actual.Patch)
			}
			if opts.MCPToolsOnly && (decodeErr != nil || !codexSupervisionVersionSupported(peer.UserAgent)) {
				err = errors.New("Codex supervision could not verify the actual 0.164.0 protocol peer")
			}
		}
		if err != nil {
			initializeLatency := time.Since(initializeStarted)
			var handshakeErr *codexHandshakeTimeoutError
			timedOut := errors.As(err, &handshakeErr) && handshakeErr.Method == "initialize"
			if timedOut {
				// 超时的 initialize 可能在主机放弃之后仍完成。先杀整个进程组再等，避免
				// 一个因 stdin EOF 退出的带头进程留下脱离 stdio 的后代。
				signalProcessGroup(cmd, syscall.SIGKILL)
			} else if runCtx.Err() != nil {
				// processCtx deliberately outlives runCtx so active turns can use
				// turn/interrupt. Initialize has no interruptible turn, so a parent
				// cancellation must take the fast process-tree cleanup path.
				stopProcess()
			}
			drainAndWait() // 采样 Tail 前先排空 os/exec 的 stderr goroutine
			finalStatus = "failed"
			finalError = fmt.Sprintf("codex initialize failed: %v", err)
			contextEnded := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
			if !timedOut && !contextEnded {
				// 超时的 stderr 是供应商侧不可信输出，可能回显模式清洗识别不出的不透明
				// Config.Env/auth 值。父任务期限/取消赢了单 RPC 握手超时的竞态时同理。
				// 别让它进持久化/用户可见的 Result；清理生命周期仍记录有界的字节/截断元数据。
				finalError = withAgentStderr(finalError, "codex", sanitizeCodexDiagnostic(stderrBuf.Tail()))
			}
			retrySafe := timedOut && !semanticObserved.Load() && cleanupConfirmed && codexInitializeRetrySupported()
			if timedOut && !cleanupConfirmed {
				finalError += "; retry suppressed: process cleanup/reap not confirmed"
			} else if timedOut && cleanupConfirmed && !codexInitializeRetrySupported() {
				finalError += "; retry suppressed: process-tree cleanup cannot be confirmed on this platform"
			}
			b.cfg.Logger.Warn("codex lifecycle", "phase", "initialize_failure", "task_id", b.cfg.TaskID, "runtime_id", b.cfg.RuntimeID, "pid", pid, "attempt", attempt, "latency", initializeLatency.Round(time.Millisecond).String(), "semantic_activity", semanticObserved.Load(), "cleanup_confirmed", cleanupConfirmed, "retry_safe", retrySafe)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), codexInitializeRetrySafe: retrySafe}
			return
		}
		b.cfg.Logger.Info("codex lifecycle", "phase", "initialize_response", "task_id", b.cfg.TaskID, "runtime_id", b.cfg.RuntimeID, "pid", pid, "attempt", attempt, "latency", time.Since(initializeStarted).Round(time.Millisecond).String())
		c.notify("initialized")

		// 2. 新开一个线程，或为本次问题 resume 之前的线程。resume 失败时（线程在服务端
		// 被回收、schema 漂移等）回退到全新线程，让任务仍能推进。
		var threadID string
		var resumed bool
		c.unloadedGoalControl = codexUnloadedGoalControl(opts)
		if c.unloadedGoalControl {
			// Goal 查询和停止操作直接使用已物化会话；resume 会自动续跑 active Goal。
			threadID, resumed = opts.ResumeSessionID, true
		} else {
			threadID, resumed, err = c.startOrResumeThread(runCtx, opts, b.cfg.Logger)
		}
		if err != nil {
			var handshakeErr *codexHandshakeTimeoutError
			timedOut := errors.As(err, &handshakeErr) && isCodexThreadSetupRPC(handshakeErr.Method)
			if timedOut {
				// 线程建立超时的服务端结果不确定。先杀整个进程组再等，避免一个因 EOF 退出的
				// 带头进程留下带着脱离 stdio 的后代。
				signalProcessGroup(cmd, syscall.SIGKILL)
			} else if runCtx.Err() != nil {
				// No turn exists during thread start/resume, so there is nothing to
				// interrupt and no reason to spend the graceful shutdown budget.
				stopProcess()
			}
			drainAndWait() // 采样 Tail 前先排空 os/exec 的 stderr goroutine
			finalStatus = "failed"
			stderrTail := sanitizeCodexDiagnostic(stderrBuf.Tail())
			finalError = err.Error()
			if c.threadSetupMethod != "" {
				classification := classifyCodexStartupStderr(stderrTail, timedOut)
				b.cfg.Logger.Warn("codex lifecycle",
					"phase", strings.ReplaceAll(c.threadSetupMethod, "/", "_")+"_failure",
					"task_id", b.cfg.TaskID,
					"runtime_id", b.cfg.RuntimeID,
					"pid", pid,
					"attempt", attempt,
					"active_launches", activeLaunches,
					"method", c.threadSetupMethod,
					"latency", time.Since(c.threadSetupStarted).Round(time.Millisecond).String(),
					"latency_ms", time.Since(c.threadSetupStarted).Milliseconds(),
					"cleanup_confirmed", cleanupConfirmed,
					"reaped", cleanupConfirmed,
					"retry_safe", false,
					"retry_attempted", false,
					"stderr_model_refresh_failure_count", classification.modelRefreshFailure,
					"stderr_model_refresh_timeout_count", classification.modelRefreshTimeout,
					"stderr_mcp_init_transport_count", classification.mcpInitTransport,
					"stderr_bare_timeout_count", classification.bareTimeout,
				)
			}
			resCh <- Result{
				Status:         finalStatus,
				Error:          finalError,
				DurationMs:     time.Since(startTime).Milliseconds(),
				ResumeRejected: isCodexResumeOverflow(opts, err),
			}
			return
		}
		c.setThreadID(threadID)
		if opts.Model == "" {
			opts.Model = c.nativeThreadModel()
		}
		// 先建立带准确协作模式的用户回合，再激活 Goal。空闲线程上的
		// goal/set 会自行启动回合，不能随后再发送一次 turn/start。
		deferGoalCommand := !opts.CommandOnly && nativeGoalActivatingCommand(opts.Command)
		if deferGoalCommand {
			c.goalActive.Store(true)
			c.goalEngaged.Store(true)
		}
		var commandOutput string
		var commandErr error
		if !deferGoalCommand {
			commandOutput, commandErr = c.applyRuntimeCommand(runCtx, opts)
		}
		if commandErr != nil {
			resCh <- Result{Status: "failed", Error: commandErr.Error(), SessionID: threadID, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}
		if opts.CommandOnly {
			resumeID := threadID
			if !resumed {
				// 纯控制不物化 rollout；聊天控制选择由持久记录恢复。
				resumeID = ""
			}
			c.onMessage(Message{Type: MessageStatus, Status: "running", SessionID: resumeID})
			if resident != nil {
				drainAndWait()
				if residentCleanupErr != nil {
					resCh <- Result{Status: "failed", Error: residentCleanupErr.Error(), SessionID: resumeID, DurationMs: time.Since(startTime).Milliseconds()}
					return
				}
			}
			resCh <- Result{Status: "completed", Output: commandOutput, SessionID: resumeID, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}
		if resumed {
			b.cfg.Logger.Info("codex thread resumed", "thread_id", threadID)
		} else {
			b.cfg.Logger.Info("codex thread started", "thread_id", threadID)
		}

		// 3. 发送回合并等待完成。当预期 resume 却落在全新线程上时（在线的 thread/resume
		// RPC 被拒——损坏/不兼容的 rollout、服务端线程回收、schema 漂移——或传输失败迫使我们
		// 走全新重试），在提示词前拼上调用方的连续性提示，让 agent 不要假定它还拥有其实已
		// 不存在的上下文。daemon 的预检门只能拦住启动前就能发现的情况；这里覆盖的，是只有
		// 在线 resume 才会暴露出来的那些（MUL-4424）。
		//
		// 这段提示是否要 agent 转告用户，是调用方而非我们决定：取决于这个界面的对话是否
		// 仍可读，本包看不到（MUL-5722）。
		turnParams := map[string]any{
			"threadId": threadID,
			"input":    codexTurnInput(prompt, opts.ResumeExpected, resumed, opts.ResumeContinuityNotice),
		}
		if opts.MCPToolsOnly {
			turnParams["environments"] = []any{}
		}
		// 每回合的推理强度覆盖。与 startOrResumeThread 里按线程的注入对应；两者同步由
		// codex_test.go 里共享的 `codexReasoningInjection` 夹具保证（见 MUL-2339——
		// Trump 要求三个注入点不得各自漂移）。
		applyCodexReasoningEffort(turnParams, opts.ThinkingLevel)
		applyCodexServiceTier(turnParams, opts.ServiceTier)
		if err := applyCodexPlanMode(turnParams, opts); err != nil {
			resCh <- Result{Status: "failed", Error: err.Error(), SessionID: threadID, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}
		waitingForTurn := true
		var timeoutDiagnostic codexTimeoutDiagnostic
		var processExitErr error
		finishFirstItemWait := func(outcome string) {
			firstItemWait.finish(
				time.Now(),
				outcome,
				classifyCodexStartupStderr(stderrBuf.Tail(), strings.HasSuffix(outcome, "_timeout")),
			)
		}
		finishTurn := func(aborted bool) {
			c.commandMu.Lock()
			defer c.commandMu.Unlock()
			if !aborted && c.getTurnError() == "" && c.goalActive.Load() && c.getProcessErr() == nil {
				return
			}
			c.commandReady.Store(false)
			waitingForTurn = false
			switch {
			case aborted:
				finishFirstItemWait("turn_aborted")
				finalStatus = "aborted"
				if errMsg := c.getTurnError(); errMsg != "" {
					finalError = errMsg
				} else {
					finalError = "turn was aborted"
				}
			default:
				if c.goalActive.Load() && c.getProcessErr() != nil {
					finalStatus = "failed"
					finalError = "codex exited before its native goal completed"
				} else if errMsg := c.getTurnError(); errMsg != "" {
					finishFirstItemWait("turn_failed")
					finalStatus = "failed"
					finalError = errMsg
				} else if errMsg := c.goalStopError(); errMsg != "" {
					finishFirstItemWait("goal_stopped")
					finalStatus = "failed"
					finalError = errMsg
				} else {
					finishFirstItemWait("turn_completed")
				}
			}
		}
		finishRunContextDone := func() {
			c.commandReady.Store(false)
			waitingForTurn = false
			if runCtx.Err() == context.DeadlineExceeded {
				finishFirstItemWait("execution_timeout")
				finalStatus = "timeout"
				finalError = fmt.Sprintf("codex timed out after %s", timeout)
			} else {
				finishFirstItemWait("cancelled")
				finalStatus = "aborted"
				finalError = "execution cancelled"
			}
		}
		turnNotificationGate.arm()
		_, err = c.request(runCtx, "turn/start", turnParams)
		if err == nil && deferGoalCommand {
			_, err = c.applyRuntimeCommand(runCtx, opts)
			if err != nil {
				c.setTurnError(err.Error())
			}
		}
		if err != nil {
			if runCtx.Err() != nil {
				finishRunContextDone()
				if !interruptCodexTurn(c, threadID, turnDone, opts.TurnInterruptTimeout, b.cfg.Logger) {
					stopProcess()
				}
			} else {
				select {
				case aborted := <-turnDone:
					finishTurn(aborted)
				default:
					drainAndWait() // flush os/exec stderr goroutine before sampling Tail
					finalStatus = "failed"
					finalError = withAgentStderr(fmt.Sprintf("codex turn/start failed: %v", err), "codex", sanitizeCodexDiagnostic(stderrBuf.Tail()))
					resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
					return
				}
			}
		}

		lastSemanticActivity := time.Now()
		lastSemanticActivityDescription := "turn/start"
		semanticTimer := time.NewTimer(semanticInactivityTimeout)
		defer semanticTimer.Stop()

		firstTurnNoProgressTimeout := codexFirstTurnNoProgressTimeout(semanticInactivityTimeout, opts.FirstTurnNoProgressTimeout)
		var firstTurnNoProgressTimer *time.Timer
		var firstTurnNoProgressTimerC <-chan time.Time
		firstTurnStarted := false
		firstTurnProgressObserved := false
		stopFirstTurnNoProgressTimer := func() {
			if firstTurnNoProgressTimer == nil {
				return
			}
			stopTimer(firstTurnNoProgressTimer)
			firstTurnNoProgressTimerC = nil
		}
		defer stopFirstTurnNoProgressTimer()
		if waitingForTurn {
			c.commandReady.Store(true)
		}

		for waitingForTurn {
			select {
			case aborted := <-turnDone:
				if !aborted && c.getTurnError() == "" && c.goalActive.Load() {
					continue
				}
				finishTurn(aborted)
			case <-guardianRateLimitTrip:
				waitingForTurn = false
				finishFirstItemWait("guardian_rate_limit")
				finalStatus = "failed"
				finalError = codexGuardianRateLimitError
				if !interruptCodexTurn(c, threadID, turnDone, opts.TurnInterruptTimeout, b.cfg.Logger) {
					cancel()
				}
			case activity := <-semanticActivityCh:
				lastSemanticActivity = time.Now()
				lastSemanticActivityDescription = activity
				resetTimer(semanticTimer, semanticInactivityTimeout)
				if activity == "status:running" && !firstTurnStarted {
					firstTurnStarted = true
					firstItemWait.start(time.Now(), c.activeTurnID())
					firstTurnNoProgressTimer = time.NewTimer(firstTurnNoProgressTimeout)
					firstTurnNoProgressTimerC = firstTurnNoProgressTimer.C
				} else if firstTurnStarted && !firstTurnProgressObserved && isCodexFirstTurnProgressActivity(activity) {
					firstTurnProgressObserved = true
					if activity == "error:terminal" {
						finishFirstItemWait("turn_failed")
					} else {
						finishFirstItemWait("progress")
					}
					stopFirstTurnNoProgressTimer()
				}
			case <-firstTurnNoProgressTimerC:
				if c.interactions.waiting() {
					firstTurnNoProgressTimer.Reset(firstTurnNoProgressTimeout)
					continue
				}
				waitingForTurn = false
				finishFirstItemWait("no_progress_timeout")
				finalStatus = "timeout"
				turnID := c.activeTurnID()
				timeoutDiagnostic = codexTimeoutDiagnostic{
					Kind:         codexTimeoutFirstTurnNoProgress,
					Timeout:      firstTurnNoProgressTimeout,
					LastActivity: lastSemanticActivityDescription,
					ThreadID:     threadID,
					TurnID:       turnID,
					Model:        opts.Model,
				}
				b.cfg.Logger.Warn(CodexFirstTurnNoProgressMarker,
					"pid", pid,
					"thread_id", threadID,
					"turn_id", turnID,
					"timeout", firstTurnNoProgressTimeout.String(),
					"last_activity", lastSemanticActivityDescription,
				)
			case <-semanticTimer.C:
				if c.interactions.waiting() {
					resetTimer(semanticTimer, semanticInactivityTimeout)
					continue
				}
				waitingForTurn = false
				finishFirstItemWait("semantic_inactivity_timeout")
				finalStatus = "timeout"
				turnID := c.activeTurnID()
				timeoutDiagnostic = codexTimeoutDiagnostic{
					Kind:         codexTimeoutSemanticInactivity,
					Timeout:      semanticInactivityTimeout,
					LastActivity: lastSemanticActivityDescription,
					ThreadID:     threadID,
					TurnID:       turnID,
					Model:        opts.Model,
				}
				b.cfg.Logger.Warn(CodexSemanticInactivityMarker,
					"pid", pid,
					"thread_id", threadID,
					"turn_id", turnID,
					"timeout", semanticInactivityTimeout.String(),
					"last_activity", lastSemanticActivityDescription,
					"idle_for", time.Since(lastSemanticActivity).Round(time.Millisecond).String(),
				)
			case <-runCtx.Done():
				finishRunContextDone()
				if !interruptCodexTurn(c, threadID, turnDone, opts.TurnInterruptTimeout, b.cfg.Logger) {
					stopProcess()
				}
			case <-c.processDone:
				select {
				case aborted := <-turnDone:
					finishTurn(aborted)
				default:
					if runCtx.Err() != nil {
						finishRunContextDone()
					} else {
						waitingForTurn = false
						finishFirstItemWait("process_exit")
						finalStatus = "failed"
						processExitErr = c.getProcessErr()
						if processExitErr == nil {
							processExitErr = errCodexProcessExited
						}
						finalError = processExitErr.Error()
					}
				}
			}
		}
		// A turn/completed notification can race the circuit signal in the
		// select above. The already-observed repeated failures still win.
		if guardianRateLimitTripped.Load() {
			finalStatus = "failed"
			finalError = codexGuardianRateLimitError
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("codex finished", "pid", pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		// 执行清理。drainAndWait 用两个有界阶段实现「先优雅后强杀」（见其声明）：
		// 先等 reader，再等 cmd.Wait()，任一步超时则强制取消。干净停机让 codex 冲刷
		// OTEL 遥测；卡死的进程则被进程组 SIGKILL 干掉。
		drainAndWait()
		if residentCleanupErr != nil {
			finalStatus = "failed"
			finalError = residentCleanupErr.Error()
		}

		if processExitErr != nil {
			finalError = withAgentStderr(processExitErr.Error(), "codex", sanitizeCodexDiagnostic(stderrBuf.Tail()))
		}
		stderrTail := sanitizeCodexDiagnostic(stderrBuf.Tail())
		if timeoutDiagnostic.Kind != codexTimeoutNone {
			timeoutDiagnostic.CodexVersion = detectCodexVersionForDiagnostics(context.Background(), b.cfg.commandAt(execPath), cmd.Env, b.cfg.Logger)
			finalError = buildCodexTimeoutDiagnosticError(timeoutDiagnostic, stderrTail)
		}

		// 首轮因 codex 加载不了模型目录而无语义进展，属于「只在启动时发生」的失败：
		// 没有工具运行过、没有内容到达用户，重放提示词不会重复副作用。复用 initialize
		// 重试所需的进程树证据（cleanupConfirmed + 平台支持），而不是只看 ProcessState：
		// Windows 上 daemon 无法证明整棵树都退出，残留的 app-server 会和重试抢跑。
		startupRefreshRetrySafe := timeoutDiagnostic.Kind == codexTimeoutFirstTurnNoProgress &&
			!firstTurnProgressObserved &&
			strings.Contains(stderrTail, codexModelCatalogRefreshFailureSignal) &&
			cleanupConfirmed && codexInitializeRetrySupported()
		if startupRefreshRetrySafe {
			b.cfg.Logger.Warn("codex startup model catalog refresh failure is retry safe",
				"pid", pid,
				"thread_id", threadID,
				"attempt", attempt,
			)
		}

		if waitLatency, firstTurnID, outcome, classification, ok := firstItemWait.snapshot(); ok {
			if waitLatency < 0 {
				waitLatency = 0
			}
			// 超时时，cmd.Wait 是保证 stderr 拷贝 goroutine 已排空的那个同步点。用完整
			// 的有界尾部重新归类，避免最后一刻的目录/MCP 信号被误报成裸超时。成功等待则
			// 保留首次进展时拍下的快照，后续回合的 stderr 就不会污染首 item 遥测。
			if strings.HasSuffix(outcome, "_timeout") {
				classification = classifyCodexStartupStderr(stderrTail, true)
			}
			fields := []any{
				"phase", "first_item_wait",
				"task_id", b.cfg.TaskID,
				"runtime_id", b.cfg.RuntimeID,
				"pid", pid,
				"attempt", attempt,
				"active_launches", activeLaunches,
				"method", "turn/start",
				"thread_id", threadID,
				"turn_id", firstTurnID,
				"outcome", outcome,
				"latency", waitLatency.Round(time.Millisecond).String(),
				"latency_ms", waitLatency.Milliseconds(),
				"timeout", firstTurnNoProgressTimeout.String(),
				"semantic_inactivity_timeout", semanticInactivityTimeout.String(),
				"codex_version", codexVersion,
				"daemon_version", b.cfg.DaemonVersion,
				"cleanup_confirmed", cleanupConfirmed,
				"reaped", cleanupConfirmed,
				// retry_safe 描述的是终局那次 attempt，而不是本次被测量的等待区间，
				// 所以成功样本按设计应报 false。
				"retry_safe", startupRefreshRetrySafe,
				"stderr_model_refresh_failure_count", classification.modelRefreshFailure,
				"stderr_model_refresh_timeout_count", classification.modelRefreshTimeout,
				"stderr_mcp_init_transport_count", classification.mcpInitTransport,
				"stderr_bare_timeout_count", classification.bareTimeout,
			}
			switch outcome {
			case "progress", "turn_completed":
				b.cfg.Logger.Info("codex lifecycle", fields...)
			default:
				b.cfg.Logger.Warn("codex lifecycle", fields...)
			}
		}

		outputMu.Lock()
		finalOutput := codexDeliverableOutput(finalAnswer, lastAgentMessage)
		outputMu.Unlock()

		// 从累积的 codex 用量构建 usage map。先看 JSON-RPC 通知（对 Codex 常常是空的）。
		var usageMap map[string]TokenUsage
		c.usageMu.Lock()
		u := c.usage
		c.usageMu.Unlock()

		// 会话日志提供完整累计用量和实际模型，优先于可能仅上报部分用量的通知。Codex 会把
		// token_count 事件写到 $CODEX_HOME/sessions/YYYY/MM/DD/*.jsonl；这里扫的是
		// 本后端任务专属的 CODEX_HOME——会话在该目录里隔离，而不是共享的
		// ~/.codex/sessions（MUL-4424）。
		{
			taskCodexHome := strings.TrimSpace(b.cfg.Env["CODEX_HOME"])
			if scanned := scanCodexSessionUsage(startTime, taskCodexHome, threadID, resumed); scanned != nil {
				if scanned.usage.InputTokens > 0 || scanned.usage.OutputTokens > 0 || scanned.usage.CacheReadTokens > 0 || scanned.usage.CacheWriteTokens > 0 {
					u = scanned.usage
				}
				if scanned.model != "" {
					opts.Model = scanned.model
				}
			}
		}

		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
			model := opts.Model
			if model == "" {
				model = "unknown"
			}
			usageMap = map[string]TokenUsage{model: u}
		}

		resCh <- Result{
			Status:                       finalStatus,
			Output:                       finalOutput,
			Error:                        finalError,
			SessionID:                    threadID,
			DurationMs:                   duration.Milliseconds(),
			Usage:                        usageMap,
			codexStartupRefreshRetrySafe: startupRefreshRetrySafe,
		}
	}()

	supplement := func(supplementCtx context.Context, instruction string) error {
		if opts.MCPToolsOnly {
			return errors.New("supervision does not accept additional runtime input")
		}
		return supplementCodexTurn(supplementCtx, c, instruction)
	}
	supplementReady := func() bool {
		return !opts.MCPToolsOnly && c.getThreadID() != "" && c.activeTurnID() != ""
	}
	runCommand := func(commandCtx context.Context, command string) (string, error) {
		if opts.MCPToolsOnly {
			return "", errors.New("supervision does not accept native controls")
		}
		return c.runLiveCommand(commandCtx, runCtx, command, opts)
	}
	return &Session{RunCommand: runCommand, CommandReady: func() bool { return !opts.MCPToolsOnly && c.commandReady.Load() }, Supplement: supplement, SupplementReady: supplementReady, WaitingForInput: c.interactions.waiting, Messages: msgCh, Result: resCh}, nil
}

func supplementCodexTurn(ctx context.Context, c *codexClient, instruction string) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	threadID := c.getThreadID()
	turnID := c.activeTurnID()
	if threadID == "" || turnID == "" {
		return errors.New("codex turn has not started")
	}
	_, err := c.request(ctx, "turn/steer", map[string]any{
		"threadId":       threadID,
		"expectedTurnId": turnID,
		"input":          []map[string]any{{"type": "text", "text": instruction}},
	})
	return err
}

func resolveCodexHandshakeTimeouts(opts ExecOptions) (time.Duration, time.Duration) {
	handshakeTimeout := opts.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultCodexHandshakeTimeout
	}
	threadHandshakeTimeout := opts.ThreadHandshakeTimeout
	if threadHandshakeTimeout <= 0 {
		if opts.HandshakeTimeout > 0 {
			// 向后兼容：旧的显式覆盖历史上一并约束所有启动 RPC（含线程建立）。
			threadHandshakeTimeout = opts.HandshakeTimeout
		} else {
			threadHandshakeTimeout = defaultCodexThreadHandshakeTimeout
		}
	}
	return handshakeTimeout, threadHandshakeTimeout
}

// 本后端要前置的「上下文连续性提示」由调用方经 ExecOptions.ResumeContinuityNotice
// 提供，而不是写死在这里。以前它是本文件里的常量，与 daemon 各自维护一份同一段落的
// 拷贝，且本包无从知道自己跑在哪个界面上——只有知道对话是否仍可读，措辞才对（MUL-5722）。

// codexTurnInput 构造首轮 turn/start 的输入内容。当预期要 resume（resumeExpected）但
// 后端最终落在一个全新会话上（!resumed）时，它把调用方的连续性提示拼在最前面，避免这次
// 运行悄悄「当新会话」继续。提示与提示词折叠进同一个文本块，以符合 codex 单文本块
// 回合输入的要求。
//
// 提示为空说明调用方已在提示词里自述了上下文丢失，此处不得再加任何内容——这正是防止
// 同一段话出现两次的全部防线。
func codexTurnInput(prompt string, resumeExpected, resumed bool, notice string) []map[string]any {
	text := prompt
	if resumeExpected && !resumed {
		text = notice + prompt
	}
	return []map[string]any{{"type": "text", "text": text}}
}

// startOrResumeThread 依据 opts.ResumeSessionID 在 codex 的 thread/resume 与
// thread/start 之间做选择。提供了先前线程 ID 时先尝试 thread/resume；可恢复的协议错误
// （线程未知、schema 不匹配）会回退到 thread/start 让任务仍能执行，而传输/进程级失败则
// 快速失败——app-server 已经无法应答新的 start 请求。返回的 threadID 是后续 turn/start
// 必须引用的线程标识；resumed 表示是否接上了先前线程（仅用于日志）。
func (c *codexClient) startOrResumeThread(ctx context.Context, opts ExecOptions, logger *slog.Logger) (string, bool, error) {
	if priorThreadID := opts.ResumeSessionID; priorThreadID != "" {
		// thread/resume 会复用会话持久化的模型与推理强度；这里只覆盖 daemon 真正在意的
		// 字段。developerInstructions 保持 nil 的原因见下方 thread/start 的说明。
		resumeParams := map[string]any{
			"threadId":              priorThreadID,
			"config":                c.currentThreadConfig(),
			"cwd":                   opts.Cwd,
			"model":                 nilIfEmpty(opts.Model),
			"developerInstructions": nil,
			// Only omit history from the response, not the resumed model context.
			// We need the thread ID, not an unbounded single-line turns payload.
			"excludeTurns": true,
		}
		// 显式覆盖持久化的推理强度：不加这个，codex resume 会静默沿用旧会话建时的档位，
		// 即使 agent 的 thinking_level 之后已被用户改过。参见 MUL-2339——resume 必须
		// 服从「当前配置」而非「存下来的配置」。
		if opts.ApprovalPolicy == "never" {
			resumeParams["approvalPolicy"] = "never"
		}
		applyCodexReasoningEffort(resumeParams, opts.ThinkingLevel)
		applyCodexServiceTier(resumeParams, opts.ServiceTier)
		c.threadSetupMethod = "thread/resume"
		c.threadSetupStarted = time.Now()
		logger.Info("codex lifecycle",
			"phase", "thread_resume_sent",
			"task_id", c.cfg.TaskID,
			"runtime_id", c.cfg.RuntimeID,
			"pid", c.pid,
			"attempt", c.attempt,
			"active_launches", c.activeLaunches,
			"method", c.threadSetupMethod,
		)
		resumeResult, err := c.request(ctx, "thread/resume", resumeParams)
		if err == nil {
			if threadID := extractThreadID(resumeResult); threadID != "" {
				if c.cfg.CodexResumePath != "" {
					var restored struct {
						Thread struct {
							Path string `json:"path"`
						} `json:"thread"`
					}
					if json.Unmarshal(resumeResult, &restored) != nil || !sameCodexResidentRollout(restored.Thread.Path, c.cfg.CodexResumePath) || threadID != priorThreadID {
						logger.Warn("codex native resume identity mismatch", "expected_thread_id", priorThreadID, "actual_thread_id", threadID, "expected_path", c.cfg.CodexResumePath, "actual_path", restored.Thread.Path)
						return "", false, errors.New("codex native resume did not use its persistent conversation rollout")
					}
				}
				c.rememberNativeThreadModel(resumeResult)
				logger.Info("codex lifecycle",
					"phase", "thread_resume_response",
					"task_id", c.cfg.TaskID,
					"runtime_id", c.cfg.RuntimeID,
					"pid", c.pid,
					"attempt", c.attempt,
					"active_launches", c.activeLaunches,
					"method", c.threadSetupMethod,
					"latency", time.Since(c.threadSetupStarted).Round(time.Millisecond).String(),
					"latency_ms", time.Since(c.threadSetupStarted).Milliseconds(),
				)
				return threadID, true, nil
			}
			if c.cfg.CodexResumePath != "" {
				return "", false, errors.New("codex native resume did not return the original thread identity")
			}
			logger.Warn("codex thread/resume returned no thread ID; falling back to thread/start", "prior_thread_id", priorThreadID)
		} else {
			if isCodexTransportError(err) || c.cfg.CodexResumePath != "" {
				logger.Warn("codex thread/resume failed; replacement thread is prohibited", "prior_thread_id", priorThreadID, "error", err)
				return "", false, fmt.Errorf("codex thread/resume failed: %w", err)
			}
			logger.Warn("codex thread/resume failed; falling back to thread/start", "prior_thread_id", priorThreadID, "error", err)
		}
	}

	// 普通任务经工作目录 AGENTS.md 取得简报；监督禁用文件发现，通过原生开发者指令
	// 取得角色与绑定技能。两种投递路径各自只提供一份正文。
	startParams := map[string]any{
		"model":                  nilIfEmpty(opts.Model),
		"modelProvider":          nil,
		"profile":                nil,
		"cwd":                    opts.Cwd,
		"approvalPolicy":         nil,
		"sandbox":                nil,
		"config":                 c.currentThreadConfig(),
		"baseInstructions":       nil,
		"developerInstructions":  nil,
		"compactPrompt":          nil,
		"includeApplyPatchTool":  nil,
		"experimentalRawEvents":  false,
		"persistExtendedHistory": true,
	}
	if opts.MCPToolsOnly {
		startParams["environments"] = []any{}
		startParams["developerInstructions"] = nilIfEmpty(opts.SystemPrompt)
	}
	if opts.ApprovalPolicy == "never" {
		startParams["approvalPolicy"] = "never"
	}
	applyCodexReasoningEffort(startParams, opts.ThinkingLevel)
	applyCodexServiceTier(startParams, opts.ServiceTier)
	c.threadSetupMethod = "thread/start"
	c.threadSetupStarted = time.Now()
	logger.Info("codex lifecycle",
		"phase", "thread_start_sent",
		"task_id", c.cfg.TaskID,
		"runtime_id", c.cfg.RuntimeID,
		"pid", c.pid,
		"attempt", c.attempt,
		"active_launches", c.activeLaunches,
		"method", c.threadSetupMethod,
	)
	startResult, err := c.request(ctx, "thread/start", startParams)
	if err != nil {
		return "", false, fmt.Errorf("codex thread/start failed: %w", err)
	}
	threadID := extractThreadID(startResult)
	if threadID == "" {
		return "", false, fmt.Errorf("codex thread/start returned no thread ID")
	}
	c.rememberNativeThreadModel(startResult)
	logger.Info("codex lifecycle",
		"phase", "thread_start_response",
		"task_id", c.cfg.TaskID,
		"runtime_id", c.cfg.RuntimeID,
		"pid", c.pid,
		"attempt", c.attempt,
		"active_launches", c.activeLaunches,
		"method", "thread/start",
		"latency", time.Since(c.threadSetupStarted).Round(time.Millisecond).String(),
		"latency_ms", time.Since(c.threadSetupStarted).Milliseconds(),
	)
	c.trySetThreadName(ctx, threadID, opts.ThreadName, logger)
	return threadID, false, nil
}

func (c *codexClient) trySetThreadName(ctx context.Context, threadID, name string, logger *slog.Logger) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if err := c.setThreadName(ctx, threadID, name); err != nil {
		logger.Warn("codex thread/name/set failed; continuing without provider-native thread title",
			"thread_id", threadID, "error", err)
	}
}

func (c *codexClient) setThreadName(ctx context.Context, threadID, name string) error {
	_, err := c.request(ctx, "thread/name/set", map[string]any{
		"threadId": threadID,
		"name":     name,
	})
	return err
}

// applyCodexReasoningEffort 把各 agent 的 thinking_level 写进 codex app-server 请求。
// thread/start.config、thread/resume.config、turn/start.effort 这三个位置都经本 helper
// 汇入，将来协议/键名变化只改一处而不是三处（Trump 在 MUL-2339 review 中提出的约束）。
//
// 写入位置按参数键形状自动判断：
//   - turn/start 总是带 `input`，其 schema 把推理强度覆盖暴露为顶层 `effort` 字段。
//   - thread/start 与 thread/resume 把它嵌在 `config.model_reasoning_effort` 下。
//
// `level` 为空则什么都不做：调用方没有要求覆盖时我们刻意不发任何键，让上游默认
// （配置文件、账号级模型偏好）继续掌权。同时也保证 `effort: ""` 永远不会到 CLI——
// codex 拒绝该字段的空串。
func applyCodexReasoningEffort(params map[string]any, level string) {
	if params == nil || level == "" {
		return
	}
	if _, isTurnStart := params["input"]; isTurnStart {
		params["effort"] = level
		return
	}
	cfg, _ := params["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["model_reasoning_effort"] = level
	params["config"] = cfg
}

// applyCodexServiceTier 把目录持有的 service tier 写进 app-server 顶层 serviceTier 字段。
// 三种执行请求接受同样的形状。空值不做任何事，让 config.toml 保持权威。
func applyCodexServiceTier(params map[string]any, tier string) {
	if params == nil || tier == "" {
		return
	}
	params["serviceTier"] = tier
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

// codexFirstTurnNoProgressTimeout 解析首轮 watchdog 的上限：turn/started 之后，首轮在
// 被该 watchdog 判失败前最多可静默多久。这只是首轮定时器自身的时长——不是实际的首 item
// 等待。语义静默定时器是同时上膛的（同一次 status:running 会重置它并启动这个），所以真实
// 等待 = min(本值, semanticInactivityTimeout, 执行超时)。把配置值抬到语义超时之上并不能
// 延长等待，除非语义超时也一起抬——LoadConfig 在没抬时会告警。交互细节见运行循环里
// 两个定时器竞争的 select。
//
// 显式配置的覆盖（MULTICA_CODEX_FIRST_TURN_TIMEOUT）原样生效、允许上调：运营者如果
// 的 app-server 出首个事件确实慢（重的 MCP 启动、冷模型目录），可以把上限抬过默认值——
// 语义静默超时单独永远抬不到这个默认值之上（GH #3262 / #5959）。
//
// 没有覆盖时行为与过去逐字节一致：只用默认上限，配置的语义静默超时只能把它往下压、
// 永远无法上抬。`* 4 / 5` 不会溢出，因为该分支只在
// 0 < semanticInactivityTimeout <= defaultCodexFirstTurnNoProgressTimeout 时执行。
func codexFirstTurnNoProgressTimeout(semanticInactivityTimeout, configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if semanticInactivityTimeout <= 0 || semanticInactivityTimeout > defaultCodexFirstTurnNoProgressTimeout {
		return defaultCodexFirstTurnNoProgressTimeout
	}
	scaled := semanticInactivityTimeout * 4 / 5
	if scaled <= 0 {
		return semanticInactivityTimeout
	}
	return scaled
}

func isCodexFirstTurnProgressActivity(activity string) bool {
	return activity != "" && activity != "status:running" && activity != "error:retry"
}

func buildCodexTimeoutDiagnosticError(diag codexTimeoutDiagnostic, stderrTail string) string {
	stderrTail = sanitizeCodexDiagnostic(stderrTail)
	var msg string
	switch diag.Kind {
	case codexTimeoutFirstTurnNoProgress:
		msg = fmt.Sprintf("%s after %s: received turn start but no item, message, tool, turn/completed, or error event (%s)",
			CodexFirstTurnNoProgressMarker,
			diag.Timeout,
			formatCodexDiagnosticFields(diag),
		)
	case codexTimeoutSemanticInactivity:
		msg = fmt.Sprintf("%s after %s without agent progress (last activity: %s; %s)",
			CodexSemanticInactivityMarker,
			diag.Timeout,
			nonEmptyCodexDiagnosticValue(diag.LastActivity),
			formatCodexDiagnosticFields(diag),
		)
	default:
		msg = "codex timed out"
	}
	msg = appendCodexKnownStderrHint(msg, stderrTail)
	return withAgentStderr(msg, "codex", stderrTail)
}

func formatCodexDiagnosticFields(diag codexTimeoutDiagnostic) string {
	return fmt.Sprintf("codex_version=%q thread_id=%q turn_id=%q model=%q",
		nonEmptyCodexDiagnosticValue(diag.CodexVersion),
		nonEmptyCodexDiagnosticValue(diag.ThreadID),
		nonEmptyCodexDiagnosticValue(diag.TurnID),
		formatCodexDiagnosticModel(diag.Model),
	)
}

func nonEmptyCodexDiagnosticValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func formatCodexDiagnosticModel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "default(empty)"
	}
	return model
}

func appendCodexKnownStderrHint(msg, stderrTail string) string {
	if strings.Contains(stderrTail, codexModelCatalogRefreshFailureSignal) {
		return msg + "; diagnosis: Codex could not load its model catalog, which blocks the first turn. This is usually a transient network failure reaching the Codex service. Check network/proxy connectivity and retry the task, or switch to another runtime while the Codex service is unreachable"
	}
	return msg
}

func detectCodexVersionForDiagnostics(ctx context.Context, runtimeCmd Command, env []string, logger *slog.Logger) string {
	versionCtx, cancel := context.WithTimeout(ctx, codexVersionDiagnosticTimeout)
	defer cancel()

	cmd := runtimeCmd.exec(versionCtx, "--version")
	cmd.Env = env
	data, err := outputOwned(cmd, logger)
	if err != nil {
		if logger != nil {
			logger.Debug("codex version diagnostic failed", "error", err)
		}
		return "unknown"
	}
	version, _ := extractVersionLine(string(data))
	if strings.TrimSpace(version) == "" {
		return "unknown"
	}
	return version
}

func trySendString(ch chan<- string, value string) {
	select {
	case ch <- value:
	default:
	}
}

// codexDeliverableOutput 从回合产物中挑出 Result.Output：优先用 app-server 自己标注了
// `phase: "final_answer"` 的消息；旧式 `agent_message` 协议没有 phase，则回退到最近的
// agent 消息。绝不把所有消息拼起来：中间那些消息是在叙述工具调用之间的过程，属于
// transcript，不该进聊天/IM 回复（GH #6006）。
func codexDeliverableOutput(finalAnswer, lastAgentMessage string) string {
	if finalAnswer != "" {
		return finalAnswer
	}
	return lastAgentMessage
}

func logCodexAgentMessage(logger *slog.Logger, msg Message) {
	if logger == nil {
		return
	}
	attrs := []any{
		"type", string(msg.Type),
		"tool", msg.Tool,
		"call_id", msg.CallID,
		"status", msg.Status,
		"content_len", len(msg.Content),
		"output_len", len(msg.Output),
	}
	logger.Info("codex agent message received", attrs...)
	if msg.Type == MessageToolResult {
		logger.Info("codex tool_result observed", "tool", msg.Tool, "call_id", msg.CallID, "output_len", len(msg.Output))
	}
}

func describeCodexSemanticActivity(msg Message) string {
	switch msg.Type {
	case MessageToolUse, MessageToolResult:
		if msg.Tool != "" {
			return fmt.Sprintf("%s:%s", msg.Type, msg.Tool)
		}
	case MessageStatus:
		if msg.Status != "" {
			return fmt.Sprintf("%s:%s", msg.Type, msg.Status)
		}
	}
	return string(msg.Type)
}

// ── codexClient: JSON-RPC 2.0 transport ──

type codexClient struct {
	mcpToolsOnly           bool
	threadConfig           map[string]any
	threadClosed           chan struct{}
	threadClosedOnce       sync.Once
	approvalPolicy         string
	cfg                    Config
	stdin                  interface{ Write([]byte) (int, error) }
	mu                     sync.Mutex
	writeMu                sync.Mutex
	interactions           *interactionBroker
	runtimeToolCompletions map[string]bool
	nextID                 int
	pending                map[int]*pendingRPC
	processDone            chan struct{}
	processErr             error
	handshakeTimeout       time.Duration
	threadHandshakeTimeout time.Duration
	pid                    int
	attempt                int
	activeLaunches         int64
	threadSetupMethod      string
	threadSetupStarted     time.Time
	threadIDMu             sync.RWMutex
	turnIDMu               sync.RWMutex
	threadID               string
	threadModel            string
	lastTurnID             string
	turnID                 string
	onMessage              func(Message)
	// onAgentMessageChunk reports whether a text chunk was handed to the
	// daemon-facing message channel. Raw delta reconciliation advances only on
	// true, so channel pressure cannot turn observed provider bytes into a false
	// delivered prefix. Unit clients may leave this nil and use onMessage.
	onAgentMessageChunk func(text string) bool
	// onAgentMessage receives the authoritative completed text for one agent
	// message. onAgentMessageChunk may deliver that text incrementally, so
	// Result.Output fallbacks must use this callback rather than the last chunk.
	onAgentMessage           func(text string)
	onSemanticActivity       func(description string)
	onTurnDone               func(aborted bool)
	guardianRateLimitCircuit *codexGuardianRateLimitCircuit
	onGuardianRateLimit      func()
	// onFinalAnswer fires only for an agent message the app-server itself
	// labelled `phase: "final_answer"` — the turn's deliverable, as opposed to
	// the intermediate agent messages that narrate work between tool calls.
	// Both still reach onMessage: the transcript keeps the whole timeline, only
	// Result.Output is narrowed to the deliverable (GH #6006).
	onFinalAnswer func(text string)
	// acceptNotification 把当前回合与 thread/resume 恢复旧对话时重放的「同线程历史」
	// 隔离开。协议级单测把它留为 nil，直接驱动 dispatch。
	acceptNotification func(method string, params map[string]any) bool
	// onDiscardedNotification 保留带外安全信号（比如观测到活动后抑制 initialize 重试），
	// 同时不让被过滤掉的历史去改动当前回合的输出或生命周期状态。
	onDiscardedNotification func(method string, params map[string]any)
	// agentMessageStreams tracks raw-v2 agent text on the single stdout reader.
	// The first delta is handed off immediately; subsequent deltas are aggregated
	// into bounded chunks and reconciled against item/completed. delivered is
	// advanced only after onAgentMessageChunk confirms daemon handoff.
	agentMessageStreams map[string]*codexAgentMessageStream
	agentMessageOrder   []string

	notificationProtocol string // "unknown"、"legacy"、"raw"
	turnCompleted        bool
	completedWithoutID   bool
	goalActive           atomic.Bool
	unloadedGoalControl  bool
	goalEngaged          atomic.Bool
	goalMutationSequence atomic.Uint64
	goalControlUncertain atomic.Bool
	goalStatus           atomic.Value // string，原生目标停止原因
	goalStateMu          sync.Mutex
	goalRevision         uint64
	goalConfirmations    map[chan struct{}]codexGoalConfirmation
	commandMu            sync.Mutex
	commandReady         atomic.Bool
	settingsMu           sync.Mutex
	settingsWaiters      map[chan struct{}]codexPlanSelection

	usageMu       sync.Mutex
	usage         TokenUsage // 由回合事件累积而来
	modernUsage   bool
	usageTotal    codexRawTokenUsage
	usageTotalSet bool

	turnErrorMu sync.Mutex
	turnError   string // captured from turn/completed status=failed or terminal error notifications
}

type codexAgentMessageStream struct {
	delivered strings.Builder
	pending   strings.Builder
}

// codexTurnNotificationGate keeps resume-time history replay from mutating the
// output or ending the new turn. Codex app-server can emit notifications before
// the turn/start RPC response, so the gate is armed before that request and uses
// turn/started (or legacy task_started) as the actual current-turn boundary.
// Protocols that omit those start events also omit a reliable current-turn ID;
// for compatibility, their post-arm events remain accepted. We therefore rely
// on the single stdout reader's ordering guarantee that resume history emitted
// before the thread/resume response is processed while the gate is still closed.
// A replay emitted after arm but before a start event cannot be distinguished
// from a valid legacy current-turn event without breaking those older streams.
// Its mutable lifecycle fields are only touched by the stdout reader goroutine;
// armed is atomic because the lifecycle goroutine flips it.
type codexTurnNotificationGate struct {
	armed   atomic.Bool
	started bool
	turnID  string
}

func (g *codexTurnNotificationGate) arm() {
	g.armed.Store(true)
}

func (g *codexTurnNotificationGate) accept(method string, params map[string]any) bool {
	if !g.armed.Load() {
		return false
	}

	if method == "codex/event" || strings.HasPrefix(method, "codex/event/") {
		msg, _ := params["msg"].(map[string]any)
		msgType, _ := msg["type"].(string)
		if msgType == "task_started" {
			g.started = true
			return true
		}
		// 旧版 codex 事件流可能省略 task_started。一旦 turn/start 已上闩，就保留这份
		// 兼容；上闩前的重放仍会被排除。
		return true
	}

	switch {
	case method == "turn/started":
		g.started = true
		g.turnID = extractNestedString(params, "turn", "id")
		return true
	case method == "turn/completed":
		if !g.started {
			// 更老的 app-server 版本可能不先发 turn/started 就直接完成一个回合。
			// 上闩前的边界仍会挡掉 resume 重放，这个分支让这些老版本保持可用。
			return true
		}
		turnID := extractNestedString(params, "turn", "id")
		return g.turnID == "" || turnID == "" || turnID == g.turnID
	case method == "thread/status/changed" || strings.HasPrefix(method, "item/"):
		if !g.started {
			return true
		}
		turnID, _ := params["turnId"].(string)
		return g.turnID == "" || turnID == "" || turnID == g.turnID
	default:
		// 终态错误可能是失败的 turn/start 产出的第一条通知，即使没有 turn/started
		// 也必须保持可见。
		return true
	}
}

func (c *codexClient) setTurnError(msg string) {
	if msg == "" {
		return
	}
	c.turnErrorMu.Lock()
	defer c.turnErrorMu.Unlock()
	if c.turnError == "" {
		c.turnError = msg
	}
}

func (c *codexClient) getTurnError() string {
	c.turnErrorMu.Lock()
	defer c.turnErrorMu.Unlock()
	return c.turnError
}

// setThreadID publishes the thread ID resolved by thread/start or
// thread/resume. The task goroutine writes it while the stdout goroutine is
// already dispatching notifications for that same thread, so the field needs
// the same synchronization as turnID (GH #8422).
func (c *codexClient) setThreadID(threadID string) {
	c.threadIDMu.Lock()
	c.threadID = threadID
	c.threadIDMu.Unlock()
}

func (c *codexClient) getThreadID() string {
	c.threadIDMu.RLock()
	defer c.threadIDMu.RUnlock()
	return c.threadID
}

func (c *codexClient) setActiveTurnID(turnID string) {
	c.turnIDMu.Lock()
	if turnID != "" {
		c.lastTurnID = turnID
	}
	c.turnID = turnID
	c.turnIDMu.Unlock()
}

// usageTurnID retains attribution after the turn closes its steering window.
func (c *codexClient) usageTurnID() string {
	c.turnIDMu.RLock()
	defer c.turnIDMu.RUnlock()
	return c.lastTurnID
}

func (c *codexClient) activeTurnID() string {
	c.turnIDMu.RLock()
	defer c.turnIDMu.RUnlock()
	return c.turnID
}

type pendingRPC struct {
	ch               chan rpcResult
	method           string
	goalConfirmation chan struct{}
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

type codexHandshakeTimeoutError struct {
	Method  string
	Timeout time.Duration
}

func (e *codexHandshakeTimeoutError) Error() string {
	return fmt.Sprintf("%s: %s did not respond after %s", CodexHandshakeTimeoutMarker, e.Method, e.Timeout)
}

func (e *codexHandshakeTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

func isCodexHandshakeRPC(method string) bool {
	switch method {
	case "initialize", "thread/start", "thread/resume", "thread/name/set", "turn/start":
		return true
	default:
		return false
	}
}

func isCodexThreadSetupRPC(method string) bool {
	return method == "thread/start" || method == "thread/resume"
}

func (c *codexClient) handshakeTimeoutFor(method string) time.Duration {
	switch method {
	case "thread/start", "thread/resume":
		if c.threadHandshakeTimeout > 0 {
			return c.threadHandshakeTimeout
		}
		return c.handshakeTimeout
	default:
		return c.handshakeTimeout
	}
}

func codexRequestContextError(ctx context.Context) error {
	var handshakeErr *codexHandshakeTimeoutError
	if errors.As(context.Cause(ctx), &handshakeErr) {
		return handshakeErr
	}
	return ctx.Err()
}

// interruptCodexTurn follows the app-server's native stop protocol before the
// cancelled execution tears down its process. Current app-server versions emit
// final usage in thread/tokenUsage/updated immediately before turn/completed,
// so an immediate process-group SIGKILL loses otherwise valid local token
// accounting for cancelled/replaced tasks. The bool reports whether the turn
// reached that authoritative completion boundary; callers must force-stop the
// process tree when it is false.
func interruptCodexTurn(c *codexClient, threadID string, turnDone <-chan bool, configuredTimeout time.Duration, logger *slog.Logger) bool {
	c.commandReady.Store(false)
	c.commandMu.Lock()
	defer c.commandMu.Unlock()
	if logger == nil {
		logger = slog.Default()
	}
	if (c.goalActive.Load() || c.goalControlUncertain.Load()) && threadID != "" {
		c.pauseGoalBeforeStop(threadID, logger)
	}
	select {
	case <-turnDone:
		// Completion won the cancellation race; usage has already been captured.
		return true
	default:
	}

	turnID := c.activeTurnID()
	if threadID == "" || turnID == "" {
		return false
	}
	interruptTimeout := configuredTimeout
	if interruptTimeout <= 0 {
		interruptTimeout = defaultCodexTurnInterruptTimeout
	}
	started := time.Now()

	interruptCtx, cancelInterrupt := context.WithTimeout(context.Background(), interruptTimeout)
	defer cancelInterrupt()
	_, err := c.request(interruptCtx, "turn/interrupt", map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	})
	if err != nil {
		logger.Warn("codex turn interrupt failed", "thread_id", threadID, "turn_id", turnID, "error", err, "latency", time.Since(started).Round(time.Millisecond).String(), "timeout", interruptTimeout.String())
		return false
	}

	select {
	case <-turnDone:
		logger.Info("codex turn interrupted", "thread_id", threadID, "turn_id", turnID, "latency", time.Since(started).Round(time.Millisecond).String(), "timeout", interruptTimeout.String())
		return true
	case <-interruptCtx.Done():
		logger.Warn("codex turn interrupt completion timed out", "thread_id", threadID, "turn_id", turnID, "latency", time.Since(started).Round(time.Millisecond).String(), "timeout", interruptTimeout.String())
		return false
	}
}

func (c *codexClient) request(ctx context.Context, method string, params any) (response json.RawMessage, requestErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	requestCtx := ctx
	cancelRequest := func() {}
	if timeout := c.handshakeTimeoutFor(method); timeout > 0 && isCodexHandshakeRPC(method) {
		timeoutErr := &codexHandshakeTimeoutError{Method: method, Timeout: timeout}
		requestCtx, cancelRequest = context.WithTimeoutCause(ctx, timeout, timeoutErr)
	} else if strings.HasPrefix(method, "thread/goal/") {
		requestCtx, cancelRequest = context.WithTimeout(ctx, 5*time.Second)
	}
	defer cancelRequest()

	c.mu.Lock()
	if c.processErr != nil {
		err := c.processErr
		c.mu.Unlock()
		return nil, err
	}
	if c.processDone == nil {
		c.processDone = make(chan struct{})
	}
	processDone := c.processDone
	c.nextID++
	id := c.nextID
	pr := &pendingRPC{ch: make(chan rpcResult, 1), method: method}
	if method == "thread/goal/set" || method == "thread/goal/clear" {
		pr.goalConfirmation = make(chan struct{}, 1)
	}
	c.pending[id] = pr
	c.mu.Unlock()
	if pr.goalConfirmation != nil {
		defer func() { c.goalStateMu.Lock(); delete(c.goalConfirmations, pr.goalConfirmation); c.goalStateMu.Unlock() }()
	}

	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	data = append(data, '\n')
	if pr.goalConfirmation != nil {
		c.goalMutationSequence.Add(1)
		c.goalControlUncertain.Store(true)
		defer func() {
			if requestErr == nil {
				c.goalControlUncertain.Store(false)
			}
		}()
	}
	if _, err := c.write(data); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write %s: %w", method, err)
	}
	if method == "turn/start" {
		threadID := ""
		if paramMap, ok := params.(map[string]any); ok {
			threadID, _ = paramMap["threadId"].(string)
		}
		c.cfg.Logger.Info("codex turn/start sent", "request_id", id, "thread_id", threadID)
	}

	returnResponse := func(res rpcResult) (json.RawMessage, error) {
		if res.err == nil && pr.goalConfirmation != nil {
			select {
			case <-pr.goalConfirmation:
				return res.result, nil
			default:
			}
			select {
			case <-pr.goalConfirmation:
			case <-requestCtx.Done():
				return nil, codexRequestContextError(requestCtx)
			case <-processDone:
				select {
				case <-pr.goalConfirmation:
					return res.result, nil
				default:
					return nil, errors.New("codex exited before confirming goal control")
				}
			}
		}
		return res.result, res.err
	}
	select {
	case res := <-pr.ch:
		return returnResponse(res)
	case <-processDone:
		select {
		case res := <-pr.ch:
			return returnResponse(res)
		default:
		}
		c.mu.Lock()
		delete(c.pending, id)
		err := c.processErr
		c.mu.Unlock()
		if requestCtx.Err() != nil {
			return nil, codexRequestContextError(requestCtx)
		}
		if err == nil {
			err = errCodexProcessExited
		}
		return nil, err
	case <-requestCtx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, codexRequestContextError(requestCtx)
	}
}

func (c *codexClient) notify(method string) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	_, _ = c.write(data)
}

func (c *codexClient) respond(id any, result any) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	_, _ = c.write(data)
}

func (c *codexClient) respondError(id any, code int, message string) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	data, _ := json.Marshal(msg)
	data = append(data, '\n')
	_, _ = c.write(data)
}

func (c *codexClient) closeAllPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, pr := range c.pending {
		pr.ch <- rpcResult{err: err}
		delete(c.pending, id)
	}
}

func (c *codexClient) markProcessExited(err error) {
	if err == nil {
		err = errCodexProcessExited
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.processErr == nil {
		c.processErr = err
		if c.processDone != nil {
			close(c.processDone)
		}
	}
	for id, pr := range c.pending {
		pr.ch <- rpcResult{err: err}
		delete(c.pending, id)
	}
}

func (c *codexClient) getProcessErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.processErr
}

// isCodexResumeOverflow 判断一次失败的 startOrResumeThread 是否因为 thread/resume 的
// 响应大到塞不进我们的 stdout 行缓冲。
//
// codex 把整个会话序列化进那单条响应，所以足够长的会话会溢出
// agentStreamMaxLineBytes，读取 goroutine 因 bufio.ErrTooLong 而死。其它传输错误都
// 意味着 codex 没了；唯独这种意味着 codex 好好的、是我们读不动。这个区别正是把它当
// 成 resume「被拒」的原因：会话根本交接不过来，只有重新开始能治好——这正是
// Result.ResumeRejected 的语义，也是 shouldRetryWithFreshSession 首先寻找的证据。
//
// 恢复必须走 daemon 而不是本地 thread/start 兜底。到这里时读取 goroutine 已退出，
// codex 正堵在往无人读取的管道写超大行的剩余部分，本进程已无法应答任何 RPC。
// daemon 的 fresh-session 重试会重新 exec codex，这是唯一的出路（MUL-5722）。
//
// 要求 ResumeSessionID：thread/start 响应的溢出是另一种失败，改会话指针治不了。
func isCodexResumeOverflow(opts ExecOptions, err error) bool {
	return opts.ResumeSessionID != "" && errors.Is(err, bufio.ErrTooLong)
}

func isCodexTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errCodexProcessExited) {
		return true
	}
	var handshakeErr *codexHandshakeTimeoutError
	if errors.As(err, &handshakeErr) {
		return true
	}
	return strings.HasPrefix(err.Error(), "write ")
}

func (c *codexClient) handleLine(line string) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return
	}

	// 判断这是否是对我方请求的响应
	if _, hasID := raw["id"]; hasID {
		if _, hasResult := raw["result"]; hasResult {
			c.handleResponse(raw)
			return
		}
		if _, hasError := raw["error"]; hasError {
			c.handleResponse(raw)
			return
		}
		// 服务端主动发来的请求（带 id + method，需应答）
		if _, hasMethod := raw["method"]; hasMethod {
			c.handleServerRequest(raw)
			return
		}
	}

	// 通知（无 id、有 method）
	if _, hasMethod := raw["method"]; hasMethod {
		c.handleNotification(raw)
	}
}

func (c *codexClient) handleResponse(raw map[string]json.RawMessage) {
	var id int
	if err := json.Unmarshal(raw["id"], &id); err != nil {
		return
	}

	c.mu.Lock()
	pr, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()

	if !ok {
		return
	}

	if errData, hasErr := raw["error"]; hasErr {
		var rpcErr struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(errData, &rpcErr)
		pr.ch <- rpcResult{err: fmt.Errorf("%s: %s (code=%d)", pr.method, rpcErr.Message, rpcErr.Code)}
	} else {
		if pr.goalConfirmation != nil {
			if err := c.prepareGoalConfirmation(pr, raw["result"]); err != nil {
				pr.ch <- rpcResult{err: err}
				return
			}
		}
		if pr.method == "turn/start" {
			c.adoptTurnStartResponse(raw["result"])
		}
		pr.ch <- rpcResult{result: raw["result"]}
	}
}

func (c *codexClient) handleServerRequest(raw map[string]json.RawMessage) {
	id := raw["id"]

	var method string
	_ = json.Unmarshal(raw["method"], &method)
	if c.approvalPolicy == "never" {
		switch method {
		case "item/commandExecution/requestApproval", "execCommandApproval", "item/fileChange/requestApproval", "applyPatchApproval":
			c.respond(id, map[string]any{"decision": "decline"})
			return
		case "item/permissions/requestApproval":
			c.respond(id, map[string]any{"permissions": map[string]any{}, "scope": "turn"})
			return
		case "item/tool/requestUserInput", "tool/requestUserInput":
			c.respondError(id, -32000, "runtime policy prohibits requesting user input")
			return
		}
	}
	if c.interactions != nil && (method == "item/tool/requestUserInput" || method == "tool/requestUserInput" ||
		(c.interactions.request != nil && (method == "item/commandExecution/requestApproval" || method == "item/fileChange/requestApproval" || method == "item/permissions/requestApproval" || method == "execCommandApproval" || method == "applyPatchApproval"))) {
		c.handleInteractionRequest(id, method, raw["params"])
		return
	}

	// daemon 模式下自动批准所有 exec/patch 类请求：没有人在屏幕前点「允许」。
	switch method {
	case "item/commandExecution/requestApproval", "execCommandApproval":
		c.respond(id, map[string]any{"decision": "accept"})
	case "item/fileChange/requestApproval", "applyPatchApproval":
		c.respond(id, map[string]any{"decision": "accept"})
	case "item/permissions/requestApproval":
		c.respond(id, codexPermissionsApprovalResponse(raw["params"], c.cfg.Logger))
	case "mcpServer/elicitation/request":
		if c.approvalPolicy == "never" {
			c.respond(id, map[string]any{"action": "decline", "content": nil, "_meta": nil})
			return
		}
		c.respond(id, map[string]any{"action": "accept", "content": nil, "_meta": nil})
	default:
		msg := fmt.Sprintf("unsupported codex app-server request: %s", method)
		c.cfg.Logger.Warn("codex: unhandled server request", "method", method, "id", id)
		c.setTurnError(msg)
		c.respondError(id, -32601, msg)
	}
}

// codexPermissionsApprovalResponse 构造对 codex item/permissions/requestApproval
// 服务端请求的自动授权应答。daemon 模式下没有人可审批，于是把请求的 network /
// fileSystem profile 原样回显，并把范围限定在当前回合——与 handleServerRequest 里
// 其它自动接受分支一致。
//
// 授权刻意只限于我们认识的 network / fileSystem 键。解析失败与被丢弃的键都会记日志，
// 这样未来 app-server 协议若新增权限形态，能在 daemon 日志里显形，而不是被静默收窄。
func codexPermissionsApprovalResponse(params json.RawMessage, logger *slog.Logger) map[string]any {
	var payload struct {
		Permissions map[string]any `json:"permissions"`
	}
	if err := json.Unmarshal(params, &payload); err != nil && logger != nil {
		logger.Warn("codex: failed to parse permission approval request; granting empty turn-scoped profile", "error", err)
	}

	granted := map[string]any{}
	var dropped []string
	for key, value := range payload.Permissions {
		switch key {
		case "network", "fileSystem":
			if value != nil {
				granted[key] = value
			}
		default:
			dropped = append(dropped, key)
		}
	}
	if len(dropped) > 0 && logger != nil {
		sort.Strings(dropped)
		logger.Warn("codex: dropping unrecognized permission keys from approval request; add explicit handling if the app-server protocol expanded", "keys", dropped)
	}

	return map[string]any{
		"permissions": granted,
		"scope":       "turn",
	}
}

func (c *codexClient) handleNotification(raw map[string]json.RawMessage) {
	var method string
	_ = json.Unmarshal(raw["method"], &method)

	var params map[string]any
	if p, ok := raw["params"]; ok {
		_ = json.Unmarshal(p, &params)
	}
	// 在当前回合门之前先过滤多路复用的子 agent 线程。门会在 turn/started 时改动
	// started/turnID；若让别的线程先到，可能把主回合 ID 顶掉，让后续主线 item/完成事件
	// 看着像过期数据。
	if c.isNotificationFromOtherThread(params) {
		return
	}
	if method == "thread/closed" && c.threadClosed != nil {
		c.threadClosedOnce.Do(func() { close(c.threadClosed) })
		return
	}
	if method == "serverRequest/resolved" && c.interactions != nil {
		id, _ := json.Marshal(params["requestId"])
		c.interactions.resolve(string(id))
		return
	}
	// resume 的目标快照属于当前持久状态；即使历史回合通知被门挡住，
	// 也必须在新回合结束前知道是否仍由原生 Goal 负责续跑。
	c.observeGoalState(method, params)
	c.observeThreadSettings(method, params)
	if c.acceptNotification != nil && !c.acceptNotification(method, params) {
		if c.onDiscardedNotification != nil {
			c.onDiscardedNotification(method, params)
		}
		return
	}

	// 旧式 codex/event 通知
	if method == "codex/event" || strings.HasPrefix(method, "codex/event/") {
		c.notificationProtocol = "legacy"
		msgData, ok := params["msg"]
		if !ok {
			return
		}
		msgMap, ok := msgData.(map[string]any)
		if !ok {
			return
		}
		c.handleEvent(msgMap)
		return
	}

	// 原始 v2 通知
	if c.notificationProtocol != "legacy" {
		if c.notificationProtocol == "unknown" &&
			(method == "turn/started" || method == "turn/completed" || method == "thread/tokenUsage/updated" ||
				method == "thread/started" || method == "error" || strings.HasPrefix(method, "item/")) {
			c.notificationProtocol = "raw"
		}

		if c.notificationProtocol == "raw" {
			c.handleRawNotification(method, params)
		}
	}
}

// codexPatchInputMaxBytes 限制单条 patch_apply 事件记录的 diff/content 总字节数。
//
// 需要上限是因为旧式协议把 add/delete 报成整文件内容而不是 diff，单个生成文件可能任意大。
// 上限刻意只加在这个新的 codex 载荷上：其它供应商的工具输入本就不设限，在这里硬截断会
// 静默截坏今天正常渲染的 transcript。把上限统一到持久化边界是另一项独立改动。
const codexPatchInputMaxBytes = 64 * 1024

// codexNormalizeLegacyChanges 把旧式 `patch_apply_*` 的 changes map 转成归一化变更列表。
//
// 旧式线上形态是 map[path]FileChange，FileChange 内部按 `type` 打标，且每种变体带的载荷
// 字段*不同*：add/delete 带整文件 `content`，update 带 `unified_diff` 加可选 `move_path`。
// 没有哪种形态对所有情况都是一条普通 unified diff，所以归一化形式把 `diff` 与 `content`
// 作为互斥可选字段保留，而不是并成一个字段。
func codexNormalizeLegacyChanges(raw any) []any {
	changes, ok := raw.(map[string]any)
	if !ok || len(changes) == 0 {
		return nil
	}

	// Go 的 map 遍历顺序随机；排序后重放事件能得到稳定的 transcript，而不是每次运行
	// 都重排文件列表。
	paths := make([]string, 0, len(changes))
	for path := range changes {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	out := make([]any, 0, len(paths))
	for _, path := range paths {
		entry, ok := changes[path].(map[string]any)
		if !ok {
			continue
		}
		norm := map[string]any{"path": path}
		if kind, _ := entry["type"].(string); kind != "" {
			norm["kind"] = kind
		}
		if diff, _ := entry["unified_diff"].(string); diff != "" {
			norm["diff"] = diff
		}
		// 看「是否存在」而不是「是否非空」：新增的空文件 content 为 ""，仍应渲染成
		// 空 body 而非缺失。
		if content, ok := entry["content"].(string); ok {
			norm["content"] = content
		}
		if movePath, _ := entry["move_path"].(string); movePath != "" {
			norm["move_path"] = movePath
		}
		out = append(out, norm)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// codexNormalizeRawChanges 把 v2 app-server `fileChange` item 的 changes 数组转成
// 归一化变更列表。
//
// 与旧式形态不同，这里是 FileUpdateChange{path, kind, diff} 的有序数组，且 `kind` 是
// *对象*（{type, move_path?}）而不是字符串——当字符串读会得到 ""，静默丢失
// add/delete/update 的区别。
//
// `diff` 字段的名字有误导性：上游 format_file_change_diff 只对 `update` 产出 unified
// diff。对 `add`/`delete` 它把整文件内容原样返回；对带移动的 `update` 会追加一行尾部
// "\n\nMoved to: <path>"。把新增文件的内容喂给 diff 解析器会让每一行都被误标——甚至把
// 恰好以 '+'/'-' 开头的行含义颠倒——所以载荷按 `kind` 分流，而不是按字段名猜。
func codexNormalizeRawChanges(raw any) []any {
	changes, ok := raw.([]any)
	if !ok || len(changes) == 0 {
		return nil
	}
	out := make([]any, 0, len(changes))
	for _, change := range changes {
		entry, ok := change.(map[string]any)
		if !ok {
			continue
		}
		norm := map[string]any{}
		if path, _ := entry["path"].(string); path != "" {
			norm["path"] = path
		}
		kind := ""
		movePath := ""
		switch k := entry["kind"].(type) {
		case map[string]any:
			kind, _ = k["type"].(string)
			movePath, _ = k["move_path"].(string)
		case string:
			// 容忍扁平化形式：未来协议若有小改动，退化成一个有标签的变更而不是无标签的。
			kind = k
		}
		if kind != "" {
			norm["kind"] = kind
		}
		if movePath != "" {
			norm["move_path"] = movePath
		}

		body, bodyPresent := entry["diff"].(string)
		switch kind {
		case "add", "delete":
			// 虽是整文件内容，却从 `diff` 字段到达。按「字段是否存在」而非「是否非空」
			// 判定，让新增的空文件仍渲染成空 body 而不是缺失。
			if bodyPresent {
				norm["content"] = body
			} else if content, ok := entry["content"].(string); ok {
				norm["content"] = content
			}
		default:
			if body != "" {
				norm["diff"] = stripCodexMovedToSuffix(body, movePath)
			} else if content, ok := entry["content"].(string); ok {
				norm["content"] = content
			}
		}
		if len(norm) == 0 {
			continue
		}
		out = append(out, norm)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stripCodexMovedToSuffix 去掉上游追加在被移动文件 unified diff 末尾的
// "Moved to: <path>" 行。目标路径已由 move_path 携带，留着这句话会在 diff 结尾渲染成
// 两条多余的上下文行。
func stripCodexMovedToSuffix(diff, movePath string) string {
	if movePath == "" {
		return diff
	}
	suffix := "\n\nMoved to: " + movePath
	return strings.TrimSuffix(diff, suffix)
}

// codexPatchInput 把归一化后的变更包进 tool_use 的 input，并施加大小上限。当没有任何
// 东西可归一化时返回 nil——畸形或未识别的事件退化成以前那种无载荷行为，而不是弄坏
// transcript。
func codexPatchInput(changes []any) map[string]any {
	if len(changes) == 0 {
		return nil
	}
	// 在任何改写 body 之前先量一遍，这样报给读取方的是他们实际产出的补丁大小。
	originalBytes := codexPatchBodyBytes(changes)

	// 脱敏必须先于大小预算，绝不能反过来。好几条规则只把「整个凭据」当作整体匹配——
	// PEM 规则需要 BEGIN 与 END 两个标记——所以按预算截断 body 可能把私钥的前半截留在
	// 任何模式都再也匹配不到的文本里。daemon 与 server 在更下游还会各自脱敏，但两者都
	// 无法恢复已被截断弄得面目全非的密钥。
	safe, ok := redact.InputMap(map[string]any{"changes": changes})["changes"].([]any)
	if !ok {
		safe = changes
	}

	input := map[string]any{"changes": safe}
	// 以脱敏后的 body 计预算，因为落库的是它们。脱敏通常会缩小 body——整把密钥塌缩成
	// 一段占位符——所以这个测量也决定到底需不需要裁剪。
	if codexPatchBodyBytes(safe) > codexPatchInputMaxBytes {
		applyCodexPatchBudget(safe)
		input["truncated"] = true
		input["original_bytes"] = originalBytes
	}
	return input
}

// codexPatchBodyBytes 统计一条变更列表携带的 diff/content 载荷总字节。
func codexPatchBodyBytes(changes []any) int {
	total := 0
	for _, change := range changes {
		entry, ok := change.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"diff", "content"} {
			if body, _ := entry[key].(string); body != "" {
				total += len(body)
			}
		}
	}
	return total
}

// applyCodexPatchBudget 原地裁剪 diff/content body，直到载荷装进 codexPatchInputMaxBytes。
// path 与 kind 绝不丢弃：它们很小，且是 body 没了之后审阅者仍需要看的部分。
func applyCodexPatchBudget(changes []any) {
	remaining := codexPatchInputMaxBytes
	for _, change := range changes {
		entry, ok := change.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"diff", "content"} {
			body, _ := entry[key].(string)
			if body == "" {
				continue
			}
			if len(body) <= remaining {
				remaining -= len(body)
				continue
			}
			if kept := truncateUTF8(body, remaining); kept != "" {
				entry[key] = kept
			} else {
				delete(entry, key)
			}
			entry["truncated"] = true
			remaining = 0
		}
	}
}

// truncateUTF8 把 s 截到至多 max 字节且不切断 UTF-8 字符，保证结果仍能序列化成合法 JSON。
func truncateUTF8(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "")
}

// codexNormalizePatchStatus 把两种协议的 status 拼写统一到同一套 snake_case 词汇表：
// 旧式枚举本来就是 snake_case（completed/failed/declined），v2 枚举是 camelCase 且多了
// inProgress。
func codexNormalizePatchStatus(status string) string {
	switch status {
	case "":
		return ""
	case "inProgress", "in_progress":
		return "in_progress"
	default:
		return status
	}
}

// codexPatchResultOutput 为已结束的 patch 渲染 tool_result 行。只要有任何已知信息，
// 它总会返回非空串——空 output 在 transcript 里会渲染成一行无法展开的空行。
func codexPatchResultOutput(status string, changes []any, stdout, stderr string) string {
	segments := make([]string, 0, 3)

	if headline := codexPatchHeadline(status, len(changes)); headline != "" {
		segments = append(segments, headline)
	}
	if out := strings.TrimSpace(stdout); out != "" {
		segments = append(segments, out)
	}
	if err := strings.TrimSpace(stderr); err != "" {
		segments = append(segments, err)
	}
	return strings.Join(segments, "\n")
}

// codexMCPToolInput 让远程服务器在 transcript 里保持可见，同时在一个稳定键下保留
// 供应商原生参数形状。在适配层边界就脱敏（daemon 里也会再脱敏一次）：其它 Message
// 消费方不必知道 MCP 调用可能在深层嵌套参数里携带凭据或私有查询文本。
func codexMCPToolInput(item map[string]any) map[string]any {
	input := make(map[string]any, 2)
	if server, _ := item["server"].(string); strings.TrimSpace(server) != "" {
		input["server"] = server
	}
	if arguments, ok := item["arguments"]; ok {
		input["arguments"] = arguments
	}
	return redact.InputMap(input)
}

func codexMCPToolName(item map[string]any) string {
	if tool, _ := item["tool"].(string); strings.TrimSpace(tool) != "" {
		return tool
	}
	return "mcp_tool"
}

// codexMCPToolResultOutput 刻意排除 result.content 与 structuredContent：这些值可能很大、
// 且可能含私有数据。Agent Log 需要一个可审计的结果，而不是 codex 早已消费过的供应商
// 载荷的又一份拷贝。
func codexMCPToolResultOutput(item map[string]any) string {
	status, _ := item["status"].(string)
	status = codexNormalizePatchStatus(status)
	if status == "" {
		status = "completed"
	}

	segments := []string{status}
	if durationMS := codexInt64(item, "durationMs", "duration_ms"); durationMS > 0 {
		segments = append(segments, fmt.Sprintf("duration: %d ms", durationMS))
	}
	if errMessage := extractNestedString(item, "error", "message"); strings.TrimSpace(errMessage) != "" {
		segments = append(segments, "error: "+sanitizeCodexDiagnostic(errMessage))
	}
	return strings.Join(segments, "\n")
}

func codexPatchHeadline(status string, fileCount int) string {
	switch {
	case status == "" && fileCount == 0:
		return ""
	case status == "":
		return fmt.Sprintf("%s changed", pluralizeFiles(fileCount))
	case fileCount == 0:
		return status
	default:
		return fmt.Sprintf("%s (%s)", status, pluralizeFiles(fileCount))
	}
}

func pluralizeFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

func (c *codexClient) handleEvent(msg map[string]any) {
	msgType, _ := msg["type"].(string)

	switch msgType {
	case "task_started":
		if c.onMessage != nil {
			c.onMessage(Message{Type: MessageStatus, Status: "running", SessionID: c.getThreadID()})
		}
	case "agent_message":
		text, _ := msg["message"].(string)
		if text != "" && c.onMessage != nil {
			c.onMessage(Message{Type: MessageText, Content: text})
		}
		if text != "" && c.onAgentMessage != nil {
			c.onAgentMessage(text)
		}
	case "exec_command_begin":
		callID, _ := msg["call_id"].(string)
		command, _ := msg["command"].(string)
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolUse,
				Tool:   "exec_command",
				CallID: callID,
				Input:  map[string]any{"command": command},
			})
		}
	case "exec_command_end":
		callID, _ := msg["call_id"].(string)
		output, _ := msg["output"].(string)
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolResult,
				Tool:   "exec_command",
				CallID: callID,
				Output: output,
			})
		}
	case "patch_apply_begin":
		callID, _ := msg["call_id"].(string)
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolUse,
				Tool:   "patch_apply",
				CallID: callID,
				Input:  codexPatchInput(codexNormalizeLegacyChanges(msg["changes"])),
			})
		}
	case "patch_apply_end":
		callID, _ := msg["call_id"].(string)
		stdout, _ := msg["stdout"].(string)
		stderr, _ := msg["stderr"].(string)
		status, _ := msg["status"].(string)
		if status == "" {
			// `status` 晚于 `success` 出现；回退让更老的构建也能报出结果，而不是只有
			// 干巴巴的文件数。
			if success, ok := msg["success"].(bool); ok {
				if success {
					status = "completed"
				} else {
					status = "failed"
				}
			}
		}
		changes := codexNormalizeLegacyChanges(msg["changes"])
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolResult,
				Tool:   "patch_apply",
				CallID: callID,
				Output: codexPatchResultOutput(codexNormalizePatchStatus(status), changes, stdout, stderr),
			})
		}
	case "task_complete":
		// 旧式 task_complete 若带用量则提取。
		c.extractUsageFromMap(msg)
		if c.onTurnDone != nil {
			c.onTurnDone(false)
		}
	case "turn_aborted":
		if c.onTurnDone != nil {
			c.onTurnDone(true)
		}
	}
}

func (c *codexClient) handleRawNotification(method string, params map[string]any) {
	// 忽略非我们所跟踪线程发来的通知。codex 会在同一条 stdio 管道上多路复用子 agent
	// 线程（如记忆整合）；只有我们的线程才能驱动回合生命周期与输出。
	//
	// v2 app-server 协议 schema 保证每条通知都带顶层 threadId。handleNotification 在
	// 有状态的当前回合门之前就做这个守卫；这里再保留一份作为直接调用方的纵深防御。若
	// codex 未来版本出现不带 threadId 的通知，它们会穿透——升级 codex 时要重新审计此守卫。
	if c.isNotificationFromOtherThread(params) {
		return
	}

	switch method {
	case "thread/tokenUsage/updated":
		c.updateThreadTokenUsage(params)
	case "turn/plan/updated":
		c.emitRuntime("plan", map[string]any{"turn_id": params["turnId"], "plan": params["plan"], "explanation": params["explanation"]})

	case "turn/started":
		if turnID := extractNestedString(params, "turn", "id"); turnID != "" {
			if turnID != c.usageTurnID() {
				c.turnCompleted = false
			}
			c.setActiveTurnID(turnID)
		}
		if c.onMessage != nil {
			c.onMessage(Message{Type: MessageStatus, Status: "running", SessionID: c.getThreadID()})
		}

	case "turn/completed":
		turnID := extractNestedString(params, "turn", "id")
		status := extractNestedString(params, "turn", "status")
		threadID, _ := params["threadId"].(string)
		c.cfg.Logger.Info("codex turn/completed received", "thread_id", threadID, "turn_id", turnID, "status", status)
		if c.turnCompleted || (turnID == "" && c.completedWithoutID) {
			return
		}
		c.turnCompleted = true
		if turnID == "" {
			// 缺少 ID 的旧完成事件只能接收一次，无法归属到后续回合。
			c.completedWithoutID = true
		}
		c.setActiveTurnID("")
		aborted := status == "cancelled" || status == "canceled" ||
			status == "aborted" || status == "interrupted"

		// 捕获失败回合的错误消息，让调用方能给出真实原因，而不是退化成「空输出」。
		if status == "failed" {
			errMsg := extractNestedString(params, "turn", "error", "message")
			if errMsg == "" {
				errMsg = "codex turn failed"
			}
			c.setTurnError(errMsg)
		}

		// turn/completed 若带用量则提取（例如 params.turn.usage）。
		if turn, ok := params["turn"].(map[string]any); ok && !c.modernUsage {
			c.extractUsageFromMap(turn)
		}

		// Some cancellation and transport-failure paths do not emit an
		// item/completed snapshot. Flush the aggregate of every complete delta
		// notification before publishing the terminal boundary.
		c.flushAgentMessageDeltas()

		if c.onTurnDone != nil {
			c.onTurnDone(aborted)
		}

	case "error":
		// 顶层协议错误。带 willRetry=true 的通知只是瞬时重连尝试；只捕获终态错误，
		// 免得后面用重试占位符盖掉一次真实失败。
		willRetry, _ := params["willRetry"].(bool)
		errMsg := extractNestedString(params, "error", "message")
		if errMsg == "" {
			errMsg = extractNestedString(params, "message")
		}
		if errMsg != "" {
			c.cfg.Logger.Warn("codex error notification", "message", errMsg, "will_retry", willRetry)
			if c.onSemanticActivity != nil {
				if willRetry {
					c.onSemanticActivity("error:retry")
				} else {
					c.onSemanticActivity("error:terminal")
				}
			}
			if !willRetry {
				c.setTurnError(errMsg)
			}
		}

	case "thread/status/changed":
		// 状态变更只是信息性的。只有 turn/completed 携带原始协议回合的权威终态。

	case "item/autoApprovalReview/completed":
		// This item notification also signals semantic progress. Preserve the
		// existing watchdog behavior when handling it specially below.
		c.handleItemNotification(method, params)
		if c.guardianRateLimitCircuit != nil &&
			c.guardianRateLimitCircuit.observeReview(params) &&
			c.onGuardianRateLimit != nil {
			c.onGuardianRateLimit()
		}

	default:
		if strings.HasPrefix(method, "item/") {
			c.handleItemNotification(method, params)
		}
	}
}

func (c *codexClient) isNotificationFromOtherThread(params map[string]any) bool {
	threadID, ok := params["threadId"].(string)
	if !ok {
		return false
	}
	// Compare against one snapshot: this runs on the stdout goroutine, ahead of
	// the current-turn gate, so the gate's state cannot serialize the read.
	currentThreadID := c.getThreadID()
	return currentThreadID != "" && threadID != currentThreadID
}

func (c *codexClient) emitAgentMessageChunk(text string) bool {
	if text == "" {
		return true
	}
	if c.onAgentMessageChunk != nil {
		return c.onAgentMessageChunk(text)
	}
	if c.onMessage != nil {
		c.onMessage(Message{Type: MessageText, Content: text})
		return true
	}
	return false
}

func (c *codexClient) agentMessageStream(itemID string) *codexAgentMessageStream {
	if c.agentMessageStreams == nil {
		c.agentMessageStreams = make(map[string]*codexAgentMessageStream)
	}
	stream := c.agentMessageStreams[itemID]
	if stream == nil {
		stream = &codexAgentMessageStream{}
		c.agentMessageStreams[itemID] = stream
		c.agentMessageOrder = append(c.agentMessageOrder, itemID)
	}
	return stream
}

func (c *codexClient) handleAgentMessageDelta(itemID, delta string) {
	if itemID == "" || delta == "" {
		return
	}

	stream := c.agentMessageStream(itemID)
	// The leading delta is the latency-critical event. Deliver it immediately;
	// coalesce the rest so a burst of token notifications cannot fill the
	// daemon channel before its consumer is scheduled.
	if stream.delivered.Len() == 0 && stream.pending.Len() == 0 {
		if c.emitAgentMessageChunk(delta) {
			stream.delivered.WriteString(delta)
			return
		}
	}

	stream.pending.WriteString(delta)
	if stream.pending.Len() >= codexAgentMessageAggregateBytes {
		c.flushAgentMessageStream(stream)
	}
}

func (c *codexClient) flushAgentMessageStream(stream *codexAgentMessageStream) bool {
	if stream == nil || stream.pending.Len() == 0 {
		return true
	}
	text := stream.pending.String()
	if !c.emitAgentMessageChunk(text) {
		return false
	}
	stream.delivered.WriteString(text)
	stream.pending.Reset()
	return true
}

// flushAgentMessageDeltas preserves complete delta events when a turn is
// cancelled or stdout ends before item/completed. agentMessageOrder retains
// provider order; iterating the map directly would make two unfinished items
// nondeterministic.
func (c *codexClient) flushAgentMessageDeltas() {
	for _, itemID := range c.agentMessageOrder {
		stream := c.agentMessageStreams[itemID]
		if stream == nil {
			continue
		}
		if c.flushAgentMessageStream(stream) {
			delete(c.agentMessageStreams, itemID)
		}
	}
}

func (c *codexClient) completeAgentMessage(itemID, text string) {
	stream := c.agentMessageStreams[itemID]
	if text == "" {
		if c.flushAgentMessageStream(stream) {
			delete(c.agentMessageStreams, itemID)
		}
		return
	}

	delivered := ""
	if stream != nil {
		delivered = stream.delivered.String()
	} else {
		stream = c.agentMessageStream(itemID)
	}

	if !strings.HasPrefix(text, delivered) {
		// Already-persisted text cannot be retracted without a new event/schema.
		// Appending the full snapshot would duplicate it, but every complete delta
		// event still belongs in the append-only transcript. Flush pending normally;
		// if the channel refuses it, leave the stream intact for terminal/EOF retry.
		// onAgentMessage remains the authoritative Result.Output fallback.
		if c.cfg.Logger != nil {
			c.cfg.Logger.Warn("codex agent-message delta did not match completed text",
				"item_id", itemID, "delivered_bytes", len(delivered), "completed_bytes", len(text))
		}
		if c.flushAgentMessageStream(stream) {
			delete(c.agentMessageStreams, itemID)
		}
		return
	}

	// With an acknowledged prefix match, the completed snapshot supersedes
	// observed-but-unacknowledged bytes and supplies their authoritative suffix.
	stream.pending.Reset()
	suffix := strings.TrimPrefix(text, delivered)
	if suffix == "" || c.emitAgentMessageChunk(suffix) {
		delete(c.agentMessageStreams, itemID)
		return
	}
	// A process cancellation can race the final handoff. Keep the authoritative
	// suffix so the stdout-EOF flush gets one more chance without duplicating the
	// prefix that was already accepted.
	stream.pending.WriteString(suffix)
}

func (c *codexClient) handleItemNotification(method string, params map[string]any) {
	item, _ := params["item"].(map[string]any)
	itemType, _ := item["type"].(string)
	itemID, _ := item["id"].(string)
	if method == "item/agentMessage/delta" {
		// Unlike item/started and item/completed, the real app-server delta
		// schema is flat: {threadId, turnId, itemId, delta}.
		itemType = "agentMessage"
		itemID, _ = params["itemId"].(string)
	}
	if isCodexItemProgressActivity(method) && c.onSemanticActivity != nil {
		c.onSemanticActivity(describeCodexItemProgressActivity(method, itemType, itemID))
	}
	if item == nil && method != "item/agentMessage/delta" {
		return
	}
	if method == "item/completed" && (itemType == "commandExecution" || itemType == "mcpToolCall" || itemType == "fileChange") {
		c.emitToolDuration(params, item)
	}

	switch {
	case (method == "item/started" || method == "item/completed") && itemType == "contextCompaction":
		phase := "start"
		if method == "item/completed" {
			phase = "end"
		}
		c.emitRuntime("compaction", map[string]any{"turn_id": params["turnId"], "item_id": itemID, "phase": phase})
	case method == "item/completed" && itemType == "plan":
		c.emitRuntime("plan", map[string]any{"turn_id": params["turnId"], "item_id": itemID, "text": item["text"], "phase": "completed"})
	case method == "item/agentMessage/delta" && itemType == "agentMessage":
		// JSON-RPC framing remains authoritative: handleLine invokes us only
		// after a whole newline-delimited JSON event parsed successfully. Stream
		// the event's text payload, not arbitrary stdout bytes.
		delta, _ := params["delta"].(string)
		c.handleAgentMessageDelta(itemID, delta)

	case method == "item/started" && itemType == "commandExecution":
		command, _ := item["command"].(string)
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolUse,
				Tool:   "exec_command",
				CallID: itemID,
				Input:  map[string]any{"command": command},
			})
		}

	case method == "item/completed" && itemType == "commandExecution":
		output, _ := item["aggregatedOutput"].(string)
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolResult,
				Tool:   "exec_command",
				CallID: itemID,
				Output: output,
			})
		}

	case method == "item/started" && itemType == "fileChange":
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolUse,
				Tool:   "patch_apply",
				CallID: itemID,
				Input:  codexPatchInput(codexNormalizeRawChanges(item["changes"])),
			})
		}

	case method == "item/completed" && itemType == "fileChange":
		status, _ := item["status"].(string)
		changes := codexNormalizeRawChanges(item["changes"])
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolResult,
				Tool:   "patch_apply",
				CallID: itemID,
				Output: codexPatchResultOutput(codexNormalizePatchStatus(status), changes, "", ""),
			})
		}

	case method == "item/started" && itemType == "mcpToolCall":
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolUse,
				Tool:   codexMCPToolName(item),
				CallID: itemID,
				Input:  codexMCPToolInput(item),
			})
		}

	case method == "item/completed" && itemType == "mcpToolCall":
		status, _ := item["status"].(string)
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolResult,
				Tool:   codexMCPToolName(item),
				CallID: itemID,
				Output: codexMCPToolResultOutput(item),
				Status: codexNormalizePatchStatus(status),
			})
		}

	case method == "item/completed" && itemType == "agentMessage":
		text, _ := item["text"].(string)
		if text != "" && c.onAgentMessage != nil {
			c.onAgentMessage(text)
		}
		c.completeAgentMessage(itemID, text)
		phase, _ := item["phase"].(string)
		if phase == "final_answer" {
			// 门的存在是为了防止子 agent 或重放的历史回合提前结束「我们的」回合；本函数
			// 顶部的线程守卫已把外来线程挡在外面。在我们观测到 turn/started 之前到达的
			// final answer 仍是本线程的交付物，丢掉它就会退化成叙述性消息。
			if text != "" && c.onFinalAnswer != nil {
				c.onFinalAnswer(text)
			}
			// 流保持打开直到 turn/completed。final answer 只是交付物，不是权威的生命周期边界。
		}
	}
}

// updateThreadTokenUsage consumes the v2 app-server's real usage notification.
// Resume can replay a historical snapshot after thread/resume returns, so only
// notifications attributed to this run's active or just-completed turn are eligible.
// A late final snapshot must survive closing supplement admission. The first current-
// turn snapshot contributes `last`; later snapshots contribute the monotonic
// delta from `total`. This retains multi-response tool loops without charging
// replayed history or duplicate snapshots twice.
func (c *codexClient) updateThreadTokenUsage(params map[string]any) {
	turnID, _ := params["turnId"].(string)
	if turnID == "" || turnID != c.usageTurnID() {
		return
	}

	tokenUsage, ok := params["tokenUsage"].(map[string]any)
	if !ok {
		return
	}
	totalMap, ok := tokenUsage["total"].(map[string]any)
	if !ok {
		return
	}
	last, ok := tokenUsage["last"].(map[string]any)
	if !ok {
		return
	}

	total := codexRawTokenUsageFromV2(totalMap)
	delta := codexRawTokenUsageFromV2(last)
	c.usageMu.Lock()
	duplicate := c.usageTotalSet && total == c.usageTotal
	if c.usageTotalSet {
		delta = subtractCodexRawTokenUsage(total, c.usageTotal)
	}
	c.usageTotal = total
	c.usageTotalSet = true
	c.modernUsage = true
	c.usage.InputTokens += codexPlainInputTokens(delta.InputTokens, delta.CachedInputTokens, delta.CacheWriteInputTokens)
	c.usage.OutputTokens += delta.OutputTokens
	c.usage.CacheReadTokens += delta.CachedInputTokens
	c.usage.CacheWriteTokens += delta.CacheWriteInputTokens
	c.usageMu.Unlock()
	if !duplicate {
		fields := map[string]any{
			"turn_id": turnID, "scope": "turn_delta",
			"input_tokens":  codexPlainInputTokens(delta.InputTokens, delta.CachedInputTokens, delta.CacheWriteInputTokens),
			"output_tokens": delta.OutputTokens, "cache_read_tokens": delta.CachedInputTokens,
			"cache_write_tokens": delta.CacheWriteInputTokens, "reasoning_tokens": delta.ReasoningOutputTokens,
		}
		if value, ok := last["totalTokens"]; ok {
			fields["pressure_tokens"] = value
		}
		if window := codexInt64(tokenUsage, "modelContextWindow"); window > 0 {
			fields["context_window"] = window
		}
		c.emitRuntime("context", fields)
	}
}

func codexRawTokenUsageFromV2(usage map[string]any) codexRawTokenUsage {
	return codexRawTokenUsage{
		InputTokens:           codexInt64(usage, "inputTokens"),
		OutputTokens:          codexInt64(usage, "outputTokens"),
		CachedInputTokens:     codexInt64(usage, "cachedInputTokens"),
		CacheWriteInputTokens: codexInt64(usage, "cacheWriteInputTokens"),
		ReasoningOutputTokens: codexInt64(usage, "reasoningOutputTokens"),
	}
}

func isCodexItemProgressActivity(method string) bool {
	return strings.HasPrefix(method, "item/")
}

func describeCodexItemProgressActivity(method, itemType, itemID string) string {
	if itemType == "" {
		itemType = "unknown"
	}
	if itemID == "" {
		return fmt.Sprintf("%s:%s", method, itemType)
	}
	return fmt.Sprintf("%s:%s:%s", method, itemType, itemID)
}

// extractUsageFromMap 从一个可能含 "usage"、"token_usage" 或 "tokens" 字段的 map 里
// 提取 token 用量，兼容 codex 的各种格式。
func (c *codexClient) extractUsageFromMap(data map[string]any) {
	// 依次试常用字段名。
	var usageMap map[string]any
	for _, key := range []string{"usage", "token_usage", "tokens"} {
		if v, ok := data[key].(map[string]any); ok {
			usageMap = v
			break
		}
	}
	if usageMap == nil {
		return
	}

	c.usageMu.Lock()
	defer c.usageMu.Unlock()

	// codex 把缓存输入作为 prompt-token 的明细上报：cached_input_tokens 已含在
	// input_tokens 里。持久化互斥分桶，让仪表盘的成本计算不会把缓存输入计两次。
	inputTokens := codexInt64(usageMap, "input_tokens", "input", "prompt_tokens")
	cacheReadTokens := codexInt64(usageMap, "cached_input_tokens", "cache_read_tokens", "cache_read_input_tokens")
	c.usage.InputTokens += codexUncachedInputTokens(inputTokens, cacheReadTokens)
	c.usage.OutputTokens += codexInt64(usageMap, "output_tokens", "output", "completion_tokens")
	c.usage.CacheReadTokens += cacheReadTokens
	c.usage.CacheWriteTokens += codexInt64(usageMap, "cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens")
}

func codexUncachedInputTokens(inputTokens, cachedInputTokens int64) int64 {
	uncached := inputTokens - cachedInputTokens
	if uncached < 0 {
		return 0
	}
	return uncached
}

// codexPlainInputTokens removes both cache details from Codex's raw input
// total, which includes cache-read and cache-write input in v2 and JSONL usage.
func codexPlainInputTokens(inputTokens, cacheReadTokens, cacheWriteTokens int64) int64 {
	return codexUncachedInputTokens(
		codexUncachedInputTokens(inputTokens, cacheReadTokens),
		cacheWriteTokens,
	)
}

// codexInt64 returns the first non-zero int64 value from the map for the given keys.
func codexInt64(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			if v != 0 {
				return int64(v)
			}
		case int64:
			if v != 0 {
				return v
			}
		}
	}
	return 0
}

// ── Codex 会话日志扫描器 ──

// codexSessionUsage 保存从 codex 会话 JSONL 文件里提取出的用量。
type codexSessionUsage struct {
	usage TokenUsage
	model string
}

// scanCodexSessionUsage 为 threadID 从其 codex rollout 里提取用量。codex 0.144.4 把
// app-server 线程 ID 同时嵌在 rollout 文件名和首个 session_meta 条目里。把扫描绑定到
// 该 ID，可防止并发创建的 rollout（例如 codex 子 agent 的）被记到本任务头上。resume 的
// rollout 保留它最初的日期目录，所以平面布局与 YYYY/MM/DD 布局都会搜。
func scanCodexSessionUsage(startTime time.Time, codexHome, threadID string, resumed bool) *codexSessionUsage {
	root := codexSessionRoot(codexHome)
	if root == "" || strings.TrimSpace(threadID) == "" {
		return nil
	}

	type candidate struct {
		path    string
		modTime time.Time
	}
	var files []candidate
	for _, path := range findCodexSessionRollouts(root, threadID) {
		info, err := os.Stat(path)
		if err != nil || info.ModTime().Before(startTime) {
			continue
		}
		files = append(files, candidate{path: path, modTime: info.ModTime()})
	}
	if len(files) == 0 {
		return nil
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})

	// 布局迁移期间，同一线程可能短暂对应多个路径。它们同属一个主人，所以确定性地
	// 取最新的一个，且绝不跨进别的线程的 rollout。
	result := parseCodexSessionFileSince(files[len(files)-1].path, startTime, resumed)
	if result == nil || (result.usage.InputTokens == 0 && result.usage.OutputTokens == 0 &&
		result.usage.CacheReadTokens == 0 && result.usage.CacheWriteTokens == 0 && result.model == "") {
		return nil
	}
	return result
}

// findCodexSessionRollouts 返回 threadID 拥有的、未被压缩的 rollout 文件，覆盖
// codex 0.14x 支持的两种布局。filepath.Glob 会穿过符号链接的 sessions 根目录，而
// WalkDir 会把符号链接根当单个文件、绝不访问其子项。常规路径采用 codex 的文件名约定，
// 并在存在 session_meta.id 时校验它。若未来 codex 改文件名，元数据这趟仍能保持精确的
// 归属，而不是静默丢掉用量。在 glob 之后再过滤，也避免把供应商提供的线程 ID 拼进
// glob 表达式。
func findCodexSessionRollouts(root, threadID string) []string {
	threadID = strings.TrimSpace(threadID)
	if root == "" || threadID == "" {
		return nil
	}

	patterns := []string{
		filepath.Join(root, "rollout-*.jsonl"),
		filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"),
	}
	seen := make(map[string]bool)
	var candidates []string
	for _, pattern := range patterns {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, path := range paths {
			if seen[path] {
				continue
			}
			seen[path] = true
			candidates = append(candidates, path)
		}
	}

	// 当前 codex 约定的快路径。若文件名命中的 rollout 里，权威 session_meta 明确
	// 指向别的线程，则拒绝该匹配。
	suffix := "-" + threadID + ".jsonl"
	var matches []string
	for _, path := range candidates {
		if !strings.HasSuffix(filepath.Base(path), suffix) {
			continue
		}
		if metadataID, ok := readCodexRolloutThreadID(path); ok && metadataID != threadID {
			continue
		}
		matches = append(matches, path)
	}
	if len(matches) > 0 {
		return matches
	}

	// 面向未来文件名格式的兼容路径：在 codex 自己的 resume 读取逻辑里，首个
	// session_meta 仍是 rollout 的权威归属者。
	for _, path := range candidates {
		if metadataID, ok := readCodexRolloutThreadID(path); ok && metadataID == threadID {
			matches = append(matches, path)
		}
	}
	return matches
}

// readCodexRolloutThreadID 只读 rollout 的头部。codex 会把权威 session_meta 写在最前面；
// 行数上限防止一个没有元数据的畸形旧文件把归属检查拖成全量扫描几 GB。
func readCodexRolloutThreadID(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	var evt struct {
		Type    string `json:"type"`
		Payload *struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lineCount := 0; lineCount < 64 && scanner.Scan(); lineCount++ {
		line := scanner.Bytes()
		if !bytesContainsStr(line, "session_meta") {
			continue
		}
		if err := json.Unmarshal(line, &evt); err == nil && evt.Type == "session_meta" && evt.Payload != nil && evt.Payload.ID != "" {
			return evt.Payload.ID, true
		}
	}
	return "", false
}

// codexSessionRoot 返回 codex 的 sessions 目录。优先用后端正在运行的显式 per-task
// codexHome（用量从 codex 实际写入的任务级 sessions 里读），其次用环境变量 CODEX_HOME，
// 最后回退 ~/.codex。
func codexSessionRoot(codexHome string) string {
	if codexHome = strings.TrimSpace(codexHome); codexHome == "" {
		codexHome = os.Getenv("CODEX_HOME")
	}
	if codexHome != "" {
		dir := filepath.Join(codexHome, "sessions")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	dir := filepath.Join(home, ".codex", "sessions")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

type codexRawTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	CacheReadInputTokens  int64 `json:"cache_read_input_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// codexSessionTokenCount 表示 codex JSONL 里的一条 token_count 事件。
type codexSessionTokenCount struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   *struct {
		Type string `json:"type"`
		Info *struct {
			TotalTokenUsage *codexRawTokenUsage `json:"total_token_usage"`
			LastTokenUsage  *codexRawTokenUsage `json:"last_token_usage"`
			Model           string              `json:"model"`
		} `json:"info"`
		Model string `json:"model"`
	} `json:"payload"`
}

// parseCodexSessionFile 从 codex 会话文件里提取最后一条 token_count。
func parseCodexSessionFile(path string) *codexSessionUsage {
	return parseCodexSessionFileSince(path, time.Time{}, false)
}

// parseCodexSessionFileSince 提取 startTime 之后累积的用量。codex 对整段被 resume 的
// 会话按 total_token_usage 累积上报，所以要从最终总数里减去 startTime 之前的最后一条
// total。resume 的 rollout 里无时间戳的事件只算作基线，直到某条明确晚于 startTime 的
// 时间戳确立边界；全新会话则保持旧的整文件行为，因为每个事件都属于新任务。
func parseCodexSessionFileSince(path string, startTime time.Time, resumed bool) *codexSessionUsage {
	// 1. Historical evidence also supplies an end boundary, while live accounting keeps its existing behavior.
	return parseCodexSessionFileBetween(path, startTime, time.Time{}, resumed)
}

func parseCodexSessionFileBetween(path string, startTime, endTime time.Time, resumed bool) *codexSessionUsage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var result codexSessionUsage
	var previousTotal, accumulated, finalUsage codexRawTokenUsage
	previousTotalFound := false
	finalUsageFound := false
	afterStartBoundary := false

	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := readCodexUsageLine(reader)
		if err != nil && len(line) == 0 {
			break
		}

		// 快速预筛，避免对每一行都做完整 JSON 解析。
		if !bytesContainsStr(line, "token_count") && !bytesContainsStr(line, "turn_context") {
			continue
		}

		var evt codexSessionTokenCount
		if err := json.Unmarshal(line, &evt); err != nil || evt.Payload == nil {
			continue
		}
		// 2. A later resume in the same file must never become part of an earlier run's evidence.
		if !endTime.IsZero() && (evt.Timestamp.IsZero() || evt.Timestamp.After(endTime)) {
			continue
		}
		timestampAfterStart := !startTime.IsZero() && !evt.Timestamp.IsZero() && evt.Timestamp.After(startTime)
		if timestampAfterStart {
			afterStartBoundary = true
		}

		// 从 turn_context 事件跟踪模型名。
		if evt.Type == "turn_context" && evt.Payload.Model != "" {
			result.model = evt.Payload.Model
			continue
		}

		// 从 token_count 事件提取 token 用量。
		if evt.Payload.Type == "token_count" && evt.Payload.Info != nil {
			afterStart := startTime.IsZero() || timestampAfterStart ||
				(evt.Timestamp.IsZero() && (!resumed || afterStartBoundary))
			if usage := evt.Payload.Info.TotalTokenUsage; usage != nil {
				current := normalizeCodexRawTokenUsage(*usage)
				if afterStart {
					delta := current
					if previousTotalFound {
						delta = subtractCodexRawTokenUsage(current, previousTotal)
					}
					accumulated = addCodexRawTokenUsage(accumulated, delta)
					finalUsage = accumulated
					finalUsageFound = true
				}
				previousTotal = current
				previousTotalFound = true
			} else if usage := evt.Payload.Info.LastTokenUsage; usage != nil && afterStart {
				// 保持事件顺序：更晚的 last_token_usage 正是旧的整文件解析器会选中的
				// 同一个回退。
				finalUsage = normalizeCodexRawTokenUsage(*usage)
				finalUsageFound = true
			}
			if evt.Payload.Info.Model != "" {
				result.model = evt.Payload.Info.Model
			}
		}
	}

	if !finalUsageFound {
		if result.model != "" {
			return &result
		}
		return nil
	}
	cacheReadTokens := finalUsage.CachedInputTokens
	cacheWriteTokens := finalUsage.CacheWriteInputTokens
	result.usage = TokenUsage{
		InputTokens:      codexPlainInputTokens(finalUsage.InputTokens, cacheReadTokens, cacheWriteTokens),
		OutputTokens:     finalUsage.OutputTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
	}
	return &result
}

func subtractCodexRawTokenUsage(total, baseline codexRawTokenUsage) codexRawTokenUsage {
	total = normalizeCodexRawTokenUsage(total)
	baseline = normalizeCodexRawTokenUsage(baseline)
	// 各计数器独立防护。把一个字段的重置当成整份快照的重置，会把仍单调递增的字段
	// 重新上报一遍，复现这个回退本想防止的重复计数。
	return codexRawTokenUsage{
		InputTokens:           nonNegativeTokenDelta(total.InputTokens, baseline.InputTokens),
		OutputTokens:          nonNegativeTokenDelta(total.OutputTokens, baseline.OutputTokens),
		CacheWriteInputTokens: nonNegativeTokenDelta(total.CacheWriteInputTokens, baseline.CacheWriteInputTokens),
		CachedInputTokens:     nonNegativeTokenDelta(total.CachedInputTokens, baseline.CachedInputTokens),
		ReasoningOutputTokens: nonNegativeTokenDelta(total.ReasoningOutputTokens, baseline.ReasoningOutputTokens),
	}
}

func normalizeCodexRawTokenUsage(usage codexRawTokenUsage) codexRawTokenUsage {
	if usage.CachedInputTokens == 0 {
		usage.CachedInputTokens = usage.CacheReadInputTokens
	}
	usage.CacheReadInputTokens = 0
	return usage
}

func addCodexRawTokenUsage(a, b codexRawTokenUsage) codexRawTokenUsage {
	return codexRawTokenUsage{
		InputTokens:           a.InputTokens + b.InputTokens,
		OutputTokens:          a.OutputTokens + b.OutputTokens,
		CacheWriteInputTokens: a.CacheWriteInputTokens + b.CacheWriteInputTokens,
		CachedInputTokens:     a.CachedInputTokens + b.CachedInputTokens,
		ReasoningOutputTokens: a.ReasoningOutputTokens + b.ReasoningOutputTokens,
	}
}

func nonNegativeTokenDelta(total, baseline int64) int64 {
	if total < baseline {
		// 计数器发生过重置，说明最终值已属于新的时间段。
		return total
	}
	return total - baseline
}

// bytesContainsStr 判断 b 是否包含字符串 s（不额外分配内存）。
func bytesContainsStr(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}

// ── 辅助函数 ──

func extractThreadID(result json.RawMessage) string {
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ""
	}
	return r.Thread.ID
}

func extractNestedString(m map[string]any, keys ...string) string {
	current := any(m)
	for _, key := range keys {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = obj[key]
	}
	s, _ := current.(string)
	return s
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Tool results and image records can exceed Scanner's limit. Discard oversized
// records in bounded chunks while continuing to the small accounting records.
func readCodexUsageLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	oversized := false
	for {
		part, more, err := reader.ReadLine()
		if !oversized {
			if len(line)+len(part) > 1024*1024 {
				oversized = true
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if !more || err != nil {
			return line, err
		}
	}
}
