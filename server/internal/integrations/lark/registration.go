// registration.go：Lark PersonalAgent 注册的设备流 client——对 accounts.feishu.cn
// （大陆） / accounts.larksuite.com（国际版）的 RFC 8628 实现。
//
// Lark PersonalAgent 注册是对 RFC 8628（OAuth 2.0 Device Authorization Grant）
// 的 1:1 实现，面向 accounts.feishu.cn（大陆）/ accounts.larksuite.com（国际版）。
// 协议只有两阶段：
//
//  1. begin —— POST <domain>/oauth/v1/app/registration，带 action=begin 与
//     （archetype=PersonalAgent / auth_method=client_secret /
//     request_user_info=open_id）。Lark 返回 device_code、
//     verification_uri_complete（二维码目标）、轮询间隔和有效期。Multica 渲染
//     二维码，用户在 Lark App 里扫码、走“给该账号创建 PersonalAgent”流程并授权。
//
//  2. poll —— 对同一 URL POST action=poll 和 device_code。服务器回以下之一：
//     - {error: "authorization_pending"} —— 继续轮询。
//     - {error: "slow_down"}             —— 间隔 +5s 后再轮询。
//     - {user_info: {tenant_brand: "lark"}}
//     —— 用户用国际版 tenant 授权；我们把
//     轮询主机切到 accounts.larksuite.com
//     继续。
//     - {client_id, client_secret, user_info: {open_id}}
//     —— 终态成功。
//     - {error: "expired_token"|"access_denied"}
//     —— 终态失败。
//
// 我们刻意内联这个 client（而不是依赖
// github.com/larksuite/oapi-sdk-go/scene/registration），让注册表面与 lark 包
// 其余部分保持相同的 go.mod 占用面——~270 行单文件 client 刚好是自己该拥有的
// 规模，替代方案是为一个端点拖进整个 SDK 及其传递依赖。
package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	registrationDefaultFeishuDomain = "https://accounts.feishu.cn"
	registrationDefaultLarkDomain   = "https://accounts.larksuite.com"

	registrationEndpoint = "/oauth/v1/app/registration"

	// registrationDefaultPollSeconds 是服务器省略 `interval` 时 Lark 用的默认
	// 轮询节奏。5 秒与 Lark SDK 一致；更小只会招来 slow_down 而换不到任何延迟
	// 收益。
	registrationDefaultPollSeconds = 5

	// registrationDefaultExpireSeconds 是默认注册窗口（10 分钟）——够用户扫码、
	// 切 App、走完创建 Bot 流程并在手机上授权，又短到被放弃的会话不会占着资源
	// 数小时。
	registrationDefaultExpireSeconds = 600

	// registrationTenantBrandLark 是 Lark 用来标记“你用 Lark（国际版）账号扫的，
	// 不是飞书（大陆版）”的内部 tenant 品牌标签。看到它时我们把轮询重新指向
	// accounts.larksuite.com，并【不】等轮询间隔就立刻重发下一次 poll——上游
	// SDK 表明 Lark 服务器在轮询流中恰好发一次 tenant_brand 提示，随后的 poll
	// 必须落在新域名上才能拿到凭据。
	registrationTenantBrandLark = "lark"

	// registrationTenantBrandFeishu 是反方向的镜像品牌标签：用户点了“绑定到
	// Lark”的 CTA，实际却用大陆飞书账号授权。分流 CTA（MUL-3083）对
	// accounts.larksuite.com 渲染二维码，但 Lark 的 poll 流在授权落到错误云上
	// 完成时同样会浮出 tenant_brand="feishu"，我们对称地尊重它——把轮询改指向
	// accounts.feishu.cn，让下一次 poll 从正确的主机取凭据。没有它，“进错门”
	// 在 lark→feishu 方向上就成了硬安装失败，尽管 feishu→lark 方向本来能自动
	// 恢复。
	registrationTenantBrandFeishu = "feishu"
)

// RegistrationConfig 配置设备流 client。所有字段可选；零值指向标准
// http.Client 上的 accounts.feishu.cn（每次调用 30 秒超时，卡死的 poll 不会在
// 整个有效期内悄悄钉住会话 goroutine）。
type RegistrationConfig struct {
	// Domain 是初始轮询主机。默认 "https://accounts.feishu.cn"；staging 部署可
	// 指向 mock 或 Lark beta 端点。
	Domain string

	// LarkDomain 是 Lark 的 poll 响应浮出 user_info.tenant_brand="lark" 时 client
	// 切换到的国际版租户轮询主机。默认 "https://accounts.larksuite.com"。
	LarkDomain string

	// HTTPClient 是 client 每个请求用的传输。空值默认新的 *http.Client 带 30 秒
	// 超时——设备流端点通常亚秒级返回，但我们为跨地域路径留点余量。
	HTTPClient *http.Client

	// Source 标记二维码 URL 的 `source` 查询参数，让 Lark 的遥测能把安装归因回
	// Multica。空值默认 "multica"。
	Source string

	// Now 可覆盖，用于做确定性的过期边界测试。
	Now func() time.Time
}

// withDefaults 把未设置的配置项填成默认值。
func (c RegistrationConfig) withDefaults() RegistrationConfig {
	if c.Domain == "" {
		c.Domain = registrationDefaultFeishuDomain
	}
	if c.LarkDomain == "" {
		c.LarkDomain = registrationDefaultLarkDomain
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Source == "" {
		c.Source = "multica"
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// RegistrationClient 跑设备流协议，但【不】持有会话状态或安装供给——
// RegistrationService 把 client 与会话存储、DB 写入路径组合起来。拆开能让协议
// client 变得确定、易用 httptest fake 测试，无需牵扯数据库。
type RegistrationClient struct {
	cfg RegistrationConfig
}

// NewRegistrationClient 构建设备流 client。
func NewRegistrationClient(cfg RegistrationConfig) *RegistrationClient {
	return &RegistrationClient{cfg: cfg.withDefaults()}
}

// BeginResult 是 Begin 返回给 RegistrationService 的结果。
type BeginResult struct {
	DeviceCode string
	// QRCodeURL 是带 Multica `source` 遥测参数的 verification_uri_complete；
	// 客户端侧把它渲染成二维码图片。
	QRCodeURL string
	// Domain 是本会话开启时用的轮询主机。service 必须把它传回 Poll，国际版
	// tenant 切换（StatusDomainSwitched）才能重新指向后续轮询。
	Domain string
	// Interval 是 Lark 建议的轮询节奏。slow_down 响应再加 5s；遵守更新后的节奏
	// 是 service 的责任。
	Interval time.Duration
	// ExpiresIn 是 device_code 的绝对寿命。窗口之后的 poll 返回 expired_token；
	// 会话 goroutine 用它来决定 context.WithTimeout 的大小。
	ExpiresIn time.Duration
}

// PollResult 是所有“终态/非终态”轮询结果的判别并集。调用方按已填充字段分支：
//   - Success         （ClientID + ClientSecret + OpenID）→ 安装
//   - SwitchedDomain  （新 domain 字符串）               → 换主机，立即重 poll
//   - Status          （"authorization_pending" / "slow_down"）→ 等一会再 poll
//   - Err             （终态错误）                       → 中止会话
type PollResult struct {
	ClientID     string
	ClientSecret string
	OpenID       OpenID

	// SwitchedDomain 在 Lark 告诉我们“这是错误的云，去那边重 poll”时非空。它与
	// SwitchedRegion 成对出现，调用方能一步同时更新轮询主机和每安装地域。
	// 最初只在 Feishu→Lark 方向触发（Lark 国际版用户在飞书优先的 begin 上授权）；
	// MUL-3083 跟进后它是对称的，所以点了“错误”绑定 CTA 的用户也能恢复——
	// service 必须更新会话里存储的 domain 与 region，并且【不】遵守间隔就重 poll
	// （SDK 也一样——上游行为是紧接着的下一次 poll 落在新域名上并返回真正的
	// 凭据）。
	SwitchedDomain string
	// SwitchedRegion 是新域名所属的地域。与 SwitchedDomain 同步设置；
	// SwitchedDomain 为空时忽略。在这里携带 region，省得调用方在更新会话时再从
	// domain 字符串反推。
	SwitchedRegion Region

	// Status 携带非终态协议信号——通常是 "authorization_pending" 或 "slow_down"。
	// service 用它们决定是否把轮询间隔往上加。
	Status string

	// Err 是终态错误码（例如 "access_denied"、"expired_token"，或我们没有预料到
	// 的 Lark 自由格式错误码）。非终态结果时为 nil。
	Err *RegistrationError
}

// RegistrationError 是类型化的 Lark 协议错误。handler 管线把 `Code` 映射成稳定
// 的用户可见原因，UI 不用解析措辞就能渲染正确的文案。
type RegistrationError struct {
	Code        string
	Description string
}

// Error 实现 error 接口。
func (e *RegistrationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Description == "" {
		return fmt.Sprintf("registration: %s", e.Code)
	}
	return fmt.Sprintf("registration: %s: %s", e.Code, e.Description)
}

// Begin 对请求地域的开放平台主机开启新的设备流会话。地域通常由调用方显式选择
// （用户在 UI 里选了“飞书”或“Lark”），二维码因此对着用户预期扫码的同一朵云
// 渲染；空值退回飞书（大陆版），与地域感知安装出现前的调用方兼容。即使用户在
// 飞书主机上 begin，Lark 仍可能在后续 poll 浮出国际版 tenant——RegistrationService
// 里的 SwitchedDomain 分支为“进错门”的用户保住了这条自动探测兜底，所以显式选
// 地域只是路由优化（省一次往返、一开始就渲染对的那朵云的二维码），而不是对
// 设备流可恢复范围的限制。
//
// namePreset 会预填 Lark “创建 PersonalAgent”表单上的 bot/app 名，让装出来的
// bot 默认叫如 "<agent> - Multica"，而不是 Lark 自动生成的“{用户姓名}的智能
// 助手”。它是用户可编辑的默认值（用户仍可在表单上改），并搭载在二维码 URL
// 上——不是 begin POST 体上（那里没有 name 字段）。空值省略预填。
func (c *RegistrationClient) Begin(ctx context.Context, namePreset string, region Region) (*BeginResult, error) {
	// 按请求地域挑 begin 域名。空/未知地域退回飞书（大陆）——与
	// RegionOrDefault 相同的向后兼容不变量，早于本签名的调用方（传 ""）仍可用。
	domain := c.cfg.Domain
	if region == RegionLark {
		domain = c.cfg.LarkDomain
	}
	var resp struct {
		DeviceCode              string `json:"device_code"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		VerificationURI         string `json:"verification_uri"`
		UserCode                string `json:"user_code"`
		Interval                int    `json:"interval"`
		ExpireIn                int    `json:"expire_in"`
		Error                   string `json:"error"`
		ErrorDescription        string `json:"error_description"`
	}
	form := url.Values{
		"action":            []string{"begin"},
		"archetype":         []string{"PersonalAgent"},
		"auth_method":       []string{"client_secret"},
		"request_user_info": []string{"open_id"},
	}
	if err := c.doForm(ctx, domain, form, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, &RegistrationError{Code: resp.Error, Description: resp.ErrorDescription}
	}
	if resp.DeviceCode == "" {
		return nil, &RegistrationError{Code: "invalid_response", Description: "device_code is empty"}
	}
	if resp.VerificationURIComplete == "" {
		return nil, &RegistrationError{Code: "invalid_response", Description: "verification_uri_complete is empty"}
	}
	qr, err := decorateQRCodeURL(resp.VerificationURIComplete, c.cfg.Source, namePreset)
	if err != nil {
		return nil, &RegistrationError{Code: "invalid_response", Description: "verification_uri_complete is not a URL: " + err.Error()}
	}
	interval := registrationDefaultPollSeconds
	if resp.Interval > 0 {
		interval = resp.Interval
	}
	expireIn := registrationDefaultExpireSeconds
	if resp.ExpireIn > 0 {
		expireIn = resp.ExpireIn
	}
	return &BeginResult{
		DeviceCode: resp.DeviceCode,
		QRCodeURL:  qr,
		Domain:     domain,
		Interval:   time.Duration(interval) * time.Second,
		ExpiresIn:  time.Duration(expireIn) * time.Second,
	}, nil
}

// Poll 对给定 domain（调用方可能已通过上次 PollResult 的 SwitchedDomain 在会话
// 中途更新过它）跑一次单轮 poll 往返。域名选择放在 client 之外，让
// RegistrationService 里的会话状态机成为“下一次该打哪个主机”的唯一事实来源。
func (c *RegistrationClient) Poll(ctx context.Context, domain, deviceCode string) (*PollResult, error) {
	if deviceCode == "" {
		return nil, &RegistrationError{Code: "invalid_argument", Description: "device_code is required"}
	}
	if domain == "" {
		domain = c.cfg.Domain
	}
	var resp struct {
		ClientID     string `json:"client_id,omitempty"`
		ClientSecret string `json:"client_secret,omitempty"`
		UserInfo     *struct {
			OpenID      string `json:"open_id,omitempty"`
			TenantBrand string `json:"tenant_brand,omitempty"`
		} `json:"user_info,omitempty"`
		Error            string `json:"error,omitempty"`
		ErrorDescription string `json:"error_description,omitempty"`
	}
	form := url.Values{
		"action":      []string{"poll"},
		"device_code": []string{deviceCode},
	}
	if err := c.doForm(ctx, domain, form, &resp); err != nil {
		return nil, err
	}

	// tenant-brand 驱动的域名切换。当被授权的账号与 begin 打到的云不匹配时，
	// Lark 会在转换轮次上恰好发一次这个信号；下一次 poll 必须落在匹配的开放平台
	// 主机上才能拿到凭据。我们把切换（domain + region）作为类型化信号浮出来，
	// service 既不用知道品牌字符串、也不用从主机反推地域。
	//
	// 两个方向都处理：feishu→lark（用户拿 Lark 国际版账号扫飞书二维码），以及
	// lark→feishu（用户点了新的“绑定到 Lark”CTA，实际却用大陆飞书账号授权）。
	// 对称性很重要，因为分流 CTA UI（MUL-3083）也可能直接在 accounts.larksuite.com
	// 上 begin——没有反向切换，“进错门”那侧的安装会一路带着 RegionLark 走到
	// finishSuccess，并在 GetBotInfo 失败（或提交错误地域的行）。检查以当前域名
	// 为门，避免在已匹配的品牌上打转。
	if resp.UserInfo != nil {
		switch resp.UserInfo.TenantBrand {
		case registrationTenantBrandLark:
			if !strings.HasPrefix(domain, c.cfg.LarkDomain) {
				return &PollResult{
					SwitchedDomain: c.cfg.LarkDomain,
					SwitchedRegion: RegionLark,
				}, nil
			}
		case registrationTenantBrandFeishu:
			if !strings.HasPrefix(domain, c.cfg.Domain) {
				return &PollResult{
					SwitchedDomain: c.cfg.Domain,
					SwitchedRegion: RegionFeishu,
				}, nil
			}
		}
	}

	// 成功：client_id 与 client_secret 与安装者 open_id 必须都在。残缺响应按协议
	// 错误处理，RegistrationService 就绝不会写下半条 lark_installation 行。
	if resp.ClientID != "" && resp.ClientSecret != "" {
		if resp.UserInfo == nil || resp.UserInfo.OpenID == "" {
			return nil, &RegistrationError{
				Code:        "invalid_response",
				Description: "success response missing installer open_id",
			}
		}
		return &PollResult{
			ClientID:     resp.ClientID,
			ClientSecret: resp.ClientSecret,
			OpenID:       OpenID(resp.UserInfo.OpenID),
		}, nil
	}

	switch resp.Error {
	case "authorization_pending", "slow_down":
		return &PollResult{Status: resp.Error}, nil
	case "access_denied", "expired_token":
		return &PollResult{
			Err: &RegistrationError{Code: resp.Error, Description: resp.ErrorDescription},
		}, nil
	case "":
		// 空 error 且空凭据 = 继续轮询；这与上游 SDK 对“服务器在 authorize-redirect
		// 窗口内短暂返回空体”的宽容处理一致。
		return &PollResult{Status: "authorization_pending"}, nil
	default:
		return &PollResult{
			Err: &RegistrationError{Code: resp.Error, Description: resp.ErrorDescription},
		}, nil
	}
}

// doForm 对注册端点做一次 form 编码 POST 并把 JSON 响应解码进 out。
func (c *RegistrationClient) doForm(ctx context.Context, domain string, form url.Values, out any) error {
	endpoint := strings.TrimRight(domain, "/") + registrationEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("registration: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("registration: http do: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("registration: read body: %w", err)
	}
	if len(body) == 0 {
		return &RegistrationError{
			Code:        fmt.Sprintf("http_%d", resp.StatusCode),
			Description: "empty body",
		}
	}
	// RFC 8628 设备流服务器对非 2xx 会带一个 JSON 体，其 `error` 字段才是真正的
	// 信号——`authorization_pending` 和 `slow_down` 以 HTTP 400 到达，而不是
	// 2xx。先解码 body、让调用方按 `resp.Error` 路由，正是上游 Go SDK 的做法；
	// 把一切非 2xx 当作硬协议错误（以前的行为）会让每个会话在第一次 poll 就死，
	// 因为那时用户还没扫二维码。
	if jsonErr := json.Unmarshal(body, out); jsonErr == nil {
		return nil
	}
	// body 解析不了——把原始状态码 + 负载尾部浮出来，让 ops 能区分 Lark 故障/
	// 代理劫持与 schema 漂移。调用方把它当终态协议错误。
	return &RegistrationError{
		Code:        fmt.Sprintf("http_%d", resp.StatusCode),
		Description: truncate(string(body), 256),
	}
}

// decorateQRCodeURL 给二维码图片 URL 追加 Lark 期望的 SDK 风格遥测参数。
// 没有 `from=sdk&tp=sdk&source=<src>`，用户手机上的扫码 UI 会显示较粗糙的
// 提示，Lark 的 analytics 也无法把安装归因回 Multica。
//
// namePreset 非空时作为 `name=<...>` 追加，预填 Lark “创建 PersonalAgent”表单
// 上的 bot/app 名。这镜像上游 SDK 的 AppPreset.Name：Lark 从 verification/QR
// URL 读它（begin POST 体没有 name 字段），并把它当作用户可编辑的默认值，而非
// 锁定的最终名字。
func decorateQRCodeURL(raw, source, namePreset string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("from", "sdk")
	q.Set("tp", "sdk")
	q.Set("source", "go-sdk/"+source)
	if namePreset != "" {
		q.Set("name", namePreset)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ErrRegistrationAccessDenied 是用户在 Lark UI 里显式拒绝安装时
// RegistrationService 返回的错误。与其它终态失败区分，UI 才能渲染“你取消了
// 安装”而不是笼统的错误。
var ErrRegistrationAccessDenied = errors.New("lark registration: access denied by user")

// ErrRegistrationExpired 是 device_code 有效期在用户授权前耗尽时
// RegistrationService 返回的错误。区分出来，UI 才能提示“重新扫码——上一个
// 二维码已过期”。
var ErrRegistrationExpired = errors.New("lark registration: expired")
