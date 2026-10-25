// 本文件实现公开的 /api/config 端点：把部署/构建级配置（注册开关、Google OAuth 客户端
// id、CDN 形态、前端可用特性开关、daemon 安装地址、分析埋点 key、服务版本等）无认证地
// 发给 web 前端，让自助托管实例无需重建前端镜像即可在运行时切换行为。只暴露匿名调用者
// 也安全的数据，绝不夹带用户/租户级内容。
package handler

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/featureflags"
)

// AppConfig 是 GET /api/config 的响应：web 应用启动时读取的实例级公开配置。
type AppConfig struct {
	EnvironmentID   string `json:"environment_id,omitempty"`
	EnvironmentName string `json:"environment_name,omitempty"`
	CdnDomain       string `json:"cdn_domain"`
	// CdnSigned 告知客户端上面的 CDN 域名伺服的是「私有内容、经带时限的签名 URL 访问」
	// （即开了 CloudFront 签名）。为 true 时，CDN 域名上的原始存储 URL 无法公开拉取——
	// 渲染器不能把它当原生 <img>/<video> 源，应退回按附件的 API 端点或新签发的
	// download_url（MUL-3254）。为 false 时省略，让旧客户端看到之前的形态。
	CdnSigned bool `json:"cdn_signed,omitempty"`
	// 以下公开的认证配置由 web 应用在运行时读取，这样自助托管部署在运维开关注册或接入
	// Google OAuth 时，不需要重建前端镜像。
	AllowSignup    bool   `json:"allow_signup"`
	GoogleClientID string `json:"google_client_id,omitempty"`
	// WorkspaceCreationDisabled 镜像服务端的 DISABLE_WORKSPACE_CREATION 环境变量，让 UI
	// 在自助托管实例上隐藏所有「创建工作区」入口。为 false 时从 JSON 中省略，让托管云的
	// 常见响应保持与先前形态一致（#3433）。
	WorkspaceCreationDisabled bool `json:"workspace_creation_disabled,omitempty"`
	// 公开的 daemon 安装配置由 web 应用在运行时读取，让自助托管实例显示
	// `multica setup self-host` 命令时带上运维自己的域名，而不是 Multica Cloud 的缺省值。
	DaemonServerURL string `json:"daemon_server_url,omitempty"`
	DaemonAppURL    string `json:"daemon_app_url,omitempty"`

	// VCSIntegrationAvailable 镜像 MULTICA_VCS_INTEGRATION_ENABLED 部署开关，让 Settings UI
	// 在关闭该能力的部署（即托管云）上隐藏整个「自助托管 Git 提供方」区块，而不是渲染出来
	// 再冒出一个云用户无法解决的「缺 MULTICA_VCS_SECRET_KEY」的运维提示。为 false 时省略，
	// 托管云的响应保持原形；UI 把缺省解读为 false（隐藏）。
	VCSIntegrationAvailable bool `json:"vcs_integration_available,omitempty"`

	// 给前端的 PostHog 公开配置。key 就是后端用的同一个 Project API Key；在这里下发
	// （而不是用 NEXT_PUBLIC_* 打进前端包）意味着自助托管实例——其服务端返回空 key——
	// 会自动连前端事件上报一起关掉。
	PosthogKey           string `json:"posthog_key"`
	PosthogHost          string `json:"posthog_host"`
	AnalyticsEnvironment string `json:"analytics_environment"`

	// FeatureFlags 只暴露「前端安全」的布尔决策。别在这里倒原始规则：/api/config 是公开
	// 的，可能被匿名调用。
	FeatureFlags map[string]bool `json:"feature_flags,omitempty"`

	// LocalWorktreeSupported 告知客户端：本服务端理解 local_directory 的 execution_mode，
	// 并在保存资源时强制 worktree 能力闸。
	//
	// 它对「客户端」是承重的，对本服务端不是。v0.4.25 之前的版本把该 ref 反序列化进一个
	// 没有此字段的结构体再序列化出去，于是 `execution_mode: "worktree"` 被静默丢弃、仍回
	// 201——资源随后按 in_place 跑，直接改写了用户要求隔离的工作副本，且没有任何闸能拦住。
	// 新客户端无法从成功中看出这一点，所以必须先问，且「缺省」必须读成「不支持」：凡是丢掉
	// 该字段的版本也一并丢掉了本字段。介于修复与本次信号之间的版本确实会闸住保存但不吭声，
	// 于是按同样方式对待——客户端区分不出它们，而两种猜测里只有一种安全。
	LocalWorktreeSupported bool `json:"local_worktree_supported"`

	// AgentConversationStartersSupported 告知独立部署的客户端：agent 创建/更新会持久化
	// conversation_starters。更老的 handler 会忽略未知 JSON 字段并照样返回成功，所以当本
	// 声明缺省时客户端必须 fail closed。
	AgentConversationStartersSupported bool `json:"agent_conversation_starters_supported"`

	// 只删除所选评论并保留回复，客户端可使用专用路由。
	CommentDeleteKeepRepliesSupported bool `json:"comment_delete_keep_replies_supported"`
	// POST /api/issues 支持在同一事务内写入自定义属性。
	IssueCreatePropertiesSupported bool `json:"issue_create_properties_supported"`
	// ServerVersion 是运行中的 API 构建版本，自助托管运维可据此确认部署内容并在 bug 报告
	// 中带上它。只在自助托管部署上返回——托管云持续部署、用户无法对其版本做动作，因此省略
	// ——没有用 -X main.version 打戳的 dev 构建则为空。
	ServerVersion string `json:"server_version,omitempty"`
}

// GetConfig 挂在公开（无认证）路由组上，因为 web 应用在登录前就调用它来决定是否渲染
// Google 登录按钮与注册 UI。这里只允许加「对匿名调用者安全」的字段——绝不加用户或租户
// 维度的数据。
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	config := AppConfig{
		EnvironmentID: auth.EnvironmentID(),
		// 这是本构建的属性，不是部署的属性：只要这段代码在跑，保存闸就跟它一起在跑。
		LocalWorktreeSupported:             true,
		AgentConversationStartersSupported: true,
		IssueCreatePropertiesSupported:     true,
		CommentDeleteKeepRepliesSupported:  true,
		AllowSignup:                        os.Getenv("ALLOW_SIGNUP") != "false",
		GoogleClientID:                     os.Getenv("GOOGLE_CLIENT_ID"),
		WorkspaceCreationDisabled:          os.Getenv("DISABLE_WORKSPACE_CREATION") == "true",
	}
	switch auth.EnvironmentID() {
	case "int":
		config.EnvironmentName = "联调"
	case "prod":
		config.EnvironmentName = "生产"
	}
	if h.Storage != nil {
		config.CdnDomain = h.Storage.CdnDomain()
	}
	config.CdnSigned = h.CFSigner != nil
	config.DaemonServerURL, config.DaemonAppURL = daemonSetupURLsFromEnv()
	config.VCSIntegrationAvailable = h.cfg.VCSIntegrationEnabled
	config.FeatureFlags = featureflags.EvaluateFrontendPublicFlags(r.Context(), h.FeatureFlags)
	// 只在自助托管部署上暴露构建版本。托管云持续部署、用户无法选择构建版本，Help 弹层里
	// 的版本行在那里只会是噪音（MUL-4108）。
	if !isOfficialCloudDeployment() {
		config.ServerVersion = h.cfg.ServerVersion
	}

	// 每次请求都重读环境变量，让运维可经 secret 刷新轮换 key，无需重启服务。
	if v := os.Getenv("ANALYTICS_DISABLED"); v != "true" && v != "1" {
		config.PosthogKey = os.Getenv("POSTHOG_API_KEY")
		config.PosthogHost = os.Getenv("POSTHOG_HOST")
		config.AnalyticsEnvironment = analytics.EnvironmentFromEnv()
		if config.PosthogHost == "" && config.PosthogKey != "" {
			config.PosthogHost = "https://us.i.posthog.com"
		}
	}

	writeJSON(w, http.StatusOK, config)
}

// daemonSetupURLsFromEnv 从环境变量推出要下发给客户端的 daemon 安装地址
// （MULTICA_DAEMON_SERVER_URL 或退回 MULTICA_PUBLIC_URL / 前端地址）；拿不到任何可用
// 地址、或识别出官方托管云（其 daemon 安装命令固定为 `multica setup`）时返回空。
func daemonSetupURLsFromEnv() (string, string) {
	serverURL := normalizePublicURL(os.Getenv("MULTICA_DAEMON_SERVER_URL"))
	if serverURL == "" {
		serverURL = normalizePublicURL(os.Getenv("MULTICA_PUBLIC_URL"))
	}
	appURL := resolveFrontendAppURL()
	if appURL == "" {
		return "", ""
	}

	if serverURL == "" {
		serverURL = appURL
	}
	if isOfficialCloudDaemonConfig(appURL) {
		return "", ""
	}
	return serverURL, appURL
}

// resolveFrontendAppURL 返回运维配置的前端源地址（MULTICA_APP_URL，缺省退回
// FRONTEND_ORIGIN），并做归一化。daemon 安装地址与托管云识别共用它，保证两处读的是同一
// 个信号。
func resolveFrontendAppURL() string {
	appURL := normalizePublicURL(os.Getenv("MULTICA_APP_URL"))
	if appURL == "" {
		appURL = normalizePublicURL(os.Getenv("FRONTEND_ORIGIN"))
	}
	return appURL
}

// normalizePublicURL 去掉原始 URL 首尾空白与末尾斜杠，得到统一的规范形态。
func normalizePublicURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// isOfficialCloudDaemonConfig 判断本部署是不是官方 Multica Cloud——只看前端 host 一个信号
// （multica.ai）。托管云的 daemon 安装恒为 `multica setup`（内部写死 api.multica.ai），所以
// 即便 MULTICA_PUBLIC_URL 未设或配错，也要从 /api/config 里省略按部署定制的 URL。早前还
// 要求 serverURL==api.multica.ai，结果忘记设 MULTICA_PUBLIC_URL 的云部署会漏出去一条
// `setup self-host --server-url https://multica.ai` 命令——把 daemon 的后端指到了前端上
// （没有 /health，也没有 WebSocket 代理）。
func isOfficialCloudDaemonConfig(appURL string) bool {
	return urlHostEquals(appURL, "multica.ai")
}

// isOfficialCloudDeployment 判断本服务端是不是官方 Multica Cloud，复用的还是 daemon 安装
// 用到的同一个前端 host 信号（multica.ai）。只对托管云生效的行为——比如抑制 Help 弹层里
// 的服务器版本行，那行只对自助托管运维有意义——都以它为闸。
func isOfficialCloudDeployment() bool {
	return isOfficialCloudDaemonConfig(resolveFrontendAppURL())
}

// urlHostEquals 比较归一化后的 host 是否等于 want（不区分大小写、容忍末尾点）。
func urlHostEquals(raw, want string) bool {
	host := canonicalURLHost(raw)
	if host == "" {
		return false
	}
	want = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(want)), ".")
	return host == want
}

// canonicalURLHost 解析出 URL 的主机名（小写、去末尾点）；未带 scheme 的串按 https 前缀
// 补全后再解析。解析失败返回空串。
func canonicalURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" && !strings.Contains(raw, "://") {
		u, err = url.Parse("https://" + raw)
		if err != nil {
			return ""
		}
		host = u.Hostname()
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
