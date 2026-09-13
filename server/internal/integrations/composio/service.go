// Package composio 是 Multica 与独立 Composio SDK（server/pkg/composio）之间的
// Stage 2 业务集成胶水层。它承载 Multica 侧的语义：带签名的连接握手 state、本地
// user_composio_connection 镜像表、幂等的断开连接，以及按用户划分的 MCP 会话助手。
//
// 它刻意不再用另一个 HTTP client 包一层 SDK——而是通过 SDK 接口直接组合
// *sdk.Client，这样测试能塞进一个 fake。
//
// MVP 范围（MUL-3720）：工具包动态发现。工具包→auth-config 的映射在请求时从
// Composio 的 /auth_configs 端点解析（短暂缓存），因此只要在 Composio 控制台为某个
// 工具包启用了 auth config，它立刻就能被连接——无需环境变量、无需重新部署。没有任何
// 已启用 auth config 的工具包会被拒绝。
package composio

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/multica-ai/multica/server/pkg/composio"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 抛给 handler 层的服务级错误。
var (
	// ErrToolkitNotSupported 由 BeginConnect 返回：请求的工具包在 Composio 项目里没有
	// 启用的 auth config，因而拿不到可用来开 connect link 的 auth_config_id。
	ErrToolkitNotSupported = errors.New("composio: toolkit not supported")
	// ErrConnectNotSuccessful 由 CompleteCallback 返回：Composio 报告状态非成功——
	// 不写活跃行。
	ErrConnectNotSuccessful = errors.New("composio: connection was not successful")
	// ErrConnectionNotFound 由 Disconnect 返回：连接 id 不属于该用户（或根本不存在）。
	ErrConnectionNotFound = errors.New("composio: connection not found")
	// ErrAccountVerification 由 CompleteCallback 返回：回调携带的 connected_account_id
	// 无法（向 Composio）确认归属于签名 state 里指定的用户/auth-config——即被篡改或未知
	// 的账户 id。不写本地行。
	ErrAccountVerification = errors.New("composio: connected account verification failed")
)

// defaultStateTTL 限制一次连接握手在 BeginConnect 与 Composio 回调之间能搁置多久。
// 对托管 OAuth 流程五分钟足够宽裕，同时把重放窗口压得很小。
const defaultStateTTL = 5 * time.Minute

// defaultAuthCacheTTL 限制已解析的 工具包→auth-config map 在被重新拉取前缓存多久。
// 短到在控制台启用一个 auth config 后几分钟内就能生效；长到一批集中的 connect/list
// 请求不会反复冲击 /auth_configs。
const defaultAuthCacheTTL = 5 * time.Minute

// maxAuthConfigPages / maxToolkitPages 给分页全量拉取的循环加上限，让病态或出 bug 的
// 上游游标无法无限空转。以每页 limit=1000 计，这些上限远超任何真实项目/目录的规模。
const (
	maxAuthConfigPages  = 20
	maxToolkitPages     = 20
	listPageLimit       = 1000
	composioLogoBaseURL = "https://logos.composio.dev/api"
)

// SDK 是服务依赖的 *sdk.Client 子集。声明成接口，handler/service 测试就能注入 fake
// 而不用真的去打 Composio。*sdk.Client 满足该接口。
type SDK interface {
	CreateLink(ctx context.Context, req sdk.CreateLinkRequest) (*sdk.CreateLinkResponse, error)
	ListConnectedAccounts(ctx context.Context, req sdk.ListConnectedAccountsRequest) (*sdk.ListConnectedAccountsResponse, error)
	ListAuthConfigs(ctx context.Context, req sdk.ListAuthConfigsRequest) (*sdk.ListAuthConfigsResponse, error)
	ListToolkits(ctx context.Context, req sdk.ListToolkitsRequest) (*sdk.ListToolkitsResponse, error)
	RevokeConnection(ctx context.Context, connectedAccountID string) error
	DeleteConnectedAccount(ctx context.Context, connectedAccountID string) error
	CreateSession(ctx context.Context, req sdk.CreateSessionRequest) (*sdk.CreateSessionResponse, error)
	MCPAuthHeaders() map[string]string
}

// Store 是本地连接镜像表的持久化接缝。*db.Queries 满足它；测试用内存 fake。
type Store interface {
	UpsertUserComposioConnection(ctx context.Context, arg db.UpsertUserComposioConnectionParams) (db.UserComposioConnection, error)
	ListActiveUserComposioConnections(ctx context.Context, userID pgtype.UUID) ([]db.UserComposioConnection, error)
	GetUserComposioConnection(ctx context.Context, arg db.GetUserComposioConnectionParams) (db.UserComposioConnection, error)
	MarkUserComposioConnectionRevoked(ctx context.Context, arg db.MarkUserComposioConnectionRevokedParams) error
}

// Config 配置一个 Service。
type Config struct {
	// StateSecret 给连接 state 的 HMAC 签名。必填（非空）。
	StateSecret []byte
	// CallbackBaseURL 是本 API 的绝对、公网、无尾斜杠的根地址（如 "https://multica.ai"）。
	// Composio 回调 URL 由 CallbackBaseURL + CallbackPath 拼出。必填。
	CallbackBaseURL string
	// FrontendBaseURL 是用于拼「回调后浏览器重定向目标」的 web 应用根地址（如
	// "https://multica.ai"）。可为空，此时 CallbackRedirect 返回站内相对路径。
	FrontendBaseURL string
	// StateTTL 覆盖默认的连接 state 寿命。0 使用 defaultStateTTL。
	StateTTL time.Duration
	// AuthConfigTTL 覆盖「已解析的工具包→auth-config map」的缓存时长。0 使用
	// defaultAuthCacheTTL。
	AuthConfigTTL time.Duration
	// Now 可在确定性测试中覆盖。nil 使用 time.Now。
	Now func() time.Time
}

// callbackPath 是 Composio 把浏览器重定向回来的 API 路径。它是常量（不可配置），
// 这样 SDK 回调 URL 与 router 路由永远不会漂移失配。
const callbackPath = "/api/integrations/composio/callback"

// Service 是 Composio 业务集成服务。
type Service struct {
	sdk         SDK
	store       Store
	secret      []byte
	callbackURL string
	frontendURL string
	stateTTL    time.Duration
	now         func() time.Time

	// authCache 保存项目的 toolkit_slug → auth_config_id map。首次使用或 authCacheExp
	// 过期后从 Composio 的 /auth_configs 端点重建；authCacheMu 保护这两个字段。
	authCacheMu  sync.Mutex
	authCache    map[string]string
	authCacheExp time.Time
	authCacheTTL time.Duration
}

// NewService 校验入参并返回一个就绪的 Service。必要依赖缺失时报错，让配置错误的启动
// 在启动时就大声失败，而不是等到请求期才回一堆 500。
func NewService(client SDK, store Store, cfg Config) (*Service, error) {
	if client == nil {
		return nil, errors.New("composio: SDK client is required")
	}
	if store == nil {
		return nil, errors.New("composio: store is required")
	}
	if len(cfg.StateSecret) == 0 {
		return nil, errors.New("composio: StateSecret is required")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.CallbackBaseURL), "/")
	if base == "" {
		return nil, errors.New("composio: CallbackBaseURL is required")
	}

	ttl := cfg.StateTTL
	if ttl <= 0 {
		ttl = defaultStateTTL
	}
	authTTL := cfg.AuthConfigTTL
	if authTTL <= 0 {
		authTTL = defaultAuthCacheTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return &Service{
		sdk:          client,
		store:        store,
		secret:       cfg.StateSecret,
		callbackURL:  base + callbackPath,
		frontendURL:  strings.TrimRight(strings.TrimSpace(cfg.FrontendBaseURL), "/"),
		stateTTL:     ttl,
		now:          now,
		authCacheTTL: authTTL,
	}, nil
}

// Connection 是面向 API 的本地连接行视图。Composio 的 connected_account_id 与
// auth_config_id 刻意省略——它们是服务内部句柄，不是 API 表面。
type Connection struct {
	ID          string  `json:"id"`
	ToolkitSlug string  `json:"toolkit_slug"`
	Status      string  `json:"status"`
	ConnectedAt string  `json:"connected_at"`
	LastUsedAt  *string `json:"last_used_at"`
}

// MCPSession 是 CreateMCPSession 的结果：可流式 MCP URL 加上 MCP 客户端必须携带的
// 请求头。Headers 携带 Composio 的 x-api-key，所以调用方在写日志前必须先让它们过
// 脱敏管线。
type MCPSession struct {
	URL     string
	Headers map[string]string
}

// ToolkitView 是面向 API 的单个 Composio 工具包描述。只有带已启用项目 auth 配置的
// 工具包才会被返回。
type ToolkitView struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	LogoURL  string `json:"logo,omitempty"`
	Category string `json:"category,omitempty"`
}

// toolkitLogoURL 决定工具包的 logo 地址：上游给了就用上游的，否则退回按 slug 拼
// Composio 静态 logo 服务地址。
func toolkitLogoURL(slug, upstreamLogoURL string) string {
	if upstreamLogoURL != "" {
		return upstreamLogoURL
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return ""
	}
	return composioLogoBaseURL + "/" + url.PathEscape(slug)
}

// BeginConnect 校验工具包、铸造一个带签名的 state，并向 Composio 索取一个托管
// Connect Link。返回的重定向 URL 就是调用方要引导用户浏览器去的地方。
//
// auth_config_id 从项目已启用的 auth config（有缓存）里动态解析，所以一个工具包可连接
// 当且仅当控制台为它配了 auth config——没有静态 env map。配了的才连得上；没有的返回
// ErrToolkitNotSupported。
//
// 传给 Composio 的 composio_user_id 原样采用 Multica 用户 id——这是整个集成其余部分
// 依赖的不变量。
func (s *Service) BeginConnect(ctx context.Context, userID pgtype.UUID, toolkitSlug string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(toolkitSlug))
	authConfigID, err := s.authConfigForToolkit(ctx, slug)
	if err != nil {
		return "", err
	}
	if authConfigID == "" {
		return "", ErrToolkitNotSupported
	}
	if !userID.Valid {
		return "", errors.New("composio: invalid user id")
	}
	composioUserID := util.UUIDToString(userID)

	state, err := signState(s.secret, stateClaims{
		UserID:       composioUserID,
		ToolkitSlug:  slug,
		AuthConfigID: authConfigID,
		Exp:          s.now().Add(s.stateTTL).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("composio: sign state: %w", err)
	}

	// Composio 会在回调 URL 上追加它自己的 status / connected_account_id 查询参数，
	// 同时保留我们带的参数，所以这份有符号 state 会随重定向一起原样回到我们这里。
	callbackURL := s.callbackURL + "?state=" + url.QueryEscape(state)

	resp, err := s.sdk.CreateLink(ctx, sdk.CreateLinkRequest{
		AuthConfigID: authConfigID,
		UserID:       composioUserID,
		CallbackURL:  callbackURL,
	})
	if err != nil {
		return "", fmt.Errorf("composio: create link: %w", err)
	}
	return resp.RedirectURL, nil
}

// CompleteCallback 校验有符号 state，并在 Composio 状态为成功时 upsert 本地连接行。
// 它返回 state 里的工具包 slug，好让 handler 即便走在「不成功」的路径上也能拼对
// 重定向目标。
//
// 幂等性：upsert 以 (user_id, connected_account_id) 为键，所以重复回调只会重新激活同一
// 行，而不会造出第二行。
func (s *Service) CompleteCallback(ctx context.Context, state, status, connectedAccountID string) (string, error) {
	claims, err := verifyState(s.secret, state, s.now())
	if err != nil {
		return "", err
	}

	if !strings.EqualFold(strings.TrimSpace(status), "success") {
		// 仍然用 state 里的 slug 拼重定向，但不写活跃行。
		return claims.ToolkitSlug, ErrConnectNotSuccessful
	}
	if strings.TrimSpace(connectedAccountID) == "" {
		return claims.ToolkitSlug, errors.New("composio: callback missing connected_account_id")
	}

	userID, err := util.ParseUUID(claims.UserID)
	if err != nil {
		return claims.ToolkitSlug, fmt.Errorf("composio: state has invalid user id: %w", err)
	}

	// auth_config_id 是在 BeginConnect 时解析并签进 state 的，所以这里拿 state 里那份
	// 精确值来比对，而不是在此重新解析（重新解析一旦失败或漂移就会 fail-open：曾经因为
	// 缺失期望的 auth config 而跳过检查，让别的工具包的账户 id 被绑到本工具包 slug 下）。
	// 空值会在 verifyAccountOwnership 里 fail-closed。
	authConfigID := claims.AuthConfigID

	// 纵深防御（PR 4608 review）：签名 state 证明了「谁发起了握手」与「哪个工具包」，
	// 但 connected_account_id 是 Composio 追加到我们回调 URL 上的明文查询参数。一条
	// 精心构造的重定向可以把一份有效、未过期的 state 配上别人的账户 id，而我们原样
	// 镜像。写入前先向 Composio 确认：该账户确实属于 state 里的用户（composio_user_id
	// == multica 用户 id 不变量），并且是在该工具包的 auth config 下创建的。任何不匹配
	// 都以 ErrAccountVerification 失败关闭。
	if err := s.verifyAccountOwnership(ctx, connectedAccountID, claims.UserID, authConfigID); err != nil {
		return claims.ToolkitSlug, err
	}

	if _, err := s.store.UpsertUserComposioConnection(ctx, db.UpsertUserComposioConnectionParams{
		UserID:             userID,
		ToolkitSlug:        claims.ToolkitSlug,
		AuthConfigID:       authConfigID,
		ConnectedAccountID: connectedAccountID,
		// 不变量：composio_user_id == Multica 用户 id。
		ComposioUserID: claims.UserID,
	}); err != nil {
		return claims.ToolkitSlug, fmt.Errorf("composio: upsert connection: %w", err)
	}
	return claims.ToolkitSlug, nil
}

// ListConnections 返回用户的活跃连接。
func (s *Service) ListConnections(ctx context.Context, userID pgtype.UUID) ([]Connection, error) {
	rows, err := s.store.ListActiveUserComposioConnections(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Connection, 0, len(rows))
	for _, row := range rows {
		out = append(out, rowToConnection(row))
	}
	return out, nil
}

// Disconnect 在 Composio 侧吊销并删除连接，然后把本地行标记为 revoked。它是幂等的：
// Composio 回 404（已经没了）按成功处理，重复吊销已 revoked 的本地行也是空操作。
//
// 不属于该用户的连接 id（或根本不存在）返回 ErrConnectionNotFound，让 handler 能回
// 404 而不跨用户泄漏存在性。
func (s *Service) Disconnect(ctx context.Context, userID, connectionID pgtype.UUID) error {
	row, err := s.store.GetUserComposioConnection(ctx, db.GetUserComposioConnectionParams{
		ID:     connectionID,
		UserID: userID,
	})
	if err != nil {
		// pgx.ErrNoRows 或 fake 的 "not found"——对拥有者而言都是 not found。
		return ErrConnectionNotFound
	}

	// 本地已断开：重复 DELETE 是纯空操作。在这里短路让「断开」保持幂等，即使上游现在对
	// revoke/delete 回的是非 404 错误（PR 4608 review）：账户已经没了，再打一次 Composio
	// 可能把第二次 DELETE 变成 502，破坏 204-幂等契约。首次断开时已经在上游吊销过并
	// 标记过这一行了。
	if !strings.EqualFold(row.Status, "active") {
		return nil
	}

	// 先吊销上游授权，再删 Composio 记录。两者都对 404 做了幂等化，因此重复断开（或一条
	// 在 Composio 已被删的连接）仍能正确了结本地行。
	if err := s.sdk.RevokeConnection(ctx, row.ConnectedAccountID); err != nil && !isNotFound(err) {
		return fmt.Errorf("composio: revoke connection: %w", err)
	}
	// DeleteConnectedAccount 在 SDK 里已吞掉 404，这里仍防御性地再判一次。
	if err := s.sdk.DeleteConnectedAccount(ctx, row.ConnectedAccountID); err != nil && !isNotFound(err) {
		return fmt.Errorf("composio: delete connected account: %w", err)
	}

	if err := s.store.MarkUserComposioConnectionRevoked(ctx, db.MarkUserComposioConnectionRevokedParams{
		ID:     connectionID,
		UserID: userID,
	}); err != nil {
		return fmt.Errorf("composio: mark revoked: %w", err)
	}
	return nil
}

// CreateMCPSession 开一个限定到「用户活跃连接」的 Composio tool-router（MCP）会话。
// 用户没有任何活跃连接时返回 (nil, nil)——调用方把这种情况当作「该用户无 MCP overlay」。
//
// connected_accounts 按工具包固定到用户自己的已连接账户 id 数组，让会话不可能浮出用户
// 没连接过的账户。这个助手尚未接入任务派发（Stage 3）；它先存在，是为了让将来的接线
// 成为某个已被测熟接缝的纯消费者。
//
// 单账户约束（v1，PR 4608 review 后续）：MVP 连接流程假设每个 (user, toolkit) 至多
// 一个活跃连接——没有 UI 或 API 来持有多个。若真出现重复，必须确定性地选择：行按
// 最新在前到达（ListActive 按 connected_at DESC 排序），因此我们保留每个工具包最先出现
// 的那条（最近连接的账户），而不是让后面的 map 写入悄悄选中一个更旧的。
//
// 真正接入派发前，Stage 3 要做出决策：要么在连接时强制单活跃约束（新连接时吊销同一
// 工具包之前的活跃行），要么把 CreateSession 扩展成多账户请求形态。在那之前，新者胜
// 让行为保持确定性，而不是依赖到达顺序。
func (s *Service) CreateMCPSession(ctx context.Context, userID pgtype.UUID) (*MCPSession, error) {
	rows, err := s.store.ListActiveUserComposioConnections(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	connectedAccounts := make(map[string]any, len(rows))
	for _, row := range rows {
		// 每个工具包保留第一条（最新）账户，忽略更旧的重复。
		if _, exists := connectedAccounts[row.ToolkitSlug]; exists {
			continue
		}
		connectedAccounts[row.ToolkitSlug] = []string{row.ConnectedAccountID}
	}

	resp, err := s.sdk.CreateSession(ctx, sdk.CreateSessionRequest{
		UserID:            util.UUIDToString(userID),
		ConnectedAccounts: connectedAccounts,
	})
	if err != nil {
		return nil, fmt.Errorf("composio: create session: %w", err)
	}
	return &MCPSession{
		URL:     resp.MCP.URL,
		Headers: s.sdk.MCPAuthHeaders(),
	}, nil
}

// CallbackRedirect 为回调 handler 拼浏览器重定向目标。成功时指向设置页（Integrations
// 标签）并携带已连接的工具包 slug；失败时携带一个稳定错误码。路径刻意采用无 slug 的
// `/settings?tab=integrations&...` 形态：web 代理的 legacy-route 重定向会自动补上用户
// 最近工作区的 slug，从而落到真正挂载 Composio 标签的 `/{slug}/settings?tab=integrations`
// 路由。更老的 `/settings/integrations` 路径不是真实路由，legacy 改写后会 404。
// FrontendBaseURL 未设置时返回站内相对路径。
func (s *Service) CallbackRedirect(slug string, success bool) string {
	var path string
	if success {
		path = "/settings?tab=integrations&connected=" + url.QueryEscape(slug)
	} else {
		path = "/settings?tab=integrations&error=composio_connect_failed"
	}
	return s.frontendURL + path
}

// rowToConnection 把一条 DB 行映射成面向 API 的 Connection 视图。
func rowToConnection(row db.UserComposioConnection) Connection {
	c := Connection{
		ID:          util.UUIDToString(row.ID),
		ToolkitSlug: row.ToolkitSlug,
		Status:      row.Status,
	}
	if row.ConnectedAt.Valid {
		c.ConnectedAt = row.ConnectedAt.Time.UTC().Format(time.RFC3339)
	}
	c.LastUsedAt = util.TimestampToPtr(row.LastUsedAt)
	return c
}

// ListToolkits 只返回项目真正能连接的 Composio 工具包（即带已启用 auth config 的）。
// 没有启用 auth config 的工具包被整个过滤掉（MUL-4009）：用户无法对它做任何操作，摆
// 一张卡片只是噪音；旧的灰色 "Not configured" 标签存在只是为了不让按钮变死——直接去掉
// 条目就不需要它了。它会取完所有页（受 maxToolkitPages 上限约束），让 UI 一次调用就
// 拿到完整的可连接清单；目录不过几百条，单个 JSON 响应绰绰有余。构造上保证每个返回项
// 都有已启用的 auth 配置。
//
// 与之前行为不同：resolver 错误不再被伪装成「全不可连接」的目录。有了过滤后，那种掩蔽
// 会渲染成一句误导性的空态 "no apps configured"。这里把错误原样返回，让 handler 能抛
// 502、UI 能展示它诚实的加载失败态。
func (s *Service) ListToolkits(ctx context.Context) ([]ToolkitView, error) {
	// connectable 是项目「已启用的 toolkit_slug → auth_config_id」map。它是「展示什么」
	// 的事实来源：只有出现在这里的 slug 才会被返回。
	connectable, err := s.authConfigMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("composio: resolve connectable toolkits: %w", err)
	}

	out := []ToolkitView{}
	seen := make(map[string]struct{})
	cursor := ""
	for page := 0; page < maxToolkitPages; page++ {
		resp, err := s.sdk.ListToolkits(ctx, sdk.ListToolkitsRequest{
			Limit:  listPageLimit,
			Cursor: cursor,
			SortBy: "usage",
		})
		if err != nil {
			return nil, fmt.Errorf("composio: list toolkits: %w", err)
		}
		for _, tk := range resp.Items {
			slug := strings.ToLower(strings.TrimSpace(tk.Slug))
			if slug == "" {
				continue
			}
			if _, dup := seen[slug]; dup {
				continue
			}
			seen[slug] = struct{}{}
			// 过滤掉没有启用 auth config 的工具包：用户对它们没有任何可用的动作，因此
			// 从目录中剔除。
			if _, canConnect := connectable[slug]; !canConnect {
				continue
			}
			category := ""
			if len(tk.Categories) > 0 {
				category = tk.Categories[0]
			}
			out = append(out, ToolkitView{
				Slug:     tk.Slug,
				Name:     tk.Name,
				LogoURL:  toolkitLogoURL(slug, tk.LogoURL),
				Category: category,
			})
		}
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}

	return out, nil
}

// authConfigForToolkit 返回某工具包 slug 选定的 auth_config_id；项目对它没有启用的
// auth config 时返回 ""。它读全项目缓存 map（TTL 到期自动刷新）。
func (s *Service) authConfigForToolkit(ctx context.Context, slug string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return "", nil
	}
	m, err := s.authConfigMap(ctx)
	if err != nil {
		return "", err
	}
	return m[slug], nil
}

// authConfigMap 返回项目的 toolkit_slug → auth_config_id map；缓存为空或过期时从
// Composio 重建。并发调用方在 authCacheMu 上串行；抓取在持锁下进行——对这个短 TTL、
// 构建便宜、又只有低频设置面去读的 map 来说可以接受。刷新时整体赋新 map（绝不原地改），
// 所以已经拿出去给调用方的引用可以安全继续读。
func (s *Service) authConfigMap(ctx context.Context) (map[string]string, error) {
	s.authCacheMu.Lock()
	defer s.authCacheMu.Unlock()
	if s.authCache != nil && s.now().Before(s.authCacheExp) {
		return s.authCache, nil
	}
	m, err := s.fetchAuthConfigMap(ctx)
	if err != nil {
		// 有旧快照就先喂旧快照——一次瞬时 /auth_configs 抖动不该让所有工具包突然全部
		// 不可连接。
		if s.authCache != nil {
			return s.authCache, nil
		}
		return nil, err
	}
	s.authCache = m
	s.authCacheExp = s.now().Add(s.authCacheTTL)
	return m, nil
}

// authCandidate 是解析过程中「竞争代表某个工具包」的一个项目 auth config。
type authCandidate struct {
	id      string
	managed bool
	updated string
}

// fetchAuthConfigMap 翻页遍历项目「已启用的」auth config，把它们归约成每个工具包 slug
// 选定一个 auth_config_id。当一个工具包有好几个（如一个 Composio 托管 + 一个自定义
// 白标），由 betterAuthConfig 选出赢家。
func (s *Service) fetchAuthConfigMap(ctx context.Context) (map[string]string, error) {
	best := make(map[string]authCandidate)
	cursor := ""
	for page := 0; page < maxAuthConfigPages; page++ {
		resp, err := s.sdk.ListAuthConfigs(ctx, sdk.ListAuthConfigsRequest{
			ShowDisabled: false,
			Limit:        listPageLimit,
			Cursor:       cursor,
		})
		if err != nil {
			return nil, fmt.Errorf("composio: list auth configs: %w", err)
		}
		for _, ac := range resp.Items {
			if ac.ID == "" || strings.EqualFold(ac.Status, "DISABLED") {
				continue
			}
			slug := strings.ToLower(strings.TrimSpace(ac.Toolkit.Slug))
			if slug == "" {
				continue
			}
			cand := authCandidate{id: ac.ID, managed: ac.IsComposioManaged, updated: ac.LastUpdatedAt}
			if cur, ok := best[slug]; !ok || betterAuthConfig(cand, cur) {
				best[slug] = cand
			}
		}
		if resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	out := make(map[string]string, len(best))
	for slug, c := range best {
		out[slug] = c.id
	}
	return out, nil
}

// betterAuthConfig 报告候选 a 是否应胜过同工具包当前选中的 b。自定义（自带 OAuth）配置
// 优于 Composio 托管的——那是产品想要的白标路径——同类的配置里最近更新的胜出。
func betterAuthConfig(a, b authCandidate) bool {
	if a.managed != b.managed {
		return !a.managed
	}
	return a.updated > b.updated
}

// verifyAccountOwnership 向 Composio 确认 connectedAccountID 确实属于 expectedUserID、
// 且在 expectedAuthConfigID 下创建，让回调上被篡改或跨工具包的 connected_account_id
// 无法把别人的账户偷运进本地镜像表。它失败关闭：上游错误、未知账户、归属不匹配、
// 期望 auth config 为 EMPTY、auth config 不匹配——一律返回 ErrAccountVerification。
// 要求一个非空且精确匹配的 auth config，正是堵住跨工具包绑定的洞——期望值是 BeginConnect
// 时签进 state 的 auth_config_id，它与工具包一一对应。
func (s *Service) verifyAccountOwnership(ctx context.Context, connectedAccountID, expectedUserID, expectedAuthConfigID string) error {
	resp, err := s.sdk.ListConnectedAccounts(ctx, sdk.ListConnectedAccountsRequest{
		ConnectedAccountIDs: []string{connectedAccountID},
	})
	if err != nil {
		return fmt.Errorf("composio: verify connected account: %w", err)
	}
	var acct *sdk.ConnectedAccount
	for i := range resp.Items {
		if resp.Items[i].ID == connectedAccountID {
			acct = &resp.Items[i]
			break
		}
	}
	if acct == nil {
		return ErrAccountVerification
	}
	if acct.UserID != expectedUserID {
		return ErrAccountVerification
	}
	// 失败关闭：账户必须是在 connect link 所用的那个确切 auth config 下创建的。期望值为
	// 空（state 缺字段，或解析缺口）时直接拒绝而不是跳过——跳过就是当年那个让跨工具包
	// 账户 id 绑进本地的 fail-open 洞。
	accountAuthConfigID := acct.AuthConfigID
	if accountAuthConfigID == "" {
		accountAuthConfigID = acct.AuthConfig.ID
	}
	if expectedAuthConfigID == "" || accountAuthConfigID != expectedAuthConfigID {
		return ErrAccountVerification
	}
	return nil
}

// isNotFound 报告 err 是否为 Composio 的 404 APIError，用于让 revoke/delete 幂等。
func isNotFound(err error) bool {
	var apiErr *sdk.APIError
	return errors.As(err, &apiErr) && apiErr.IsNotFound()
}
