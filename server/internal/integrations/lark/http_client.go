// http_client.go：APIClient 接口的真实线上实现——飞书（Lark）开放平台 HTTP 客户端。
// 本文件是全仓库较大的单文件：集中处理 tenant_access_token 的获取/缓存/失效恢复、
// IM v1 消息的发送与更新、消息资源（图片/视频等）下载，以及安装期的 Bot 身份查询，
// 是 lark 包所有出站请求的必经之地。包级总览见 doc.go。
package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件剩余部分实现 httpAPIClient——真实的飞书开放平台 HTTP 客户端。
//
// 职责范围：tenant_access_token 的获取与缓存、IM v1 交互式卡片的发送/更新、绑定引导
// 卡片的专属出站，以及安装期 Bot 身份查询（/open-apis/bot/v3/info）——后者由
// RegistrationService 在设备流扫码授权成功后立刻调用。PersonalAgent 的注册协议本身是
// 另一个客户端（RegistrationClient）：它访问的域名不同（accounts.feishu.cn）、认证模型
// 也不同（没有 tenant_access_token——响应体本身就是凭据），所以不在本文件里。
//
// 凭据注入方式：每次调用通过参数 InstallationCredentials 传入「本次安装」的凭据；
// 本客户端从不直接读 lark_installation 表。tenant_access_token 以 app_id 为 key 缓存在
// 进程内，并按照 Lark 返回的 `expire` 字段扣除一段安全余量来判定过期，保证调用方不会
// 在请求中途拿出一个即将失效的 token。

// DefaultResourceDownloadTimeout 是单次消息资源下载的超时上限。导出它是为了让「渠道
// 媒体 settle 不变量」测试能断言：reconciler 的结算延迟远大于任何一条流水线预算。
const DefaultResourceDownloadTimeout = 45 * time.Second

const (
	// defaultLarkBaseURL 是飞书（中国大陆）开放平台的默认域名，是区域为 feishu（或未
	// 设置）的安装的兜底主机；Region.OpenPlatformBaseURL 会把区域为 lark 的安装映射到
	// open.larksuite.com。运维不再通过 MULTICA_LARK_HTTP_BASE_URL 选云——每个安装的
	// 区域字段会自动决定。该环境变量只保留为「部署级覆盖」（代理/mock/单云预发环境）；
	// 测试则把它换成 httptest.Server 的 URL。
	defaultLarkBaseURL = "https://open.feishu.cn"

	// tokenSafetyMargin 是自 Lark 返回的 `expire` 里扣除的安全余量，让我们在 token 真正
	// 失效之前就提前刷新。60 秒远超下面设定的任何单次 HTTP 超时，保证请求往返期间
	// token 一定还有效。
	tokenSafetyMargin = 60 * time.Second

	// defaultRequestTimeout 是普通调用的 HTTP 超时。Lark API 通常远低于 1 秒就能返回；
	// 留出余量是为了覆盖自建 Multica 部署访问 feishu.cn 的跨地域延迟。
	defaultRequestTimeout = 10 * time.Second

	// defaultResourceDownloadTimeout 刻意比普通 OpenAPI 调用长：消息视频是二进制传输，
	// 不是 JSON RPC。但要低于入站去重的 stale-claim 窗口（60 秒），否则一个下载很慢的
	// 副本会被另一个副本抢先回收同一条消息，导致本条无法 append 并标记为已处理。
	defaultResourceDownloadTimeout = DefaultResourceDownloadTimeout

	// 飞书把单条消息资源上限封在 100 MiB。本地传输层的防护闸门要和该契约对齐；
	// 脱离 connector ACK 路径的大媒体处理，不会因大体积传输而阻塞 ACK。
	maxMessageResourceBytes = 100 << 20

	// 访问凭据被拒绝的错误码。看到它说明当前出示的 token 不可用：应丢弃缓存条目，
	// 从 /tenant_access_token/internal 重新签发。二者均出自 Lark 通用错误码表：
	// open.feishu.cn/document/server-docs/api-call-guide/generic-error-code。
	//
	// 99991663 覆盖本客户端唯一会签发的凭据的两半情形——"tenant_access_token 已过期"
	// 与"凭证无效"共用这一个码——也是 #7611 报告的 wedged-cache（token 卡死在缓存里）
	// bug 命中的那个码。
	//
	// 99991664 是 app_access_token 的错误码，本客户端从不签发。它仍被刻意留在同一
	// 分类里：它毫不含糊地表示「你出示的凭据被拒绝」，若哪天 Lark 对我们用 tenant token
	// 认证的调用回了这个码，重新签发是唯一恢复手段。误判一次的代价是有界的一次刷新 +
	// 一次重放；漏掉它则会让缓存再次卡死。
	codeTenantTokenInvalid = 99991663
	codeAppTokenInvalid    = 99991664
)

// HTTPClientConfig 配置真实的 Lark HTTP APIClient（构造函数见 NewHTTPAPIClient）。
type HTTPClientConfig struct {
	// BaseURL 是对 Lark 开放平台根地址的可选部署级覆盖，例如 "https://open.feishu.cn"
	// 或 "https://open.larksuite.com"。一旦设置，它会强制所有调用——无论安装的区域是
	// 什么——都打到该主机；测试把它设为 httptest.Server 的 URL。为空（生产默认）时，
	// 每次调用根据 InstallationCredentials.Region 自行解析主机，这样单个部署能同时服务
	// 飞书和 Lark 两种区域。末尾的 "/" 会被去掉。
	BaseURL string

	// HTTPClient 是所有出站调用使用的 transport。测试会换成 Transport 指向
	// httptest.Server 的 *http.Client。为空时默认新建一个带 defaultRequestTimeout 超时的
	// http.Client。
	HTTPClient *http.Client

	// ResourceHTTPClient 仅用于消息资源下载。它刻意不共享 HTTPClient 的较短超时：
	// 图片/视频资源的传输改用 ResourceDownloadTimeout 兜底。
	ResourceHTTPClient *http.Client

	// ResourceDownloadTimeout 限制单次消息资源下载。0 值时默认采用
	// defaultResourceDownloadTimeout。
	ResourceDownloadTimeout time.Duration

	// Now 可被覆盖，用于让 token 过期相关的测试可确定性推演时间。
	Now func() time.Time

	// Logger 接收关于 Lark 错误码的告警。为 nil 时使用 slog.Default()。
	Logger *slog.Logger
}

func (c HTTPClientConfig) withDefaults() HTTPClientConfig {
	// BaseURL 这里刻意不默认成 defaultLarkBaseURL：空 BaseURL 表示「没有部署级覆盖」，
	// 每次调用再按 InstallationCredentials.Region 解析主机（见 resolveBaseURL），使一个
	// 客户端同时服务飞书与 Lark；非空 BaseURL（MULTICA_LARK_HTTP_BASE_URL，或测试里的
	// httptest URL）则把一切区域都强制指向该主机。
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	if c.ResourceDownloadTimeout == 0 {
		c.ResourceDownloadTimeout = defaultResourceDownloadTimeout
	}
	if c.ResourceHTTPClient == nil {
		c.ResourceHTTPClient = &http.Client{Timeout: c.ResourceDownloadTimeout}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// NewHTTPAPIClient 构造走 HTTPS 与 Lark 开放平台通信的真实 APIClient。每次调用的
// InstallationCredentials 参数携带「本次安装」的凭据；token 以 app_id 为 key 缓存，
// 因此单个 Multica server 可跨多次对同一 app 的调用复用 Lark 的 tenant_access_token。
func NewHTTPAPIClient(cfg HTTPClientConfig) APIClient {
	cfg = cfg.withDefaults()
	return &httpAPIClient{cfg: cfg, tokens: make(map[string]*cachedToken)}
}

// httpAPIClient 是 APIClient 的真实 HTTP 实现，内部只维护配置与进程内 token 缓存。
type httpAPIClient struct {
	cfg HTTPClientConfig

	// mu 保护 tokens 这个 map。
	mu sync.Mutex

	// tokens 按 app_id 缓存 tenant_access_token——注意不是按 (app_id, region)。这是
	// 安全的：一个 Lark/飞书 app_id（"cli_..." 凭据）在两个云上都是全局唯一的，且一个
	// app 只存在于其中一个云，所以同一个 app_id 绝不会对应两个区域。DB 侧用
	// lark_installation 表上的 UNIQUE(app_id) 约束背书了同一假设。万一 Lark 哪天跨云
	// 复用 app_id，这里的缓存 key 和那条约束都需要把 region 加进去。
	tokens map[string]*cachedToken
}

// cachedToken 是缓存的 token 及其本进程判定下的过期时刻。
type cachedToken struct {
	value     string
	expiresAt time.Time
}

// IsConfigured 恒返回 true：只要这个客户端对象存在，出站传输路径（send / patch /
// 绑定引导 / bot info）就已接好。桩实现返回 false，因为那边所有调用都会以
// ErrAPIClientNotConfigured 报错；真实客户端是该契约的反面。
func (c *httpAPIClient) IsConfigured() bool { return true }

// tenantAccessToken 返回指定安装可用的 tenant_access_token：token 在存活期内（已扣除
// 安全余量）直接复用缓存，否则向 Lark 重新换取。
//
// 并发说明：未命中缓存的路径上，并发调用方会在客户端级互斥锁上串行；命中缓存的路径
// 只在查表那一小段持锁，随即在发起任何 I/O 前释放。因此稳态争用只是一次持锁的 map
// 读，而不是每次调用都打一次 HTTP 往返。
func (c *httpAPIClient) tenantAccessToken(ctx context.Context, creds InstallationCredentials) (string, error) {
	if creds.AppID == "" {
		return "", errors.New("lark http client: missing app_id")
	}
	if creds.AppSecret == "" {
		return "", errors.New("lark http client: missing app_secret")
	}

	now := c.cfg.Now()
	c.mu.Lock()
	if t, ok := c.tokens[creds.AppID]; ok && t.expiresAt.After(now) {
		val := t.value
		c.mu.Unlock()
		return val, nil
	}
	c.mu.Unlock()

	// 自建应用（internal）专用端点。市场上架应用 / 多租户应用应改用
	// /tenant_access_token/v3 且请求体结构不同；MVP 阶段 PersonalAgent 是每个
	// workspace 的自建应用，所以固定在 /internal。
	body := map[string]string{
		"app_id":     creds.AppID,
		"app_secret": creds.AppSecret,
	}
	var resp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int64  `json:"expire"`
	}
	if err := c.doJSON(ctx, c.resolveBaseURL(creds), http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", "", body, &resp); err != nil {
		return "", fmt.Errorf("lark http client: tenant_access_token: %w", err)
	}
	if resp.Code != 0 || resp.TenantAccessToken == "" {
		return "", fmt.Errorf("lark http client: tenant_access_token: code=%d msg=%q", resp.Code, resp.Msg)
	}

	expire := time.Duration(resp.Expire) * time.Second
	// 下限钳到安全余量的 2 倍，防止上游异常返回一个不足一分钟的 expire 时，我们缓存了
	// 一个已过安全窗口的 token。
	if expire < tokenSafetyMargin*2 {
		expire = tokenSafetyMargin * 2
	}
	expiresAt := c.cfg.Now().Add(expire - tokenSafetyMargin)

	c.mu.Lock()
	c.tokens[creds.AppID] = &cachedToken{value: resp.TenantAccessToken, expiresAt: expiresAt}
	c.mu.Unlock()

	return resp.TenantAccessToken, nil
}

// resolveBaseURL 为一次调用挑选开放平台主机：显式配置的 cfg.BaseURL（来自
// MULTICA_LARK_HTTP_BASE_URL，或测试里的 httptest URL）覆盖一切区域、把所有流量都
// 导向那里；没有覆盖时按安装的区域取主机，使同一进程服务的飞书与 Lark 安装各回各的云。
func (c *httpAPIClient) resolveBaseURL(creds InstallationCredentials) string {
	if c.cfg.BaseURL != "" {
		return c.cfg.BaseURL
	}
	return creds.Region.OpenPlatformBaseURL()
}

// invalidateToken 丢弃某 app_id 的缓存 token。当 Lark 返回过期/无效 token 错误码时
// 调用，让下一次调用去刷新，而不是反复重试一个过期条目。
func (c *httpAPIClient) invalidateToken(appID string) {
	c.mu.Lock()
	delete(c.tokens, appID)
	c.mu.Unlock()
}

// InvalidateTokenCache 供客户端外部主动丢弃某 app_id 的缓存 tenant_access_token。
// 它服务于凭据轮换场景：重新注册 Bot 会签发新的 app_secret，导致 Lark 吊销所有由旧
// secret 签发的 token；而本缓存 key 是 app_id（它没变），若不主动清掉，会一直重放一个
// Lark 现已拒绝的 token。
func (c *httpAPIClient) InvalidateTokenCache(appID string) {
	c.invalidateToken(appID)
}

// doAuthedJSON 执行一次携带 tenant_access_token 认证的调用。当 Lark 直接拒绝 token
// 本身时，会丢弃缓存条目、签发新 token，并把原请求原样重放恰好一次。
//
// 对这里的所有调用方，重放都是安全的：Lark 在认证阶段、请求被真正处理之前就会拒绝
// 坏 token，所以那时什么都还没投递，重试不会造成消息重复。不做重放的话，刷新只会惠及
// 下一次调用，而触发刷新的那一次回复仍然会丢。
//
// 只有 token 错误会被重试。业务错误码、5xx、网络失败一律原样返回——它们要么是定论、
// 要么投递状态不明，换新 token 都解决不了。
func (c *httpAPIClient) doAuthedJSON(ctx context.Context, creds InstallationCredentials, method, path string, body, out any) error {
	token, err := c.tenantAccessToken(ctx, creds)
	if err != nil {
		return err
	}
	err = c.doJSON(ctx, c.resolveBaseURL(creds), method, path, token, body, out)
	if !isTokenError(larkErrorCode(err)) {
		return err
	}
	c.cfg.Logger.Warn("lark http client: tenant_access_token rejected; refreshing and retrying once",
		"app_id", creds.AppID, "path", path, "err", err)
	c.invalidateToken(creds.AppID)
	fresh, refreshErr := c.tenantAccessToken(ctx, creds)
	if refreshErr != nil {
		return fmt.Errorf("%w (token refresh failed: %v)", err, refreshErr)
	}
	return c.doJSON(ctx, c.resolveBaseURL(creds), method, path, fresh, body, out)
}

// outboundMessageRequest 拼出三个发送方法共享的 (path, body)。当 target.IsSet() 时，
// 消息走 Lark 的回复端点（POST /im/v1/messages/{message_id}/reply），从而挂回触发它的
// 话题线程里——reply_in_thread 携带 target 的 InThread 标志（若父消息本身已在某话题
// 内，Lark 也会自动保持回复在该话题）。否则消息走以 receive_id=chat_id 定位的群聊
// 发送端点，即历史行为。body 用 map[string]any（而非 map[string]string），是因为
// reply_in_thread 是个 bool。
func outboundMessageRequest(chatID ChatID, msgType, content string, target ReplyTarget) (string, map[string]any) {
	if target.IsSet() {
		return "/open-apis/im/v1/messages/" + url.PathEscape(target.MessageID) + "/reply", map[string]any{
			"msg_type":        msgType,
			"content":         content,
			"reply_in_thread": target.InThread,
		}
	}
	q := url.Values{}
	q.Set("receive_id_type", "chat_id")
	return "/open-apis/im/v1/messages?" + q.Encode(), map[string]any{
		"receive_id": string(chatID),
		"msg_type":   msgType,
		"content":    content,
	}
}

// SendInteractiveCard 往群里投递一张全新的交互式卡片，返回 Lark 的 message_id，便于
// Patcher 后续对该卡片做定向 patch。
func (c *httpAPIClient) SendInteractiveCard(ctx context.Context, p SendCardParams) (string, error) {
	if p.ChatID == "" {
		return "", errors.New("lark http client: missing chat_id")
	}
	if p.CardJSON == "" {
		return "", errors.New("lark http client: missing card json")
	}
	path, body := outboundMessageRequest(p.ChatID, "interactive", p.CardJSON, p.ReplyTarget)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := c.doAuthedJSON(ctx, p.InstallationID, http.MethodPost, path, body, &resp); err != nil {
		return "", fmt.Errorf("lark http client: send interactive card: %w", err)
	}
	if resp.Code != 0 || resp.Data.MessageID == "" {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return "", &APIError{Op: "send interactive card", Code: resp.Code, Msg: resp.Msg}
	}
	return resp.Data.MessageID, nil
}

// SendTextMessage 往 Lark 群里投递一条纯文本 IM 消息。这是 Patcher 回复 agent 聊天时
// 的主要出站方式——用普通文本气泡而非交互式卡片，让自由格式的回复读起来像原生 Lark
// 对话。Lark 期望的 content 信封是 JSON 编码的 {"text": "..."}；这里代调用方完成编码，
// 调用方只需传原始文本。
func (c *httpAPIClient) SendTextMessage(ctx context.Context, p SendTextParams) (string, error) {
	if p.ChatID == "" {
		return "", errors.New("lark http client: missing chat_id")
	}
	if p.Text == "" {
		return "", errors.New("lark http client: missing text")
	}
	// Lark 的 text 类型要求 content = JSON 编码的 {"text": "..."}。json.Marshal 负责
	// 转义换行/引号/unicode，保证 agent 回复的内容原样往返、不丢字符。
	contentBytes, err := json.Marshal(map[string]string{"text": p.Text})
	if err != nil {
		return "", fmt.Errorf("lark http client: encode text content: %w", err)
	}
	path, body := outboundMessageRequest(p.ChatID, "text", string(contentBytes), p.ReplyTarget)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := c.doAuthedJSON(ctx, p.InstallationID, http.MethodPost, path, body, &resp); err != nil {
		return "", fmt.Errorf("lark http client: send text message: %w", err)
	}
	if resp.Code != 0 || resp.Data.MessageID == "" {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return "", &APIError{Op: "send text message", Code: resp.Code, Msg: resp.Msg}
	}
	return resp.Data.MessageID, nil
}

// SendMarkdownCard 用 Lark schema-2.0 的信封把 agent 的回复发成交互式卡片，卡身是一个
// 单独的 tag: "markdown" 元素。Lark 客户端会把 markdown 渲染成富文本（粗体、斜体、
// 列表、链接、围栏代码块、表格……），而不是像 msg_type=text 那样把原始 markdown 字符
// 原样显示。我们刻意保留 SendTextMessage 作为纯散文回复的独立路径——给一行「Hello!」
// 套卡片是用户不想要的视觉负担；选 markdown 还是 text 的决策放在 Patcher 层。
//
// 为什么用 schema 2.0 而不是带 div + lark_md 文本元素的旧 schema：旧 lark_md 标签的
// markdown 方言窄得多——没有围栏代码块（语法高亮）、没有表格、没有标题字号。
// schema-2.0 的 markdown 标签更接近 GFM。
func (c *httpAPIClient) SendMarkdownCard(ctx context.Context, p SendMarkdownCardParams) (string, error) {
	if p.ChatID == "" {
		return "", errors.New("lark http client: missing chat_id")
	}
	if p.Markdown == "" {
		return "", errors.New("lark http client: missing markdown body")
	}
	card := map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []any{
				map[string]any{"tag": "markdown", "content": p.Markdown},
			},
		},
	}
	if p.Summary != "" {
		card["config"] = map[string]any{
			"summary": map[string]any{"content": p.Summary},
		}
	}
	cardBytes, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("lark http client: encode markdown card: %w", err)
	}
	path, body := outboundMessageRequest(p.ChatID, "interactive", string(cardBytes), p.ReplyTarget)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := c.doAuthedJSON(ctx, p.InstallationID, http.MethodPost, path, body, &resp); err != nil {
		return "", fmt.Errorf("lark http client: send markdown card: %w", err)
	}
	if resp.Code != 0 || resp.Data.MessageID == "" {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return "", &APIError{Op: "send markdown card", Code: resp.Code, Msg: resp.Msg}
	}
	return resp.Data.MessageID, nil
}

// PatchInteractiveCard 更新一张既有卡片的正文。Lark 的 message-patch 端点会整体替换
// 卡片载荷；调用方（即 Patcher）每次都渲染完整的最新卡片再传进来。
func (c *httpAPIClient) PatchInteractiveCard(ctx context.Context, p PatchCardParams) error {
	if p.LarkCardMessageID == "" {
		return errors.New("lark http client: missing card message id")
	}
	if p.CardJSON == "" {
		return errors.New("lark http client: missing card json")
	}
	body := map[string]string{"content": p.CardJSON}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	path := "/open-apis/im/v1/messages/" + url.PathEscape(p.LarkCardMessageID)
	if err := c.doAuthedJSON(ctx, p.InstallationID, http.MethodPatch, path, body, &resp); err != nil {
		return fmt.Errorf("lark http client: patch interactive card: %w", err)
	}
	if resp.Code != 0 {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return fmt.Errorf("lark http client: patch interactive card: code=%d msg=%q", resp.Code, resp.Msg)
	}
	return nil
}

// SendBindingPromptCard 渲染「成员绑定」引导卡片，并直接投递给未绑定用户的 open_id
// （不是投到群里）。把卡片模板放在本客户端里——而非 dispatcher——意味着 dispatcher
// 永远不必了解 Lark 的卡片 schema。
func (c *httpAPIClient) SendBindingPromptCard(ctx context.Context, p BindingPromptParams) error {
	if p.OpenID == "" {
		return errors.New("lark http client: missing open_id")
	}
	if p.BindURL == "" {
		return errors.New("lark http client: missing bind url")
	}
	cardJSON, err := bindingPromptTemplate(p.BindURL)
	if err != nil {
		return fmt.Errorf("lark http client: render binding prompt: %w", err)
	}
	q := url.Values{}
	q.Set("receive_id_type", "open_id")
	body := map[string]string{
		"receive_id": string(p.OpenID),
		"msg_type":   "interactive",
		"content":    cardJSON,
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	path := "/open-apis/im/v1/messages?" + q.Encode()
	if err := c.doAuthedJSON(ctx, p.InstallationID, http.MethodPost, path, body, &resp); err != nil {
		return fmt.Errorf("lark http client: send binding prompt: %w", err)
	}
	if resp.Code != 0 {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return fmt.Errorf("lark http client: send binding prompt: code=%d msg=%q", resp.Code, resp.Msg)
	}
	return nil
}

// GetBotInfo 调用 /open-apis/bot/v3/info 取得 Bot 在该安装下的 open_id，随后再调
// /open-apis/contact/v3/users/{open_id}?user_id_type=open_id 解析其稳定的 union_id。
// 唯一调用方是 RegistrationService——在设备流注册刚拿到新的 client_id / client_secret
// 之后，服务用这套凭据签发 tenant_access_token 并调用本方法，好让安装行在安装者绑定
// 的同一事务里带着两个 Bot 标识一起冻结入库。
//
// 为什么要两次 API 而不是一次：/bot/v3/info 的公开 schema 不返回 union_id。WS 入站
// 解码器需要 union_id 来区分多 Bot 群聊里 @ 的是哪一个 bot（mention 上按 app 划分的
// open_id 字段在 WS 不同视角下结构相反——见 MUL-2671 的排查结论），所以我们在安装期
// 多付一次 HTTP 往返把它取回来，避免此后每个事件都跑错 supervisor。
//
// union_id 缺失（应用的联系人权限不足被拒，或 Lark 返回空字段）在这里不算硬失败——
// 该安装仍可用于一对一私聊，解码器在运维修复权限前可以先退回（有缺陷的）open_id 匹配
// 路径。我们记一条告警，让这个缺口可见。
//
// 上游 API 返回的其他字段（显示名、头像、IP 白名单）刻意丢弃；下游有需要时可以凭
// bot_open_id 按需再查。把那些冻结进我们的 schema，只会给每次运维在 Lark 侧修改 Bot
// 造成一个漂移面。
func (c *httpAPIClient) GetBotInfo(ctx context.Context, creds InstallationCredentials) (BotInfo, error) {
	if creds.AppID == "" || creds.AppSecret == "" {
		return BotInfo{}, errors.New("lark http client: missing app credentials for GetBotInfo")
	}
	var botResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := c.doAuthedJSON(ctx, creds, http.MethodGet, "/open-apis/bot/v3/info", nil, &botResp); err != nil {
		return BotInfo{}, fmt.Errorf("lark http client: bot info: %w", err)
	}
	if botResp.Code != 0 {
		if isTokenError(botResp.Code) {
			c.invalidateToken(creds.AppID)
		}
		return BotInfo{}, fmt.Errorf("lark http client: bot info: code=%d msg=%q", botResp.Code, botResp.Msg)
	}
	if botResp.Bot.OpenID == "" {
		return BotInfo{}, errors.New("lark http client: bot info: response missing open_id")
	}

	// 经通讯录端点解析 union_id。软失败处理：记日志并返回 UnionID 为空的 BotInfo。
	// 调用方（RegistrationService.finishSuccess）接受这个缺口，把已有的字段落库。
	unionID, lookupErr := c.fetchBotUnionID(ctx, creds, botResp.Bot.OpenID)
	if lookupErr != nil {
		c.cfg.Logger.Warn("lark http client: bot union_id lookup failed; continuing without it",
			"app_id", creds.AppID,
			"bot_open_id", botResp.Bot.OpenID,
			"err", lookupErr)
	}
	return BotInfo{OpenID: OpenID(botResp.Bot.OpenID), UnionID: unionID}, nil
}

// GetMessage 按 id 取回一条消息：
// GET /open-apis/im/v1/messages/{message_id}。该端点总把结果包在 data.items[] 里——
// 普通消息是一个元素，merge_forward 则是一个转发哨兵后跟打包的子消息们。我们传
// user_id_type=open_id，让 sender.id 与 mentions[].id 以 open_id 返回，与包内其余
// 代码使用的标识一致。
//
// body.content 原样透传（Lark 二次编码的、按 msg_type 区分的 JSON 字符串），由
// enricher 的 flattener 负责解释。已删除 / 越权的消息会以 Lark 错误码浮现，我们把它
// 转成普通 Go error，让 enricher 可以降级为 "[unable to fetch]" 占位而不中断入站管线。
func (c *httpAPIClient) GetMessage(ctx context.Context, creds InstallationCredentials, messageID string) ([]LarkMessage, error) {
	if messageID == "" {
		return nil, errors.New("lark http client: missing message_id")
	}
	q := url.Values{}
	q.Set("user_id_type", "open_id")
	path := "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "?" + q.Encode()

	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []larkRESTMessageItem `json:"items"`
		} `json:"data"`
	}
	if err := c.doAuthedJSON(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("lark http client: get message: %w", err)
	}
	if resp.Code != 0 {
		if isTokenError(resp.Code) {
			c.invalidateToken(creds.AppID)
		}
		return nil, fmt.Errorf("lark http client: get message: code=%d msg=%q", resp.Code, resp.Msg)
	}

	out := make([]LarkMessage, 0, len(resp.Data.Items))
	for _, it := range resp.Data.Items {
		out = append(out, it.normalize())
	}
	return out, nil
}

// larkListMessagesMaxPageSize 是 Lark 对单页 im/v1/messages 的硬上限。我们收敛到这个
// 值，让请求更多的调用方静默拿到上限页，而不是被 Lark 回 400。
const larkListMessagesMaxPageSize = 50

// ListChatMessages 走 GET /open-apis/im/v1/messages 取一段有界、最近的会话消息窗口。
// 与 GetMessage（按 id 取单条）不同，这里是列会话；它支撑 enricher 的群组上下文预取。
// 容器默认是群聊（container_id_type=chat），当 p.ThreadID 有值时则是单个话题
// （container_id_type=thread）——话题容器可避免话题里的 @ 提及看到共享同一 chat_id 的
// 兄弟话题（#5835）。我们传 sort_type=ByCreateTimeDesc 让最新消息排最前，于是很小的
// page_size 就能覆盖「最近 N 条」而无需翻页，把入站 ACK 路径的扇出压到单次往返。
// user_id_type=open_id 与包内其余代码的标识一致；body.content 原样透传给 enricher 的
// flattener 解释。
func (c *httpAPIClient) ListChatMessages(ctx context.Context, creds InstallationCredentials, p ListMessagesParams) ([]LarkMessage, error) {
	if p.ChatID == "" {
		return nil, errors.New("lark http client: missing chat_id")
	}
	size := p.PageSize
	if size <= 0 {
		size = 1
	} else if size > larkListMessagesMaxPageSize {
		size = larkListMessagesMaxPageSize
	}
	q := url.Values{}
	if p.ThreadID != "" {
		// 话题范围的窗口：只看这个话题的消息，让话题内的 @ 提及不会拉到共享 chat_id
		// 的兄弟话题（#5835）。thread 容器不接受 end_time，所以这里不传；调用方改为在
		// 客户端把窗口锚定到触发时间上。
		q.Set("container_id_type", "thread")
		q.Set("container_id", p.ThreadID)
	} else {
		q.Set("container_id_type", "chat")
		q.Set("container_id", string(p.ChatID))
		if p.EndTime > 0 {
			q.Set("end_time", strconv.FormatInt(p.EndTime, 10))
		}
	}
	q.Set("sort_type", "ByCreateTimeDesc")
	q.Set("page_size", strconv.Itoa(size))
	q.Set("user_id_type", "open_id")
	path := "/open-apis/im/v1/messages?" + q.Encode()

	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []larkRESTMessageItem `json:"items"`
		} `json:"data"`
	}
	if err := c.doAuthedJSON(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("lark http client: list chat messages: %w", err)
	}
	if resp.Code != 0 {
		if isTokenError(resp.Code) {
			c.invalidateToken(creds.AppID)
		}
		return nil, fmt.Errorf("lark http client: list chat messages: code=%d msg=%q", resp.Code, resp.Msg)
	}

	out := make([]LarkMessage, 0, len(resp.Data.Items))
	for _, it := range resp.Data.Items {
		out = append(out, it.normalize())
	}
	return out, nil
}

// DownloadMessageResource 从 Lark/飞书取回一条二进制消息资源（图片、视频、文件、音频）。
// 部分失败场景下，业务错误仍以带 code/msg 的 JSON 形式返回，所以在把响应当资源字节
// 处理之前要先检查「长得像 JSON」的响应。
func (c *httpAPIClient) DownloadMessageResource(ctx context.Context, creds InstallationCredentials, p DownloadResourceParams) (DownloadedResource, error) {
	stream, err := c.DownloadMessageResourceStream(ctx, creds, p)
	if err != nil {
		return DownloadedResource{}, err
	}
	defer stream.Body.Close()
	rawBody, err := io.ReadAll(stream.Body)
	if err != nil {
		return DownloadedResource{}, fmt.Errorf("lark http client: download resource: read body: %w", err)
	}
	sizeBytes := stream.SizeBytes
	if sizeBytes == 0 {
		sizeBytes = int64(len(rawBody))
	}
	return DownloadedResource{
		Data:        rawBody,
		ContentType: stream.ContentType,
		Filename:    stream.Filename,
		SizeBytes:   sizeBytes,
	}, nil
}

// DownloadMessageResourceStream 以流式返回资源下载（DownLoadResourceParams 定位具体
// 文件）。流式是为了把大文件的传输与上层处理解耦。downloadMessageResourceStreamOnce
// 真正发请求；本方法在外面套了与 doAuthedJSON 相同的刷新-重试循环。
func (c *httpAPIClient) DownloadMessageResourceStream(ctx context.Context, creds InstallationCredentials, p DownloadResourceParams) (DownloadedResourceStream, error) {
	if p.MessageID == "" {
		return DownloadedResourceStream{}, errors.New("lark http client: missing message_id")
	}
	if p.FileKey == "" {
		return DownloadedResourceStream{}, errors.New("lark http client: missing file_key")
	}
	q := url.Values{}
	if p.Type != "" {
		q.Set("type", p.Type)
	}
	path := "/open-apis/im/v1/messages/" + url.PathEscape(p.MessageID) + "/resources/" + url.PathEscape(p.FileKey)
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	// 与 doAuthedJSON 相同的「刷新并重放一次」契约：Lark 在吐出任何字节之前就拒绝坏
	// token，所以把这个 GET 重放一次不可能得到半个下载好的资源。
	for attempt := 0; ; attempt++ {
		token, err := c.tenantAccessToken(ctx, creds)
		if err != nil {
			return DownloadedResourceStream{}, err
		}
		stream, err := c.downloadMessageResourceStreamOnce(ctx, creds, path, token)
		if err == nil {
			return stream, nil
		}
		if attempt > 0 || !isTokenError(larkErrorCode(err)) {
			return DownloadedResourceStream{}, err
		}
		c.cfg.Logger.Warn("lark http client: tenant_access_token rejected on resource download; refreshing and retrying once",
			"app_id", creds.AppID, "err", err)
		c.invalidateToken(creds.AppID)
	}
}

// downloadMessageResourceStreamOnce 实际执行一次资源下载请求：带超时的 GET + Bearer 鉴权
// + 状态码与体积守卫，成功时返回包装好的下载流。区别于 doJSON，它处理的是二进制而非
// JSON RPC，走独立的 ResourceHTTPClient 与 ResourceDownloadTimeout。
func (c *httpAPIClient) downloadMessageResourceStreamOnce(ctx context.Context, creds InstallationCredentials, path, token string) (DownloadedResourceStream, error) {
	downloadCtx := ctx
	var cancel context.CancelFunc
	if c.cfg.ResourceDownloadTimeout > 0 {
		downloadCtx, cancel = context.WithTimeout(ctx, c.cfg.ResourceDownloadTimeout)
	}
	req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, c.resolveBaseURL(creds)+path, nil)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return DownloadedResourceStream{}, fmt.Errorf("lark http client: download resource: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.cfg.ResourceHTTPClient.Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return DownloadedResourceStream{}, fmt.Errorf("lark http client: download resource: http do: %w", err)
	}
	// 关闭响应体时顺带 cancel 超时 ctx，释放定时器资源；后续每个提前 return 的路径都
	// 复用这个闭包，避免忘记 close。
	closeWithCancel := func() {
		resp.Body.Close()
		if cancel != nil {
			cancel()
		}
	}
	if resp.ContentLength > maxMessageResourceBytes {
		closeWithCancel()
		return DownloadedResourceStream{}, fmt.Errorf("lark http client: download resource: resource exceeds %d bytes", maxMessageResourceBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 非 2xx 响应体可能携带 Lark 的业务错误码，读出并解析它，好让调用方（含 token
		// 重试循环）能按码分类。
		rawBody, readErr := readMessageResourceErrorBody(resp.Body)
		closeWithCancel()
		if readErr != nil {
			return DownloadedResourceStream{}, readErr
		}
		code, msg := parseLarkErrorBody(rawBody)
		return DownloadedResourceStream{}, fmt.Errorf("lark http client: download resource: %w", &larkAPIStatusError{
			StatusCode: resp.StatusCode,
			Code:       code,
			Msg:        msg,
			Raw:        truncate(string(rawBody), 512),
		})
	}
	contentType := resp.Header.Get("Content-Type")
	// 少数失败情况下资源端点也会返回「HTTP 200 + JSON 错误体」，这里先嗅探 Content-Type
	// 是 JSON 的响应，优先按业务错误解析，而不是把错误 JSON 当二进制资源交付。
	if strings.Contains(strings.ToLower(contentType), "json") {
		rawBody, readErr := readMessageResourceErrorBody(resp.Body)
		closeWithCancel()
		if readErr != nil {
			return DownloadedResourceStream{}, readErr
		}
		var apiResp struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(rawBody, &apiResp); err == nil && apiResp.Code != 0 {
			// token 失效处理是调用方重试循环的事——该循环同时拥有这个形状和那个非 2xx
			// 形状的错误。
			return DownloadedResourceStream{}, &APIError{Op: "download resource", Code: apiResp.Code, Msg: apiResp.Msg}
		}
		return DownloadedResourceStream{
			Body:        cancelOnClose(io.NopCloser(bytes.NewReader(rawBody)), cancel),
			ContentType: contentType,
			Filename:    filenameFromContentDisposition(resp.Header.Get("Content-Disposition")),
			SizeBytes:   int64(len(rawBody)),
		}, nil
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	sizeBytes := resp.ContentLength
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	// 正常二进制资源：用 maxBytesReadCloser 在读取侧限制体积，超过 100 MiB 即报错，
	// 而不依赖 Content-Length（chunked 或错误头时可能缺失或不准）。
	return DownloadedResourceStream{
		Body:        cancelOnClose(&maxBytesReadCloser{r: resp.Body, remaining: maxMessageResourceBytes}, cancel),
		ContentType: contentType,
		Filename:    filenameFromContentDisposition(resp.Header.Get("Content-Disposition")),
		SizeBytes:   sizeBytes,
	}, nil
}

// readMessageResourceErrorBody 读出错误响应体，并套用 maxMessageResourceBytes 上限，
// 防止一个失控的错误页无限占用内存。
func readMessageResourceErrorBody(body io.Reader) ([]byte, error) {
	rawBody, err := io.ReadAll(io.LimitReader(body, maxMessageResourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("lark http client: download resource: read body: %w", err)
	}
	if len(rawBody) > maxMessageResourceBytes {
		return nil, fmt.Errorf("lark http client: download resource: resource exceeds %d bytes", maxMessageResourceBytes)
	}
	return rawBody, nil
}

// maxBytesReadCloser 是带剩余字节配额的读取器：一旦读满配额后流里还有数据，就返回
// 「资源超过上限」错误，实现读取侧的体积守卫。
type maxBytesReadCloser struct {
	r         io.ReadCloser
	remaining int64
}

func (r *maxBytesReadCloser) Read(p []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(p)) > r.remaining {
			p = p[:r.remaining]
		}
		n, err := r.r.Read(p)
		r.remaining -= int64(n)
		return n, err
	}
	// 配额已用尽：再多读一个字节，若上游仍有数据，说明资源超限，报错而非截断交付。
	var one [1]byte
	n, err := r.r.Read(one[:])
	if n > 0 {
		return 0, fmt.Errorf("lark http client: download resource: resource exceeds %d bytes", maxMessageResourceBytes)
	}
	return 0, err
}

func (r *maxBytesReadCloser) Close() error {
	return r.r.Close()
}

// cancelReadCloser 包装一个在 Close 时顺带触发 cancel 的响应体，用于及时释放下载用的
// 超时上下文。
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

// cancelOnClose 让 body 在 Close 时同时调用 cancel；cancel 为 nil 时原样返回 body。
func cancelOnClose(body io.ReadCloser, cancel context.CancelFunc) io.ReadCloser {
	if cancel == nil {
		return body
	}
	return &cancelReadCloser{ReadCloser: body, cancel: cancel}
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

// filenameFromContentDisposition 从 Content-Disposition 响应头解析 filename 参数，
// 拿不到或解析失败时返回空串。
func filenameFromContentDisposition(raw string) string {
	if raw == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return ""
	}
	return params["filename"]
}

// larkBatchGetUsersMaxIDs 是 Lark 对单次 contact/v3/users/batch 调用 user_ids 数量的
// 硬上限。超出部分直接丢弃而非报错，让请求更多的调用方也能拿到前 50 个的解析结果。
const larkBatchGetUsersMaxIDs = 50

// AddMessageReaction 经 POST /open-apis/im/v1/messages/{message_id}/reactions 给一条
// 消息加 emoji 回应，返回 reaction_id 供之后删除该回应使用。
func (c *httpAPIClient) AddMessageReaction(ctx context.Context, p AddReactionParams) (string, error) {
	if p.MessageID == "" {
		return "", errors.New("lark http client: missing message_id")
	}
	if p.EmojiType == "" {
		return "", errors.New("lark http client: missing emoji_type")
	}
	body := map[string]any{
		"reaction_type": map[string]string{"emoji_type": p.EmojiType},
	}
	path := "/open-apis/im/v1/messages/" + url.PathEscape(p.MessageID) + "/reactions"
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ReactionID string `json:"reaction_id"`
		} `json:"data"`
	}
	if err := c.doAuthedJSON(ctx, p.InstallationID, http.MethodPost, path, body, &resp); err != nil {
		return "", fmt.Errorf("lark http client: add message reaction: %w", err)
	}
	if resp.Code != 0 || resp.Data.ReactionID == "" {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return "", fmt.Errorf("lark http client: add message reaction: code=%d msg=%q", resp.Code, resp.Msg)
	}
	return resp.Data.ReactionID, nil
}

// DeleteMessageReaction 经 DELETE /open-apis/im/v1/messages/{message_id}/reactions/
// {reaction_id} 从消息上移除一个回应。
func (c *httpAPIClient) DeleteMessageReaction(ctx context.Context, p DeleteReactionParams) error {
	if p.MessageID == "" {
		return errors.New("lark http client: missing message_id")
	}
	if p.ReactionID == "" {
		return errors.New("lark http client: missing reaction_id")
	}
	path := "/open-apis/im/v1/messages/" + url.PathEscape(p.MessageID) + "/reactions/" + url.PathEscape(p.ReactionID)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := c.doAuthedJSON(ctx, p.InstallationID, http.MethodDelete, path, nil, &resp); err != nil {
		return fmt.Errorf("lark http client: delete message reaction: %w", err)
	}
	if resp.Code != 0 {
		if isTokenError(resp.Code) {
			c.invalidateToken(p.InstallationID.AppID)
		}
		return fmt.Errorf("lark http client: delete message reaction: code=%d msg=%q", resp.Code, resp.Msg)
	}
	return nil
}

// BatchGetUsers 经 GET /open-apis/contact/v3/users/batch?user_ids=…&user_id_type=open_id
// 把用户 open_id 解析成显示名。它是 fetchBotUnionID 那种单用户通讯录查询的批量版。
// 只有 API 实际返回的 id->name 对会进结果；通讯录权限受限或 id 未知时只是得到一张更小
// 的 map（code==0 但 items 更少），绝不会报错，enricher 因此可降级为位置式发言人标签。
// 超出每调用 50 个上限的 id 会被丢弃。
func (c *httpAPIClient) BatchGetUsers(ctx context.Context, creds InstallationCredentials, openIDs []string) (map[string]string, error) {
	if len(openIDs) == 0 {
		return map[string]string{}, nil
	}
	if len(openIDs) > larkBatchGetUsersMaxIDs {
		openIDs = openIDs[:larkBatchGetUsersMaxIDs]
	}
	q := url.Values{}
	q.Set("user_id_type", "open_id")
	for _, id := range openIDs {
		if id != "" {
			q.Add("user_ids", id)
		}
	}
	path := "/open-apis/contact/v3/users/batch?" + q.Encode()

	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				OpenID string `json:"open_id"`
				Name   string `json:"name"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := c.doAuthedJSON(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("lark http client: batch get users: %w", err)
	}
	if resp.Code != 0 {
		if isTokenError(resp.Code) {
			c.invalidateToken(creds.AppID)
		}
		return nil, fmt.Errorf("lark http client: batch get users: code=%d msg=%q", resp.Code, resp.Msg)
	}

	out := make(map[string]string, len(resp.Data.Items))
	for _, it := range resp.Data.Items {
		if it.OpenID != "" && it.Name != "" {
			out[it.OpenID] = it.Name
		}
	}
	return out, nil
}

// larkRESTMessageItem 是 get/list 端点返回的 IM v1 消息条目结构。它与 WS 收消息事件有
// 两处 enricher 关心的差异：字段名是 msg_type（而非 message_type）；sender.id /
// mentions[].id 是扁平的字符串（而非嵌套的 id 对象）。
type larkRESTMessageItem struct {
	MessageID      string `json:"message_id"`
	RootID         string `json:"root_id"`
	ParentID       string `json:"parent_id"`
	ThreadID       string `json:"thread_id"`
	UpperMessageID string `json:"upper_message_id"`
	MsgType        string `json:"msg_type"`
	CreateTime     string `json:"create_time"`
	Deleted        bool   `json:"deleted"`
	Sender         struct {
		ID         string `json:"id"`
		IDType     string `json:"id_type"`
		SenderType string `json:"sender_type"`
	} `json:"sender"`
	Body struct {
		Content string `json:"content"`
	} `json:"body"`
	Mentions []struct {
		Key  string `json:"key"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"mentions"`
}

// normalize 把 REST 条目转换成包内统一的 LarkMessage，完成 msg_type→MessageType 等
// 字段名与形状的适配。
func (it larkRESTMessageItem) normalize() LarkMessage {
	m := LarkMessage{
		MessageID:      it.MessageID,
		MessageType:    it.MsgType,
		Content:        it.Body.Content,
		SenderID:       it.Sender.ID,
		SenderType:     it.Sender.SenderType,
		CreateTime:     it.CreateTime,
		ParentID:       it.ParentID,
		RootID:         it.RootID,
		ThreadID:       it.ThreadID,
		UpperMessageID: it.UpperMessageID,
		Deleted:        it.Deleted,
	}
	for _, mn := range it.Mentions {
		m.Mentions = append(m.Mentions, LarkMessageMention{Key: mn.Key, ID: mn.ID, Name: mn.Name})
	}
	return m
}

// fetchBotUnionID 经 /open-apis/contact/v3/users/{open_id}?user_id_type=open_id 从 Bot
// 的 open_id 解析 union_id。从 GetBotInfo 拆出来，是为了让失败模式显式化，也让只需
// open_id 的调用方不必付出第二次 HTTP 往返的代价。
//
// 返回空串 + nil error 是合法结局：应用通讯录权限受限时，Lark 的用户端点可能以 code=0
// 返回但不带 union_id 字段。调用方记录日志后继续即可；单 Bot 部署里基于 open_id 的匹配
// 没有歧义，解码器照常工作。
func (c *httpAPIClient) fetchBotUnionID(ctx context.Context, creds InstallationCredentials, openID string) (string, error) {
	if openID == "" {
		return "", errors.New("empty open_id")
	}
	q := url.Values{}
	q.Set("user_id_type", "open_id")
	path := "/open-apis/contact/v3/users/" + url.PathEscape(openID) + "?" + q.Encode()
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			User struct {
				UnionID string `json:"union_id"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := c.doAuthedJSON(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return "", fmt.Errorf("contact users: %w", err)
	}
	if resp.Code != 0 {
		// invalidateToken 以 app_id 为 key（对应 httpAPIClient.tokens 的缓存键），
		// 而不是 bearer 字符串。传 bearer 进去不会清掉任何东西，过期的 token 会一直被
		// 复用，直到自然 TTL 到期。
		if isTokenError(resp.Code) {
			c.invalidateToken(creds.AppID)
		}
		return "", fmt.Errorf("contact users: code=%d msg=%q", resp.Code, resp.Msg)
	}
	return resp.Data.User.UnionID, nil
}

// doJSON 把「动词 + URL + 鉴权头 + JSON 编解码」这套例行动作收进来，让每个公开方法
// 保持成只做形状适配的薄层。baseURL 是调用方经 resolveBaseURL 解析出的本次开放平台
// 主机（区域感知）。token == "" 时跳过 Authorization 头（只有 tenant_access_token 端点
// 走这条路）。
func (c *httpAPIClient) doJSON(ctx context.Context, baseURL, method, path, token string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Lark 在非 2xx 回复的 body 里仍会带业务 code，所以解析它而不是把响应折叠成
		// 一个不透明的传输错误。否则一切按 code 分类的逻辑——首当其冲是 token 失效——
		// 都永远够不到。
		code, msg := parseLarkErrorBody(rawBody)
		return &larkAPIStatusError{
			StatusCode: resp.StatusCode,
			Code:       code,
			Msg:        msg,
			Raw:        truncate(string(rawBody), 512),
		}
	}
	if out != nil && len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, out); err != nil {
			return fmt.Errorf("decode body: %w (raw=%s)", err, truncate(string(rawBody), 256))
		}
	}
	return nil
}

// parseLarkErrorBody 尽力从错误体里取出 Lark 的 {code, msg} 信封。body 根本不是 JSON
// 时（代理的 HTML 错误页、空的 502）返回 0 / ""——没有 code、无可分类——从而让这类
// 失败保持在纯传输错误路径上。
func parseLarkErrorBody(raw []byte) (int, string) {
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, ""
	}
	return env.Code, env.Msg
}

// isTokenError 报告该 Lark 业务码是否属于「出示的凭据被拒」这一类。
func isTokenError(code int) bool {
	return code == codeTenantTokenInvalid || code == codeAppTokenInvalid
}

// larkAPIStatusError 是「HTTP 状态非 2xx」的失败回复。它携带 Lark 放在 body 里的业务
// code，因为 Lark 正是这样报告认证失败的：无效或过期的 tenant_access_token 会以
// HTTP 400 + {"code":99991663} 回来，而不是一个 2xx 信封（#7611）。只读 2xx body 的
// 客户端因此永远发现不了缓存的 token 已死，会把死 token 一直重放到进程重启。
//
// 当 body 里没有可解析的 Lark 信封时 Code 为 0。
type larkAPIStatusError struct {
	StatusCode int
	Code       int
	Msg        string
	Raw        string
}

func (e *larkAPIStatusError) Error() string {
	// 保留历史措辞——运维 grep 日志就靠它。
	return fmt.Sprintf("http %d: %s", e.StatusCode, e.Raw)
}

// larkErrorCode 从 err 里挖出 Lark 业务码，两种携带该码的错误形状都认：非 2xx 回复
// （larkAPIStatusError）或 2xx 信封里调用方拒绝的业务错误（APIError）。其余一切——
// 包括网络失败与超时——返回 0，于是对结果做按码查表永远不会匹配上一个含混的传输错误。
func larkErrorCode(err error) int {
	code, _, _ := larkErrorCodeMsg(err)
	return code
}

// larkErrorCodeMsg 是 larkErrorCode 的增强版，额外返回 Lark 的 msg 与「err 是否真的
// 携带了 code」。那些要回退到匹配错误文本的分类器需要第三个结果：「没有 code」和
// 「code 为 0」不能都像 Lark 的裁决——否则网关的 HTML 502 也会被归类成 Lark 裁决。
func larkErrorCodeMsg(err error) (int, string, bool) {
	var statusErr *larkAPIStatusError
	if errors.As(err, &statusErr) {
		return statusErr.Code, statusErr.Msg, statusErr.Code != 0
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code, apiErr.Msg, apiErr.Code != 0
	}
	return 0, "", false
}

// APIError 是结构化的 Lark 业务错误：请求到达 Lark、HTTP 返回 200，但 Lark 以非零
// `code` 拒绝了它。这与 doJSON 抛出的传输层错误（网络失败、5xx、超时，都是普通包装
// 错误）不同。这个区别对话题回复的回退决策至关重要：业务错误码是定论（「什么都没发出，
// 原因如下」），传输错误则是含混的（「消息可能已投递也可能没有」），后者绝不能触发可能
// 造成回复重复或泄漏到别处的群聊级重试。
type APIError struct {
	Op   string
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("lark http client: %s: code=%d msg=%q", e.Op, e.Code, e.Msg)
}

// threadReplyUnsupportedCodes 是回复端点这样一类业务码：它们确凿地表示「这条触发消息 /
// 话题收不了话题内回复」且什么都没发出，而对同一群聊的普通群聊级发送不受影响。只有这些
// 码才配得上群聊级回退。限流（230020）、"消息正在发送"（230049，含混）、权限/内容错误
// （在群聊级一样会失败），以及所有传输/5xx/超时失败都被刻意排除：那些保持为失败，这样
// 我们既不会重复回复，也不会把本该只在话题里的回复泄漏到主群聊。码出自 IM 回复消息端点
// 的错误码表。
var threadReplyUnsupportedCodes = map[int]struct{}{
	230011: {}, // 触发消息已被撤回
	230019: {}, // 话题不存在
	230050: {}, // 触发消息对运维者不可见
	230071: {}, // 该群不支持话题内回复
	230072: {}, // 聚合消息不支持话题内回复
	230111: {}, // 不能回复一条阅后即焚的消息
}

// isThreadReplyUnsupported 报告 err 是否携带「话题回复落不到这个目标上」的 Lark 业务码。
// 只有这类错误可以安全地在群聊级重试。传输错误与其他业务码都返回 false。
//
// 码经 larkErrorCode 读出，所以无论 Lark 是回了 2xx 信封还是非 2xx 状态都能找到——
// 「什么都没发」的定论在哪种形态下都一样是定论，能走到这里的只有名单上的码。没有 Lark
// 码的错误（网络失败、超时、代理的 HTML 502）得出 0，不在名单里，因此含混的投递状态
// 永远不会触发回退。
func isThreadReplyUnsupported(err error) bool {
	_, ok := threadReplyUnsupportedCodes[larkErrorCode(err)]
	return ok
}

// truncate 把字符串截到 n 个字节并加省略号，用于把可能很长的错误体塞进错误信息。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// bindingPromptTemplate 渲染「你需要绑定」引导卡片：单个指向兑换 URL 的主 CTA，其余
// 卡身为纯文本中文文案，与应用内语气一致。
//
// 放在这里（而非 defaultRenderer），是为了让绑定卡模板能独立于 Patcher 渲染的流式状态
// 卡片演进——二者生命周期不同（绑定卡一次性发出，状态卡原地 patch）。
func bindingPromptTemplate(bindURL string) (string, error) {
	doc := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": "blue",
			"title":    map[string]any{"tag": "plain_text", "content": "Multica"},
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": "你还没有绑定 Multica 账户。点击下方按钮完成绑定后即可使用此 Agent。",
				},
			},
			map[string]any{
				"tag": "action",
				"actions": []any{
					map[string]any{
						"tag":  "button",
						"text": map[string]any{"tag": "plain_text", "content": "去绑定"},
						"type": "primary",
						"url":  bindURL,
					},
				},
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
