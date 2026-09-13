// media_ingest.go：飞书消息媒体（图片/视频/文件/音频）的解析与入库——下载飞书
// 资源、上传到对象存储、挂成聊天附件。
package lark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"path"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

// mediaStorage 是媒体对象存储的窄接口（整体上传）。
type mediaStorage interface {
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	// ObjectURL 是 key 上传成功后返回的 URL——纯粹是配置的函数，所以意图账本能
	// 在 PUT 之前就持久化它。
	ObjectURL(key string) string
}

// mediaStreamStorage 是支持流式上传的对象存储（S3 PutObject 等）。
type mediaStreamStorage interface {
	UploadStream(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string, filename string) (string, error)
}

// messageResourceStreamer 是支持流式下载飞书消息资源的 APIClient 进阶接口。
type messageResourceStreamer interface {
	DownloadMessageResourceStream(ctx context.Context, creds InstallationCredentials, p DownloadResourceParams) (DownloadedResourceStream, error)
}

// feishuMediaResolver 实现 engine.MediaResolver：从飞书拉取消息媒体并上传到
// 对象存储，产出供会话/issue 绑定的 MediaRef。
type feishuMediaResolver struct {
	api     APIClient
	creds   CredentialsResolver
	storage mediaStorage
	ledger  engine.MediaIntentLedger
	logger  *slog.Logger
}

// NewFeishuMediaResolver 构造飞书媒体解析器。
func NewFeishuMediaResolver(api APIClient, creds CredentialsResolver, storage mediaStorage, ledger engine.MediaIntentLedger, logger *slog.Logger) engine.MediaResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &feishuMediaResolver{api: api, creds: creds, storage: storage, ledger: ledger, logger: logger}
}

// HasMedia 报告消息是否携带可下载的飞书资源（独立 image/video/file/audio，
// 或 post 内嵌的 img/media span）。纯内存解码已收到的负载——它跑在 connector
// 的 ACK 路径上。
func (r *feishuMediaResolver) HasMedia(msg channel.InboundMessage) bool {
	lm, err := larkMsgFromRaw(msg)
	if err != nil {
		return false
	}
	return len(mediaResourcesFromMessage(lm)) > 0
}

// ResolveMedia 实现 engine.MediaResolver：解析消息里的全部媒体资源，逐个下载
// 并上传，把得到的引用追加到 msg.MediaRefs。
func (r *feishuMediaResolver) ResolveMedia(ctx context.Context, inst engine.ResolvedInstallation, _ engine.ResolvedIdentity, _ pgtype.UUID, chatMessageID pgtype.UUID, msg channel.InboundMessage) channel.InboundMessage {
	lm, err := larkMsgFromRaw(msg)
	if err != nil {
		r.logMediaWarn("lark media ingest skipped: raw decode failed", InboundMessage{MessageID: msg.MessageID}, err)
		return msg
	}
	resources := mediaResourcesFromMessage(lm)
	if len(resources) == 0 {
		return msg
	}
	if r.api == nil || r.creds == nil || r.storage == nil || r.ledger == nil {
		r.logMediaWarn("lark media ingest skipped: missing dependency", lm, nil)
		return msg
	}
	larkInst, ok := inst.Platform.(Installation)
	if !ok {
		r.logMediaWarn("lark media ingest skipped: installation payload unavailable", lm, nil)
		return msg
	}
	creds, err := installationCredentialsFor(larkInst, r.creds)
	if err != nil {
		r.logMediaWarn("lark media ingest skipped: credentials unavailable", lm, err)
		return msg
	}
	for resIndex, res := range resources {
		key := mediaObjectKey(inst, chatMessageID, res)
		link := r.storage.ObjectURL(key)
		// 在任何写入可能发生之前先持久化“上传意图”。此后的每个失败——下载错、
		// 上传错（即使 store 可能仍在处理中）、解析超时、崩溃——都只是把这行留给
		// reconciler；绝不在内联路径删除任何东西。
		ok, err := r.ledger.RecordPendingMediaObject(ctx, engine.RecordPendingMediaObjectParams{
			StorageKey:     key,
			WorkspaceID:    inst.WorkspaceID,
			ChatMessageID:  chatMessageID,
			StorageURL:     link,
			InstallationID: inst.ID,
		})
		if err != nil {
			// 没有持久化意图就不上传——失败安全的方向。
			r.logMediaWarn("lark media ingest skipped: intent record failed", lm, err)
			continue
		}
		if !ok {
			// reconciler 正持有这个键（'deleting'）——绝不复活它。
			r.logMediaWarn("lark media ingest skipped: key owned by reconciler", lm, nil)
			continue
		}
		got, err := r.downloadResource(ctx, creds, DownloadResourceParams{
			MessageID: res.messageID,
			FileKey:   res.key,
			Type:      res.fetchType,
		})
		if err != nil {
			r.logMediaWarn("lark media download failed", lm, err)
			continue
		}
		contentType := mediaContentType(res, got)
		filename := mediaFilename(lm, res, got, contentType, resIndex)
		uploadedBytes, err := r.uploadResource(ctx, key, got.Body, got.SizeBytes, contentType, filename)
		if err != nil {
			// store 可能仍在处理 PUT——在这里删除可能跟它乱序。上面写的意图行
			// 无论哪种情况都罩着这个对象；由 reconciler 了结。
			r.logMediaWarn("lark media upload failed", lm, err)
			continue
		}
		sizeBytes := got.SizeBytes
		if sizeBytes == 0 {
			sizeBytes = uploadedBytes
		}
		msg.MediaRefs = append(msg.MediaRefs, channel.MediaRef{
			Type:       res.kind,
			StorageKey: key,
			StorageURL: link,
			Filename:   filename,
			MimeType:   contentType,
			SizeBytes:  sizeBytes,
		})
	}
	return msg
}

// mediaObjectKey 从对象要挂靠的【CHAT 消息】推导对象键，而不是仅凭平台消息：
// 平台消息可能被摄入两次（去重行的认领在 60 秒过期后即可回收、24 小时后被
// 清理），共享键会让第二次摄入撞上第一次的账本行；那行可能是墓碑——意图
// upsert 拒绝任何已离开 'pending' 的键，第二次摄入的媒体会在重删调度跑完之前
// 被静默丢弃。按聊天消息取键让两次摄入彼此独立；不会泄漏，因为每次的对象都有
// 自己的账本行罩着。
func mediaObjectKey(inst engine.ResolvedInstallation, chatMessageID pgtype.UUID, res larkMediaResource) string {
	sum := sha256.Sum256([]byte(uuidString(chatMessageID) + "\x00" + res.messageID + "\x00" + res.fetchType + "\x00" + res.key))
	return path.Join(
		"workspaces",
		uuidString(inst.WorkspaceID),
		"lark",
		uuidString(inst.ID),
		hex.EncodeToString(sum[:]),
	)
}

// downloadResource 优先走流式下载接口；没有则退回整体下载后包装成流。
func (r *feishuMediaResolver) downloadResource(ctx context.Context, creds InstallationCredentials, p DownloadResourceParams) (DownloadedResourceStream, error) {
	if streamer, ok := r.api.(messageResourceStreamer); ok {
		return streamer.DownloadMessageResourceStream(ctx, creds, p)
	}
	got, err := r.api.DownloadMessageResource(ctx, creds, p)
	if err != nil {
		return DownloadedResourceStream{}, err
	}
	return DownloadedResourceStream{
		Body:        io.NopCloser(bytes.NewReader(got.Data)),
		ContentType: got.ContentType,
		Filename:    got.Filename,
		SizeBytes:   got.SizeBytes,
	}, nil
}

// uploadResource 把下载到的资源上传到对象存储：优先流式（带字节计数），长度
// 未知时退回整体缓冲后走可寻址的 Upload 路径。
func (r *feishuMediaResolver) uploadResource(ctx context.Context, key string, body io.ReadCloser, sizeBytes int64, contentType string, filename string) (int64, error) {
	defer body.Close()
	counter := &countingReader{r: body}
	if streamStorage, ok := r.storage.(mediaStreamStorage); ok && sizeBytes > 0 {
		_, err := streamStorage.UploadStream(ctx, key, counter, sizeBytes, contentType, filename)
		return counter.n, err
	}
	// 长度未知的 HTTP 体不能以不可寻址的流喂给 S3 PutObject。传输层已强制
	// 100 MiB 资源上限，所以把这个不常见的兜底缓冲起来，走可寻址的 Upload 路径。
	data, err := io.ReadAll(counter)
	if err != nil {
		return counter.n, err
	}
	_, err = r.storage.Upload(ctx, key, data, contentType, filename)
	return int64(len(data)), err
}

// countingReader 包一层 reader，顺带统计读过的字节数。
type countingReader struct {
	r io.Reader
	n int64
}

// Read 实现 io.Reader 并累计计数。
func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

// logMediaWarn 以统一字段记录媒体相关告警。
func (r *feishuMediaResolver) logMediaWarn(msg string, lm InboundMessage, err error) {
	if r.logger == nil {
		return
	}
	args := []any{"message_id", lm.MessageID, "message_type", lm.MessageType}
	if err != nil {
		args = append(args, "err", err)
	}
	r.logger.Warn(msg, args...)
}

// larkMediaResource 描述消息里一个待摄取媒体：它在飞书侧的 key、下载类型，
// 以及落库后的元数据。
type larkMediaResource struct {
	key       string
	kind      channel.MsgType
	fetchType string
	filename  string
	mimeType  string
	sizeBytes int64
	messageID string
}

// mediaResourcesFromMessage 按 msg_type 从飞书消息的原始 content JSON 里列出
// 需要下载的媒体资源。
func mediaResourcesFromMessage(lm InboundMessage) []larkMediaResource {
	var payload struct {
		ImageKey    string `json:"image_key"`
		FileKey     string `json:"file_key"`
		FileName    string `json:"file_name"`
		Name        string `json:"name"`
		MimeType    string `json:"mime_type"`
		ContentType string `json:"content_type"`
		Size        int64  `json:"size"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if lm.Content == "" || json.Unmarshal([]byte(lm.Content), &payload) != nil {
		return nil
	}
	filename := firstNonEmpty(payload.FileName, payload.Name)
	mimeType := firstNonEmpty(payload.MimeType, payload.ContentType)
	sizeBytes := payload.SizeBytes
	if sizeBytes == 0 {
		sizeBytes = payload.Size
	}
	switch lm.MessageType {
	case "image":
		if payload.ImageKey == "" {
			return nil
		}
		return []larkMediaResource{{
			key:       payload.ImageKey,
			kind:      channel.MsgTypeImage,
			fetchType: "image",
			filename:  filename,
			mimeType:  mimeType,
			sizeBytes: sizeBytes,
			messageID: lm.MessageID,
		}}
	case "post":
		return mediaResourcesFromPost(lm)
	case "media", "video":
		if payload.FileKey == "" {
			return nil
		}
		return []larkMediaResource{{
			key:       payload.FileKey,
			kind:      channel.MsgTypeVideo,
			fetchType: "file",
			filename:  filename,
			mimeType:  mimeType,
			sizeBytes: sizeBytes,
			messageID: lm.MessageID,
		}}
	case "file", "audio":
		if payload.FileKey == "" {
			return nil
		}
		kind := channel.MsgTypeFile
		if lm.MessageType == "audio" {
			kind = channel.MsgTypeAudio
		}
		return []larkMediaResource{{
			key:       payload.FileKey,
			kind:      kind,
			fetchType: "file",
			filename:  filename,
			mimeType:  mimeType,
			sizeBytes: sizeBytes,
			messageID: lm.MessageID,
		}}
	default:
		return nil
	}
}

// mediaResourcesFromPost 从富文本 post 内容的 img/media span 里列出媒体资源。
func mediaResourcesFromPost(lm InboundMessage) []larkMediaResource {
	if lm.Content == "" {
		return nil
	}
	var doc larkPostContent
	if err := json.Unmarshal([]byte(lm.Content), &doc); err != nil {
		return nil
	}
	var out []larkMediaResource
	// 一条 post 可能在不止一个 span 里引用同一个 image_key/file_key。对象键由
	// (message, type, key) 推导，重复会上传到同一个键两次：后面失败的尝试可能
	// 毁掉前面成功已产出的对象（悬挂附件），后面的成功又会产生同一对象的两行
	// 附件。在任意上传之前就在这里去重。
	seen := make(map[string]bool)
	for _, para := range doc.Content {
		for _, span := range para {
			switch span.Tag {
			case "img":
				if span.ImageKey == "" || seen["image\x00"+span.ImageKey] {
					continue
				}
				seen["image\x00"+span.ImageKey] = true
				out = append(out, larkMediaResource{
					key:       span.ImageKey,
					kind:      channel.MsgTypeImage,
					fetchType: "image",
					filename:  firstNonEmpty(span.FileName, span.Name),
					mimeType:  span.MimeType,
					messageID: lm.MessageID,
				})
			case "media":
				if span.FileKey == "" || seen["file\x00"+span.FileKey] {
					continue
				}
				seen["file\x00"+span.FileKey] = true
				out = append(out, larkMediaResource{
					key:       span.FileKey,
					kind:      channel.MsgTypeVideo,
					fetchType: "file",
					filename:  firstNonEmpty(span.FileName, span.Name),
					mimeType:  span.MimeType,
					messageID: lm.MessageID,
				})
			}
		}
	}
	return out
}

// mediaFilename 为存储对象挑一个名字。index 消除自动生成形态的重名：同一条
// 消息上的所有资源共享一个 MessageID，所以一次发送里的三张照片以前会产出三个
// 都叫 "feishu-image-<msg>.jpg" 的对象。
func mediaFilename(lm InboundMessage, res larkMediaResource, got DownloadedResourceStream, contentType string, index int) string {
	for _, candidate := range []string{got.Filename, res.filename} {
		if name := cleanFilename(candidate); name != "" {
			return ensureAudioFilenameExtension(name, res.kind, contentType)
		}
	}
	prefix := "feishu-file"
	switch res.kind {
	case channel.MsgTypeImage:
		prefix = "feishu-image"
	case channel.MsgTypeVideo:
		prefix = "feishu-video"
	case channel.MsgTypeAudio:
		prefix = "feishu-audio"
	}
	name := prefix + "-" + safePathSegment(lm.MessageID)
	if index > 0 {
		name += "-" + strconv.Itoa(index+1)
	}
	return name + mediaExtension(contentType)
}

// mediaContentType 决定存储对象应使用的 Content-Type：优先下载响应里的类型，
// 音频消息再兜底为协议级格式（飞书音频是 Opus）。
func mediaContentType(res larkMediaResource, got DownloadedResourceStream) string {
	contentType := strings.TrimSpace(got.ContentType)
	if res.kind == channel.MsgTypeAudio && isGenericBinaryContentType(contentType) {
		if hinted := strings.TrimSpace(res.mimeType); !isGenericBinaryContentType(hinted) {
			return hinted
		}
		// 飞书音频消息是 Opus。它的资源端点可能返回无扩展名的 Content-Disposition
		// 文件名，同时配一个笼统的 audio/octet-stream 类型，所以在这里保留协议级
		// 格式。
		return "audio/opus"
	}
	if contentType == "" {
		contentType = strings.TrimSpace(res.mimeType)
	}
	if contentType == "" {
		return "application/octet-stream"
	}
	return contentType
}

// isGenericBinaryContentType 判断 content type 是否属于“笼统二进制”（空、
// application/octet-stream、audio/octet-stream）。
func isGenericBinaryContentType(contentType string) bool {
	if semi := strings.IndexByte(contentType, ';'); semi >= 0 {
		contentType = contentType[:semi]
	}
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "", "application/octet-stream", "audio/octet-stream":
		return true
	default:
		return false
	}
}

// ensureAudioFilenameExtension 给缺少扩展名的音频文件名补上按内容类型推导的
// 扩展名。
func ensureAudioFilenameExtension(name string, kind channel.MsgType, contentType string) string {
	if kind != channel.MsgTypeAudio || path.Ext(name) != "" {
		return name
	}
	return name + mediaExtension(contentType)
}

// cleanFilename 把不可信的候选文件名清理成可用于展示/存储的相对名；清不干净
// 就返回空串，让调用方落到自动生成的名字上。
func cleanFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	// path.Base 能解析路径，但它会把 ".." 与 "..." 原样还回来。纯点号构成的
	// 名字不是文件名；放行意味着对象的显示名在之后任何渲染或另存的地方都会是
	// ".."。落到自动生成的名字上去。
	if strings.Trim(name, ".") == "" || name == "/" {
		return ""
	}
	return name
}

// mediaExtension 按 content type 推导常用文件扩展名；个别常见类型手工钉死，
// 其余交给 mime.ExtensionsByType。
func mediaExtension(contentType string) string {
	if semi := strings.IndexByte(contentType, ';'); semi >= 0 {
		contentType = strings.TrimSpace(contentType[:semi])
	}
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
	case "audio/opus":
		return ".opus"
	case "audio/ogg":
		return ".ogg"
	case "audio/amr":
		return ".amr"
	case "audio/mpeg":
		return ".mp3"
	case "application/pdf":
		// mime.ExtensionsByType 在多数系统上返回 ExtensionsByType(".pdf")，
		// 但它读的是 /etc/mime.types——精简容器镜像不带这个文件——所以常见
		// 文档类型在这里钉死，而不是交给宿主机。
		return ".pdf"
	}
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// safePathSegment 把任意字符串清洗成能安全用作 URL/路径段的形态。
func safePathSegment(s string) string {
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

// firstNonEmpty 返回第一个非空白字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
