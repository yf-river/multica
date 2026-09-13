// 本文件实现附件的「下载能力（capability）」签名与赎回：为代理下载模式下的私有附件铸造
// 一条短时、单附件、签名的 URL，让原生/跨站下载请求无需 cookie/Authorization 也能拿到文件。
// 包含签名的铸造（HMAC、密钥派生、载荷形态、意图绑定）、校验（fail-closed），以及把
// 「合法下载能力」兑现成代理流式下载的公开路由。背景与不变量详见文件内下节导读。
package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/auth"
)

// 附件下载能力（capability）—— MUL-5292。
//
// 「原生下载」是浏览器级请求。跨站 webview 的请求既不带客户端的 Authorization 头、也没有
// 会话 cookie，所以需要认证的 /api/attachments/{id}/download 端点会回 401，用户永远拿不到
// 文件。
//
// CloudFront 与 presign 部署早已绕开这点——已认证的 GetAttachmentByID 会给那些客户端一条
// 不再需要本站凭据的签名存储 URL。代理模式（本地磁盘、私有对象主机）没有对应物，一直回着
// 需要认证的 API 路径——这正是 bug 的全部：一个本可正确的设计里，有一条没做完的分支。
//
// capability 用与另外两种模式相同的方式合上这条分支：已经过了认证的 GetAttachmentByID 铸
// 一条短时签名，只授予「读这一个附件」的权限；一条独立的公开路由接受它。成员身份在铸造
// 时校验、赎回时绝不重查——签名本身就是「当时校验过」的证明。
//
// 刻意不做成通用凭据：
//   - 绑死单个附件 id，不能重放到别的附件上；
//   - 60 秒 TTL，因为喂给它的下载会立刻开始；
//   - 用一把与 JWT 密钥域分离的密钥签名，既不能被伪造，也不能拿去伪造会话 token；
//   - 从不持久化，也绝不进入列表响应。

const (
	// attachmentCapabilityVersion 参与签名消息，让消息格式日后可演进：v1 签名绝不会被 v2
	// 校验器验过。
	attachmentCapabilityVersion = "v1"

	// attachmentCapabilityTTL 刻意设得很短。客户端铸出 capability 后在同一拍内就交给原生
	// 下载器；再长只会加宽「泄露的 URL 仍可赎回」的时间窗。
	attachmentCapabilityTTL = 60 * time.Second

	// attachmentCapabilityKeyDomain 把这个签名域与部署从同一根密钥派生的所有其他 HMAC
	// 隔离开。
	attachmentCapabilityKeyDomain = "attachment-download-capability:"

	// attachmentCapabilityDownloadIntent 会折进「强制下载（下载按钮）」型 capability 的签名
	// 消息。这是刻意的冗余空间，不是为当下威胁打的补丁：目前加载意图与下载意图的链接在同
	// 一次响应里铸造，把加载链接改成强制下载只会让它更「不危险」（inline 才是危险姿态），
	// 所以绑定意图挡不住攻击者当下能利用的任何东西。留着它，是防备将来某天某种意图比加载
	// 链接授予更多——届时签名已经绑定了意图，低权限链接不能重放成它。它随 URL 走 dl=1；
	// 空的（加载）意图签的是历史的三字段消息，逐字节一致。
	attachmentCapabilityDownloadIntent = "attachment"
)

var (
	attachmentCapabilityKeyOnce sync.Once
	attachmentCapabilityKey     []byte
)

// attachmentCapabilitySigningKey 经 SHA-256 从部署的 JWT 密钥派生出 capability 专用密钥，
// 手法与 server/cmd/server/router.go 里的 composioStateSecret 一致。派生而非复用意味着
// capability 签名永远不可能与 JWT 签名撞车；轮换 JWT_SECRET 还会顺带作废所有未兑现的
// capability——这正是运维对轮换的预期行为。
func attachmentCapabilitySigningKey() []byte {
	attachmentCapabilityKeyOnce.Do(func() {
		sum := sha256.Sum256(append([]byte(attachmentCapabilityKeyDomain), auth.JWTSecret()...))
		attachmentCapabilityKey = sum[:]
	})
	return attachmentCapabilityKey
}

// signAttachmentCapability 返回对 capability 字段做的 HMAC（hex 编码）。
//
// 各字段用「UUID 或十进制时间戳里都不会出现」的分隔符拼接，所以任何 (id, exp) 组合都不
// 能被重新切分成另一对、产生同样的签名消息。
func signAttachmentCapability(attachmentID string, exp int64) string {
	return signAttachmentCapabilityIntent(attachmentID, exp, "")
}

// signAttachmentCapabilityIntent 对 (version, id, exp) 签名；intent 非空时再追加一个额外的
// 域分离项。加载意图（intent ""）签的恰是历史的三字段消息，签名与之前逐字节一致；下载
// 意图（attachmentCapabilityDownloadIntent）追加 "|attachment"。绑定意图是前瞻性冗余，不是
// 当下缓解——今天这两类意图互换本身不可利用（见 attachmentCapabilityDownloadIntent）——
// 但若将来某个意图比加载链接权限更高，它能让签名保持诚实。
func signAttachmentCapabilityIntent(attachmentID string, exp int64, intent string) string {
	mac := hmac.New(sha256.New, attachmentCapabilitySigningKey())
	mac.Write([]byte(attachmentCapabilityVersion))
	mac.Write([]byte("|"))
	mac.Write([]byte(attachmentID))
	mac.Write([]byte("|"))
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	if intent != "" {
		mac.Write([]byte("|"))
		mac.Write([]byte(intent))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// attachmentCapabilityPath 拼出作为 `download_url` 交还的站内相对 capability URL。
//
// 刻意用站内相对形态。客户端已经会拿配置的 API base 去解析 `download_url`，保持相对形态让
// packages/views/editor/attachment.tsx 里的「inline 媒体重签」路径原样不动：那个 hook 只把
// 绝对 URL 升级成已签名形式，所以它会像今天一样继续无视代理模式的响应——而不会把一条
// 60 秒的 URL 钉进它会缓存 20 分钟的 <img> 里。
func attachmentCapabilityPath(attachmentID string, now time.Time) string {
	exp := now.Add(attachmentCapabilityTTL).Unix()
	return "/api/attachments/" + attachmentID + "/signed-download" +
		"?exp=" + strconv.FormatInt(exp, 10) +
		"&sig=" + signAttachmentCapability(attachmentID, exp)
}

// attachmentDownloadCapabilityPath 拼出「强制下载（下载按钮）」意图的站内相对 capability
// URL。与 attachmentCapabilityPath 同样短时、单 id，另加 dl=1——赎回路由把它变成
// Content-Disposition: attachment。独立铸造、独立签名，这样预览路径消费的加载意图链接可以
// 继续把媒体以 inline 伺服。用站内相对出于同样的原因：inline 媒体重签 hook 只升级绝对 URL，
// 所以它会继续无视这一条。
func attachmentDownloadCapabilityPath(attachmentID string, now time.Time) string {
	exp := now.Add(attachmentCapabilityTTL).Unix()
	return "/api/attachments/" + attachmentID + "/signed-download" +
		"?exp=" + strconv.FormatInt(exp, 10) +
		"&sig=" + signAttachmentCapabilityIntent(attachmentID, exp, attachmentCapabilityDownloadIntent) +
		"&dl=1"
}

// verifyAttachmentCapability 在每条路径上都 fail closed：字段缺失、exp 解析不了、已经过期、
// 签名畸形、或签名是为另一个附件铸的——一律返回 false。
//
// 签名覆盖声明的过期时间，所以把 `exp` 改大只会作废签名，而不是延长 capability。
func verifyAttachmentCapability(attachmentID, rawExp, rawSig, intent string, now time.Time) bool {
	if attachmentID == "" || rawExp == "" || rawSig == "" {
		return false
	}
	exp, err := strconv.ParseInt(rawExp, 10, 64)
	if err != nil {
		return false
	}
	if now.Unix() > exp {
		return false
	}
	got, err := hex.DecodeString(rawSig)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(signAttachmentCapabilityIntent(attachmentID, exp, intent))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}

// ---------------------------------------------------------------------------
// DownloadAttachmentWithCapability — GET /api/attachments/{id}/signed-download
// ---------------------------------------------------------------------------
//
// 注册为公开路由。查询串里的 capability 本身就是凭据，而原生下载请求没有可供
// middleware.Auth 读取的东西——把它放进 Auth 组只会让它失去存在的意义。已认证的
// /api/attachments/{id}/download 端点保持原样——本路由是追加式的，先于它的客户端照常
// 工作，也不存在 middleware.Auth 那套 header/cookie/PAT/任务令牌解析的第二份拷贝。
//
// 一律走代理流式。capability 只在代理模式下铸造，而流式意味着本路由绝不发出跨域跳转，
// 签名后的查询串不会经 Referer 漏给 CDN。
func (h *Handler) DownloadAttachmentWithCapability(w http.ResponseWriter, r *http.Request) {
	attachmentID := chi.URLParam(r, "id")
	query := r.URL.Query()
	// dl=1 是「强制下载（下载按钮）」意图。它由另一份签名覆盖，所以加载意图链接没法靠
	// 追尾 dl=1 把自己翻转成下载——下面的校验会失败。
	intent := ""
	if query.Get("dl") == "1" {
		intent = attachmentCapabilityDownloadIntent
	}
	if !verifyAttachmentCapability(attachmentID, query.Get("exp"), query.Get("sig"), intent, time.Now()) {
		// 所有失败原因共用同一条拒绝文案，调用方无法区分「过期 / 伪造 / 附件不符」，
		// 也就没法拿差异去试探签名者。
		writeError(w, http.StatusForbidden, "invalid or expired download link")
		return
	}

	attUUID, ok := parseUUIDOrBadRequest(w, attachmentID, "attachment id")
	if !ok {
		return
	}
	att, err := h.Queries.GetAttachmentByIDOnly(r.Context(), attUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "storage not configured")
		return
	}

	h.setAttachmentPreviewSecurityHeaders(w)
	// 签名随查询串走。若流式返回的 body 本身是加载子资源的文档，no-referrer 能防止该查询串
	// 漏进出站 Referer。
	w.Header().Set("Referrer-Policy", "no-referrer")

	h.proxyAttachmentDownload(w, r, att, h.Storage.KeyFromURL(att.Url), intent == attachmentCapabilityDownloadIntent)
}
