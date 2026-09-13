// 本文件实现 Content-Security-Policy（CSP，内容安全策略）响应头的统一注入。
// CSP 是浏览器侧的安全防线：声明「页面只允许从哪些来源加载脚本/样式/图片/发起连接」，
// 万一 XSS 注入成功，浏览器也会因来源不符而拒绝执行/加载。
// 大部分页面用严格策略 cspHeader；附件预览页因要内嵌附件内容/被 iframe 嵌入，放宽到
// 独立的 attachmentPreviewCSPHeader。
package middleware

import (
	"net/http"
	"strings"
)

// cspBaseHeader 各策略共用的前半段：
//   - default-src 'self'：没单独列出的资源类型默认只允许同源；
//   - script-src 'self'：脚本只允许同源（不放行任何第三方脚本/CDN）；
//   - style-src 额外放行 'unsafe-inline'：内联样式在 React/样式系统里不可避免；
//   - img-src 放行 https: 与 data:（外部图片与内联 base64 图）；
//   - connect-src 放行同源与 wss:（WebSocket 走加密长连接）。
const cspBaseHeader = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' https: data:; " +
	"connect-src 'self' wss:; "

// cspHeader 普通页面策略：禁止被任何页面 iframe 嵌入（frame-ancestors 'none'）、
// 禁 object（老式插件，几乎只剩攻击面）、base-uri 限定同源、表单只能提交同源。
const cspHeader = cspBaseHeader +
	"frame-ancestors 'none'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// attachmentPreviewCSPHeader 附件预览专用策略：与 cspHeader 唯一区别是
// frame-ancestors 放宽成 'self'——预览文档要允许被 Multica 自己的页面 iframe 展示。
const attachmentPreviewCSPHeader = cspBaseHeader +
	"frame-ancestors 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// ContentSecurityPolicy 是注入 CSP 头的中间件，按请求路径挑选合适策略后写入响应头。
func ContentSecurityPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicyForRequest(r))
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicyForRequest 按路径分流策略：附件预览文档用放宽版，其余用严格版。
func contentSecurityPolicyForRequest(r *http.Request) string {
	if isAttachmentPreviewDocumentPath(r.URL.Path) {
		return attachmentPreviewCSPHeader
	}
	return cspHeader
}

// isAttachmentPreviewDocumentPath 判断路径是否是「附件预览文档」端点：
// /api/attachments/ 下的 /download 或 /content。这两类响应体是要展示给用户的
// 附件文档本身，走预览放宽策略。
func isAttachmentPreviewDocumentPath(path string) bool {
	return strings.HasPrefix(path, "/api/attachments/") &&
		(strings.HasSuffix(path, "/download") || strings.HasSuffix(path, "/content"))
}
