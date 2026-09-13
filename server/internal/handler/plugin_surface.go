// 本文件：插件 surface（自定义 UI 页面）的宿主机制——mint 一个短期「启动 URL + 一次性
// challenge」、由专用内容 host 渲染插件代码。安全性要点：URL 里的凭据是加密、认证、短命、
// 且绑定到安装的不可变版本的；专用 host 拒绝接收任何应用凭据，CSP 把插件能连的域收死在
// 授权 scope 范围内。
package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

const (
	pluginSurfaceLaunchTTL       = 2 * time.Minute
	pluginSurfaceProtocolVersion = 2
	pluginSurfaceConnectMessage  = "multica:plugin-bridge-connect"
	pluginSurfacePortGlobal      = "__multicaPluginBridgePortV2"
)

// 上方常量的分工：pluginSurfaceLaunchTTL 是 surface 启动链接的有效期；pluginSurfaceProtocolVersion
// 是页面与父窗口的握手协议版本；pluginSurfaceConnectMessage / pluginSurfacePortGlobal 是握手
// 消息类型与注入到页面全局的消息通道端口名。

// errInvalidPluginSurfaceOrigin：插件 surface origin 必须是「不带路径」的绝对 HTTP(S) origin。
var errInvalidPluginSurfaceOrigin = errors.New("plugin surface origin must be an absolute HTTP(S) origin without a path")

// pluginSurfaceLaunchClaims 是写进 surface 启动 token 的负载：绑定工作区/安装/版本/surface，
// 并带文档摘要与一次性 challenge，全部信息签名加密后防篡改。
type pluginSurfaceLaunchClaims struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	VersionID      string `json:"version_id"`
	SurfaceKey     string `json:"surface_key"`
	Digest         string `json:"digest"`
	Challenge      string `json:"challenge"`
	ExpiresAt      int64  `json:"expires_at"`
}

// pluginSurfaceLaunch 是发起 surface 时返回给宿主页的结构：去专用 host 打开它的 URL、
// 给父窗口的桥接 token（即 challenge）等。
type pluginSurfaceLaunch struct {
	URL         string `json:"url"`
	BridgeToken string `json:"bridge_token"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
}

// NewPluginSurfaceTokenBox 从插件部署密钥派生一把独立的 AES-GCM 密钥。这样即使 operator
// 用一个部署配置同时轮换两者，surface URL 也绝不可能被当成存储的配置机密打开。
func NewPluginSurfaceTokenBox(deploymentKey []byte) (*secretbox.Box, error) {
	if len(deploymentKey) != secretbox.KeySize {
		return nil, secretbox.ErrInvalidKey
	}
	mac := hmac.New(sha256.New, deploymentKey)
	_, _ = mac.Write([]byte("multica/plugin-surface-launch/v1"))
	return secretbox.New(mac.Sum(nil))
}

// parsePluginSurfaceOrigin 把配置串解析成严格的「绝对 HTTP(S) origin」；带路径/查询/片段/
// 用户信息或不是 http(s) 的都报错。
func parsePluginSurfaceOrigin(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, errInvalidPluginSurfaceOrigin
	}
	parsed.Path = ""
	return parsed, nil
}

// pluginSurfaceOriginIsDedicated 判断所给 origin 是否与 API/App/附件宿主是同一台（同 host）。
// 专用 host 才有资格挂插件内容。
func (h *Handler) pluginSurfaceOriginIsDedicated(origin *url.URL) bool {
	candidates := make([]string, 0, len(h.cfg.AttachmentFrameAncestors)+2)
	candidates = append(candidates, h.cfg.PublicURL)
	candidates = append(candidates, h.cfg.AppURL)
	candidates = append(candidates, h.cfg.AttachmentFrameAncestors...)
	for _, candidate := range candidates {
		appOrigin, err := parsePluginSurfaceOrigin(candidate)
		if err == nil && strings.EqualFold(appOrigin.Host, origin.Host) {
			return false
		}
	}
	return true
}

// PluginSurfaceHostBoundary 把配置的内容 host 收窄成一个「单路由 origin」的中间件。复用 API
// 进程在运维上很方便；但把它的登录、JSON API、上传或重定向也暴露到插件 host 下，不在
// 这笔交易之内——所以主机头命中插件 host 且路径不是 /plugin-surfaces/ 的请求一律 404。
func (h *Handler) PluginSurfaceHostBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, err := parsePluginSurfaceOrigin(h.cfg.PluginSurfaceOrigin)
		if err == nil && h.pluginSurfaceOriginIsDedicated(origin) && strings.EqualFold(r.Host, origin.Host) &&
			!strings.HasPrefix(r.URL.Path, "/plugin-surfaces/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// randomPluginSurfaceChallenge 生成 32 字节随机 challenge（URL 安全 base64）。
func randomPluginSurfaceChallenge() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// mintPluginSurfaceToken 把 claims 加密密封成一段 URL 安全 token（AES-GCM + 认证）。
func (h *Handler) mintPluginSurfaceToken(claims pluginSurfaceLaunchClaims) (string, error) {
	if h.PluginSurfaceTokens == nil {
		return "", errors.New("plugin surface token encryption is not configured")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal plugin surface launch: %w", err)
	}
	sealed, err := h.PluginSurfaceTokens.Seal(payload)
	if err != nil {
		return "", fmt.Errorf("seal plugin surface launch: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// openPluginSurfaceToken 解开并校验 surface token：解码、解密、解析，并检查必填字段齐全且
// 未过期——任何一步失败都当无效 token。
func (h *Handler) openPluginSurfaceToken(token string) (pluginSurfaceLaunchClaims, error) {
	if h.PluginSurfaceTokens == nil {
		return pluginSurfaceLaunchClaims{}, errors.New("plugin surface token encryption is not configured")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return pluginSurfaceLaunchClaims{}, errors.New("invalid plugin surface token encoding")
	}
	payload, err := h.PluginSurfaceTokens.Open(sealed)
	if err != nil {
		return pluginSurfaceLaunchClaims{}, errors.New("invalid plugin surface token")
	}
	var claims pluginSurfaceLaunchClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return pluginSurfaceLaunchClaims{}, errors.New("invalid plugin surface claims")
	}
	if claims.WorkspaceID == "" || claims.InstallationID == "" || claims.VersionID == "" ||
		claims.SurfaceKey == "" || claims.Digest == "" || claims.Challenge == "" ||
		claims.ExpiresAt <= time.Now().Unix() {
		return pluginSurfaceLaunchClaims{}, errors.New("expired or incomplete plugin surface claims")
	}
	return claims, nil
}

// GetPluginSurfaceLaunch 返回的没有任何可执行字节：它铸造一个短命的托管 URL，外加那份
// 文档必须向「请求它的那个确切 frame」出示的证据（challenge）。宿主页拿到后去开 iframe。
func (h *Handler) GetPluginSurfaceLaunch(w http.ResponseWriter, r *http.Request) {
	if !h.requirePluginsV1(w, r) {
		return
	}
	origin, err := parsePluginSurfaceOrigin(h.cfg.PluginSurfaceOrigin)
	if err != nil || h.PluginSurfaceTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "Plugin surfaces are unavailable: MULTICA_PLUGIN_SURFACE_ORIGIN and MULTICA_PLUGIN_SECRET_KEY must be configured")
		return
	}
	if !h.pluginSurfaceOriginIsDedicated(origin) {
		writeError(w, http.StatusServiceUnavailable, "Plugin surfaces require a dedicated content origin separate from the app and API origins")
		return
	}

	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace_id")
	if !ok {
		return
	}
	installation, err := h.PluginService.InstallationForWorkspace(r.Context(), workspaceID, chi.URLParam(r, "installationId"))
	if err != nil {
		writePluginError(w, err, "failed to load the Plugin")
		return
	}
	if !installation.Enabled {
		writeError(w, http.StatusForbidden, "this Plugin is disabled")
		return
	}
	script, err := h.PluginService.SurfaceScript(r.Context(), installation, chi.URLParam(r, "surfaceKey"))
	if err != nil {
		writePluginError(w, err, "failed to load the Plugin surface")
		return
	}
	challenge, err := randomPluginSurfaceChallenge()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create the Plugin surface launch")
		return
	}
	claims := pluginSurfaceLaunchClaims{
		WorkspaceID:    util.UUIDToString(workspaceID),
		InstallationID: util.UUIDToString(installation.ID),
		VersionID:      util.UUIDToString(installation.PackageVersionID),
		SurfaceKey:     chi.URLParam(r, "surfaceKey"),
		Digest:         script.Digest,
		Challenge:      challenge,
		ExpiresAt:      time.Now().Add(pluginSurfaceLaunchTTL).Unix(),
	}
	token, err := h.mintPluginSurfaceToken(claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create the Plugin surface launch")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, pluginSurfaceLaunch{
		URL:         origin.String() + "/plugin-surfaces/" + token,
		BridgeToken: challenge,
		Version:     script.Version,
		Digest:      script.Digest,
	})
}

// ServePluginSurface 只在配置的内容 host 上渲染插件代码。路径里的凭据是加密、认证、短命、
// 且绑定到安装的不可变版本的，所以这条路由不需要会话、也收不到应用 cookie。
func (h *Handler) ServePluginSurface(w http.ResponseWriter, r *http.Request) {
	origin, err := parsePluginSurfaceOrigin(h.cfg.PluginSurfaceOrigin)
	if err != nil || !h.pluginSurfaceOriginIsDedicated(origin) || !strings.EqualFold(r.Host, origin.Host) {
		http.NotFound(w, r)
		return
	}
	// 在请求边界兑现部署承诺。宽父域认证 cookie 必须让 surface 明显失败，而不是悄悄落到
	// 一个本该无 cookie 的 host 上。
	if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
		writeError(w, http.StatusBadRequest, "the Plugin content origin must not receive app credentials")
		return
	}
	claims, err := h.openPluginSurfaceToken(chi.URLParam(r, "token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	workspaceID, err := util.ParseUUID(claims.WorkspaceID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	installation, err := h.PluginService.InstallationForWorkspace(r.Context(), workspaceID, claims.InstallationID)
	if err != nil || !installation.Enabled || util.UUIDToString(installation.PackageVersionID) != claims.VersionID {
		http.NotFound(w, r)
		return
	}
	script, err := h.PluginService.SurfaceScript(r.Context(), installation, claims.SurfaceKey)
	if err != nil || !hmac.Equal([]byte(script.Digest), []byte(claims.Digest)) {
		http.NotFound(w, r)
		return
	}
	var scopes []string
	if err := json.Unmarshal(installation.GrantedScopes, &scopes); err != nil {
		http.NotFound(w, r)
		return
	}

	document := buildPluginSurfaceDocument(script.Code, claims.Challenge)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Content-Security-Policy", pluginSurfaceCSP(scopes))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(document))
}

// pluginSurfaceCSP 按授权 scope 算出 surface 页的 Content-Security-Policy：默认全禁，插件
// 的 connect-src 只放行 scope 里授权过的 https 域。
func pluginSurfaceCSP(scopes []string) string {
	connect := make([]string, 0)
	for _, domain := range plugincontract.NetDomains(scopes) {
		// 安装 scope 写入时已校验过，但 CSP 仍是防线——防止某行被破坏或从老库恢复时漏进来。
		if plugincontract.ValidateScope(plugincontract.ScopeNetPrefix+domain) != nil {
			continue
		}
		connect = append(connect, "https://"+domain)
	}
	connectSource := "'none'"
	if len(connect) > 0 {
		connectSource = strings.Join(connect, " ")
	}
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'unsafe-inline'",
		"style-src 'unsafe-inline'",
		"img-src data: blob:",
		"font-src data:",
		"connect-src " + connectSource,
		"frame-src 'none'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'none'",
	}, "; ")
}

// buildPluginSurfaceDocument 拼出 surface 宿主页：把插件代码 base64 藏进一个 text/plain
// 节点，再执行一段 bootstrap——它先把 MessageChannel 的 port 注册到全局（供插件与父窗口
// 双向通信），向父窗口发握手消息（带 challenge），出错/离开页面都会通知父窗口，然后才真正
// 解码并注入插件脚本。
func buildPluginSurfaceDocument(code, challenge string) string {
	encodedCode := base64.StdEncoding.EncodeToString([]byte(code))
	bootstrap := fmt.Sprintf(`(function () {
  var challenge = %s;
  var codeElement = document.getElementById("multica-surface-code");
  var bootstrapElement = document.currentScript;
  var failed = false;
  function reportSurfaceError() {
    if (failed) return;
    failed = true;
    parent.postMessage({ type: "multica:plugin-surface-error" }, "*");
  }
  window.addEventListener("error", reportSurfaceError);
  window.addEventListener("unhandledrejection", reportSurfaceError);
  try {
    var channel = new MessageChannel();
    Object.defineProperty(globalThis, %s, {
      value: channel.port2,
      configurable: true,
      enumerable: false,
      writable: false
    });
    window.addEventListener("pagehide", function () {
      parent.postMessage({ type: "multica:plugin-surface-navigated" }, "*");
    });
    parent.postMessage({
      type: %s,
      version: %d,
      challenge: challenge
    }, "*", [channel.port1]);
    challenge = "";

    var binary = atob(codeElement.textContent || "");
    var bytes = new Uint8Array(binary.length);
    for (var index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
    codeElement.remove();
    bootstrapElement.remove();
    var plugin = document.createElement("script");
    plugin.textContent = new TextDecoder().decode(bytes);
    document.body.appendChild(plugin);
  } catch (error) {
    reportSurfaceError();
  }
})();`, strconv.Quote(challenge), strconv.Quote(pluginSurfacePortGlobal), strconv.Quote(pluginSurfaceConnectMessage), pluginSurfaceProtocolVersion)

	return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
:root { color-scheme: light dark; }
* { box-sizing: border-box; }
html, body { margin: 0; padding: 0; }
body {
  background: var(--background, transparent);
  color: var(--foreground, inherit);
  font: 400 var(--text-body, 14px)/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}
</style>
</head>
<body>
<div id="root"></div>
<script type="text/plain" id="multica-surface-code">` + html.EscapeString(encodedCode) + `</script>
<script>` + bootstrap + `</script>
</body>
</html>`
}
