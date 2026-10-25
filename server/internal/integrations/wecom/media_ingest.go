// media_ingest.go —— WeCom 的 engine.MediaResolver。
//
// 总骨架是 lark/media_ingest.go 确立、且 Router 依赖的那一套：HasMedia 纯粹在
// 内存里看手上已有的 payload；ResolveMedia 脱离连接器 ACK 路径运行；每次上传都
// 由一条「在 PUT 之前就写好」的 intent-ledger 行兜底；任何东西都不内联删除——
// 一步失败，要么把行留给对账器（reconciler）收尾，要么保留消息里的占位文本不
// 动。
//
// wecom 特有的在中间：企微给的是预签名 COS url 加一把 per-url 密钥，而不是「带
// 租户 token 去抓的资源 id」，所以这里没有 API 客户端也没有凭证——只有一次 HTTP
// GET 加一次解密（media_download.go、media_crypt.go）。回调 body 除了这两条字符
// 串，对文件本身只字不提，所以名字取自下载的 Content-Disposition，类型要么按
// 名字扩展名推断、要么嗅探解密后的字节。
package wecom

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
	"path"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
)

// mediaFailureNotice 提示语。企微部署只在中国，所以沿用本适配器其余部分已在写
// 的中文产品口吻（wecom_channel.go 的回执、router.go 的会话标题）。
const (
	mediaUnreadableNotice = "抱歉，有附件没能收到，麻烦重新发一次。"
	mediaTooLargeNotice   = "抱歉，附件太大了，我这边收不下。"
)

// mediaStorage 是本解析器驱动的 storage.Storage 切片。ObjectURL 是配置的纯函
// 数，这正是「对象还没存在，intent ledger 就能先把它的 URL 记下来」的原因。
type mediaStorage interface {
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	ObjectURL(key string) string
}

// mediaStreamStorage 是流式那一半，两个生产后端都实现了它（storage.S3Storage、
// storage.LocalStorage）。没有它的后端会退回缓冲式路径——结果正确，只是内存不
// 平坦。
type mediaStreamStorage interface {
	UploadStream(ctx context.Context, key string, data io.Reader, sizeBytes int64, contentType string, filename string) (string, error)
}

type wecomMediaResolver struct {
	storage mediaStorage
	ledger  engine.MediaIntentLedger
	http    *http.Client
	// notify 触达该安装的活 aibot socket，告诉发送者某个附件没送达。为 nil 时
	// 不发通知，只留日志。
	notify *sendersRegistry
	logger *slog.Logger
}

// NewMediaResolver 构造 wecom 的 MediaResolver。storage 与 ledger 是必需的——
// 缺了任何一个，附件就没有可指向的持久化目标，解析器退化成只把占位符留在原地。
// senders 可选：没有它，失败附件只写日志。
func NewMediaResolver(storage mediaStorage, ledger engine.MediaIntentLedger, senders *sendersRegistry, logger *slog.Logger) engine.MediaResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &wecomMediaResolver{
		storage: storage,
		ledger:  ledger,
		// 绝不裸用一个 http.Client：被抓的 URL 是从线上来的，而这个客户端拒绝连
		// 接任何不是公网的地方（media_guard.go）。
		http:   newMediaHTTPClient(mediaGuard{}),
		notify: senders,
		logger: logger,
	}
}

// HasMedia 报告这条回调是否携带需要下载的东西。它在连接器 ACK 路径上同步执行，
// 决定这条消息要不要为此付一份媒体 deadline、一次延迟 run、一个信号量槽位——
// 所以它只是对已到手字节的一次解码。
func (r *wecomMediaResolver) HasMedia(msg channel.InboundMessage) bool {
	wm, err := wecomMsgFromRaw(msg)
	if err != nil {
		return false
	}
	return len(wm.Media) > 0
}

// ResolveMedia 下载、解密、存储消息上的每个附件，返回带「每个成功落库对象一个
// MediaRef」的消息。附件彼此独立：一个失败不拖累其余，末尾统一把没到的清单告诉
// 发送者一次。
func (r *wecomMediaResolver) ResolveMedia(ctx context.Context, inst engine.ResolvedInstallation, _ engine.ResolvedIdentity, _ pgtype.UUID, chatMessageID pgtype.UUID, msg channel.InboundMessage) channel.InboundMessage {
	wm, err := wecomMsgFromRaw(msg)
	if err != nil {
		r.logger.Warn("wecom media ingest skipped: raw decode failed", "message_id", msg.MessageID, "err", err)
		return msg
	}
	if len(wm.Media) == 0 {
		return msg
	}
	if r.storage == nil || r.ledger == nil {
		r.logger.Warn("wecom media ingest skipped: no storage configured",
			"msg_id", wm.MsgID, "attachments", len(wm.Media))
		return msg
	}

	var failures []mediaFailure
	for i, m := range wm.Media {
		ref, err := r.ingestOne(ctx, inst, chatMessageID, wm, i, m)
		if err != nil {
			failure := classifyMediaFailure(err)
			failures = appendFailure(failures, failure)
			// 被拒地址要单独一行，因为运维接下来的动作和这里其他所有失败不同：
			// 附件本身没问题，是部署拒绝拨号到它主机解析出的那个地址，所以修法
			// 是改配置、不是重试。只对它说一句 "ingest failed" 会把人领去看企微。
			logLine := "wecom media ingest failed"
			if failure == mediaFailureBlocked {
				logLine = "wecom media ingest refused by the media address guard: the host resolved to a non-public address and was not dialed. If this deployment sits behind a fake-IP proxy, declare its range in MULTICA_WECOM_MEDIA_ALLOW_CIDRS; otherwise this is a URL that should not have been sent."
			}
			// url 和密钥永远不进日志：一个是签名地址（谁都能顺着抓），另一个能解
			// 锁它。stripURL 已把 url 从 err 里摘掉，而闸门的拒绝本身也不带地址，
			// 所以 err 可以整条放心记。
			r.logger.Warn(logLine,
				"installation_id", util.UUIDToString(inst.ID),
				"msg_id", wm.MsgID,
				"attachment", i,
				"kind", string(m.Kind),
				"err", err)
			continue
		}
		msg.MediaRefs = append(msg.MediaRefs, ref)
	}

	r.tellTheSender(inst, wm, failures)
	return msg
}

// ingestOne 把单个附件从 url 一路带到 MediaRef。ledger 行最先写：从那一刻起，
// 任何失败——下载、解密、上传、崩溃——都会留下一条由对账器去结清的 intent，
// 而这里什么都不会删。
func (r *wecomMediaResolver) ingestOne(ctx context.Context, inst engine.ResolvedInstallation, chatMessageID pgtype.UUID, wm InboundMessage, index int, m InboundMedia) (channel.MediaRef, error) {
	key := mediaObjectKey(inst, chatMessageID, wm.MsgID, index, m.Kind)
	link := r.storage.ObjectURL(key)

	ok, err := r.ledger.RecordPendingMediaObject(ctx, engine.RecordPendingMediaObjectParams{
		StorageKey:     key,
		WorkspaceID:    inst.WorkspaceID,
		ChatMessageID:  chatMessageID,
		StorageURL:     link,
		InstallationID: inst.ID,
	})
	if err != nil {
		// 没有持久化的 intent 就不上传——这是「朝失败方向倒」的安全侧。
		return channel.MediaRef{}, fmt.Errorf("record media intent: %w", err)
	}
	if !ok {
		// 这把 key 归对账器所有；绝不复活它。
		return channel.MediaRef{}, fmt.Errorf("media key %s is owned by the reconciler", key)
	}

	streamer, canStream := r.storage.(mediaStreamStorage)
	if canStream {
		// 内存平坦路径：密文从 socket 流进解密，明文落进一个已 unlink 的临时文
		// 件，上传从那里读。全程没有谁持有完整附件。
		ref, err := r.ingestStreaming(ctx, streamer, wm, index, m, key, link)
		if err == nil || !errors.Is(err, errStreamingUnavailable) {
			return ref, err
		}
		// 往下走：后端宣称能流式、结果不行。
	}

	got, err := downloadMedia(ctx, r.http, m.URL)
	if err != nil {
		return channel.MediaRef{}, err
	}
	traceMediaHeaders(r.logger, wm.MsgID, index, got.mediaHeaders)
	plain, err := decryptMedia(m.AESKey, got.Body)
	if err != nil {
		return channel.MediaRef{}, err
	}

	filename, contentType := describeMedia(wm, index, m, got.Filename, plain)
	if _, err := r.storage.Upload(ctx, key, plain, contentType, filename); err != nil {
		// 存储端可能还在处理这次 PUT；在这里删可能和它交错。无论如何 intent 行
		// 都覆盖着这个对象。
		return channel.MediaRef{}, fmt.Errorf("upload media: %w", err)
	}
	return m.inline(channel.MediaRef{
		Type:       m.Kind,
		StorageKey: key,
		StorageURL: link,
		Filename:   filename,
		MimeType:   contentType,
		SizeBytes:  int64(len(plain)),
	}), nil
}

// mediaObjectKey 给对象起名。它从 CHAT 消息派生而不是从企微消息派生，理由 lark
// 有文档：同一条平台消息可能被入站去重逻辑收两遍（去重占位过期后可以重新认领），
// 共用一把 key 会让第二次入站撞上第一次的 ledger 行——若那行已是墓碑（tombstone）
// 会被 intent upsert 拒绝，媒体就静默丢了。附件在消息里的位置也进 key，因为一条
// 图文混排可以带好几个附件，而 url 不够稳定、不能拿来做 key。
func mediaObjectKey(inst engine.ResolvedInstallation, chatMessageID pgtype.UUID, msgID string, index int, kind channel.MsgType) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d",
		util.UUIDToString(chatMessageID), msgID, string(kind), index)))
	return path.Join(
		"workspaces",
		util.UUIDToString(inst.WorkspaceID),
		"wecom",
		util.UUIDToString(inst.ID),
		hex.EncodeToString(sum[:]),
	)
}

// describeMedia 推断出该怎么叫这个文件、说它是什么。回调 body 两个都不带，所以
// 名字取自下载的 Content-Disposition、类型取名字扩展名，回退到嗅探解密后的字
// 节——对「其实是 zip 容器」的格式（.docx、.xlsx）扩展名是更好的信号，完全没
// 名字时嗅探是更好的信号。
func describeMedia(wm InboundMessage, index int, m InboundMedia, headerName string, plain []byte) (filename, contentType string) {
	filename = cleanMediaFilename(headerName)
	if ext := path.Ext(filename); ext != "" {
		contentType = mime.TypeByExtension(ext)
	}
	if contentType == "" {
		contentType = http.DetectContentType(plain)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if filename == "" {
		filename = fallbackMediaName(wm.MsgID, index, m.Kind, contentType)
	}
	return filename, contentType
}

// fallbackMediaName 给服务端没起名的附件造一个名字。它必须在消息内唯一，所以
// 附件位置也拼进去——否则一条图文混排里的两张照片会变成「两个同名文件」。
func fallbackMediaName(msgID string, index int, kind channel.MsgType, contentType string) string {
	prefix := "wecom-file"
	switch kind {
	case channel.MsgTypeImage:
		prefix = "wecom-image"
	case channel.MsgTypeVideo:
		prefix = "wecom-video"
	}
	return fmt.Sprintf("%s-%s-%d%s", prefix, safeMediaSegment(msgID), index, mediaExtension(contentType))
}

// mediaExtension 给内容类型挑一个文件扩展名，优先用大家熟悉的写法，而不是 mime
// 库恰好先列出的那个（image/jpeg 在某些系统上解析成 ".jfif"）。
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
	case "application/pdf":
		return ".pdf"
	}
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// safeMediaSegment 把 id 收窄成文件名里安全的那些字符。
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

// ---- telling the sender ----

// mediaFailure 是坏消息的「种类」，不是错误本身：发送者要被告知的是下一步该怎么
// 做不一样，「太大」和「没收到」各有各的答复。
type mediaFailure int

const (
	mediaFailureUnreadable mediaFailure = iota
	mediaFailureTooLarge
	// mediaFailureBlocked 是闸门拒绝媒体主机解析出的地址。它从
	// mediaFailureUnreadable 里分出来是为 OPERATOR（运维）而不是发送者：附件
	// 本身没问题，在部署自己的配置改变之前，重试多少次都没用。见 ResolveMedia
	// 里的日志分支。
	mediaFailureBlocked
)

func classifyMediaFailure(err error) mediaFailure {
	if errors.Is(err, errMediaTooLarge) {
		return mediaFailureTooLarge
	}
	if errors.Is(err, ErrMediaAddrBlocked) {
		return mediaFailureBlocked
	}
	return mediaFailureUnreadable
}

// appendFailure 每类只保留一条。一条消息带四个附件、四个都过期，是「一件事出了
// 问题」，把它说四遍对谁都没帮助。
func appendFailure(list []mediaFailure, f mediaFailure) []mediaFailure {
	for _, existing := range list {
		if existing == f {
			return list
		}
	}
	return append(list, f)
}

// tellTheSender 往附件来自的那个会话里写一条简短说明，走的是和其他企微消息相同
// 的那条活 aibot socket。
//
// 没有它，失败会以最糟的方式隐形：落库的正文仍写着 [Image]，于是 agent 好像看
// 到过一张它从未收到的图似的作答。agent 的运行被刻意放行继续——人自己看得见图
// 没传过来，而他为图旁那行字配的答案仍然值得拿到。
func (r *wecomMediaResolver) tellTheSender(inst engine.ResolvedInstallation, wm InboundMessage, failures []mediaFailure) {
	if len(failures) == 0 || r.notify == nil || !inst.ID.Valid {
		return
	}
	chatID := wm.ChatID
	if chatID == "" {
		chatID = wm.SenderUserID
	}
	if chatID == "" {
		return
	}
	sender := r.notify.get(inst.ID)
	if sender == nil {
		// 正处于重连中：这条安装用来写消息的 socket 此刻不存在。下面这行日志就
		// 是全部记录了。
		r.logger.Warn("wecom media failure notice not delivered: no live connection",
			"installation_id", util.UUIDToString(inst.ID), "msg_id", wm.MsgID)
		return
	}
	chatType := chatTypeSingleInt
	if strings.EqualFold(wm.ChatType, "group") {
		chatType = chatTypeGroupInt
	}
	lines := make([]string, 0, len(failures))
	for _, f := range failures {
		notice := mediaUnreadableNotice
		if f == mediaFailureTooLarge {
			notice = mediaTooLargeNotice
		}
		// mediaFailureBlocked 刻意落到「不可读」那套措辞上。从发送者一侧看，
		// 地址被拒和下载失败是同一件事——附件没到——能把它们分开的是运维必须
		// 改的东西，那只属于日志。解析出的地址和签名 url 都不会进聊天消息。
		//
		// 两类共用一套措辞，正是去重从 appendFailure 移到这里的原因：一条同时
		// 带「被拒」和「不可读」两个附件消息，仍然只是一条要报的新闻。
		if !slices.Contains(lines, notice) {
			lines = append(lines, notice)
		}
	}
	if err := sender.sendText(chatID, chatType, strings.Join(lines, "\n")); err != nil {
		r.logger.Warn("wecom media failure notice not delivered",
			"installation_id", util.UUIDToString(inst.ID), "msg_id", wm.MsgID, "err", err)
	}
}

// errStreamingUnavailable 表示内存平坦路径因为某个「不是附件过错」的原因没能走
// 成——没有临时空间、后端宣称支持 UploadStream 却拒绝。调用方退回缓冲式路径，
// 而不是为一个优化把附件判失败。
var errStreamingUnavailable = errors.New("wecom: streaming media ingest unavailable")

// ingestStreaming 携带单个附件而不把它整体拿在手上：密文从响应正文流进解密，
// 明文落进一个已 unlink 的临时文件，上传用此刻已知的精确长度从这个文件读。
func (r *wecomMediaResolver) ingestStreaming(
	ctx context.Context,
	streamer mediaStreamStorage,
	wm InboundMessage,
	index int,
	m InboundMedia,
	key, link string,
) (channel.MediaRef, error) {
	body, headers, err := openMedia(ctx, r.http, m.URL)
	if err != nil {
		return channel.MediaRef{}, err
	}
	defer body.Close()
	traceMediaHeaders(r.logger, wm.MsgID, index, headers)

	plain, size, err := decryptToFile(m.AESKey, body, "")
	if err != nil {
		// 解密失败是附件自己的问题、必须照附件上报；只有「临时文件做不出来」才
		// 有理由回退，decryptToFile 在它的错误信息里说明了这一点。
		if strings.Contains(err.Error(), "media temp file") {
			return channel.MediaRef{}, fmt.Errorf("%w: %v", errStreamingUnavailable, err)
		}
		return channel.MediaRef{}, err
	}
	defer plain.Close()

	// 类型从文件头嗅探而不是整份嗅——http.DetectContentType 本来就只读 512 字
	// 节。
	head, err := peekFile(plain, 512)
	if err != nil {
		return channel.MediaRef{}, fmt.Errorf("wecom: media sniff: %w", err)
	}
	filename, contentType := describeMedia(wm, index, m, headers.Filename, head)

	if _, err := streamer.UploadStream(ctx, key, plain, size, contentType, filename); err != nil {
		return channel.MediaRef{}, fmt.Errorf("upload media: %w", err)
	}
	return m.inline(channel.MediaRef{
		Type:       m.Kind,
		StorageKey: key,
		StorageURL: link,
		Filename:   filename,
		MimeType:   contentType,
		SizeBytes:  size,
	}), nil
}
