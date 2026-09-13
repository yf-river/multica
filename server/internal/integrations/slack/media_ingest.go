// media_ingest.go：Slack 媒体摄取——消息被接受并落库后，把网络与存储 IO 挪出
// 连接器 ACK 路径，异步完成：下载文件、存入对象存储、把 MediaRef 回填到消息。
//
// HasMedia 只检查翻译后的入站负载、不做 IO；ResolveMedia 逐个下载文件，上传前先
// 落一条持久的 intent，这样任何失败或崩溃都留够了状态，让 reconciler 能清掉没有
// 绑定对象的残留。
//
// Slack 私密文件 URL 需要拿安装的 bot 令牌当 bearer 凭证去取。下载客户端因此只
// 接受 Slack 自家域名上的 HTTPS URL，并且每个跳转目标都做同样的域名校验。
package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Slack 单条消息最多 10 个文件；字节上限让「整读进内存再传」的路径稳稳落在
// Router 的 45 秒媒体时限和内存预算之内。
const (
	maxFilesPerMessage   = 10
	maxInboundFileBytes  = 20 << 20
	fileFetchTimeout     = 30 * time.Second
	maxDownloadRedirects = 3
)

// mediaStorage 定义摄取需要的对象存储操作。ObjectURL 必须【不碰 IO】就能推出最终
// URL——resolver 要先把这个 URL 写进 intent 台账，再去上传对象。
type mediaStorage interface {
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	ObjectURL(key string) string
}

type slackMediaResolver struct {
	decrypt Decrypter
	storage mediaStorage
	ledger  engine.MediaIntentLedger
	fetch   *http.Client
	logger  *slog.Logger
	// allowedHost 决定哪些主机能接收 bot 令牌。生产恒为 isSlackFileHost；测试
	// 换成本地 httptest 的主机。
	allowedHost func(host string) bool
}

var _ engine.MediaResolver = (*slackMediaResolver)(nil)

// NewMediaResolver 构造 Slack 媒体 resolver。storage 与 intent 台账都必须给；
// 缺了任一个，ResolveMedia 只记日志并原样返回消息（媒体静默跳过）。
func NewMediaResolver(decrypt Decrypter, storage mediaStorage, ledger engine.MediaIntentLedger, logger *slog.Logger) engine.MediaResolver {
	if logger == nil {
		logger = slog.Default()
	}
	r := &slackMediaResolver{
		decrypt:     decrypt,
		storage:     storage,
		ledger:      ledger,
		logger:      logger,
		allowedHost: isSlackFileHost,
	}
	r.fetch = &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxDownloadRedirects {
				return errors.New("too many redirects")
			}
			// 每次跳转在 net/http 真正跟上之前先做校验：无论 net/http 怎么传播
			// Authorization 头，这层域名检查都能拦住「跳出 Slack 域名」的请求。
			if err := r.validateDownloadURL(req.URL); err != nil {
				return err
			}
			// net/http 只会把 Authorization 转发给同主机或其子域名；跳到
			// files-origin.slack.com 这类兄弟域名时请求就是裸奔的，Slack 会回它
			// 的 HTML 登录页——跟「缺 scope」表现一模一样，文件会被误丢。上面的
			// 检查已证明目标归 Slack 所有，所以这里把令牌补回去。
			if auth := via[0].Header.Get("Authorization"); auth != "" {
				req.Header.Set("Authorization", auth)
			}
			return nil
		},
	}
	return r
}

// isSlackFileHost 判断 host 是否 Slack 自家域名。url_private 都挂在
// files.slack.com 上（企业版也仍是 *.slack.com 子域名）。
func isSlackFileHost(host string) bool {
	host = strings.ToLower(host)
	return host == "slack.com" || strings.HasSuffix(host, ".slack.com")
}

// isFetchableSlackFileURL 是 validateDownloadURL 的纯函数形态——固定用生产主机
// 谓词而不是 resolver 的测试接缝。入站翻译用它把 resolver 永远取不到的文件在进
// 信封前就滤掉。
func isFetchableSlackFileURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && isSlackFileHost(parsed.Hostname())
}

// HasMedia 只对翻译后的入站负载做纯解码判断。它在连接器 ACK 路径上同步执行，
// 因此不能做任何 IO。
func (r *slackMediaResolver) HasMedia(msg channel.InboundMessage) bool {
	raw, err := decodeSlackRaw(msg)
	return err == nil && len(raw.Files) > 0
}

// ResolveMedia 下载并存储消息上的每个文件，返回带每个成功落库对象的 MediaRef 的
// 消息。各文件相互独立：一个失败不影响其余继续。
func (r *slackMediaResolver) ResolveMedia(ctx context.Context, inst engine.ResolvedInstallation, _ engine.ResolvedIdentity, _ pgtype.UUID, chatMessageID pgtype.UUID, msg channel.InboundMessage) channel.InboundMessage {
	raw, err := decodeSlackRaw(msg)
	if err != nil || len(raw.Files) == 0 {
		return msg
	}
	if r.storage == nil || r.ledger == nil {
		r.logWarn(msg, errors.New("media dependency missing"))
		return msg
	}
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		r.logWarn(msg, errors.New("installation platform row unavailable"))
		return msg
	}
	creds, err := decodeCredentials(row.Config, r.decrypt)
	if err != nil {
		r.logWarn(msg, fmt.Errorf("decode credentials: %w", err))
		return msg
	}
	files := raw.Files
	if len(files) > maxFilesPerMessage {
		r.logWarn(msg, fmt.Errorf("%d files exceed the limit of %d; extra files skipped", len(files), maxFilesPerMessage))
		files = files[:maxFilesPerMessage]
	}
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			// 媒体时限一过，Router 会把所有 ref 一并丢弃，再开下一个文件毫无
			// 意义，直接停。
			r.logWarn(msg, fmt.Errorf("media budget spent after %d of %d files: %w", i, len(files), err))
			break
		}
		fileCtx, cancel := context.WithTimeout(ctx, fileFetchBudget(ctx, len(files)-i))
		ref, err := r.ingestOne(fileCtx, inst, chatMessageID, i, f, creds.BotToken)
		cancel()
		if err != nil {
			r.logger.Warn("slack media ingest failed",
				"installation_id", util.UUIDToString(inst.ID),
				"message_id", msg.MessageID,
				"file", i,
				"err", err)
			continue
		}
		msg.MediaRefs = append(msg.MediaRefs, ref)
	}
	return msg
}

// fileFetchBudget 给单文件的下+传卡预算。固定超时能管住一次卡死的传输，但
// maxFilesPerMessage 个文件串行加起来会远超 Router 的媒体时限——而时限一过 Router
// 会丢掉【所有】ref，一条慢文件会连累整条消息的附件。把 ctx 剩余预算平摊给还没取
// 的文件，慢的早文件不至于饿死后面的；取完得快的文件会把没用掉的份额顺延给身后
// 的。
func fileFetchBudget(ctx context.Context, remainingFiles int) time.Duration {
	if remainingFiles < 1 {
		remainingFiles = 1
	}
	budget := fileFetchTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if share := time.Until(deadline) / time.Duration(remainingFiles); share < budget {
			budget = share
		}
	}
	return budget
}

// ingestOne 把单个文件从 URL 带到 MediaRef。台账行【先】落：自那以后每一个失败
// ——下载、上传、乃至进程崩溃——都会留下一条由 reconciler 结算的 intent，而这里
// 什么都不会擅自删除。
func (r *slackMediaResolver) ingestOne(ctx context.Context, inst engine.ResolvedInstallation, chatMessageID pgtype.UUID, index int, f slackRawFile, botToken string) (channel.MediaRef, error) {
	// Slack 会预先报文件大小，超限的直接拒，省得白烧一条 intent 行和一次完整传
	// 输。虚报小尺寸的文件也逃不过下载时的字节上限。
	if f.Size > maxInboundFileBytes {
		return channel.MediaRef{}, fmt.Errorf("file size %d exceeds the %d MiB limit", f.Size, maxInboundFileBytes>>20)
	}
	key := slackMediaObjectKey(inst, chatMessageID, f, index)
	link := r.storage.ObjectURL(key)
	owned, err := r.ledger.RecordPendingMediaObject(ctx, engine.RecordPendingMediaObjectParams{
		StorageKey:     key,
		WorkspaceID:    inst.WorkspaceID,
		ChatMessageID:  chatMessageID,
		StorageURL:     link,
		InstallationID: inst.ID,
	})
	if err != nil {
		// 没有持久 intent 就不上传——宁可漏，也不留无主对象。
		return channel.MediaRef{}, fmt.Errorf("record media intent: %w", err)
	}
	if !owned {
		// 这个键归 reconciler 管（可能是墓碑）；绝不复活它。
		return channel.MediaRef{}, errors.New("media key owned by reconciler")
	}
	data, responseType, err := r.download(ctx, f.DownloadURL, botToken)
	if err != nil {
		return channel.MediaRef{}, err
	}
	contentType, err := slackFileContentType(f, responseType, data)
	if err != nil {
		return channel.MediaRef{}, err
	}
	filename := slackFileName(f, index, contentType)
	if _, err := r.storage.Upload(ctx, key, data, contentType, filename); err != nil {
		// 存储端可能仍在处理这次 PUT；无论结果如何，intent 行都能兜住这个对象。
		return channel.MediaRef{}, fmt.Errorf("upload file: %w", err)
	}
	return channel.MediaRef{
		Type:       slackMediaKind(contentType),
		StorageKey: key,
		StorageURL: link,
		Filename:   filename,
		MimeType:   contentType,
		SizeBytes:  int64(len(data)),
	}, nil
}

func (r *slackMediaResolver) download(ctx context.Context, rawURL, botToken string) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", errors.New("invalid file download URL")
	}
	if err := r.validateDownloadURL(parsed); err != nil {
		return nil, "", err
	}
	// 每文件的预算已经带在 ctx 上（见 fileFetchBudget），它同时覆盖紧随其后的
	// 上传。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", errors.New("build file download request failed")
	}
	req.Header.Set("Authorization", "Bearer "+botToken)
	resp, err := r.fetch.Do(req)
	if err != nil {
		// net/http 的 *url.Error 会把完整请求 URL 原样带进错误；私密 Slack 文件
		// URL 不能进日志，所以剥掉。
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, "", fmt.Errorf("download file request failed: %w", urlErr.Err)
		}
		return nil, "", errors.New("download file request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download file: http %d", resp.StatusCode)
	}
	// 多读 1 字节用于探测是否超限，避免读满整个超大文件。
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxInboundFileBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}
	if len(data) > maxInboundFileBytes {
		return nil, "", fmt.Errorf("file exceeds the %d MiB limit", maxInboundFileBytes>>20)
	}
	contentType := resp.Header.Get("Content-Type")
	if semi := strings.IndexByte(contentType, ';'); semi >= 0 {
		contentType = strings.TrimSpace(contentType[:semi])
	}
	return data, contentType, nil
}

func (r *slackMediaResolver) validateDownloadURL(parsed *url.URL) error {
	if parsed == nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("invalid file download URL shape")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("invalid file download URL scheme %q", parsed.Scheme)
	}
	if !r.allowedHost(parsed.Hostname()) {
		return errors.New("blocked non-Slack file download host")
	}
	return nil
}

// slackFileContentType 决定存下来的对象算什么类型。令牌无效/缺 scope 时 Slack 会
// 以 200 返回它自己的 HTML 登录页，所以「文件声称不是 HTML，响应的却是 HTML」就
// 该判下载失败而不是一个文件——把它挂上消息等于把登录页当用户的文档递给 agent。
func slackFileContentType(f slackRawFile, responseType string, data []byte) (string, error) {
	declared := strings.ToLower(strings.TrimSpace(f.Mimetype))
	responseType = strings.ToLower(strings.TrimSpace(responseType))
	if responseType == "text/html" && declared != "text/html" {
		return "", errors.New("download returned an HTML page instead of the file (missing files:read scope?)")
	}
	if declared != "" {
		return declared, nil
	}
	if responseType != "" {
		return responseType, nil
	}
	// 事件没带 mime 类型、响应也没有 Content-Type 时，抓取前 512 字节嗅探。
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	contentType := http.DetectContentType(sniff)
	if semi := strings.IndexByte(contentType, ';'); semi >= 0 {
		contentType = strings.TrimSpace(contentType[:semi])
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType, nil
}

// slackMediaKind 把 mime 类型映射成归一化的媒体种类。
func slackMediaKind(contentType string) channel.MsgType {
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return channel.MsgTypeImage
	case strings.HasPrefix(contentType, "video/"):
		return channel.MsgTypeVideo
	case strings.HasPrefix(contentType, "audio/"):
		return channel.MsgTypeAudio
	}
	return channel.MsgTypeFile
}

// slackMediaObjectKey 用对象要挂靠的 CHAT 消息来派生对象键，而不是单用平台消息：
// 一条平台消息可能被摄取两次（入站去重 claim 过期后是可回收的），若共用同一个键，
// 第二次摄取会撞上第一次的台账行——那可能是个墓碑，intent upsert 会拒绝它，媒体
// 就悄悄丢了。
func slackMediaObjectKey(inst engine.ResolvedInstallation, chatMessageID pgtype.UUID, f slackRawFile, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", util.UUIDToString(chatMessageID), f.ID, index)))
	return path.Join(
		"workspaces",
		util.UUIDToString(inst.WorkspaceID),
		"slack",
		util.UUIDToString(inst.ID),
		hex.EncodeToString(sum[:]),
	)
}

// slackFileName 挑存储时用的展示名：上传者自带的名字可用就用它，否则按文件 id +
// 位置生成一个。
func slackFileName(f slackRawFile, index int, contentType string) string {
	if name := cleanFileName(f.Name); name != "" {
		return name
	}
	return fmt.Sprintf("slack-file-%s-%d%s", safeMediaSegment(f.ID), index+1, mediaExtension(contentType))
}

// cleanFileName 把 Slack 给的文件名净化成安全的展示名：去掉路径、拒绝纯点名的
// 目录穿越形态。
func cleanFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	// path.Base 会把 ".."、"..." 原样交回来；纯由点构成的名字根本不能当文件名。
	if strings.Trim(name, ".") == "" || name == "/" {
		return ""
	}
	return name
}

// mediaExtension 给内容类型挑一个扩展名：优先用熟悉的拼写，而不是 mime 库碰巧排
// 最前的那个（image/jpeg 在某些系统上解析成 ".jfif"），并固定住瘦身容器镜像的
// mime 库可能没收录的常见类型。
func mediaExtension(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	}
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// safeMediaSegment 把 id 规约成文件名安全字符（字母数字 + - _），其余一律替换成
// 下划线，防止文件系统/对象存储被特殊字符钻空子。
func safeMediaSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unknown"
	}
	return out
}

// logWarn 统一记一条「本次媒体摄取被跳过」的警告（依赖缺失、凭据解析失败等）。
func (r *slackMediaResolver) logWarn(msg channel.InboundMessage, err error) {
	r.logger.Warn("slack media resolve skipped", "message_id", msg.MessageID, "error", err)
}
