// ws_endpoint.go：为飞书 WS 长连接获取“一次性”wss 引导地址（bootstrap）的
// HTTP 实现。
//
// 长连接 `/callback/ws/endpoint` 引导请求的“主机”是该安装的开放平台主机——
// 飞书（中国大陆）是 open.feishu.cn，Lark（国际版）是 open.larksuite.com——
// 每次调用按 InstallationCredentials.Region 经 Region.OpenPlatformBaseURL 解析
// （真正的 wss URL 由 Lark 在响应体里返回，所以只有引导 POST 的主机需要感知
// 地域）。部署级的 MULTICA_LARK_CALLBACK_BASE_URL 仍会在设置时（staging /
// mock）覆盖所有安装的主机。
package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPConnectionTokenFetcher 是生产环境用的 EndpointFetcher。它用每个安装的
// app 凭据，向 Lark 开放平台主机的 `POST /callback/ws/endpoint` 换取一个
// 短时效的 WebSocket URL + ClientConfig——官方 `larksuite/oapi-sdk-go/v3/ws`
// client 用的正是这条引导路径。请求体明文携带 `{AppID, AppSecret}`（不带
// tenant_access_token bearer）；响应携带一次性 wss URL（内置了 device_id/
// service_id 鉴权）和一个以秒计的 ClientConfig（PingInterval /
// ReconnectInterval / ReconnectNonce / ReconnectCount）。
//
// 我们不缓存响应。wss URL 按设计是一次性的（每次引导都会轮换内嵌的
// `device_id`），重连时复用会得到一个貌似 Lark 故障的鉴权拒绝。connector 每次
// Run 调一次 Endpoint()。
//
// PersonalAgent 兼容性——【开放风险】（MUL-2671 评审线程）：官方飞书文档把
// 长连接模式描述为“仅支持企业自建应用”。PersonalAgent 设备流原型不在支持
// 列表里，待线上确认。若引导调用返回结构化的“app 类型不支持”错误，本代码会
// 直接把 code+msg 抛出来，让 Hub 的退避循环记录真正的原因，而不是静默空转。
// 冒烟路径：`multica` -> 注册一个 PersonalAgent -> 开启 WS -> 观察日志。
type HTTPConnectionTokenFetcher struct {
	cfg HTTPConnectionTokenConfig
}

// HTTPConnectionTokenConfig 装配 fetcher 的依赖。BaseURL 是可选部署级覆盖；
// 为空（生产默认）时 Endpoint() 按地域解析每台安装的引导主机。测试会替换成
// httptest.Server 的 URL，把所有地域都指向假服务器。
type HTTPConnectionTokenConfig struct {
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
	Logger     *slog.Logger
}

// withDefaults 填充未设置的配置项为默认值。
func (c HTTPConnectionTokenConfig) withDefaults() HTTPConnectionTokenConfig {
	// BaseURL 刻意不在这里兜底。空 = “无部署级覆盖”——Endpoint() 随后按
	// InstallationCredentials.Region 解析引导主机，这样一个 fetcher 就能同时
	// 服务飞书与 Lark。非空 BaseURL（MULTICA_LARK_CALLBACK_BASE_URL，或测试的
	// httptest URL）则把所有安装强制指到那个主机。
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// NewHTTPConnectionTokenFetcher 返回绑定给定配置的生产 EndpointFetcher。
func NewHTTPConnectionTokenFetcher(cfg HTTPConnectionTokenConfig) (*HTTPConnectionTokenFetcher, error) {
	return &HTTPConnectionTokenFetcher{cfg: cfg.withDefaults()}, nil
}

// bootstrapRequest 镜像 SDK 的 BootstrapRequest。字段名必须用 PascalCase——
// 因为服务端 JSON tag 就是 PascalCase（`AppID` 而非 `app_id`）；SDK 的 pbbp2
// schema 决定了这个格式，用 lower_snake_case 对不上。
type bootstrapRequest struct {
	AppID     string `json:"AppID"`
	AppSecret string `json:"AppSecret"`
}

// endpointResponse 镜像 SDK 的 EndpointResp + Endpoint + ClientConfig。字段
// 命名用 PascalCase 以匹配飞书的线上形态。
type endpointResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		URL          string `json:"URL"`
		ClientConfig struct {
			ReconnectCount    int `json:"ReconnectCount"`
			ReconnectInterval int `json:"ReconnectInterval"`
			ReconnectNonce    int `json:"ReconnectNonce"`
			PingInterval      int `json:"PingInterval"`
		} `json:"ClientConfig"`
	} `json:"data"`
}

// Endpoint 实现 EndpointFetcher。
func (f *HTTPConnectionTokenFetcher) Endpoint(ctx context.Context, creds InstallationCredentials) (WSEndpoint, error) {
	if creds.AppID == "" || creds.AppSecret == "" {
		return WSEndpoint{}, errors.New("lark ws endpoint: missing app_id / app_secret")
	}
	body := bootstrapRequest{AppID: creds.AppID, AppSecret: creds.AppSecret}
	raw, err := json.Marshal(body)
	if err != nil {
		return WSEndpoint{}, fmt.Errorf("marshal body: %w", err)
	}
	// 每次调用解析一次引导主机：显式 cfg.BaseURL 覆盖优先（env / httptest），
	// 否则由安装地域在飞书与 Lark 间选择——这样一个 fetcher 服务两朵云。
	base := f.cfg.BaseURL
	if base == "" {
		base = creds.Region.OpenPlatformBaseURL()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/callback/ws/endpoint", bytes.NewReader(raw))
	if err != nil {
		return WSEndpoint{}, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	// locale header 是 SDK 原样带上的——Lark 用它决定错误 `msg` 字段的语言
	// （中文或英文）。这里选 zh 是因为 Multica 服务器日志目前的读者以中文为主；
	// 以后若要做 i18n，可以改成环境变量或每安装一个旋钮。
	req.Header.Set("locale", "zh")
	resp, err := f.cfg.HTTPClient.Do(req)
	if err != nil {
		return WSEndpoint{}, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()
	rawResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return WSEndpoint{}, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WSEndpoint{}, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(rawResp), 512))
	}
	var decoded endpointResponse
	if err := json.Unmarshal(rawResp, &decoded); err != nil {
		return WSEndpoint{}, fmt.Errorf("decode response: %w (raw=%s)", err, truncate(string(rawResp), 256))
	}
	if decoded.Code != 0 || decoded.Data.URL == "" {
		// 把结构化的 Lark 错误原样抛出——操作者正需要它来区分“app 类型不支持”
		// （PersonalAgent 风险）、“凭据错误”与“Lark 故障”。下游 Hub 的退避会在
		// 每次重连尝试时记录它。
		return WSEndpoint{}, fmt.Errorf("lark ws endpoint: code=%d msg=%q", decoded.Code, decoded.Msg)
	}
	serviceID, err := parseServiceIDFromURL(decoded.Data.URL)
	if err != nil {
		return WSEndpoint{}, fmt.Errorf("parse service_id from wss url: %w", err)
	}
	return WSEndpoint{
		URL:               decoded.Data.URL,
		Headers:           http.Header{},
		ServiceID:         serviceID,
		PingInterval:      time.Duration(decoded.Data.ClientConfig.PingInterval) * time.Second,
		ReconnectInterval: time.Duration(decoded.Data.ClientConfig.ReconnectInterval) * time.Second,
		ReconnectNonce:    time.Duration(decoded.Data.ClientConfig.ReconnectNonce) * time.Second,
		ReconnectCount:    decoded.Data.ClientConfig.ReconnectCount,
	}, nil
}

// parseServiceIDFromURL 从 wss URL 里提取 Lark 内嵌的 `service_id` 查询参数。
// connector 发 ping/pong 与 ACK 帧时需要用这个值填充出站的 Frame.Service；
// SDK 也这么做。
func parseServiceIDFromURL(rawURL string) (int32, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, err
	}
	sid := u.Query().Get("service_id")
	if sid == "" {
		return 0, errors.New("missing service_id query parameter")
	}
	n, err := strconv.ParseInt(sid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("service_id %q is not an int: %w", sid, err)
	}
	return int32(n), nil
}
