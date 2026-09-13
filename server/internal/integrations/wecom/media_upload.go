// media_upload.go —— 把文件「发进」企业微信会话。
//
// 机器人对这件事没有 REST 端点：媒体和别的东西一样走同一条 WebSocket，一共
// 三个 cmd，不带 access_token
// （https://developer.work.weixin.qq.com/document/path/101463）。
//
//	aibot_upload_media_init   → 声明文件，回一个 upload_id
//	aibot_upload_media_chunk  → 一段字节，base64 后发出去，共 × N 次
//	aibot_upload_media_finish → 交回消息可以携带的 media_id
//
// 这三步的每一步都会回比「已接受」更多的东西，所以三步都走 wsSender.request、
// 读回执里带回来的裁定。
//
// 中间那步的两个特性撑起了整个设计：分片可以乱序、并发发出；重复发同一片是幂
// 等的。所以分片是按「一次几片」而不是「一片一片排队」上传，而 ack 一直没回来
// 的那一片直接重发——一百片里丢一片绝不能赔上整个文件。
//
// 它不会做的事：把图片嵌进一条回复。长连接没有 msg_item，所以文件永远是一条
// 独立消息；带附件的回答就是「先回答，再补文件」。
package wecom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"
)

// 三个上传用的 cmd。
const (
	cmdUploadMediaInit   = "aibot_upload_media_init"
	cmdUploadMediaChunk  = "aibot_upload_media_chunk"
	cmdUploadMediaFinish = "aibot_upload_media_finish"
)

// mediaMsgType 是企业微信把某个文件当成一条消息后会叫它的类型。四个取值是协议
// 自己的；其他一律不收。
type mediaMsgType string

const (
	mediaTypeFile  mediaMsgType = "file"
	mediaTypeImage mediaMsgType = "image"
	mediaTypeVoice mediaMsgType = "voice"
	mediaTypeVideo mediaMsgType = "video"
)

// mediaChunkBytes 是单片的字节上限（base64 之前——所以线上一帧还要再大三分之
// 一左右）。maxMediaChunks 是协议自己对片数设的顶，两者相乘就是这条传输通道
// 能表达的最大文件——50 MB。两个数都是企微自家 SDK 的数字（WecomTeam/
// aibot-node-sdk src/client.ts，uploadMedia：`CHUNK_SIZE = 512 * 1024`，
// 且 `totalChunks > 100` 会抛 "max ~50MB"）。
const (
	mediaChunkBytes        = 512 * 1024
	maxMediaChunks         = 100
	maxMediaTransportBytes = mediaChunkBytes * maxMediaChunks
)

// maxMediaUploadBytes 是我们真正愿意发送的最大文件，只有传输通道能表达上限的
// 三分之一。
//
// 分片算术放行的 50 MB 是「组帧层能描述的尺寸」，不是「平台会收的尺寸」。企微
// 在 init cmd 自己那份文档里写明了可接受尺寸：developer.work.weixin.qq.com/
// document/path/101463 的 aibot_upload_media_init、body.total_size 一栏——
// 图片最多 10MB、语音 2MB、视频 10MB、普通文件 20MB。经典媒体上传接口也写了
// 同样的四种（document/path/90253，媒体文件大小限制一节）。普通文件 20 MB 是
// 各类别里最宽的上限。
//
// 各类型的上限在上一层 wecomMediaKind 里落地：超大的图片会降级成 file 发，而
// 不是递给服务器一个它注定要拒的东西。上面引的已发布通道限制才是权威。
//
// 把一个 40 MB 的文件切片上传、最后才被拒，代价是八十次往返、整份文件常驻内
// 存，以及用户干等几分钟只等到一句「不行」。
const maxMediaUploadBytes = 20 << 20

// 视频消息两个必填字段各自的字节上限。
const (
	videoTitleBytes       = 64
	videoDescriptionBytes = 512
)

// mediaChunkParallelism 是允许同时在飞的片数，它随文件变大而变小。
//
// 这个阶梯是企业微信自己的，出自 SDK 的 uploadMedia（WecomTeam/aibot-node-sdk
// src/client.ts）：`totalChunks <= 4 ? totalChunks : totalChunks <= 10 ? 3 : 2`。
// 为什么往下退，就写在它旁边的注释里——超过十个分片后，企微后端对并发突发的一
// 批分片会回一个系统错误。那是服务端限制，再怎么重试也绕不过去，固定用 4 反而
// 恰好会让最需要成功的大文件失败。
//
// 小端也有讲究，方向相反：四片及以下所有片一次全发，所以 2 MB 的文件不会为了
// 一个只对大文件成立的谨慎而被串行化。
//
// 还要压低并行还有一个理由：这条 socket 和该机器人所在的每个会话共用，一个文件
// 满负荷上传会卡住别人那边的回复。
func mediaChunkParallelism(chunks int) int {
	switch {
	case chunks < 1:
		return 1
	case chunks <= 4:
		return chunks
	case chunks <= 10:
		return 3
	default:
		return 2
	}
}

// mediaChunkAttempts 是一片最多被送出几次。第二次尝试只为「ack 丢了」存在，
// 不为别的：被服务端拒绝的片再发还是被拒，所以一次拒绝当场结束整个上传。
const mediaChunkAttempts = 2

var (
	// errMediaUploadTooLarge —— 超过了企微接受的尺寸。在读到第一个字节之前就
	// 拒绝，而不是等八十次往返之后。
	errMediaUploadTooLarge = fmt.Errorf("wecom: media exceeds the %d byte upload limit", maxMediaUploadBytes)

	// errMediaUploadEmpty —— 零字节文件。企微没有可存的，用户也没有可开的。
	errMediaUploadEmpty = errors.New("wecom: media has no bytes to upload")
)

// outboundMedia 是一个正在发往会话的文件。
type outboundMedia struct {
	// Kind 决定最终那条消息用哪个 msgtype，企业微信会据此校验字节——把一个
	// .pptx 声明成 image 会被拒，而不是被转换。
	Kind mediaMsgType

	// Filename 是接收方在文件卡片上看到的名字。它也是服务端判断格式的唯一
	// 线索，所以要保住扩展名。
	Filename string

	Data []byte
}

func (m outboundMedia) validate() error {
	switch m.Kind {
	case mediaTypeFile, mediaTypeImage, mediaTypeVoice, mediaTypeVideo:
	default:
		return fmt.Errorf("wecom: %q is not an uploadable media type", m.Kind)
	}
	if strings.TrimSpace(m.Filename) == "" {
		return errors.New("wecom: media upload requires a filename")
	}
	return nil
}

// mediaSend 是一次已完成的上传、以消息形态发出。
type mediaSend struct {
	Kind    mediaMsgType
	MediaID string

	// Title 与 Description 只属于视频——另外三种类型没有这两个字段，body 里
	// 多带了会被拒。
	Title       string
	Description string
}

// splitMediaChunks 把文件切成协议收的那种片。切片是对输入的引用别名；只有到
// 某一片要 base64 进它自己的帧时才会发生拷贝。
func splitMediaChunks(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, errMediaUploadEmpty
	}
	if len(data) > maxMediaUploadBytes {
		return nil, errMediaUploadTooLarge
	}
	chunks := make([][]byte, 0, (len(data)+mediaChunkBytes-1)/mediaChunkBytes)
	for start := 0; start < len(data); start += mediaChunkBytes {
		end := start + mediaChunkBytes
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, data[start:end])
	}
	return chunks, nil
}

// uploadMedia 把一个文件走完三步，返回可以用来组装消息的 media_id。
// ctx 约束整个过程。
func (s *wsSender) uploadMedia(ctx context.Context, m outboundMedia) (string, error) {
	if err := m.validate(); err != nil {
		return "", err
	}
	chunks, err := splitMediaChunks(m.Data)
	if err != nil {
		return "", err
	}
	uploadID, err := s.uploadMediaInit(ctx, m, len(chunks))
	if err != nil {
		return "", err
	}
	if err := s.uploadMediaChunks(ctx, uploadID, chunks); err != nil {
		return "", err
	}
	return s.uploadMediaFinish(ctx, uploadID)
}

// uploadMediaInit 声明文件并把 upload_id 带回来。
//
// 可选的 md5 字段刻意不传：文档只列了它，没说它是拿什么算的——原始文件还是它
// 的 base64——如果服务器校验的是一个我们猜错的值，就会拒绝每一次上传、而且错误
// 码完全不说为什么。
func (s *wsSender) uploadMediaInit(ctx context.Context, m outboundMedia, chunks int) (string, error) {
	body, err := s.request(ctx, cmdUploadMediaInit, map[string]any{
		"type":         string(m.Kind),
		"filename":     m.Filename,
		"total_size":   len(m.Data),
		"total_chunks": chunks,
	})
	if err != nil {
		return "", err
	}
	var res struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("wecom: decode upload init response: %w", err)
	}
	if res.UploadID == "" {
		return "", errors.New("wecom: upload init returned no upload_id")
	}
	return res.UploadID, nil
}

// uploadMediaChunks 把每一片发出去，一次少量并发。第一个失败会取消其余：
// 不存在值得收尾的半成品上传。
func (s *wsSender) uploadMediaChunks(ctx context.Context, uploadID string, chunks [][]byte) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(mediaChunkParallelism(len(chunks)))
	for i, chunk := range chunks {
		g.Go(func() error { return s.uploadMediaChunk(gctx, uploadID, i, chunk) })
	}
	return g.Wait()
}

// uploadMediaChunk 发一片，若它的裁定一直没回来则再发一次。
//
// chunk_index 以数字形态上线。文档里的字段表叫它字符串，旁边的工作示例却传的
// 是数字；企微自己的 SDK 发的也是数字，所以服务端确知收的就是数字。
func (s *wsSender) uploadMediaChunk(ctx context.Context, uploadID string, index int, chunk []byte) error {
	body := map[string]any{
		"upload_id":   uploadID,
		"chunk_index": index,
		"base64_data": base64.StdEncoding.EncodeToString(chunk),
	}
	var lastErr error
	for attempt := 0; attempt < mediaChunkAttempts; attempt++ {
		_, err := s.request(ctx, cmdUploadMediaChunk, body)
		if err == nil {
			return nil
		}
		lastErr = err
		// 被拒是服务端的回答，再发还会是同样的回答。只有「裁定一直没回来」才值
		// 得再递一次，而协议自己的幂等性正是让这次重发安全的原因。
		if !errors.Is(err, errAckTimeout) {
			break
		}
		s.log.Warn("wecom: media chunk got no verdict, sending it again",
			"upload_id", uploadID, "chunk_index", index, "attempt", attempt+1)
	}
	return fmt.Errorf("wecom: upload chunk %d: %w", index, lastErr)
}

// uploadMediaFinish 封口这次上传并把 media_id 带回来。
func (s *wsSender) uploadMediaFinish(ctx context.Context, uploadID string) (string, error) {
	body, err := s.request(ctx, cmdUploadMediaFinish, map[string]any{"upload_id": uploadID})
	if err != nil {
		return "", err
	}
	var res struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("wecom: decode upload finish response: %w", err)
	}
	if res.MediaID == "" {
		return "", errors.New("wecom: upload finish returned no media_id")
	}
	return res.MediaID, nil
}

// sendMedia 把一份已上传的文件当作一条消息送达，走的是和文字相同的
// aibot_send_msg 推送。
//
// 文档留下了两条路，而且对它们自相矛盾：aibot_send_msg 的字段表只列了
// template_card 和 markdown，而 aibot_respond_msg 的 msgtype 表确实写了 file、
// image、voice、video。最终仍然选推送，有两个理由。企微自家的 Node SDK 就是
// 用 aibot_send_msg 推媒体——这是两个方向里唯一来自「在跑代码」的证据。而且
// aibot_respond_msg 是靠开启那一轮的回调的 req_id 来寻址的，这条投递路径手里
// 根本没有那个 id：答案已经作为独立推送发出去了，没有一条活着的轮次可回。
//
// 真实的机器人到底收哪条路，还没观察确认过。拒绝会原样返回、不会换条路重试——
// 调用方如实告诉用户文件没送到；而往一个我们并不持有的地址上再试一次，反而
// 不诚实。
func (s *wsSender) sendMedia(ctx context.Context, chatID string, chatType int, m mediaSend) error {
	body, err := sendMsgMediaBody(chatID, chatType, m)
	if err != nil {
		return err
	}
	if _, err := s.request(ctx, cmdSendMsg, body); err != nil {
		// 一直没来的裁定不是拒绝。帧已经出去了，读取循环可能只是太忙。对这种
		// 情况重发会把同一个 media_id 再送一次，对方会把图片看到两遍、还没法
		// 撤回——所以按原样上报，并在日志里说清楚，因为「可能已经到了」和
		// 「被拒了」是两种需要追的东西。
		if errors.Is(err, errAckTimeout) {
			s.log.Warn("wecom: media push not acknowledged in time; not resending",
				"media_id", m.MediaID, "msgtype", string(m.Kind))
		}
		return err
	}
	s.log.Info("wecom: media delivered",
		"route", cmdSendMsg, "media_id", m.MediaID, "msgtype", string(m.Kind))
	return nil
}

// ---- message bodies ----

// sendMsgMediaBody 构造一条携带媒体的 aibot_send_msg body。
func sendMsgMediaBody(chatID string, chatType int, m mediaSend) (map[string]any, error) {
	if chatID == "" {
		return nil, errors.New("wecom: send_msg requires chat_id")
	}
	if chatType != chatTypeSingleInt && chatType != chatTypeGroupInt {
		return nil, errors.New("wecom: send_msg chat_type must be 1 (single) or 2 (group)")
	}
	body, err := mediaBodyFields(m)
	if err != nil {
		return nil, err
	}
	body["chatid"] = chatID
	body["chat_type"] = chatType
	return body, nil
}

// mediaBodyFields 是媒体帧携带的 {msgtype, <kind>:{...}} 这一对。
func mediaBodyFields(m mediaSend) (map[string]any, error) {
	if m.MediaID == "" {
		return nil, errors.New("wecom: media message requires a media_id")
	}
	nested := map[string]any{"media_id": m.MediaID}
	switch m.Kind {
	case mediaTypeFile, mediaTypeImage, mediaTypeVoice:
	case mediaTypeVideo:
		nested["title"] = clipUTF8(m.Title, videoTitleBytes)
		nested["description"] = clipUTF8(m.Description, videoDescriptionBytes)
	default:
		return nil, fmt.Errorf("wecom: %q is not a media msgtype", m.Kind)
	}
	return map[string]any{
		"msgtype":      string(m.Kind),
		string(m.Kind): nested,
	}, nil
}

// clipUTF8 把字符串按字符边界截到一个字节预算内。企微按字节计数，一个中文字
// 符标题要花三个字节，而从字符中间切一刀会把坏掉的 UTF-8 发给服务器。
func clipUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
