// plugin_hook.go —— hook 引擎：Multica 唯一会「向外呼叫插件自有服务器」的地方。
// 在此之前的每一条路径都反着走——沙箱化的 surface 请求宿主、宿主以登录用户的会话行事，所以没有任何
// 请求会凭插件一句话就离开我们的基础设施。hook 会离开，因此这里的检查都针对「目的地」而非「调用方」：
// 目的地必须是 manifest 通过 `net:` 权限声明过的宿主、在拨号那一刻解析到公网地址，且 body 必须签名，
// 让接收方能分辨我们的调用与别人的调用。
package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const (
	// 签名方案版本，随 header 携带——方案演进时每个 handler 不必猜自己看到的是哪一版。
	hookSignatureVersion = "v1"
	// 请求时间戳与我们时钟的最大偏差。限住重放：被截获的请求只在这个窗口内可复用；handler 若再
	// 记住已见签名，就能把窗口彻底关死。
	hookTimestampTolerance = 5 * time.Minute
	// manifest 未声明 timeout_ms 时的兜底超时。
	hookDefaultTimeout = 10 * time.Second
	// 响应体会被读给调用方，所以要封顶。回一 GB 的 hook 应当失败，而不是拖垮宿主。
	hookMaxResponseBytes = 1 << 20

	// 事件重试节奏。共三次尝试，之后放弃该次调用。
	hookEventAttempts = 3
	hookEventBackoff  = 2 * time.Second

	// 熔断器。窗口内失败数达到该值，该 hook 的后台投递暂停直到窗口滑走——已经宕了一分钟的端点，
	// 不需要每来一个事件/每次计划 occurrence 都去打扰它一次才知道。
	hookBreakerThreshold = 5
	hookBreakerWindow    = 5 * time.Minute

	// 每个 hook 的限流，按尝试次数计。
	hookRateLimit  = 120
	hookRateWindow = time.Minute
)

// HookResult 是一次 hook 调用完成后返回给调用方的结果。
type HookResult struct {
	Status   string          `json:"status"`
	Output   json.RawMessage `json:"output,omitempty"`
	Error    string          `json:"error,omitempty"`
	Latency  int             `json:"latency_ms"`
	HookKey  string          `json:"hook_key"`
	Trigger  string          `json:"trigger"`
	Attempts int             `json:"attempts"`
}

// HookActor 是「由此产生的写入」归属谁。
//
// 这是 hook 的整套身份模型，且它跟随触发而非插件：ui/manual hook 因某人按下某个按钮而跑，所以它的
// 写入保持那个人的（标记 via_plugin_id）；event/schedule hook 背后没有人，绝不能借用最后碰过该资源的
// 那个人。
type HookActor struct {
	Type string
	ID   pgtype.UUID
}

// HookInvocation 是一次引擎被请求执行的调用。
type HookInvocation struct {
	// ID 在出站请求前分配。接收方可以记下它，把响应与持久化调用行关联起来——哪怕调用本身失败。
	ID           pgtype.UUID
	Installation db.PluginInstallation
	Hook         plugincontract.Hook
	Trigger      string
	// EventType 只在 event 触发时设置，点明发生了什么。
	EventType string
	Actor     HookActor
	// IssueID 是本次调用涉及的那个 issue（若有）。它收窄回调令牌——处理某条 issue 的 handler 无法
	// 用同一个授权去够工作区里别的东西。
	IssueID pgtype.UUID
	Input   any
	// DeliveryID 在同一次计划 occurrence 的各次重试间保持稳定。PlannedAt 是规范的 UTC cron occurrence
	// 时间，不是投递的墙上钟时间。
	DeliveryID string
	PlannedAt  pgtype.Timestamptz
	Attempt    int
}

// hookRequestBody 是线上格式。刻意稳定而小巧：按 v1 写的 hook handler 不必关心宿主日后还会发送
// 什么别的东西。
type hookRequestBody struct {
	Version      int       `json:"version"`
	InvocationID string    `json:"invocation_id"`
	DeliveryID   string    `json:"delivery_id,omitempty"`
	Attempt      int       `json:"attempt"`
	OccurredAt   time.Time `json:"occurred_at"`
	HookKey      string    `json:"hook_key"`
	Trigger      string    `json:"trigger"`
	EventType    string    `json:"event_type,omitempty"`
	WorkspaceID  string    `json:"workspace_id"`
	Installation string    `json:"installation_id"`
	// IssueID 是本次调用涉及的 issue，已经宿主解析并做权限校验。发出它，是因为否则每个 handler
	// 都得从客户端自带的 `input` 里读——未经校验，且 event 触发时压根没有客户端参与、会完全缺席。
	IssueID string           `json:"issue_id,omitempty"`
	Actor   hookRequestActor `json:"actor"`
	Input   json.RawMessage  `json:"input,omitempty"`
	// Config 是安装的非 secret 配置，即管理员在宿主渲染表单里敲的值。发出它，是因为 handler 没有
	// 别的途径读这些值——Action API 刻意没有配置端点——而插件被迫自存第二份副本的话，manifest 的
	// config 块就沦为装饰。
	//
	// Secret 型字段绝不包含。它们是插件为「它自己的服务」准备的凭证；插件本来就有，放上线路等于把
	// 每个值都递给任何持有端点的人。
	Config map[string]any `json:"config,omitempty"`
	// CallbackToken 让 handler 在它有效的几分钟内回调 Action API。比安装自带的 token 更窄、且绑死
	// 这一次调用——handler 泄漏它，泄漏的只是几分钟它本就在用的权限，而非长期访问。
	CallbackToken string               `json:"callback_token,omitempty"`
	CallbackURL   string               `json:"callback_url,omitempty"`
	Schedule      *hookRequestSchedule `json:"schedule,omitempty"`
}

type hookRequestActor struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

type hookRequestSchedule struct {
	PlannedAt time.Time `json:"planned_at"`
}

// FindHook 从安装的（被同意的）manifest 里找出指定名字的 hook。
//
// 从安装行读取，绝不用来源 URL 此刻供应的 manifest：管理员同意的是具体的一组端点，插件日后若把自己
// 的 hook 重新指向别处，必须走升级流程才生效。
func FindHook(installation db.PluginInstallation, hookKey string) (plugincontract.Hook, error) {
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		return plugincontract.Hook{}, pluginErrf(PluginErrorInvalid, "plugin manifest is unreadable")
	}
	for _, hook := range manifest.Contributes.Hooks {
		if hook.Key == hookKey {
			return hook, nil
		}
	}
	return plugincontract.Hook{}, pluginErrf(PluginErrorNotFound, "this Plugin has no hook named %q", hookKey)
}

// HookAllowsTrigger 报告 hook 是否声明过该触发。manifest 没列的触发，不是宿主可以自行发明的调用点。
func HookAllowsTrigger(hook plugincontract.Hook, trigger string) bool {
	for _, declared := range hook.Triggers {
		if declared == trigger {
			return true
		}
	}
	return false
}

// InvokeHook 执行一次调用并记录它。
//
// 阻塞与否由调用方决定，不由本函数：ui/manual 等人看结果所以 await，event 投递跑在宿主请求之外，
// schedule 投递跑在持久化调度租约下。
func (s *PluginService) InvokeHook(ctx context.Context, invocation HookInvocation, attempt int) (HookResult, error) {
	if !invocation.ID.Valid {
		invocation.ID = pgtype.UUID{Bytes: uuid.New(), Valid: true}
	}
	if attempt < 1 {
		attempt = 1
	}
	invocation.Attempt = attempt
	hook := invocation.Hook
	result := HookResult{HookKey: hook.Key, Trigger: invocation.Trigger, Attempts: attempt}

	if !invocation.Installation.Enabled {
		return result, pluginErrf(PluginErrorForbidden, "this Plugin is disabled")
	}
	if !HookAllowsTrigger(hook, invocation.Trigger) {
		return result, pluginErrf(PluginErrorForbidden, "hook %q does not declare the %s trigger", hook.Key, invocation.Trigger)
	}
	if hook.Transport.Type != plugincontract.TransportHTTP {
		return result, pluginErrf(PluginErrorIncompatible, "hook transport %q is not supported yet", hook.Transport.Type)
	}
	if err := s.checkHookRate(ctx, invocation.Installation.ID, hook.Key); err != nil {
		return result, err
	}

	started := time.Now()
	output, callErr := s.callHookEndpoint(ctx, invocation)
	latency := int(time.Since(started).Milliseconds())

	result.Latency = latency
	status := "ok"
	message := ""
	if callErr != nil {
		status = hookFailureStatus(callErr)
		message = redactHookError(callErr)
		result.Error = message
	} else {
		result.Output = output
	}
	result.Status = status

	s.recordInvocation(ctx, invocation, status, attempt, latency, message)
	if callErr != nil {
		return result, callErr
	}
	return result, nil
}

// callHookEndpoint 是网络那一半：校验目的地、签名、发送。
func (s *PluginService) callHookEndpoint(ctx context.Context, invocation HookInvocation) (json.RawMessage, error) {
	installation := invocation.Installation
	granted := decodeScopes(installation.GrantedScopes)

	// 目的地必须在被同意的 `net:` 集合内。把已授予的域直接当白名单用，意味着管理员在授权屏上
	// 同意的那串字符串就是约束请求的东西——没有第二张表可能不同步。
	domains := plugincontract.NetDomains(granted)
	if len(domains) == 0 {
		return nil, pluginErrf(PluginErrorForbidden, "this Plugin was granted no net: scope, so it cannot call out")
	}

	// 两条到达端点的路，只有网络守卫不同。同意检查不因路径而异：落在被授予 `net:` 集合外的
	// 目的地，两条路都被拒——那正是管理员同意的内容，任何部署配置都不得放大它。
	var endpoint *url.URL
	client := &http.Client{}
	if s.isDevOrigin(invocation.Hook.Transport.URL) {
		// 运维在 MULTICA_PLUGIN_DEV_ORIGINS 里点名了这个确切 origin——与「允许 manifest 从本地开发
		// 服务器发布」同一种选择加入、出于同一个理由：正在开发 hook 的作者，还没有公网地方可指。
		parsed, err := url.Parse(invocation.Hook.Transport.URL)
		if err != nil {
			return nil, &PluginError{Kind: PluginErrorInvalid, Message: "hook endpoint is not a valid URL", Err: err}
		}
		if !hostInNetScopes(parsed.Hostname(), domains) {
			return nil, pluginErrf(PluginErrorForbidden, "hook endpoint host is not covered by a net: scope")
		}
		endpoint = parsed
		if s.HookClient != nil {
			client = s.HookClient
		}
	} else {
		validated, err := remotemcp.ValidatePublicHTTPSEndpoint(ctx, invocation.Hook.Transport.URL, domains, nil)
		if err != nil {
			return nil, &PluginError{Kind: PluginErrorForbidden, Message: "hook endpoint is not allowed", Err: err}
		}
		endpoint = validated
		// NewSecureHTTPClient 会在拨号时重新解析并拒绝非公网地址——通过校验后翻脸成私网 IP 的
		// 主机名，因此无法用来打进网络内部。
		client = remotemcp.NewSecureHTTPClient(endpoint)
	}

	body, err := s.buildHookBody(ctx, invocation)
	if err != nil {
		return nil, err
	}
	// 授权活得与它为之签发的那次调用一样长。没有这一步，handler 已经应答后它还会在其 TTL 的剩余
	// 时间里一直可用。
	if s.Callbacks != nil && body.CallbackToken != "" {
		defer s.Callbacks.Revoke(body.CallbackToken)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorInvalid, Message: "encode hook request", Err: err}
	}

	timeout := hookDefaultTimeout
	if invocation.Hook.TimeoutMs > 0 {
		timeout = time.Duration(invocation.Hook.TimeoutMs) * time.Millisecond
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorInvalid, Message: "build hook request", Err: err}
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	signature, err := s.SignHookPayload(installation.ID, timestamp, encoded)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Multica-Timestamp", timestamp)
	request.Header.Set("X-Multica-Signature", hookSignatureVersion+"="+signature)
	request.Header.Set("X-Multica-Plugin-Installation", uuidString(installation.ID))
	request.Header.Set("User-Agent", "Multica-Hooks/1")

	response, err := client.Do(request)
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "hook endpoint did not answer", Err: err}
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(response.Body, hookMaxResponseBytes))
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "read hook response", Err: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, pluginErrf(PluginErrorUnavailable, "hook endpoint returned %d", response.StatusCode)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, nil
	}
	if !json.Valid(payload) {
		return nil, pluginErrf(PluginErrorInvalid, "hook endpoint returned a non-JSON body")
	}
	return json.RawMessage(payload), nil
}

// nonSecretConfig 读取安装的已存配置，丢弃 manifest 声明为 secret 的任何值。
//
// 按 MANIFEST 过滤而不是按已存形状过滤：secret 住在自己的表里、本就不该出现在 config 列——所以
// 万一真出现，这道检查会把它拦住。
func nonSecretConfig(installation db.PluginInstallation) map[string]any {
	if len(installation.Config) == 0 {
		return nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(installation.Config, &values); err != nil {
		return nil
	}
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		// 没有 manifest 就无法判断哪些键是 secret，那就一个都不发。
		return nil
	}
	for key := range values {
		field, declared := manifest.Config.Field(key)
		if !declared || field.Type == plugincontract.ConfigSecret {
			delete(values, key)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

// buildHookBody 组装出站请求体：invocation 元数据 + 非 secret 配置 + 回调令牌。
func (s *PluginService) buildHookBody(ctx context.Context, invocation HookInvocation) (hookRequestBody, error) {
	attempt := invocation.Attempt
	if attempt < 1 {
		attempt = 1
	}
	body := hookRequestBody{
		Version:      1,
		InvocationID: uuidString(invocation.ID),
		DeliveryID:   invocation.DeliveryID,
		Attempt:      attempt,
		OccurredAt:   time.Now().UTC(),
		HookKey:      invocation.Hook.Key,
		Trigger:      invocation.Trigger,
		EventType:    invocation.EventType,
		WorkspaceID:  uuidString(invocation.Installation.WorkspaceID),
		Installation: uuidString(invocation.Installation.ID),
		Actor:        hookRequestActor{Type: invocation.Actor.Type, ID: uuidString(invocation.Actor.ID)},
	}
	if invocation.PlannedAt.Valid {
		body.Schedule = &hookRequestSchedule{PlannedAt: invocation.PlannedAt.Time.UTC()}
	}
	if invocation.IssueID.Valid {
		body.IssueID = uuidString(invocation.IssueID)
	}
	body.Config = nonSecretConfig(invocation.Installation)
	if invocation.Input != nil {
		encoded, err := json.Marshal(invocation.Input)
		if err != nil {
			return hookRequestBody{}, &PluginError{Kind: PluginErrorInvalid, Message: "encode hook input", Err: err}
		}
		body.Input = encoded
	}
	if s.Callbacks != nil {
		token, err := s.Callbacks.Issue(ctx, invocation)
		if err != nil {
			return hookRequestBody{}, err
		}
		body.CallbackToken = token
		body.CallbackURL = s.CallbackBaseURL
	}
	return body, nil
}

// SignHookPayload 算出接收方校验用的 hex HMAC。
//
// 被签名的串以「时间戳 + 不可能出现在时间戳里的分隔符 + body」拼成。只签 body，会让被截获的请求
// 永远可重放；签朴素拼接，则让精心构造的时间戳与 body 能在两个字段之间交换字节。
func (s *PluginService) SignHookPayload(installationID pgtype.UUID, timestamp string, body []byte) (string, error) {
	key, err := s.hookSigningKey(installationID)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// hookSigningKey 从部署密钥派生出该安装的签名密钥。
//
// 派生而非存储，而这正是那个决定性的不对称：宿主必须「产出」这个值来签名，所以单向哈希不可行；
// 而把可恢复的副本存进每个安装行，等于把可用的密钥放在任何数据库读取都够得到的地方。安装 token
// 方向相反——插件产出、宿主只校验——所以那个存的是哈希。同一个部署密钥，两个相反方向。
func (s *PluginService) hookSigningKey(installationID pgtype.UUID) ([]byte, error) {
	if len(s.DeploymentKey) == 0 {
		return nil, pluginErrf(PluginErrorUnavailable, "hooks are disabled: MULTICA_PLUGIN_SECRET_KEY is not configured")
	}
	if len(s.DeploymentKey) != 32 {
		return nil, pluginErrf(PluginErrorUnavailable, "hooks are disabled: MULTICA_PLUGIN_SECRET_KEY must decode to 32 bytes")
	}
	mac := hmac.New(sha256.New, s.DeploymentKey)
	mac.Write([]byte("multica-plugin-hook-signature:v1:"))
	mac.Write([]byte(uuidString(installationID)))
	return mac.Sum(nil), nil
}

// HookSigningSecret 是同一值、以作者在自己的服务器上配置时用的形式给出。安装时与 install token
// 并排展示一次。
func (s *PluginService) HookSigningSecret(installationID pgtype.UUID) (string, error) {
	key, err := s.hookSigningKey(installationID)
	if err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(key), nil
}

// VerifyHookSignature 是接收方的一半；导出让用 Go 写插件的作者与我们的测试用同一种方式校验签名——
// 只有一方能实现的方案，是没人能评审的方案。
func VerifyHookSignature(secretHex, timestamp string, body []byte, presented string, now time.Time) error {
	secretHex = strings.TrimPrefix(secretHex, "whsec_")
	key, err := hex.DecodeString(secretHex)
	if err != nil {
		return errors.New("signing secret is not valid hex")
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("timestamp is not an integer")
	}
	drift := now.Sub(time.Unix(seconds, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift > hookTimestampTolerance {
		return errors.New("timestamp is outside the accepted window")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	presented = strings.TrimPrefix(presented, hookSignatureVersion+"=")
	// 常量时间比较：逐字节比较会泄漏「猜对的签名长度」，那足以让人推出剩余部分。
	if subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) != 1 {
		return errors.New("signature does not match")
	}
	return nil
}

// checkHookRate 限制单个 hook 的调用频次。
func (s *PluginService) checkHookRate(ctx context.Context, installationID pgtype.UUID, hookKey string) error {
	since := pgtype.Timestamptz{Time: time.Now().Add(-hookRateWindow), Valid: true}
	count, err := s.Queries.CountRecentPluginInvocations(ctx, db.CountRecentPluginInvocationsParams{
		InstallationID: installationID,
		HookKey:        hookKey,
		CreatedAt:      since,
	})
	if err != nil {
		// 一次失败的遥测读取，不该把功能一起拖下水。
		return nil
	}
	if count >= hookRateLimit {
		return pluginErrf(PluginErrorQuota, "hook %q exceeded %d calls per minute", hookKey, hookRateLimit)
	}
	return nil
}

// HookBreakerOpen 报告该 hook 的后台投递是否因连续失败而暂停中。
func (s *PluginService) HookBreakerOpen(ctx context.Context, installationID pgtype.UUID, hookKey string) bool {
	since := pgtype.Timestamptz{Time: time.Now().Add(-hookBreakerWindow), Valid: true}
	failures, err := s.Queries.CountRecentPluginFailures(ctx, db.CountRecentPluginFailuresParams{
		InstallationID: installationID,
		HookKey:        hookKey,
		CreatedAt:      since,
	})
	if err != nil {
		return false
	}
	return failures >= hookBreakerThreshold
}

// recordInvocation 把一次调用落成调用记录行。
func (s *PluginService) recordInvocation(ctx context.Context, invocation HookInvocation, status string, attempt, latency int, message string) {
	params := db.CreatePluginInvocationParams{
		ID:             invocation.ID,
		InstallationID: invocation.Installation.ID,
		WorkspaceID:    invocation.Installation.WorkspaceID,
		HookKey:        invocation.Hook.Key,
		Trigger:        invocation.Trigger,
		Status:         status,
		Attempt:        int32(attempt),
		LatencyMs:      int32(latency),
	}
	if invocation.DeliveryID != "" {
		params.DeliveryID = pgtype.Text{String: invocation.DeliveryID, Valid: true}
	}
	if invocation.PlannedAt.Valid {
		params.PlannedAt = invocation.PlannedAt
	}
	if invocation.EventType != "" {
		params.EventType = pgtype.Text{String: invocation.EventType, Valid: true}
	}
	if message != "" {
		params.Error = pgtype.Text{String: truncate(message, 500), Valid: true}
	}
	// 尽力而为：遥测绝不能让它正在描述的那次调用失败。
	_, _ = s.Queries.CreatePluginInvocation(ctx, params)
}

// hookFailureStatus 把调用错误归成记录用的状态：明确被拒（refused）、超时（timeout）还是失败（failed）。
func hookFailureStatus(err error) string {
	var pluginErr *PluginError
	if errors.As(err, &pluginErr) {
		switch pluginErr.Kind {
		case PluginErrorForbidden, PluginErrorQuota, PluginErrorIncompatible:
			return "refused"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "failed"
}

// redactHookError 保留宿主自己的描述、丢掉远端提供的一切。会把输入回显出来的端点，否则可能把 issue
// 内容写进一张没有删除路径的表。
func redactHookError(err error) string {
	var pluginErr *PluginError
	if errors.As(err, &pluginErr) {
		return truncate(pluginErr.Message, 500)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "hook endpoint timed out"
	}
	return "hook call failed"
}

// hostInNetScopes 是授权屏承诺的「精确主机」检查。绝不做后缀匹配：一条 scope 串在这里、在 surface
// CSP 里、在授权屏上必须指同一个东西。
func hostInNetScopes(host string, domains []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range domains {
		if host == strings.ToLower(domain) {
			return true
		}
	}
	return false
}

// truncate 按字节截断到上限（超限即硬切）。
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
