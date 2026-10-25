// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（file.go）实现附件（attachment）的上传、列表、下载与删除，以及围绕
// 附件 URL 的一整套策略：根据部署形态（CloudFront 签名 / S3 预签名 / API 代理 /
// 本地磁盘）生成不同的下载/内嵌 URL，并提供 markdown 可持久化 URL、文本类
// 附件的内联预览代理（支持断点续传的 Range 请求）。上传走 multipart，
// 100MB 上限，文件类型以服务端嗅探为准。
package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// extContentTypes 覆盖 http.DetectContentType 对部分扩展名判断错误的情况——
// Go 的嗅探器会把 SVG 判成 text/xml、CSS/JS 判成 text/plain 等。
var extContentTypes = map[string]string{
	".svg":  "image/svg+xml",
	".css":  "text/css",
	".js":   "application/javascript",
	".mjs":  "application/javascript",
	".json": "application/json",
	".wasm": "application/wasm",
}

// maxUploadSize 单次上传上限：100 MB。
const maxUploadSize = 100 << 20 // 100 MB

// defaultAttachmentDownloadURLTTL 附件下载签名 URL 的默认有效期：30 分钟。
const defaultAttachmentDownloadURLTTL = 30 * time.Minute

// attachmentDownloadMode 表示"部署怎么把存储对象交给下载方"的策略名。
type attachmentDownloadMode string

const (
	attachmentDownloadModeAuto       attachmentDownloadMode = "auto"
	attachmentDownloadModeCloudFront attachmentDownloadMode = "cloudfront"
	attachmentDownloadModePresign    attachmentDownloadMode = "presign"
	attachmentDownloadModeProxy      attachmentDownloadMode = "proxy"
)

// maxPreviewTextSize 限制预览代理为文本类类型载入内存的最大字节数。更大的文件
// 回 413，UI 退化为"请下载"。尺寸取值让典型的 README/源码放得下，又让 100MB
// 的日志导出撑不爆渲染器。
const maxPreviewTextSize = 2 << 20 // 2 MB

// ---------------------------------------------------------------------------
// 响应类型
// ---------------------------------------------------------------------------

// AttachmentResponse 是单个附件的对外 JSON 形态。UploaderType/UploaderID 描述
// 上传者（member/agent + 对应 id），URL 是存储对象地址，DownloadURL 与
// AttachmentDownloadURL 分别是"用于预览加载"和"用于下载按钮强制下载"的地址，
// MarkdownURL 是可安全写进 markdown 正文的长效地址。
type AttachmentResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	IssueID       *string `json:"issue_id"`
	CommentID     *string `json:"comment_id"`
	ChatSessionID *string `json:"chat_session_id"`
	ChatMessageID *string `json:"chat_message_id"`
	UploaderType  string  `json:"uploader_type"`
	UploaderID    string  `json:"uploader_id"`
	Filename      string  `json:"filename"`
	URL           string  `json:"url"`
	DownloadURL   string  `json:"download_url"`
	// AttachmentDownloadURL 是一条不带凭据、在各种存储模式下都强制
	// Content-Disposition: attachment 的 URL，专供"下载按钮"用——它区别于
	// DownloadURL：后者是"加载意图"，保持内嵌媒体形式以便预览路径
	// （resolvePreviewMediaUrl）渲染。与 DownloadURL 一样它可能很短命（60 秒的
	// 代理 capability、预签名 URL），因此绝不能持久化，且只在单附件接口
	// （GetAttachmentByID）里输出，列表响应从不带它。对象的存储模式无法生成
	// 时留空；客户端回退用 DownloadURL。（MUL 对 #6092 / #6713 的跟进。）
	AttachmentDownloadURL string `json:"attachment_download_url,omitempty"`
	// MarkdownURL 是客户端应当写进 markdown 正文（issue 描述、评论、聊天消息）
	// 的长效、尽可能绝对化的 URL。它由 buildMarkdownURL 按部署策略算出——当
	// 存储 URL 已经是公开、长效的绝对 URL（公网 CDN / 配了
	// MULTICA_LOCAL_UPLOAD_BASE_URL 的 LocalStorage）时优先用它，否则在
	// MULTICA_PUBLIC_URL 前缀后面接上稳定的、服务端每次请求自行签名/代理的
	// 单附件接口地址。
	//
	// 为什么单独设一个字段而不是复用 URL / DownloadURL：
	//   - URL 是存储对象的原始地址——头像/logo 等场合没问题，但可能私有
	//     （S3 + CloudFront 签名模式）或站内相对路径（没配 base URL 的
	//     LocalStorage）。
	//   - DownloadURL 是本响应渲染器要用的 URL——可能是短命签名 URL
	//     （CloudFront、S3 presign），因此绝不能持久化。它会过期。
	//   - MarkdownURL 约定为可持久化：永远不带 TTL，且在每种受支持的部署形态
	//     下都能当原生浏览器资源加载（除解析出的主机上客户端已有的
	//     cookie/凭据外，不需要 Authorization 头）。
	//
	// MUL-3192——修复 Desktop / mobile-webview 回归：旧的站内相对路径
	// /api/attachments/<id>/download 只有当文档源把 /api 代理到 API 主机时才
	// 能解析。
	MarkdownURL string `json:"markdown_url"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAt   string `json:"created_at"`
}

// attachmentURLMode 决定响应里 DownloadURL 的渲染方式。
//
// MUL-5372 / GitHub #5999。CloudFront 签名的 DownloadURL 约有 800 字符，其中
// 约 630 是 Policy+Signature 对——它每次请求都重铸（策略按秒嵌入 now+TTL）。
// 在每个列表响应的每个附件上都输出它，代价是三重的：原始载荷大小、每个附件
// 每次请求一次新的 RSA 签名，而且字节每次读都不同，会废掉任何以响应内容为键
// 的缓存。agent 三种代价全付却一种都用不上：它们走单附件接口取文件，那个接口
// 只需要 id。
//
// 该模式是"调用方能力声明"，绝不是服务端默认值的切换——不认识它的客户端得到
// 的响应与以前逐字节一致。见 attachmentURLModeFromRequest。
type attachmentURLMode int

const (
	// attachmentURLModeSigned 把授权预先绑进 DownloadURL，让调用方可以直接交给
	// 无法带 Authorization 头的原生资源加载（浏览器 <img>、Linking.openURL）。
	// 这是每个未主动退出的调用方的默认值。
	attachmentURLModeSigned attachmentURLMode = iota
	// attachmentURLModeStable 让 DownloadURL 渲染成稳定的 /api/attachments/{id}/
	// download 路径。该接口每次命中都重新签名并 302，所以值永远正确，且只花约
	// 95 字符而非约 800。选择此模式的调用方必须能跟随带认证的重定向，或在把
	// URL 交给原生加载器之前先从单附件接口取一份新签名。
	attachmentURLModeStable
)

// ClientCapabilityStableAttachmentURLs 是调用方通过 X-Client-Capabilities
// 声明"批量响应里给我稳定附件路径、不要预签名 URL"的标记。复用现有的能力头
// （而非加查询参数）让声明保持客户端全局：它描述的是调用方处理能力，不是被
// 请求资源的属性。
const ClientCapabilityStableAttachmentURLs = "stable_attachment_urls"

// attachmentURLModeFromRequest 解析请求要求的模式。未声明或无法识别的声明一律
// 解析成 attachmentURLModeSigned——这使本变更可以早于任何客户端发布：服务端
// 默认值不动，调用方按自己的发布节奏迁移。
func attachmentURLModeFromRequest(r *http.Request) attachmentURLMode {
	if r != nil && requestHasClientCapability(r, ClientCapabilityStableAttachmentURLs) {
		return attachmentURLModeStable
	}
	return attachmentURLModeSigned
}

// attachmentToResponse 把附件库行按指定 URL 模式转成对外响应。
func (h *Handler) attachmentToResponse(a db.Attachment, mode attachmentURLMode) AttachmentResponse {
	id := uuidToString(a.ID)
	resp := AttachmentResponse{
		ID:           id,
		WorkspaceID:  uuidToString(a.WorkspaceID),
		UploaderType: a.UploaderType,
		UploaderID:   uuidToString(a.UploaderID),
		Filename:     a.Filename,
		URL:          a.Url,
		DownloadURL:  util.AttachmentDownloadPath(id),
		MarkdownURL:  h.buildMarkdownURL(a, id),
		ContentType:  a.ContentType,
		SizeBytes:    a.SizeBytes,
		CreatedAt:    a.CreatedAt.Time.Format("2006-01-02T15:04:05Z07:00"),
	}
	// 只有 CloudFront 模式在这里覆盖稳定路径；presign 与 proxy 模式本就给
	// DownloadURL 留稳定路径、在下载时再解析，所以 stable 模式对它们是无操作。
	if h.CFSigner != nil && mode != attachmentURLModeStable {
		resp.DownloadURL = h.CFSigner.SignedURL(a.Url, time.Now().Add(h.attachmentDownloadURLTTL()))
	}
	if a.IssueID.Valid {
		s := uuidToString(a.IssueID)
		resp.IssueID = &s
	}
	if a.CommentID.Valid {
		s := uuidToString(a.CommentID)
		resp.CommentID = &s
	}
	if a.ChatSessionID.Valid {
		s := uuidToString(a.ChatSessionID)
		resp.ChatSessionID = &s
	}
	if a.ChatMessageID.Valid {
		s := uuidToString(a.ChatMessageID)
		resp.ChatMessageID = &s
	}
	return resp
}

// attachmentsToResponses 对"事务里加载出来的附件快照"应用普通响应 URL 策略。
func (h *Handler) attachmentsToResponses(attachments []db.Attachment) []AttachmentResponse {
	if len(attachments) == 0 {
		return nil
	}
	responses := make([]AttachmentResponse, len(attachments))
	for i, attachment := range attachments {
		responses[i] = h.attachmentToResponse(attachment, attachmentURLModeSigned)
	}
	return responses
}

// buildMarkdownURL 挑选客户端要写进 markdown 正文的长效 URL。约定是"绝对化、
// 无 TTL、在每个受支持客户端上都能当原生浏览器资源加载"（MUL-3192）。
//
// 决策：
//
//  1. 只有部署明确表示"存储后端无需每请求认证、公网即可读"时才持久化 a.Url：
//     - Storage.CdnDomain() 非空（运营商配了面向公众的 base URL——
//     S3 后端的 S3_CDN_DOMAIN，或 LocalStorage 的 LOCAL_UPLOAD_BASE_URL），
//     并且
//     - h.CFSigner 为 nil（无每请求 CloudFront 签名——签名开启时，同一个
//     CDN 域名通过限时签名 URL 提供的是私有内容，裸的 a.Url 会拒绝匿名
//     访问），并且
//     - a.Url 本身是带签名查询的绝对 http(s) URL——既防"baseURL 未配置时
//     回填造成的旧行"，也防新签的 download_url 泄进 a.Url（最初的
//     MUL-3130 bug）。
//
//  2. 其他所有形态——CloudFront 签名模式、对没有 CDN 域名的私有桶做 S3
//     presign/proxy、裸 S3 / R2 / MinIO、没配 LOCAL_UPLOAD_BASE_URL 的
//     LocalStorage——都用那个服务端每次请求自行签名/代理的稳定单附件接口，
//     锚在 MULTICA_PUBLIC_URL 上，让持久化 URL 对不共享文档源（Desktop /
//     mobile webview）的客户端也能用。
//
//  3. 最后兜底（没配 MULTICA_PUBLIC_URL）：输出站内相对路径。Web 的 Next.js
//     rewrite 处理它；对"没配 PublicURL 的部署"上的非 web 客户端，MUL-3192
//     之前就坏着，这里保持不更坏。
func (h *Handler) buildMarkdownURL(a db.Attachment, id string) string {
	relPath := util.AttachmentDownloadPath(id)
	publicURL := strings.TrimRight(h.cfg.PublicURL, "/")

	if h.storageURLIsPubliclyReadable(a.Url) {
		return a.Url
	}

	if publicURL != "" {
		return publicURL + relPath
	}
	return relPath
}

// storageURLIsPubliclyReadable 判断部署是否声明过"a.Url 可以被未认证的原生
// 浏览器直接加载"——只有这种情况才适合把 a.Url 写进会活过当前会话的 markdown
// 正文。
func (h *Handler) storageURLIsPubliclyReadable(rawURL string) bool {
	if h.Storage == nil || h.CFSigner != nil {
		// CFSigner != nil 表示每请求签名；此时 CDN 域名通过签名 URL 提供私有
		// 内容，a.Url 是裸 S3 URL。
		return false
	}
	if h.Storage.CdnDomain() == "" {
		// 没配公网 base URL——存储 URL 要么是裸私有对象地址（S3 / R2 / MinIO），
		// 要么是不带源站信息的站内相对 LocalStorage 路径。
		return false
	}
	return isDurablePublicURL(rawURL)
}

// isDurablePublicURL 判断 rawURL 是否为可写进长期 markdown 正文的安全绝对
// http(s) URL——即它不带会让它过期的 CloudFront / S3 签名查询。
func isDurablePublicURL(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	q := u.Query()
	for _, k := range []string{
		"Signature",
		"X-Amz-Signature",
		"Key-Pair-Id",
		"Expires",
		"X-Amz-Expires",
	} {
		if q.Get(k) != "" {
			return false
		}
	}
	return true
}

// normalizeAttachmentDownloadMode 把配置字符串归一化为合法模式；空串视为 auto，
// 无法识别则返回 auto + false。
func normalizeAttachmentDownloadMode(raw string) (attachmentDownloadMode, bool) {
	switch attachmentDownloadMode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", attachmentDownloadModeAuto:
		return attachmentDownloadModeAuto, true
	case attachmentDownloadModeCloudFront:
		return attachmentDownloadModeCloudFront, true
	case attachmentDownloadModePresign:
		return attachmentDownloadModePresign, true
	case attachmentDownloadModeProxy:
		return attachmentDownloadModeProxy, true
	default:
		return attachmentDownloadModeAuto, false
	}
}

// attachmentDownloadMode 返回本实例配置的下载模式。
func (h *Handler) attachmentDownloadMode() attachmentDownloadMode {
	mode, _ := normalizeAttachmentDownloadMode(h.cfg.AttachmentDownloadMode)
	return mode
}

// attachmentDownloadURLTTL 返回下载 URL 的有效期；配置没给时用默认值。
func (h *Handler) attachmentDownloadURLTTL() time.Duration {
	if h.cfg.AttachmentDownloadURLTTL > 0 {
		return h.cfg.AttachmentDownloadURLTTL
	}
	return defaultAttachmentDownloadURLTTL
}

// groupAttachments 按 comment_id 一次查出多个评论的附件并按评论分组返回。
// 供评论列表展示附件，避免每个评论一条 N+1 查询。
func (h *Handler) groupAttachments(r *http.Request, commentIDs []pgtype.UUID) map[string][]AttachmentResponse {
	if len(commentIDs) == 0 {
		return nil
	}
	workspaceID := h.resolveWorkspaceID(r)
	attachments, err := h.Queries.ListAttachmentsByCommentIDs(r.Context(), db.ListAttachmentsByCommentIDsParams{
		Column1:     commentIDs,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		slog.Error("failed to load attachments for comments", "error", err)
		return nil
	}
	mode := attachmentURLModeFromRequest(r)
	grouped := make(map[string][]AttachmentResponse, len(commentIDs))
	for _, a := range attachments {
		cid := uuidToString(a.CommentID)
		grouped[cid] = append(grouped[cid], h.attachmentToResponse(a, mode))
	}
	return grouped
}

// groupChatMessageAttachments 按 chat_message_id 一次查出多条聊天消息的附件
// 并分组返回。镜像 groupAttachments——让聊天消息列表能给 UI 气泡带出附件元数据
// （文件卡片、点击下载）而不必每条消息做一次 N+1 查询。
func (h *Handler) groupChatMessageAttachments(ctx context.Context, workspaceID string, messageIDs []pgtype.UUID) map[string][]AttachmentResponse {
	if len(messageIDs) == 0 {
		return nil
	}
	attachments, err := h.Queries.ListAttachmentsByChatMessageIDs(ctx, db.ListAttachmentsByChatMessageIDsParams{
		Column1:     messageIDs,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		slog.Error("failed to load attachments for chat messages", "error", err)
		return nil
	}
	grouped := make(map[string][]AttachmentResponse, len(messageIDs))
	for _, a := range attachments {
		mid := uuidToString(a.ChatMessageID)
		grouped[mid] = append(grouped[mid], h.attachmentToResponse(a, attachmentURLModeSigned))
	}
	return grouped
}

// ---------------------------------------------------------------------------
// UploadFile — POST /api/upload-file
// ---------------------------------------------------------------------------

// UploadFile 处理 multipart 文件上传（≤100MB）。需要登录；有 workspace 上下文
// 时校验成员身份并登记附件行（可挂 issue/comment/chat_session/task），无
// workspace 上下文（如头像上传）则只存对象、返回裸 URL。类型用服务端嗅探 +
// 扩展名纠正，不信任客户端头。上传者解析为实际执行者（member 或 agent）。
func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request) {
	if h.Storage == nil {
		writeFeatureDisabled(w, "file_upload_not_configured", "file upload not configured")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "file too large or invalid multipart form")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing file field: %v", err))
		return
	}
	defer file.Close()

	// 从文件字节嗅探真实内容类型，而不是相信客户端头。
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}
	contentType := http.DetectContentType(buf[:n])
	// 嗅探器判错时用扩展名类型纠正。
	if ct, ok := extContentTypes[strings.ToLower(path.Ext(header.Filename))]; ok {
		contentType = ct
	}
	// 回卷，让后续能上传完整文件。
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read file")
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read file")
		return
	}

	// 生成一个 UUIDv7，同时充当附件 ID 与 S3 对象键。
	id, err := uuid.NewV7()
	if err != nil {
		slog.Error("failed to generate uuid", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	filename := id.String() + path.Ext(header.Filename)
	var key string
	if workspaceID != "" {
		key = "workspaces/" + workspaceID + "/" + filename
	} else {
		key = "users/" + userID + "/" + filename
	}

	// 有 workspace 上下文时，上传前先校验成员身份。
	if workspaceID != "" {
		if _, err := h.getWorkspaceMember(r.Context(), userID, workspaceID); err != nil {
			writeError(w, http.StatusForbidden, "not a member of this workspace")
			return
		}

		uploaderType, uploaderID := h.resolveActor(r, userID, workspaceID)

		params := db.CreateAttachmentParams{
			ID:           pgtype.UUID{Bytes: id, Valid: true},
			WorkspaceID:  parseUUID(workspaceID),
			UploaderType: uploaderType,
			UploaderID:   parseUUID(uploaderID),
			Filename:     header.Filename,
			ContentType:  contentType,
			SizeBytes:    int64(len(data)),
		}

		if issueID := r.FormValue("issue_id"); issueID != "" {
			issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "issue_id")
			if !ok {
				return
			}
			issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
				ID:          issueUUID,
				WorkspaceID: parseUUID(workspaceID),
			})
			if err != nil {
				writeError(w, http.StatusForbidden, "invalid issue_id")
				return
			}
			params.IssueID = issue.ID
		}
		if commentID := r.FormValue("comment_id"); commentID != "" {
			commentUUID, ok := parseUUIDOrBadRequest(w, commentID, "comment_id")
			if !ok {
				return
			}
			comment, err := h.Queries.GetComment(r.Context(), commentUUID)
			// A deleted comment's tombstone takes no attachments.
			if err != nil || uuidToString(comment.WorkspaceID) != workspaceID || comment.DeletedAt.Valid {
				writeError(w, http.StatusForbidden, "invalid comment_id")
				return
			}
			params.CommentID = comment.ID
		}
		if chatSessionID := r.FormValue("chat_session_id"); chatSessionID != "" {
			// 既要求该会话在"成员可见的 Chat 投影"里，也要通过私有 agent 访问
			// 校验。一个被缓存的纯指令会话 id 不得接受上传——旧客户端可能把它
			// 挂到附件上，从而"复活"该会话。
			session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, chatSessionID)
			if !ok {
				return
			}
			params.ChatSessionID = session.ID
		}
		// task_id 上传：agent 为它的聊天回复产出图片/文件。行上打上产出任务与其
		// 聊天会话的标记，让 CompleteTask 能把它绑到合成的助手消息上。
		// 门槛：请求必须来自任务作用域的 token，表单里的 task_id 必须等于该
		// token 的任务，调用者必须是该任务的 agent，且必须是聊天任务（带
		// chat_session_id）。
		if taskID := r.FormValue("task_id"); taskID != "" {
			// 权威的任务 token 边界（承重墙，镜像 chat_history.go:
			// chatHistorySession）。X-Task-ID 只有在认证中间件从任务作用域的
			// `mat_` token 里设置时才可信——那条路径也是唯一同时打
			// X-Actor-Source=task_token、并剥离客户端伪造的 X-Task-ID 的地方。
			// 普通 JWT / `mul_` PAT 不会设 X-Actor-Source，也不会剥离伪造的
			// X-Task-ID；而 resolveActor 的回退会接受"真实的 X-Agent-ID +
			// X-Task-ID"组合。所以没有这层门槛，一个知道任务 ID 的成员可以伪造
			// 两个头、把附件塞进别的聊天任务的助手回复——跨会话/隐私泄漏。
			if r.Header.Get("X-Actor-Source") != "task_token" {
				writeError(w, http.StatusForbidden, "task_id upload is only available from within an agent task")
				return
			}
			taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
			if !ok {
				return
			}
			// 钉死在该运行自己的任务上：中间件注入的 X-Task-ID 是"此 token 能对
			// 哪个任务动手"的唯一真源，所以被授权为任务 A 的一次运行不能把附件
			// 标记到任务 B 上——哪怕是同一 agent 的另一条聊天任务，其会话可能
			// 属于另一个用户。
			boundTaskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
			if boundTaskID == "" || !strings.EqualFold(boundTaskID, strings.TrimSpace(taskID)) {
				writeError(w, http.StatusForbidden, "task_id must match the request's task token")
				return
			}
			task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
				ID:          taskUUID,
				WorkspaceID: parseUUID(workspaceID),
			})
			if err != nil {
				writeError(w, http.StatusForbidden, "invalid task_id")
				return
			}
			if uploaderType != "agent" || uuidToString(task.AgentID) != uploaderID {
				writeError(w, http.StatusForbidden, "task_id upload requires the task's own agent")
				return
			}
			if !task.ChatSessionID.Valid {
				writeError(w, http.StatusBadRequest, "task_id upload requires a chat task")
				return
			}
			params.TaskID = task.ID
			// 会话也一并绑定，让读取（groupChatMessageAttachments）与 GC 在行获得
			// message id 之前就能一致地归类它。
			params.ChatSessionID = task.ChatSessionID
		}

		link, err := h.Storage.Upload(r.Context(), key, data, contentType, header.Filename)
		if err != nil {
			slog.Error("file upload failed", "error", err)
			writeError(w, http.StatusInternalServerError, "upload failed")
			return
		}
		params.Url = link

		tx, beginErr := h.TxStarter.Begin(r.Context())
		if beginErr != nil {
			h.Storage.Delete(r.Context(), link)
			slog.Error("failed to begin attachment transaction", "error", beginErr)
			writeError(w, http.StatusInternalServerError, "upload failed")
			return
		}
		defer tx.Rollback(r.Context())
		qtx := h.Queries.WithTx(tx)
		if params.CommentID.Valid {
			if _, lockErr := qtx.LockLiveComment(r.Context(), db.LockLiveCommentParams{ID: params.CommentID, WorkspaceID: params.WorkspaceID}); lockErr != nil {
				h.deleteS3Objects(r.Context(), []string{link})
				writeError(w, http.StatusForbidden, "invalid comment_id")
				return
			}
		}
		att, err := qtx.CreateAttachment(r.Context(), params)
		if err != nil {
			slog.Error("failed to create attachment record", "error", err)
			h.Storage.Delete(r.Context(), link)
			writeError(w, http.StatusInternalServerError, "upload failed")
			return
		} else {
			persistedEvents := make([]events.Event, 0, 2)
			if att.IssueRevision > 0 && att.IssueID.Valid {
				issue, issueErr := qtx.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: att.IssueID, WorkspaceID: att.WorkspaceID})
				if issueErr != nil {
					h.Storage.Delete(r.Context(), link)
					slog.Error("failed to load attachment issue", "error", issueErr)
					writeError(w, http.StatusInternalServerError, "upload failed")
					return
				}
				event, eventErr := eventoutbox.Enqueue(r.Context(), qtx, buildIssueAttachmentsChangedEvent(issue, uploaderType, uploaderID))
				if eventErr != nil {
					h.Storage.Delete(r.Context(), link)
					writeError(w, http.StatusInternalServerError, "upload failed")
					return
				}
				persistedEvents = append(persistedEvents, event)
			}
			if att.CommentRevision > 0 && att.CommentID.Valid {
				if comment, loadErr := qtx.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
					ID:          att.CommentID,
					WorkspaceID: att.WorkspaceID,
				}); loadErr == nil {
					event, eventErr := eventoutbox.Enqueue(r.Context(), qtx,
						buildCommentMutationEvent(comment, protocol.EventCommentUpdated, uploaderType, uploaderID, att.CommentRevision))
					if eventErr != nil {
						h.Storage.Delete(r.Context(), link)
						writeError(w, http.StatusInternalServerError, "upload failed")
						return
					}
					persistedEvents = append(persistedEvents, event)
				} else {
					h.Storage.Delete(r.Context(), link)
					writeError(w, http.StatusInternalServerError, "upload failed")
					return
				}
			}
			if err := tx.Commit(r.Context()); err != nil {
				h.Storage.Delete(r.Context(), link)
				writeError(w, http.StatusInternalServerError, "upload failed")
				return
			}
			for _, event := range persistedEvents {
				h.publishEvent(event)
			}
			writeJSON(w, http.StatusOK, h.attachmentToResponse(att.Attachment(), attachmentURLModeFromRequest(r)))
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{
			"id":       "",
			"url":      link,
			"filename": header.Filename,
		})
		return
	}

	// 没有 workspace 上下文（例如头像上传）——直接上传，返回裸地址。
	link, err := h.Storage.Upload(r.Context(), key, data, contentType, header.Filename)
	if err != nil {
		slog.Error("file upload failed", "error", err)
		writeError(w, http.StatusInternalServerError, "upload failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id":       id.String(),
		"url":      link,
		"filename": header.Filename,
	})
}

// ---------------------------------------------------------------------------
// ListAttachments — GET /api/issues/{id}/attachments
// ---------------------------------------------------------------------------

// ListAttachments 列出某个 issue 的全部附件。调用者需能访问该 issue。
func (h *Handler) ListAttachments(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	attachments, err := h.Queries.ListAttachmentsByIssue(r.Context(), db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Error("failed to list attachments", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list attachments")
		return
	}

	mode := attachmentURLModeFromRequest(r)
	resp := make([]AttachmentResponse, len(attachments))
	for i, a := range attachments {
		resp[i] = h.attachmentToResponse(a, mode)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// GetAttachmentByID — GET /api/attachments/{id}
// ---------------------------------------------------------------------------

// GetAttachmentByID 返回单个附件详情（含新鲜的下载 URL 与强制下载 URL）。
// workspace_id 通过请求头解析。单附件接口是"新鲜、可原生加载 URL"的唯一来源，
// 列表与 markdown 场景都汇聚到它这里换 URL。
func (h *Handler) GetAttachmentByID(w http.ResponseWriter, r *http.Request) {
	att, ok := h.loadAttachmentForRequest(w, r)
	if !ok {
		return
	}
	h.writeSignedAttachmentMetadata(w, r, att)
}

// GetAttachmentForDownload resolves the workspace from the attachment, as the
// durable Markdown download URL does. A link can be pasted into a different
// workspace; the current workspace header must not change its target. The
// membership check is the same one used by the protected download endpoint.
func (h *Handler) GetAttachmentForDownload(w http.ResponseWriter, r *http.Request) {
	att, ok := h.loadAttachmentForDownload(w, r)
	if !ok {
		return
	}
	h.writeSignedAttachmentMetadata(w, r, att)
}

func (h *Handler) writeSignedAttachmentMetadata(w http.ResponseWriter, r *http.Request, att db.Attachment) {

	// 无论调用方声明了什么模式都签（除非代理/预签名），走签名：这个接口是"新鲜
	// 的、可原生加载的 URL"的唯一来源。stable 模式调用方（CLI `attachment
	// download`、web 内嵌媒体重签钩子）正是在这里把稳定路径换成分签——尊重它的
	// 能力声明反而会破坏"stable 模式在其他地方安全"所依赖的这个流程。
	resp := h.attachmentToResponse(att, attachmentURLModeSigned)
	// Token 模式客户端用这个带认证的接口，把"受鉴权门槛保护的 API 路径"换成本
	// 地媒体元素能加载的 URL。这里断言与 resolveAttachmentDownloadMode 决定是否
	// 预签名所用的同一个 storage.DownloadPresigner，让模式解析与预签名调用永不
	// 互相矛盾。空的 content disposition 沿用对象存储的 Content-Disposition
	// （媒体内嵌、其余附件），既能内嵌渲染图片，又保留原始下载文件名。
	switch mode := h.resolveAttachmentDownloadMode(att.Url); mode {
	case attachmentDownloadModeCloudFront:
		// CloudFront 模式：attachmentToResponse 已经把 DownloadURL 设成内嵌意图
		// 的签名 URL。下载按钮需要的是"强制下载"的姊妹 URL。
		// response-content-disposition 折进了签名的 Resource
		// （SignedURLWithContentDisposition），客户端去掉或改动它都会让签名
		// 失效。键用解析出来的模式（而不是 h.CFSigner != nil），让显式的
		// proxy/presign 覆盖即使在配了签名器时也生效；nil 守卫防止"显式
		// cloudfront 模式但没配签名器"时 panic——字段留空，客户端回退用
		// download_url，与 attachmentToResponse 用同一种优雅降级。
		if h.CFSigner != nil {
			resp.AttachmentDownloadURL = h.CFSigner.SignedURLWithContentDisposition(
				att.Url,
				storage.AttachmentContentDisposition(att.Filename),
				time.Now().Add(h.attachmentDownloadURLTTL()),
			)
		}
	case attachmentDownloadModePresign:
		if presigner, ok := h.Storage.(storage.DownloadPresigner); ok {
			key := h.Storage.KeyFromURL(att.Url)
			signedURL, err := presigner.PresignGetWithContentDisposition(r.Context(), key, h.attachmentDownloadURLTTL(), "")
			if err != nil {
				slog.Warn("failed to presign inline attachment URL", "id", uuidToString(att.ID), "key", key, "error", err)
			} else {
				resp.DownloadURL = signedURL
			}
			// 下载意图的姊妹 URL：同一个预签名对象，但强制 attachment
			// disposition，让下载按钮存文件而不是预览。它与上面 DownloadURL 的
			// 内嵌签名相互独立；两者可单独存在。
			if dlURL, err := presigner.PresignGetWithContentDisposition(r.Context(), key, h.attachmentDownloadURLTTL(), storage.AttachmentContentDisposition(att.Filename)); err != nil {
				slog.Warn("failed to presign attachment download URL", "id", uuidToString(att.ID), "key", key, "error", err)
			} else {
				resp.AttachmentDownloadURL = dlURL
			}
		}
	case attachmentDownloadModeProxy:
		// Proxy 模式没有可提供的签名存储 URL，所以本响应本来会把带鉴权门槛的
		// API 路径交给调用方——token 模式客户端上的原生下载无法对它做认证，用户
		// 就拿不到文件（MUL-5292）。改为铸造一条"单附件、60 秒"的能力 URL，
		// 让"把受鉴权门槛的路径换成原生加载器能取的地址"这条约定在三种模式下
		// 都成立。
		//
		// 只在这里、绝不在 attachmentToResponse：列表响应被客户端持有远比 TTL
		// 久，嵌进去的能力 URL 等真被用时就过期了。
		resp.DownloadURL = attachmentCapabilityPath(resp.ID, time.Now())
		// 下载意图的姊妹能力 URL（dl=1）：赎回路由把它变成 Content-Disposition:
		// attachment，供下载按钮使用。
		resp.AttachmentDownloadURL = attachmentDownloadCapabilityPath(resp.ID, time.Now())
	}

	writeJSON(w, http.StatusOK, resp)
}

// loadAttachmentForRequest 按 URL 里的 attachment id + 请求头里的 workspace_id
// 装载附件行。任一路径失败（缺参数/UUID 非法/查不到）都会写错误并返回 false。
func (h *Handler) loadAttachmentForRequest(w http.ResponseWriter, r *http.Request) (db.Attachment, bool) {
	attachmentID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Attachment{}, false
	}

	attUUID, ok := parseUUIDOrBadRequest(w, attachmentID, "attachment id")
	if !ok {
		return db.Attachment{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Attachment{}, false
	}

	att, err := h.Queries.GetAttachment(r.Context(), db.GetAttachmentParams{
		ID:          attUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return db.Attachment{}, false
	}

	return att, true
}

// loadAttachmentForDownload 是 /api/attachments/{id}/download 专用的"按行
// workspace 自解析"变体：只凭 id 找附件，再强制要求认证用户是该附件所在
// workspace 的成员。
//
// 为什么单独一条代码路径：原生浏览器在 /api/attachments/{id}/download 上的
// <img>/<video> 资源加载带不了 loadAttachmentForRequest 依赖的 X-Workspace-Slug
// / X-Workspace-ID 头。把 workspace 塞进 URL（?workspace_slug=...）机械上可行，
// 却会给每条持久化的评论 markdown 链接烤进一个非必要标识符——没必要，因为
// 附件行已经记录了它的 workspace。这个辅助函数让 URL 保持干净
// （/api/attachments/{id}/download），把"附件 id + cookie/Bearer 认证"当作
// 足够。
//
// 成员校验与 ServeLocalUpload 一样用"拒绝即 404"的形态，避免该路由充当"附件
// ID 属于别的 workspace"的 IDOR 探针。成员缓存快路径与 canReadWorkspaceUpload
// 完全一致。
func (h *Handler) loadAttachmentForDownload(w http.ResponseWriter, r *http.Request) (db.Attachment, bool) {
	attachmentID := chi.URLParam(r, "id")
	attUUID, ok := parseUUIDOrBadRequest(w, attachmentID, "attachment id")
	if !ok {
		return db.Attachment{}, false
	}
	att, err := h.Queries.GetAttachmentByIDOnly(r.Context(), attUUID)
	if err != nil {
		// 404（而非 403/401），让"非成员"与"不存在"对外部调用者看起来一致。
		// 与 ServeLocalUpload 的 canReadWorkspaceUpload 拒绝路径同形。
		writeError(w, http.StatusNotFound, "attachment not found")
		return db.Attachment{}, false
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Attachment{}, false
	}

	workspaceID := uuidToString(att.WorkspaceID)
	if workspaceID == "" {
		writeError(w, http.StatusNotFound, "attachment not found")
		return db.Attachment{}, false
	}
	if h.MembershipCache.Get(r.Context(), userID, workspaceID) {
		return att, true
	}
	if _, err := h.getWorkspaceMember(r.Context(), userID, workspaceID); err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return db.Attachment{}, false
	}
	h.MembershipCache.Set(r.Context(), userID, workspaceID)
	return att, true
}

// ---------------------------------------------------------------------------
// DownloadAttachment — GET /api/attachments/{id}/download
// ---------------------------------------------------------------------------
//
// workspace 上下文从附件行本身推导，而不是从 X-Workspace-Slug / X-Workspace-ID
// 头。这正让 markdown 里的 `<img src="/api/attachments/{id}/download">` 能当
// 原生浏览器资源加载：浏览器给 <img>/<video> 请求带不了那些头，所以经由附件行
// 解析是让 URL 跨刷新保持稳定的唯一办法（旧设计把 30 分钟签名的 /uploads URL
// 写进 markdown 正文——签名一过期那个 URL 就废了）。
//
// 成员校验在 loadAttachmentForDownload 内强制，用 404 拒绝形态，让该路由不会
// 向非成员 IDOR 泄漏附件 ID。

// DownloadAttachment 把附件以"下载"意图交给客户端：按存储模式分别走
// CloudFront 重定向、S3 预签名重定向或经 API 代理流式传输，响应均强制
// Content-Disposition: attachment 并带预览安全头。
func (h *Handler) DownloadAttachment(w http.ResponseWriter, r *http.Request) {
	att, ok := h.loadAttachmentForDownload(w, r)
	if !ok {
		return
	}
	if h.Storage == nil {
		writeFeatureDisabled(w, "storage_not_configured", "storage not configured")
		return
	}

	key := h.Storage.KeyFromURL(att.Url)
	switch h.resolveAttachmentDownloadMode(att.Url) {
	case attachmentDownloadModeCloudFront:
		if h.CFSigner == nil {
			writeError(w, http.StatusInternalServerError, "cloudfront attachment downloads are not configured")
			return
		}
		h.setAttachmentPreviewSecurityHeaders(w)
		http.Redirect(
			w,
			r,
			h.CFSigner.SignedURLWithContentDisposition(
				att.Url,
				storage.AttachmentContentDisposition(att.Filename),
				time.Now().Add(h.attachmentDownloadURLTTL()),
			),
			http.StatusFound,
		)
	case attachmentDownloadModePresign:
		presigner, ok := h.Storage.(storage.DownloadPresigner)
		if !ok {
			writeError(w, http.StatusInternalServerError, "attachment storage does not support presigned downloads")
			return
		}
		signedURL, err := presigner.PresignGetWithContentDisposition(
			r.Context(),
			key,
			h.attachmentDownloadURLTTL(),
			storage.AttachmentContentDisposition(att.Filename),
		)
		if err != nil {
			slog.Error("failed to presign attachment download", "id", uuidToString(att.ID), "key", key, "error", err)
			writeError(w, http.StatusBadGateway, "failed to create download URL")
			return
		}
		h.setAttachmentPreviewSecurityHeaders(w)
		http.Redirect(w, r, signedURL, http.StatusFound)
	case attachmentDownloadModeProxy:
		h.proxyAttachmentDownload(w, r, att, key, false)
	default:
		writeError(w, http.StatusInternalServerError, "invalid attachment download mode")
	}
}

// resolveAttachmentDownloadMode 对某个对象 URL 决定实际的下载模式。优先看
// 显式配置；配置为 auto 或为空时按签名器是否存在、URL 是否需代理、存储是否
// 支持预签名依次推导。
func (h *Handler) resolveAttachmentDownloadMode(rawURL string) attachmentDownloadMode {
	switch h.attachmentDownloadMode() {
	case attachmentDownloadModeCloudFront:
		return attachmentDownloadModeCloudFront
	case attachmentDownloadModePresign:
		return attachmentDownloadModePresign
	case attachmentDownloadModeProxy:
		return attachmentDownloadModeProxy
	}
	if h.CFSigner != nil {
		return attachmentDownloadModeCloudFront
	}
	if shouldProxyAttachmentURL(rawURL) {
		return attachmentDownloadModeProxy
	}
	if _, ok := h.Storage.(storage.DownloadPresigner); ok {
		return attachmentDownloadModePresign
	}
	return attachmentDownloadModeProxy
}

// shouldProxyAttachmentURL 判断对象 URL 是否指向"必须经 API 代理"的存储——
// 即本地/内网主机名或 IP：公网可达对象不需要代理，本地磁盘、localhost、
// .local/.internal 等内网后缀、保留/私网地址则必须走代理才能让浏览器访问。
func shouldProxyAttachmentURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return true
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u.Hostname()), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if !strings.Contains(host, ".") {
		return true
	}
	switch {
	case strings.HasSuffix(host, ".local"),
		strings.HasSuffix(host, ".localdomain"),
		strings.HasSuffix(host, ".internal"),
		strings.HasSuffix(host, ".lan"),
		strings.HasSuffix(host, ".home"),
		strings.HasSuffix(host, ".docker"):
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.IsLoopback() ||
			addr.IsPrivate() ||
			addr.IsLinkLocalUnicast() ||
			addr.IsLinkLocalMulticast() ||
			addr.IsUnspecified()
	}
	return false
}

// ServeLocalUpload 从公开的 /uploads/* 路由提供本地磁盘对象。它带与带认证的
// 下载接口相同的预览安全头，让自托管部署——同源或前后端分源——可以直接从静态
// 路由内嵌渲染图片、iframe 预览文档（PDF/HTML）。没有这些头，全局的
// "frame-ancestors 'none'" 策略会挡住这些预览。见 MUL-3821 / #4477。
func (h *Handler) ServeLocalUpload(w http.ResponseWriter, r *http.Request) {
	local, ok := h.Storage.(*storage.LocalStorage)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.setAttachmentPreviewSecurityHeaders(w)
	key := strings.TrimPrefix(r.URL.Path, "/uploads/")
	if strings.HasPrefix(path.Clean(key), "private/") {
		http.NotFound(w, r)
		return
	}
	local.ServeFile(w, r, key)
}

// proxyAttachmentDownload 经 API 把附件流式传给客户端，而不是重定向到签名存储
// URL（用于本地磁盘 / 私有主机后端，见 resolveAttachmentDownloadMode）。
//
// 支持 HTTP Range 请求，中断的下载可以用 `Range: bytes=<resume>-` 续传而不是
// 从头开始（RAS-29）。两条路径：
//
//   - 可回卷后端（本地磁盘返回 *os.File）：交给 http.ServeContent——它把 Range /
//     If-Range / 206 / Content-Range / 416 全部正确实现。
//   - 不可回卷后端（S3/MinIO 流式 body）：声明 Accept-Ranges，手工实现单区间
//     请求（serveProxyRange）。此路径不实现多区间；按 RFC 7233 多区间应被忽略
//     并以 200 返回完整内容，与可回卷路径的成功结果一致，而不是用 416 失败。
func (h *Handler) proxyAttachmentDownload(w http.ResponseWriter, r *http.Request, att db.Attachment, key string, forceAttachment bool) {
	reader, err := h.Storage.GetReader(r.Context(), key)
	if err != nil {
		slog.Error("failed to open attachment for download", "id", uuidToString(att.ID), "key", key, "error", err)
		writeError(w, http.StatusNotFound, "attachment object not found")
		return
	}
	defer reader.Close()

	if att.ContentType != "" {
		w.Header().Set("Content-Type", att.ContentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	disposition := storage.ContentDisposition(att.ContentType, att.Filename)
	if forceAttachment {
		// 下载意图的能力 URL（dl=1）：覆盖媒体感知的内嵌 disposition，让浏览器
		// 存文件而不是预览。
		disposition = storage.AttachmentContentDisposition(att.Filename)
	}
	w.Header().Set("Content-Disposition", disposition)
	// no-store 早于 Range 支持存在，保留它。Range/206 语义与缓存无关——客户端
	// 靠 Content-Range 续传，不靠缓存。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.setAttachmentPreviewSecurityHeaders(w)

	// 可回卷后端拿到标准库完整的 Range 处理。零值 modTime 禁用 Last-Modified /
	// If-Modified-Since（我们保持 no-store）；ServeContent 仍支持 Range 并自行
	// 设置 Accept-Ranges / Content-Length / Content-Range / 状态码，同时尊重
	// 我们上面设的头。
	if seeker, ok := reader.(io.ReadSeeker); ok {
		http.ServeContent(w, r, att.Filename, time.Time{}, seeker)
		return
	}

	// 不可回卷后端：单区间回退。
	h.serveProxyRange(w, r, att, reader)
}

// serveProxyRange 从只进不回的 reader 流式输出（可能部分的）附件 body。它始终
// 声明 Accept-Ranges: bytes；当请求带一条可满足的单区间 `Range` 头时，回 206 +
// Content-Range 只输出那段字节区间。没有 Range 头（或总大小未知）时按 200 流式
// 输出整个 body，保持 RAS-29 之前的行为。不可满足的区间按 RFC 7233 回 416 +
// `Content-Range: bytes */<total>`。
func (h *Handler) serveProxyRange(w http.ResponseWriter, r *http.Request, att db.Attachment, reader io.Reader) {
	total := att.SizeBytes
	w.Header().Set("Accept-Ranges", "bytes")

	rangeHeader := strings.TrimSpace(r.Header.Get("Range"))

	// 决定怎么响应。没有 Range、总大小未知（total < 0）、或 parseSingleByteRange
	// 把 Range 判为不支持时，按 200 输出完整 body——多区间、或对空对象的合法区间
	// 都属于这类（见 rangeUnsupported）。合法且落在界内的单区间输出 206；合法但
	// 越界、对象非空时是唯一输出 416 的情形。
	serveFull := rangeHeader == "" || total < 0
	var start, length int64
	if !serveFull {
		var outcome rangeParseOutcome
		start, length, outcome = parseSingleByteRange(rangeHeader, total)
		switch outcome {
		case rangeUnsupported:
			// 不支持的 Range 形态（多区间）。RFC 7233 要求"不支持的 Range 被
			// 忽略、输出完整表示（200）"，而不是用 416 拒绝。这也让不可回卷路径
			// 与可回卷的 http.ServeContent 路径不产生分歧——后者对多区间回成功
			// 的 206 multipart：两条后端现在都返回完整且标注正确的数据，而不是
			// 一条把可续传的下载变成硬 416。
			serveFull = true
		case rangeUnsatisfiable:
			// 合法 bytes 区间但与内容不重叠。RFC 7233 §4.4 要求在 416 响应的
			// Content-Range 里给出总大小。
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
			writeError(w, http.StatusRequestedRangeNotSatisfiable, "requested range not satisfiable")
			return
		}
	}

	if serveFull {
		if total >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
		}
		if _, err := io.Copy(w, reader); err != nil {
			slog.Error("failed to stream attachment download", "id", uuidToString(att.ID), "error", err)
		}
		return
	}

	// 可满足的单区间 → 206。只进不回的 reader：先丢弃 start 之前的字节，再复制
	// 请求的区间。对大的起始偏移浪费但对；可回卷后端到不了这里（它们走上面的
	// ServeContent 路径）。
	if start > 0 {
		if _, err := io.CopyN(io.Discard, reader, start); err != nil {
			// 跳过失败发生在任何响应头写出之前。这里必须显式写错误状态：光
			// return 会让 net/http 发出默认的 200 OK + 空 body，续传客户端会读成
			// "Range 被忽略，这里是完整文件"——把一次瞬时存储读错误静默变成一次
			// 损坏/空下载，恰好毁掉本路径实现的续传功能。（下面的 WriteHeader 之
			// 后复制失败就只能记日志了：206 状态已在线上，无法再改。）
			slog.Error("failed to skip to range start for attachment", "id", uuidToString(att.ID), "error", err)
			writeError(w, http.StatusBadGateway, "failed to read attachment range")
			return
		}
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, total))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(http.StatusPartialContent)
	if _, err := io.CopyN(w, reader, length); err != nil {
		slog.Error("failed to stream attachment range", "id", uuidToString(att.ID), "error", err)
	}
}

// rangeParseOutcome 为 serveProxyRange 在"只进不回（不可回卷）代理路径"上应
// 如何响应一条 `Range` 头做分类。它保证 RFC 7233 要求的三种不同 HTTP 结局不会
// 被压成一个 bool——正是当年把多区间请求在这条路径上以 416 失败、却在可回卷
// 路径上成功（206 multipart）的根因。
type rangeParseOutcome int

const (
	// rangeSatisfiable：可以按 206 + Content-Range 服务的单字节区间。
	rangeSatisfiable rangeParseOutcome = iota
	// rangeUnsatisfiable：格式合法但按我们支持的范围无法服务的请求——不与内容
	// 重叠的 bytes 区间（起点在/越过 EOF、倒序），或畸形/未知单位的 Range。
	// 标准库 http.ServeContent 对这些一律回 416，所以这里也回 416 让两条后端
	// 对齐；调用方再补 `Content-Range: bytes */<total>`（RFC 7233 §4.4）。
	rangeUnsatisfiable
	// rangeUnsupported：格式合法、但本路径选择"忽略 Range、按 200 输出完整
	// body"（按 RFC 7233）。两种情况：
	//   - 多区间（`bytes=a-b,c-d`）：可回卷路径把它当 206 multipart 服务，所以
	//     这里回完整 200 让两条路径都返回完整数据，而不是一条硬 416 失败。
	//   - 对空对象（size == 0）的合法 bytes 区间：没有任何字节能满足任何区间，
	//     而标准库 http.ServeContent 忽略 Range 返回空 200 而非 416。把它在这
	//     里归为不支持（→ 空 200）让不可回卷路径与可回卷路径对齐；回 416 会让
	//     同一次请求在可回卷后端成功、在 S3/MinIO 流上失败。真正畸形/未知单位
	//     对空对象的 Range 仍归 rangeUnsatisfiable（416），与 ServeContent 一致。
	rangeUnsupported
)

// parseSingleByteRange 针对已知内容大小解析一条单区间 HTTP `Range` 头，返回
// 绝对起始偏移、字节长度，以及告诉调用方该发哪个状态码的结果
// （见 rangeParseOutcome）。只有结果是 rangeSatisfiable 时 start/length 才有
// 意义。
//
// 支持的形式（RFC 7233）：`bytes=start-end`、`bytes=start-`（到 EOF）、
// `bytes=-suffix`（末尾后缀字节）。越界的 end 被夹到最后一个字节；非空对象上
// 起点在/越过 EOF 不可满足（416）。多区间，以及对空对象（size == 0）的任何
// 合法区间，是"合法但本实现不支持"的形式，产出 rangeUnsupported（忽略 → 完整
// body），与可回卷的 http.ServeContent 路径对这些形态的处理一致。
func parseSingleByteRange(header string, size int64) (start, length int64, outcome rangeParseOutcome) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		// 未知/缺失单位（例如 "items=0-10"、"0-100"）。标准库 http.ServeContent
		// 用 416 拒绝；这里照做。
		return 0, 0, rangeUnsatisfiable
	}
	spec := strings.TrimSpace(header[len(prefix):])
	if spec == "" {
		return 0, 0, rangeUnsatisfiable
	}
	// 多区间是合法 HTTP 形态，但本只进不回路径不实现它；忽略并按 200 输出完整
	// body，而不是用 416 失败。
	if strings.Contains(spec, ",") {
		return 0, 0, rangeUnsupported
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, rangeUnsatisfiable
	}
	startStr := strings.TrimSpace(spec[:dash])
	endStr := strings.TrimSpace(spec[dash+1:])

	if startStr == "" {
		// 后缀形式：bytes=-N → 最后 N 个字节。
		if endStr == "" {
			return 0, 0, rangeUnsatisfiable
		}
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, rangeUnsatisfiable
		}
		if size == 0 {
			// 对空对象的合法后缀：没有字节能满足它。忽略 Range 回空 200
			// （rangeUnsupported）与可回卷路径保持一致，而不是分叉成 416。
			return 0, 0, rangeUnsupported
		}
		if n > size {
			n = size
		}
		return size - n, n, rangeSatisfiable
	}

	s, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || s < 0 {
		return 0, 0, rangeUnsatisfiable
	}
	if s >= size {
		// 起点在或越过 EOF，没有任何字节重叠。对空对象（size == 0）任何起点都在
		// EOF；标准库 http.ServeContent 在那里忽略 Range 返回空 200（起点越界后
		// 它不再校验 end），所以归为 rangeUnsupported（→ 空 200）保持一致。对
		// 非空对象，越过 EOF 的起点是真正的 416。
		if size == 0 {
			return 0, 0, rangeUnsupported
		}
		return 0, 0, rangeUnsatisfiable
	}
	if endStr == "" {
		return s, size - s, rangeSatisfiable
	}
	e, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || e < s {
		return 0, 0, rangeUnsatisfiable
	}
	if e >= size {
		e = size - 1
	}
	return s, e - s + 1, rangeSatisfiable
}

// setAttachmentPreviewSecurityHeaders 给附件预览响应加安全头。预览响应可能被
// web 应用在同源部署或分源 app/api 自托管部署下加载，这里只允许 API 源自身加
// 上配置的前端/CORS 源做 frame 祖先。
func (h *Handler) setAttachmentPreviewSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", attachmentPreviewCSPHeader(h.cfg.AttachmentFrameAncestors))
}

// attachmentPreviewCSPHeader 拼出附件预览用的 CSP 头：default-src 一律 none，
// 仅放开内嵌资源必需的最小集合，frame-ancestors 取 self + 配置的源去重结果。
func attachmentPreviewCSPHeader(frameAncestors []string) string {
	ancestors := []string{"'self'"}
	seen := map[string]struct{}{"'self'": {}}
	for _, raw := range frameAncestors {
		source, ok := normalizeFrameAncestorSource(raw)
		if !ok {
			continue
		}
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		ancestors = append(ancestors, source)
	}
	return "default-src 'none'; " +
		"img-src 'self' data:; " +
		"media-src 'self'; " +
		"frame-ancestors " + strings.Join(ancestors, " ") + "; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'none'"
}

// normalizeFrameAncestorSource 把配置的 frame 祖先源规范成 "scheme://host"
// 形态；空串、通配 *、非法 URL 或非 http(s) 协议一律判不合法。
func normalizeFrameAncestorSource(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	return scheme + "://" + strings.ToLower(u.Host), true
}

// ---------------------------------------------------------------------------
// GetAttachmentContent — GET /api/attachments/{id}/content
//
// 把"可文本预览"附件的原始字节流式传给客户端。存在是为了 (a) 绕开 CloudFront
// 的 CORS（未配置）和 (b) 绕开 Content-Disposition: attachment——Chromium 对
// iframe 加载文档时会尊重它。媒体类型（image/video/audio/pdf）刻意走 download_
// url：元数据 download_url 保留 CloudFront/S3 的媒体预览行为；显式的 /download
// 路由把重定向签名成附件下载，proxy 模式则用与存储上传相同的媒体类型策略流。
//
// 硬上限：2 MB。更大的文件回 413；不在文本白名单里的类型回 415。
// ---------------------------------------------------------------------------

// GetAttachmentContent 返回文本类附件的纯文本内容（用于内联预览）。类型不在
// 白名单回 415；超过 2MB 回 413。始终以 text/plain 输出，防浏览器把恶意 HTML
// 当文档重新解释。
func (h *Handler) GetAttachmentContent(w http.ResponseWriter, r *http.Request) {
	att, ok := h.loadAttachmentForRequest(w, r)
	if !ok {
		return
	}
	attachmentID := uuidToString(att.ID)

	if !isTextPreviewable(att.ContentType, att.Filename) {
		writeError(w, http.StatusUnsupportedMediaType, "preview not supported for this file type")
		return
	}

	if h.Storage == nil {
		writeFeatureDisabled(w, "storage_not_configured", "storage not configured")
		return
	}
	key := h.Storage.KeyFromURL(att.Url)
	reader, err := h.Storage.GetReader(r.Context(), key)
	if err != nil {
		slog.Error("failed to open attachment for preview", "id", attachmentID, "key", key, "error", err)
		writeError(w, http.StatusNotFound, "attachment object not found")
		return
	}
	defer reader.Close()

	// LimitReader 读到 maxPreviewTextSize+1，这样能靠返回长度区分"恰好卡在
	// 上限"与"超出上限"。
	body, err := io.ReadAll(io.LimitReader(reader, maxPreviewTextSize+1))
	if err != nil {
		slog.Error("failed to read attachment body for preview", "id", attachmentID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to read attachment body")
		return
	}
	if len(body) > maxPreviewTextSize {
		writeError(w, http.StatusRequestEntityTooLarge, "file too large for inline preview")
		return
	}

	// 永远以 text/plain 应答，防止恶意的 HTML 载荷被浏览器当文档重新解释。原始
	// MIME 通过 X-Original-Content-Type 交给客户端分派器。
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Original-Content-Type", att.ContentType)
	// No-store：workspace 成员关系 / 附件 ACL 可能随请求变化（成员被移走、附件
	// 被删）。缓存下来的 body 会在撤销窗口之后仍可读。这里的冗余请求没问题——
	// body 上限 2 MB，且该接口只在用户显式打开预览时才被命中。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.setAttachmentPreviewSecurityHeaders(w)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	if _, err := w.Write(body); err != nil {
		slog.Error("failed to write attachment preview body", "id", attachmentID, "error", err)
	}
}

// isTextPreviewable 是文本预览代理的白名单。
//
// 重要——与客户端镜像保持同步：packages/views/editor/utils/preview.ts
// （TEXT_EXTENSIONS / TEXT_CONTENT_TYPES / TEXT_BASENAMES + extensionToLanguage）。
// 若这里放行而客户端没映射，用户看到的是没高亮的裸文本；若客户端映射了而这里
// 拒绝，用户看到 415 回退。
//
// TODO(follow-up)：把这列表抽成 JSON 单一真源并生成 TS 侧，镜像 reserved-slugs
// 的做法（见 server/internal/handler/reserved_slugs.json +
// scripts/generate-reserved-slugs.mjs）。漂移影响不大（最坏：Eye 按钮可见但
// 代理 415，弹窗显示不支持的兜底——仍可用，只是困惑），所以 v1 用手工同步。
//
// content_type 与扩展名都查，是因为 http.DetectContentType 常把 Markdown/源码
// 判成 "text/plain"，只查 content-type 会把它们全 415 掉。
func isTextPreviewable(contentType, filename string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	// 去掉参数（例如 "text/plain; charset=utf-8"）
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = strings.TrimSpace(ct[:idx])
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json",
		"application/javascript",
		"application/xml",
		"application/x-yaml",
		"application/yaml",
		"application/toml",
		"application/x-sh",
		"application/x-httpd-php":
		return true
	}

	ext := strings.ToLower(path.Ext(filename))
	switch ext {
	case ".md", ".markdown",
		".txt", ".log",
		".csv", ".tsv",
		".html", ".htm",
		".json", ".xml",
		".yml", ".yaml", ".toml", ".ini", ".conf",
		".sh", ".bash", ".zsh",
		".py", ".rb", ".go", ".rs",
		".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".css", ".scss", ".sass", ".less",
		".sql",
		".java", ".kt", ".swift",
		".c", ".cc", ".cpp", ".h", ".hpp",
		".cs", ".php", ".lua", ".vim",
		".dockerfile", ".makefile", ".gitignore":
		return true
	}
	// 没有扩展名但与知名构建文件匹配的文件名。
	base := strings.ToLower(path.Base(filename))
	switch base {
	case "dockerfile", "makefile", ".env":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// DeleteAttachment — DELETE /api/attachments/{id}
// ---------------------------------------------------------------------------

// DeleteAttachment 删除一个附件：库行 + 存储对象 + 附带事件。权限：只有上传者
// （且是 member 类型的）或 workspace 的 admin/owner 能删。SourceContextID 命中
// 的"捕获上下文附件"是不可变历史副本，只能随其目标 issue/workspace/被弃上下文
// 一起删，这里一律回 404。
func (h *Handler) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	attachmentID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	attUUID, ok := parseUUIDOrBadRequest(w, attachmentID, "attachment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	att, err := h.Queries.GetAttachment(r.Context(), db.GetAttachmentParams{
		ID:          attUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	// 捕获上下文的附件是不可变的历史副本。它们只能随目标 issue、workspace 或
	// 被弃上下文一起删除。
	if att.SourceContextID.Valid {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}

	// 只有上传者（或 workspace 管理员）能删。
	uploaderID := uuidToString(att.UploaderID)
	isUploader := att.UploaderType == "member" && uploaderID == userID
	member, hasMember := ctxMember(r.Context())
	isAdmin := hasMember && (member.Role == "admin" || member.Role == "owner")

	if !isUploader && !isAdmin {
		writeError(w, http.StatusForbidden, "not authorized to delete this attachment")
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	var deleted db.DeleteAttachmentRow
	var persistedEvents []events.Event
	err = h.withAttachmentOwnerLock(r.Context(), att, func(qtx *db.Queries) error {
		// 关联可能在等锁期间变化；取持锁后的所有者用于事件与版本更新。
		var loadErr error
		att, loadErr = qtx.GetAttachment(r.Context(), db.GetAttachmentParams{ID: att.ID, WorkspaceID: att.WorkspaceID})
		if loadErr != nil {
			return loadErr
		}
		var deleteErr error
		deleted, deleteErr = qtx.DeleteAttachment(r.Context(), db.DeleteAttachmentParams{ID: att.ID, WorkspaceID: att.WorkspaceID})
		if deleteErr != nil {
			return deleteErr
		}
		if deleted.Changed && att.IssueID.Valid {
			issue, err := qtx.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: att.IssueID, WorkspaceID: att.WorkspaceID})
			if err != nil {
				return err
			}
			event, err := eventoutbox.Enqueue(r.Context(), qtx, buildIssueAttachmentsChangedEvent(issue, actorType, actorID))
			if err != nil {
				return err
			}
			persistedEvents = append(persistedEvents, event)
		}
		if deleted.Changed && att.CommentID.Valid {
			comment, err := qtx.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{ID: att.CommentID, WorkspaceID: att.WorkspaceID})
			if err != nil {
				return err
			}
			event, err := eventoutbox.Enqueue(r.Context(), qtx, buildCommentMutationEvent(comment, protocol.EventCommentUpdated, actorType, actorID, deleted.CommentRevision))
			if err != nil {
				return err
			}
			persistedEvents = append(persistedEvents, event)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete attachment")
		return
	}
	for _, event := range persistedEvents {
		h.publishEvent(event)
	}

	h.deleteS3Object(r.Context(), att.Url)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 附件关联
// ---------------------------------------------------------------------------

// linkAttachmentsByIssueIDs 把未绑定的附件挂到某个 issue 上。调用方若此前没让
// issue 版本前进，可以请这个"可见变更"把版本恰好推进一次。
func (h *Handler) linkAttachmentsByIssueIDs(ctx context.Context, issueID, workspaceID pgtype.UUID, ids []pgtype.UUID, bumpRevision bool) (db.LinkAttachmentsToIssueRow, error) {
	return h.Queries.LinkAttachmentsToIssue(ctx, db.LinkAttachmentsToIssueParams{
		IssueID:       issueID,
		WorkspaceID:   workspaceID,
		AttachmentIds: ids,
		BumpRevision:  bumpRevision,
	})
}

// attachmentOwnerLockAttempts bounds the re-read below. An attachment gains an
// owner once, when the issue or comment it was uploaded for links it, so one
// retry is enough in practice; the bound is what keeps a pathological
// interleaving from looping.
const attachmentOwnerLockAttempts = 3

// withAttachmentOwnerLock runs write in a transaction that locks the
// attachment's owners first — the issue, then the comment when it has one —
// which is the issue -> comment -> child order LockIssueForDelete,
// UpdateComment, LockLiveComment and CreateComment all take. Locking the
// attachment row and then touching its issue is the opposite order, and closes
// a deadlock cycle with issue teardown: teardown holds the issue and reaches
// the same attachment through the issue_id cascade.
//
// The row is re-read under those locks, so a link that committed while this
// waited is seen before the write; an attachment that gained an owner is
// retried with that owner locked. Returns pgx.ErrNoRows when the attachment,
// or the comment owning it, is gone.
func (h *Handler) withAttachmentOwnerLock(ctx context.Context, att db.Attachment, write func(*db.Queries) error) error {
	for attempt := 0; ; attempt++ {
		fresh, err := h.attachmentOwnerLockAttempt(ctx, att, write)
		if !errors.Is(err, errAttachmentOwnerChanged) {
			return err
		}
		if attempt+1 >= attachmentOwnerLockAttempts {
			return errors.New("attachment owner kept changing under the lock")
		}
		att = fresh
	}
}

// errAttachmentOwnerChanged reports that the attachment gained or changed an
// owner while the transaction was waiting, so the locks it took are the wrong
// ones and the attempt must be retried against the new owner.
var errAttachmentOwnerChanged = errors.New("attachment owner changed")

func (h *Handler) attachmentOwnerLockAttempt(ctx context.Context, att db.Attachment, write func(*db.Queries) error) (db.Attachment, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.Attachment{}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if att.IssueID.Valid {
		// A missing issue is not an error here: its cascade took the attachment
		// with it, which the re-read below reports as pgx.ErrNoRows.
		if _, err := qtx.LockIssueForAttachmentWrite(ctx, db.LockIssueForAttachmentWriteParams{
			ID:          att.IssueID,
			WorkspaceID: att.WorkspaceID,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return db.Attachment{}, err
		}
	}
	if att.CommentID.Valid {
		if _, err := qtx.LockLiveComment(ctx, db.LockLiveCommentParams{
			ID:          att.CommentID,
			WorkspaceID: att.WorkspaceID,
		}); err != nil {
			return db.Attachment{}, err
		}
	}
	var fresh db.Attachment
	if att.IssueID.Valid || att.CommentID.Valid {
		fresh, err = qtx.GetAttachment(ctx, db.GetAttachmentParams{ID: att.ID, WorkspaceID: att.WorkspaceID})
	} else {
		// Nothing to lock above: take the row itself so it cannot gain an owner
		// between this read and the write.
		fresh, err = qtx.LockAttachmentRow(ctx, db.LockAttachmentRowParams{ID: att.ID, WorkspaceID: att.WorkspaceID})
	}
	if err != nil {
		return db.Attachment{}, err
	}
	if fresh.IssueID != att.IssueID || fresh.CommentID != att.CommentID {
		return fresh, errAttachmentOwnerChanged
	}
	if err := write(qtx); err != nil {
		return db.Attachment{}, err
	}
	return fresh, tx.Commit(ctx)
}

// deleteS3Object 按 CDN URL 从存储里删除单个文件。
func (h *Handler) deleteS3Object(ctx context.Context, url string) {
	if h.Storage == nil || url == "" {
		return
	}
	h.Storage.Delete(ctx, h.Storage.KeyFromURL(url))
}

// deleteS3Objects 按 CDN URL 列表从存储里删除多个文件。
func (h *Handler) deleteS3Objects(ctx context.Context, urls []string) {
	if h.Storage == nil || len(urls) == 0 {
		return
	}
	keys := make([]string, len(urls))
	for i, u := range urls {
		keys[i] = h.Storage.KeyFromURL(u)
	}
	h.Storage.DeleteKeys(ctx, keys)
}
