// 职责定位：头像（avatar）URL 的解析、签发与伺服。核心问题是「头像图存在私有存储桶里、又没
// 有公共 CDN 时，<img> 拿不到」。本文件的方案是：库里永远只存「持久对象引用」（原始存储
// URL，不存任何带 TTL 的东西），对外伺服时换成一条签名过的 /api/avatars/<sig>/<key> 稳定
// URL，由本服务按部署的下载策略（CloudFront 签名 / presign / 代理）即时解析成可加载的地址。
// 详见下方导读注释。
package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// 头像 URL（MUL-5393 / #6024）
// ---------------------------------------------------------------------------
//
// `avatar_url` 列（user / agent / squad / workspace）存的是上传时返回的「原始存储对象 URL」。
// 在桶为公开的部署上——S3/R2 挂在公共 CDN 域名后面，或 LocalStorage 后端的 /uploads/* 路由
// 公开伺服——这个 URL 在 <img> 里直接就能加载，本文件什么也不改。
//
// 在「私有桶 + 无公共 CDN」的部署上（Block Public Access 的 S3、R2、MinIO——即 #6024 报的
// 形态），那个原始 URL 在浏览器里必然 403：`ATTACHMENT_DOWNLOAD_MODE` 只作用于附件下载端点，
// 头像从未走过它。于是即使上传成功，部署里的每个头像也全是裂图。
//
// 修复选在「读时」而不是「写时」解决：
//
//   - 持久化的仍是耐久的对象引用（原始存储 URL）。任何带 TTL 的东西都绝不写进数据库——
//     这是刻意绕开的 MUL-3130 回归——而且旧版本已经存下的头像不用回填也能修好。
//   - 伺服的是 `/api/avatars/<sig>/<key>`：一条稳定 URL，本服务按部署现有的存储下载策略，
//     每个请求把它解析成一条 presigned 重定向（或代理后的 body）。
//
// 端点刻意不做认证，签名就是凭据。走 Auth 的 URL 当不了原生 <img src>：认证 cookie 是
// SameSite=Strict，Desktop/移动端 webview（token 认证、非 API 文档源）和任何跨源自托管 web
// 应用都会加载失败。这与 CloudFront 签名 URL 的理由相同，而且严格比 LocalStorage 后端现有
// 的姿态更紧——后者 /uploads/* 是「无任何认证按 key 伺服对象」。
//
// 三条规则圈定一条签名头像 URL 能触达的范围：
//
//   - HMAC 覆盖存储 key，调用方无法为「服务端从没当头像发布过的 key」私自铸造一条 URL；
//   - 只有图片扩展名会被解析（见 avatarContentType）；
//   - 且对象必须是「头像类」——一个独立的图片上传，没有挂到 issue、评论、聊天会话、聊天
//     消息或任务上（见 avatarKeyIsPublishable）。
//
// 最后一条才是授权边界，且两端都执行。能叫出某个存储对象的名字不等于有权限公开它：没有这
// 道检查，任何见过私有图片附件原始 URL 的人都能把它当自己的头像提交，然后这个无认证端点
// 就会永远替它续签——而用户头像会扩散到该用户所属的每个 workspace。写侧提前拒绝这类值；
// 读侧则每个请求都重查——这正是「这道检查存在之前写下的行也成立」的保证，而且一旦对象后来
// 被绑到评论或聊天，URL 会立即失效。
//
// 上传者身份刻意不进这条规则。复制一个 agent 会合法地复用源 agent 的头像对象——那可能是
// 另一个管理员传的。剩下「公开别人*未绑定*图片」的理论情形需要先知道那个对象的 URL，而未绑
// 定行不会出现在任何列表端点里（ListAttachments 按 issue、groupAttachments 按评论、聊天附件
// 按消息），所以唯一见过它的人就是它自己的上传者。

const avatarURLPathPrefix = "/api/avatars/"

// workspaceUploadNamespace 是 workspace 维度内容的存储前缀——issue/评论/聊天附件、头像和
// channel 媒体接收都在这下面（见 UploadFile 与 lark 的 mediaObjectKey）。这是唯一「对象可能
// 属于设置头像者之外的别人」的命名空间，所以也是唯一必须自证清白的命名空间。
const workspaceUploadNamespace = "workspaces/"

// avatarRedirectMaxAgeCap 限制客户端可复用 302（指向签名存储 URL）的最长时间。实际生效值
// 还会被 avatarRedirectMaxAge 对着 attachmentDownloadURLTTL 再夹一次，保证缓存的跳转永远
// 不会比它指向的签名活得更久，无论运维把 TTL 调得多短。
const avatarRedirectMaxAgeCap = 60

// avatarProxyMaxAge 是代理模式下的头像 body 缓存时长。头像按 key 不可变（每次上传都会铸一个
// 新的 UUIDv7 key），所以这个值只受「重传后要多快生效」约束。
const avatarProxyMaxAge = 300

// avatarExtContentTypes 是头像对象类型的白名单。
//
// SVG 刻意缺席。代理模式下 body 是从 API 源伺服出去的，而内联的 image/svg+xml 文档在导航时
// 是一个脚本执行向量——附件代理靠强制下载头避开它，头像不能那么干（否则没法渲染）。SVG
// 头像保持它（修复前）的既有行为：直接给原始存储 URL，这在所有公开桶部署上都可用。
var avatarExtContentTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
}

var (
	avatarSigningKeyOnce sync.Once
	avatarSigningKey     []byte
)

// avatarURLSigningKey 用 SHA-256 从 JWT_SECRET 派生一个头像专用 HMAC 密钥，让这个签名域
// 永远不会与会话 token 共享同一把钥匙。与 router 里的 composioStateSecret 手法一致。
func avatarURLSigningKey() []byte {
	avatarSigningKeyOnce.Do(func() {
		sum := sha256.Sum256(append([]byte("avatar-url:"), auth.JWTSecret()...))
		avatarSigningKey = sum[:]
	})
	return avatarSigningKey
}

// signAvatarKey 对头像存储 key 算 HMAC 签名（base64url，无填充）。
func signAvatarKey(key string) string {
	mac := hmac.New(sha256.New, avatarURLSigningKey())
	mac.Write([]byte(key))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// avatarKeySignatureValid 用常量时间比较校验 key 与签名的配对，防时序侧信道。
func avatarKeySignatureValid(key, sig string) bool {
	return hmac.Equal([]byte(sig), []byte(signAvatarKey(key)))
}

// avatarContentType 返回头像存储 key 对应的 Content-Type；key 不是允许的图片时返回 ""。
// 刻意按扩展名判断：这个端点只有 key 可用，而且必须在碰存储之前就做决定。
func avatarContentType(key string) string {
	return avatarExtContentTypes[strings.ToLower(path.Ext(key))]
}

// avatarURLPath 拼出某个存储 key 的「站内相对」伺服 URL。
func avatarURLPath(key string) string {
	return avatarURLPathPrefix + signAvatarKey(key) + "/" + key
}

// avatarKeyFromServedURL 从本服务先前发出的 URL 里还原存储 key 并校验签名。站内相对与
// PublicURL 绝对两种形态都接受——客户端可能把解析过的响应再写回 avatar_url，两种都可能落库。
func avatarKeyFromServedURL(raw string) (string, bool) {
	rest := raw
	if !strings.HasPrefix(rest, "/") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		rest = u.Path
	}
	rest, ok := strings.CutPrefix(rest, avatarURLPathPrefix)
	if !ok {
		return "", false
	}
	sig, key, ok := strings.Cut(rest, "/")
	if !ok || key == "" {
		return "", false
	}
	if !avatarKeySignatureValid(key, sig) {
		return "", false
	}
	return key, true
}

// resolveAvatarURL 把存的 avatar_url 变成浏览器能加载的 URL。
//
// 直接透传（常见情形，原样返回）发生在：它根本不是我们的存储对象（emoji 标记、data: URI、
// Google/GitHub 资料图 URL）、不是允许的图片类型、或该部署本来就公开伺服这个对象。
func (h *Handler) resolveAvatarURL(raw string) string {
	if raw == "" || h.Storage == nil {
		return raw
	}
	// 已经是我们发出去的形态（客户端把解析过的响应又写回了 avatar_url）。重新发出而不是再套
	// 一层前缀；签名校验保证这条路不能被拿来给任意 key 换签名。
	if key, ok := avatarKeyFromServedURL(raw); ok {
		return h.absolutizeAvatarPath(avatarURLPath(key))
	}
	key := h.ownedStorageKey(raw)
	if key == "" || avatarContentType(key) == "" {
		return raw
	}
	// 部署本来就能匿名加载：不改写，免得平白加一段经过 API 的跳转。
	if h.avatarObjectLoadsUnauthenticated(raw) {
		return raw
	}
	return h.absolutizeAvatarPath(avatarURLPath(key))
}

// resolveAvatarURLPtr 是 resolveAvatarURL 套在响应 DTO 常用的 *string 形态上的版本。nil 与
// 空串原样保留，好让「没有头像」的意思不变。
func (h *Handler) resolveAvatarURLPtr(raw *string) *string {
	if raw == nil || *raw == "" {
		return raw
	}
	resolved := h.resolveAvatarURL(*raw)
	if resolved == *raw {
		return raw
	}
	return &resolved
}

// normalizeStoredAvatarURL 是写侧的逆操作：客户端把解析过的 `/api/avatars/...` 值 PATCH 回来
// 时，落库的是耐久的对象引用。这维持了列的约定——avatar_url 里存的是存储对象 URL——因此轮换
// JWT_SECRET 也绝不会让头像困在一条服务端已不认签名的 URL 后面。
func (h *Handler) normalizeStoredAvatarURL(raw string) string {
	if h.Storage == nil {
		return raw
	}
	if key, ok := avatarKeyFromServedURL(raw); ok {
		return h.Storage.ObjectURL(key)
	}
	return raw
}

// acceptAvatarURL 校验客户端提交的 avatar_url，返回要持久化的值。校验不过时写好错误响应并
// 返回 ok=false——与 loadIssueForUser 等朋友同形。
//
// `current` 是该实体当前存的 avatar_url。原值重发直接放行、不重复校验：客户端会整体往返整
// 个对象，而「已持久化的值」本来就不会授予什么新东西。
func (h *Handler) acceptAvatarURL(w http.ResponseWriter, r *http.Request, raw, current string) (string, bool) {
	value := h.normalizeStoredAvatarURL(strings.TrimSpace(raw))
	if h.Storage == nil || value == strings.TrimSpace(current) {
		return value, true
	}
	// 不是我们的存储对象（emoji 标记、data: URI、第三方资料 URL）——没有可授权的点，这个
	// 端点也不会给它签名。
	key := h.ownedStorageKey(value)
	if key == "" {
		return value, true
	}
	if !h.avatarKeyIsPublishable(r.Context(), key) {
		writeError(w, http.StatusForbidden, "avatar_url must reference a standalone image upload, not a file attached to an issue, comment, or chat")
		return "", false
	}
	return value, true
}

// avatarKeyIsPublishable 判断某存储对象能否通过公开头像端点伺服。为什么这是授权边界而不是
// 简单健全性检查，见文件头的导读注释。
//
// key 的文件名部分就是 attachment id：UploadFile 只铸一个 UUIDv7、同时用作行 id 与对象文件名，
// 所以这里不用按 URL 反查（也不需要给 URL 建索引）就能解析出底层的行。
func (h *Handler) avatarKeyIsPublishable(ctx context.Context, key string) bool {
	if avatarContentType(key) == "" {
		return false
	}
	if !strings.HasPrefix(key, workspaceUploadNamespace) {
		// workspace 命名空间之外不可能装别人的内容：UploadFile 只写 `workspaces/…` 或
		// `users/<uploader>/…`，而 per-user 分支不产生绑定。其它前缀是运维自己放进桶里的
		// 对象，他们自然有权拿它当头像。
		return true
	}
	attID, ok := attachmentIDFromStorageKey(key)
	if !ok {
		// 在 workspace 命名空间里但对应不上上传行——频道媒体接收也会往这里写
		// `workspaces/<ws>/lark/…` key。失败时按不通过处理（fail closed）。
		return false
	}
	att, err := h.Queries.GetAttachmentByIDOnly(ctx, attID)
	if err != nil {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(att.ContentType), "image/") {
		return false
	}
	return !attachmentIsBound(att)
}

// attachmentIsBound 判断一次上传是否已挂到 workspace 内容上。已绑定的行是某人的 issue/评论/
// 聊天文件——绝不会是头像。
func attachmentIsBound(att db.Attachment) bool {
	return att.IssueID.Valid ||
		att.CommentID.Valid ||
		att.ChatSessionID.Valid ||
		att.ChatMessageID.Valid ||
		att.TaskID.Valid
}

// attachmentIDFromStorageKey 还原 UploadFile 嵌在对象文件名里的 attachment id
// （`<prefix>/<uuid><ext>`）。
func attachmentIDFromStorageKey(key string) (pgtype.UUID, bool) {
	base := path.Base(key)
	stem := strings.TrimSuffix(base, path.Ext(base))
	id, err := util.ParseUUID(stem)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return id, true
}

// ownedStorageKey 当且仅当 rawURL 是本部署存储后端产出的 URL 时，返回它背后的存储 key。
//
// 安全的关键是经过 ObjectURL 的往返：KeyFromURL 按设计是有损的（对认不出的输入退回「最后一个
// 斜杠后的全部内容」），否则 Google/GitHub 的头像 URL 也能得到看起来像样的 key。用 key 重新
// 推出 URL 并要求完全一致，能把「不是本存储铸的」一律拒掉。
func (h *Handler) ownedStorageKey(rawURL string) string {
	key := h.Storage.KeyFromURL(rawURL)
	if key == "" || key == rawURL {
		return ""
	}
	if h.Storage.ObjectURL(key) != rawURL {
		return ""
	}
	return key
}

// avatarObjectLoadsUnauthenticated 判断原始存储 URL 是否已能被匿名浏览器请求加载——若是，
// 改写它只会平白多一段经过 API 的跳转。
func (h *Handler) avatarObjectLoadsUnauthenticated(rawURL string) bool {
	// LocalStorage 对象由公开的 /uploads/* 路由伺服（存的 URL 可能是站内相对，也可能是
	// LOCAL_UPLOAD_BASE_URL 拼的绝对地址）。
	if _, ok := h.Storage.(*storage.LocalStorage); ok {
		return true
	}
	// 否则：带公共 CDN 域名、又没做逐请求 CloudFront 签名。在签名 CloudFront 模式下，同一个
	// 域名伺服私有内容，未签名的 URL 是 403，所以那种形态也走本端点解析。
	return h.storageURLIsPubliclyReadable(rawURL)
}

// absolutizeAvatarPath 配了 MULTICA_PUBLIC_URL 时把伺服路径钉到该地址上，让不共享 API 文档
// 源（Desktop、移动端 webview）的客户端也能加载。与 buildMarkdownURL 同一策略；退回站内相对
// 路径是安全的，因为每个客户端都经由 resolvePublicFileUrl 解析头像 URL——它会自动拼上 API
// base。
func (h *Handler) absolutizeAvatarPath(relPath string) string {
	if publicURL := strings.TrimRight(h.cfg.PublicURL, "/"); publicURL != "" {
		return publicURL + relPath
	}
	return relPath
}

// ---------------------------------------------------------------------------
// ServeAvatar — GET /api/avatars/{sig}/*
// ---------------------------------------------------------------------------
//
// 按设计不做认证（签名就是凭据）——见文件头导读。解析复用 resolveAttachmentDownloadMode，
// 让头像与附件在某个部署上服从同一条 ATTACHMENT_DOWNLOAD_MODE 策略；只有一个存储后端，
// 也就只有一种正确的读法。
func (h *Handler) ServeAvatar(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		http.NotFound(w, r)
		return
	}

	key, err := url.PathUnescape(chi.URLParam(r, "*"))
	if err != nil || key == "" {
		http.NotFound(w, r)
		return
	}
	contentType := avatarContentType(key)
	// 拒绝的形态一律平铺成 404——坏签名、未知 key、非图片 key、非头像类对象看起来都一样，
	// 这样这条路由不会成为这四者中任何一方的预言机。「可发布」检查每个请求都跑、而不是只在
	// 写时跑：它约束写侧闸门出现前就已写入的行，而且一旦对象后来被绑到评论/聊天，URL 会被
	// 立即吊销。
	if contentType == "" ||
		!avatarKeySignatureValid(key, chi.URLParam(r, "sig")) ||
		!h.avatarKeyIsPublishable(r.Context(), key) {
		http.NotFound(w, r)
		return
	}

	objectURL := h.Storage.ObjectURL(key)
	h.setAttachmentPreviewSecurityHeaders(w)

	// 按部署的下载策略分派：CloudFront 签名 / presign / 由本服务代理。
	switch h.resolveAttachmentDownloadMode(objectURL) {
	case attachmentDownloadModeCloudFront:
		if h.CFSigner == nil {
			writeError(w, http.StatusInternalServerError, "cloudfront avatar downloads are not configured")
			return
		}
		setAvatarCacheControl(w, h.avatarRedirectMaxAge())
		http.Redirect(w, r, h.CFSigner.SignedURL(objectURL, time.Now().Add(h.attachmentDownloadURLTTL())), http.StatusFound)
	case attachmentDownloadModePresign:
		presigner, ok := h.Storage.(storage.DownloadPresigner)
		if !ok {
			writeError(w, http.StatusInternalServerError, "avatar storage does not support presigned downloads")
			return
		}
		// Content-Disposition 传空串会继承对象存着的那份（图片通常是 inline）——正是这一点让
		// 跳转在 <img> 里可渲染，而不是触发下载。
		signedURL, err := presigner.PresignGetWithContentDisposition(r.Context(), key, h.attachmentDownloadURLTTL(), "")
		if err != nil {
			slog.Error("failed to presign avatar", "key", key, "error", err)
			writeError(w, http.StatusBadGateway, "failed to create avatar URL")
			return
		}
		setAvatarCacheControl(w, h.avatarRedirectMaxAge())
		http.Redirect(w, r, signedURL, http.StatusFound)
	case attachmentDownloadModeProxy:
		h.proxyAvatar(w, r, key, contentType)
	default:
		writeError(w, http.StatusInternalServerError, "invalid attachment download mode")
	}
}

// proxyAvatar 把对象经 API 流式回给浏览器，用于浏览器无法直连的后端（本地磁盘，或 http://
// rustfs:9000 这类私有主机）。不做 Range：头像是 <img> 加载的小图，不是可续传的下载。
func (h *Handler) proxyAvatar(w http.ResponseWriter, r *http.Request, key, contentType string) {
	reader, err := h.Storage.GetReader(r.Context(), key)
	if err != nil {
		slog.Warn("avatar object not found", "key", key, "error", err)
		http.NotFound(w, r)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setAvatarCacheControl(w, avatarProxyMaxAge)

	if _, err := io.Copy(w, reader); err != nil {
		slog.Warn("avatar stream interrupted", "key", key, "error", err)
	}
}

// avatarRedirectMaxAge 是客户端可复用那条 302 的时长，且被夹到签名 URL 自身寿命的一半。
// ATTACHMENT_DOWNLOAD_URL_TTL 接受任意正时长——若这里固定 60 秒，在把 TTL 配得比它短的部署
// 上，浏览器或代理会一直重放进一条早已过期的存储 URL。TTL 短到连安全余量都没有时返回 0
// （完全不缓存）。
func (h *Handler) avatarRedirectMaxAge() int {
	ttlSeconds := int(h.attachmentDownloadURLTTL() / time.Second)
	maxAge := ttlSeconds / 2
	if maxAge > avatarRedirectMaxAgeCap {
		return avatarRedirectMaxAgeCap
	}
	if maxAge < 0 {
		return 0
	}
	return maxAge
}

// setAvatarCacheControl 把响应标成 private：URL 虽不可猜，但它仍是某个部署的身份图片，共享
// 代理没资格留一份。max-age 为 0 时用 no-store（「完全不存」），而不是「存 0 秒」——后者
// 有些中间层会向上取整。
func setAvatarCacheControl(w http.ResponseWriter, maxAge int) {
	if maxAge <= 0 {
		w.Header().Set("Cache-Control", "no-store")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(maxAge))
}
