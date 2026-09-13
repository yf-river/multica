// outbound_media.go —— 投递 agent 产出的文件。
//
// agent 那一侧早已存在、且跨平台通用：它跑 `multica attachment upload <path>`，
// 文件落进对象存储，CompleteTask 把行绑到它刚写的那条 assistant 消息上。缺的是
// 最后那一跳。绑定之后的下游统统假设「浏览器里有个会话窗口」，所以企微会话曾被
// 告知「根本收不了文件」。
//
// 本文件的形态由三件事决定。
//
// 回答永远先走。一次上传是几兆字节、多次往返、还可能失败；agent 写的那句话不能
// 排在它后面干等，也绝不能因它而丢。所以本路径在回复发出之后跑，用独立 goroutine
// 和独立预算，它最坏的结局也就是多一行「有个文件没送到」。
//
// 文件是它自己的消息。长连接没有 msg_item，所以什么都嵌不进回复——「带附件的回
// 答」必然是两条消息。
//
// 而且企微会拿字节对照 msgtype。一个声明成 image 的 .pptx 会被拒而不是被转换，
// 每种类型还各有尺寸上限，所以「把这个文件叫什么」是一个决策、不是一次查表。
package wecom

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// mediaObjectStore 是本路径需要的 storage.Storage 切片：附件行带着对象的 URL，
// 这两个方法把它变回字节。
type mediaObjectStore interface {
	KeyFromURL(rawURL string) string
	GetReader(ctx context.Context, key string) (io.ReadCloser, error)
}

// 告诉用户的话。有三条是因为有三件可能为真的事——把它们分开正是要点（见
// deliveryState）。硬编码中文，和本适配器发出去的每个面向用户字符串一样
// （replier.go）——企微部署只在中国。
const (
	// mediaSendFailedText —— 我们知道它没到。确凿的，因为把一个确凿的失败说成
	// 「可能送达了」，正是让用户从此无视通知的做法。
	mediaSendFailedText = "⚠️ 有文件没能发出来，我这边保留着，需要的话我再试一次。"

	// mediaSendUnknownText —— 帧已发出、裁定没回来，所以文件可能已在会话里。措
	// 辞要能扛住两种结局：不能对正看着文件的人说「失败了」，也不能对始终没收到
	// 的人说「已发送」。它还要解释为什么不自动重发——那是显而易见的下一步问题，
	// 答案是「重了没法撤回」。
	mediaSendUnknownText = "⚠️ 有文件我没收到企业微信的送达回执，可能已经发到了、也可能没有。我不会自动重发，免得发重了；你那边没看到的话说一声，我再发一次。"

	// mediaLookupFailedText —— 失败在我们这一侧、而且发生在「问题都还没被回答」之
	// 前：我们读不了绑在这条回答上的东西，所以根本不知道有没有文件。这里不说话，
	// 正是让用户干等一件「从未被尝试过」的事的做法。
	mediaLookupFailedText = "⚠️ 我这边没查到这条回答带没带文件，所以要是有，这次没发出来。需要的话我再试一次。"
)

// attachmentBudget 约束一条回答的整个附件投递——读每个对象、上传、发送。宽松是
// 因为一个 20 MiB 文件按 40 片 ack、一次两片并不快，而没有任何人等着它。
const attachmentBudget = 5 * time.Minute

// deliveryState 是试发一个文件之后我们真正知道的事。三个值，因为两值版本在两个
// 方向上同时是错的：ack 没回来的发送被当作确凿失败报给用户——尽管文件很可能正
// 躺在会话里；而那些从未碰到 socket 的本地失败，却谁也没告诉。
type deliveryState int

const (
	// deliveryDelivered —— 企微确认了这次发送。唯一一个不需要向用户说任何话的
	// 状态；文件就是他们看到的东西。
	deliveryDelivered deliveryState = iota

	// deliveryDefinitelyFailed —— 什么都没到、也不可能到过。要么文件从未变成
	// media_id（上传被拒、或对象读不出来），要么发送本身被拒。原则上可以放心重
	// 试，也可以放心把它说成失败。
	deliveryDefinitelyFailed

	// deliveryUnknown —— 发送帧已发出、裁定没回来。消息可能在会话里。这个状态
	// 绝不能重试：同一个 media_id 发两遍，对方会把文件看两遍、且没有任何可撤销
	// 的手段。
	deliveryUnknown
)

// String 用「代码思考所用的词」给日志点名状态，让读日志的运维不用知道哪个
// errcode 指哪件事，就能把一次未确认的发送和一次被拒的分开。
func (d deliveryState) String() string {
	switch d {
	case deliveryDelivered:
		return "delivered"
	case deliveryDefinitelyFailed:
		return "definitely_failed"
	case deliveryUnknown:
		return "unknown"
	default:
		return "invalid"
	}
}

// 企微对上传材料施加的分类型上限：照片 10MB、视频 10MB、语音 2MB。超出某类型上
// 限的字节仍会上路——作为 file 走，它有四种里最宽的上限——因为「用户能打开的
// 文件卡片」好过「被服务器拒绝的照片」。
const (
	maxOutboundImageBytes = 10 << 20
	maxOutboundVoiceBytes = 2 << 20
	maxOutboundVideoBytes = 10 << 20
)

// attachmentTarget 是一条回答的文件要去的地方：承载它们的 socket 所属安装，以及
// 另一端的会话。
type attachmentTarget struct {
	InstallationID pgtype.UUID
	ChatID         string
	ChatType       int
	// SessionID 只为了在「几分钟后失败」的投递里，仍能在日志与计数器上点出它属
	// 于哪场会话。
	SessionID string
}

// OutboundOption 在构造时配置会话完成订阅者。
type OutboundOption func(*Outbound)

// WithAttachments 打开文件投递。没有它——不带对象存储的部署——回答与从前一模一
// 样地送达，agent 也已在它的 brief 里被告知如此。
func WithAttachments(objects mediaObjectStore) OutboundOption {
	return func(o *Outbound) { o.objects = objects }
}

// mayCarryAttachments 报告这一轮即使 agent 什么都没说、是否也值得跑那些查询。它
// 查的每一样东西都已到手，所以没有存储的部署——或一条不指名消息的事件——不会花
// 掉任何查询。
func (o *Outbound) mayCarryAttachments(e events.Event) bool {
	return o.objects != nil && e.WorkspaceID != "" && chatDoneMessageID(e.Payload) != ""
}

// deliverAttachments 把回答的文件交给它们自己的 goroutine（若有可交的）。它在话
// 说出口之后被调用，随即返回。
// carriesTheReply 表示这些文件就是这条回复的实质——agent 什么都没说、只绑了个文
// 件。为 true 时还没有任何人记录回复结局，本路径恰好欠一个；为 false 时话已落地、
// 回复结局已定，所以这里只动逐文件的计数器。
func (o *Outbound) deliverAttachments(e events.Event, to attachmentTarget, carriesTheReply bool) {
	o.deliverAttachmentsByID(chatDoneMessageID(e.Payload), e.WorkspaceID, to, carriesTheReply)
}

// deliverAttachmentsByID 是给「手上有 id 而没有事件」的调用方用的 deliverAttachments
// ——比如中继路径：事件在另一副本上发布，跨线过来的只有它的标识符。文件本身在这
// 里读——由将要发送它们的那副本读——而不是经 Redis 运送。
func (o *Outbound) deliverAttachmentsByID(rawMessageID, rawWorkspaceID string, to attachmentTarget, carriesTheReply bool) {
	if o.objects == nil || o.senders == nil {
		return
	}
	messageID, err := util.ParseUUID(rawMessageID)
	if err != nil || !messageID.Valid {
		return // 一条没有 assistant 消息的轮次，没有可绑的东西
	}
	workspaceID, err := util.ParseUUID(rawWorkspaceID)
	if err != nil || !workspaceID.Valid {
		return
	}
	if !to.InstallationID.Valid || to.ChatID == "" {
		return
	}
	// 准入在这里占位而不是在 goroutine 内部，因为一个已启动的 goroutine 已经
	// 不是这个上限能约束的了。它要跑的查询也在本闸门的另一侧：数据库慢的时候，
	// 无界的查询和无界的 goroutine 是同一个失败换了顶帽子。
	//
	// 此刻对这一轮还一无所知——有没有绑文件正是查询会告诉我们的——所以这里的拒
	// 绝只记日志、不说出口。在一轮可能根本没带文件时告诉用户「你的文件被丢了」，
	// 正是查询后那道闸门存在要避免的假警报。
	if !o.admitAttachmentDelivery() {
		// 一次调度拒绝，按自己的单位计，其他什么都不计：上面注释是字面意义的——
		// 本闸门在查询前跑，所以它既不知道这轮带几个文件、也不知道带没带。从这里
		// 喂逐文件计数器是编造基数，从这里断言回复结局是编造回复本身：一条带零个
		// 绑定文件的空完成会被计成一次被丢的回复，而查询本会把它归成 nothing_to_say。
		// 这里不可知的事，就不在这里记。
		o.attachmentShed()
		o.logger.Warn("wecom outbound: attachment delivery not admitted, too many already running",
			"installation_id", uuidStringPub(to.InstallationID),
			"admitted", maxAdmittedAttachmentDeliveries)
		return
	}
	o.spawn(func() {
		defer o.releaseAttachmentAdmission()
		ctx, cancel := context.WithTimeout(context.Background(), attachmentBudget)
		defer cancel()
		o.sendAttachments(ctx, messageID, workspaceID, to, carriesTheReply)
	})
}

// sendAttachments 投递绑在一条回答上的每个文件。文件彼此独立：一个失败不拖累其
// 余，而那些「确实没到」的文件所知的全部，在末尾统一说一次而不是各说一次。
//
// 调用方已占过准入，那是约束「跑这个的 goroutine 数」和「下面查询数」的东西。这里
// 限量的是另一样、且刻意放在查询之后：没绑文件的一轮绝不能占用 pending 槽，而
// ——这正是它对用户而非对调度器要紧的原因——一次因缺槽被拒的投递，只能由「已经
// 知道有个文件在等」的东西诚实地报出来。在查询前限量这个阶段，要么对「被丢的文
// 件」保持沉默，要么对「从不存在的文件」报警。
func (o *Outbound) sendAttachments(ctx context.Context, messageID, workspaceID pgtype.UUID, to attachmentTarget, carriesTheReply bool) {
	// replyFailed 记录这条回复的唯一结局，但只在「文件就是回复」时。话已落地时
	// 回复已定，只动逐文件计数器。
	replyFailed := func(reason dropReason, err error) {
		if carriesTheReply {
			o.droppedFor(ctx, to.SessionID, "chat:done", reason, err)
		}
	}
	rows, err := o.q.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{
		ChatMessageID: messageID,
		WorkspaceID:   workspaceID,
	})
	if err != nil {
		o.logger.WarnContext(ctx, "wecom outbound: attachment lookup failed",
			"error", err, "chat_message_id", uuidStringPub(messageID))
		o.tellUser(ctx, to, mediaLookupFailedText)
		replyFailed(dropTransport, err)
		return
	}
	if len(rows) == 0 {
		// mayCarryAttachments 说可能有一个；其实没有。
		if carriesTheReply {
			o.skippedFor(ctx, to.SessionID, skipNothingToSay)
		}
		return
	}
	// 过了这里，已知有文件在等，所以本函数每条出路都必须以「送达或对用户说一句
	// 话」收尾。

	// 当「已发现有文件的投递」积压太多时拒发。下面的信号量约束「同时 RUN」的个
	// 数；这里约束「等它的个数」，而且不同于准入、它能说出丢的是什么，所以用户会
	// 听到。
	if !o.claimAttachmentSlot() {
		// 与准入不同，行是已知的，所以账可以记到精确：每个「不会被送出」的文件
		// 一条——以及当文件就是回复时，回复自己的结局。
		for range rows {
			o.attachmentDropped(ctx, dropAttachmentNotAdmitted, nil)
		}
		replyFailed(dropAttachmentNotAdmitted, nil)
		o.logger.WarnContext(ctx, "wecom outbound: attachment delivery shed, too many already pending",
			"installation_id", uuidStringPub(to.InstallationID),
			"attachments", len(rows),
			"pending", maxPendingAttachmentDeliveries)
		o.tellUser(ctx, to, mediaSendFailedText)
		return
	}
	defer o.releaseAttachmentSlot()

	// 在这里获取信号量，而不是在 spawn 之前。Bus.Publish 在任务完成 goroutine 上
	// 是同步的，在外面阻塞会把完成路径楔住最多一个 attachmentBudget——这正是 spawn
	// 的存在要防的。
	select {
	case attachmentSlots <- struct{}{}:
		defer func() { <-attachmentSlots }()
	case <-ctx.Done():
		// 行是已知的，所以它们每一件都能结算：什么都没发——这是唯一可证「失败在
		// 本地」的情况。
		for range rows {
			o.attachmentDropped(ctx, dropTransport, ctx.Err())
		}
		o.logger.WarnContext(ctx, "wecom outbound: attachment delivery gave up waiting for a slot",
			"installation_id", uuidStringPub(to.InstallationID), "attachments", len(rows))
		// 刻意用新 context：已经过期的那把正是我们到这里的理由，复用它连那句话
		// 也一起丢了。
		o.tellUser(context.WithoutCancel(ctx), to, mediaSendFailedText)
		replyFailed(dropTransport, ctx.Err())
		return
	}

	// 在这里解析而不是从调用方带进来：送达话的那次发送可能发生在几分钟前、那条
	// socket 早已被换掉，而注册表里始终是活的那条。
	sender := o.senders.get(to.InstallationID)
	if sender == nil {
		// 没有可说它的工具——本会承载道歉的那条 socket 正是缺的那条。日志是这事
		// 唯一能去的地方。
		o.logger.WarnContext(ctx, "wecom outbound: no live connection for attachment delivery",
			"installation_id", uuidStringPub(to.InstallationID), "attachments", len(rows))
		for range rows {
			o.attachmentDropped(ctx, dropNoConnection, nil)
		}
		replyFailed(dropNoConnection, nil)
		return
	}
	failed, unknown := 0, 0
	// replyReason 在 worseDropReason 的优先级下汇总各文件「确凿」的原因，作为回复
	// 自己的结局——所以一个被拒的文件也会让回复浮出 platform_refused。未知结局不
	// 进它：一个可能已到的文件不能把回复变成确凿的丢弃。
	var replyReason dropReason
	var replyUnconfirmed string
	for _, row := range rows {
		state, err := o.sendAttachment(ctx, sender, row, to)
		switch state {
		case deliveryDefinitelyFailed:
			failed++
			r := classifyDrop(err)
			o.attachmentDropped(ctx, r, err)
			replyReason = worseDropReason(replyReason, r)
		case deliveryUnknown:
			unknown++
			// 不是失败：帧很可能已到。记在自己的计数器下面，好让丢弃率始终是「确
			// 凿丢弃」的速率。
			r := unconfirmedReason(err)
			if r == "" {
				r = "ack_timeout" // sendOutcome 与 unconfirmedReason 共用一套集合；双保险
			}
			o.attachmentUnconfirmed(ctx, r, err)
			replyUnconfirmed = worseUnconfirmedReason(replyUnconfirmed, r)
		default:
			o.attachmentDelivered()
		}
		if err != nil {
			// 对象的 URL 不进日志：它是「谁拿到就发给谁」的地址。
			o.logger.WarnContext(ctx, "wecom outbound: attachment not confirmed delivered",
				"error", err,
				"delivery", state.String(),
				"installation_id", uuidStringPub(to.InstallationID),
				"attachment_id", uuidStringPub(row.ID),
				"content_type", row.ContentType,
				"size_bytes", row.SizeBytes)
		}
	}
	if carriesTheReply {
		// 文件就是答案。三种诚实的结局：任何文件到了——送达；一个都没到、且至少
		// 有一个确凿失败——按最重的确凿原因记一条丢弃；两头都没有确凿的——记未确
		// 认，因为把一条可能已送达的回复叫成丢弃，会指引运维去重发一条用户可能已
		// 经收到的消息。
		switch {
		case failed+unknown < len(rows):
			o.delivered()
		case failed > 0:
			o.droppedFor(ctx, to.SessionID, "chat:done", replyReason, nil)
		default:
			o.unconfirmedFor(ctx, to.SessionID, "chat:done", replyUnconfirmed, nil)
		}
	}
	// 回答已经上了用户屏幕、而且很可能会提到某个文件。不说话会让他们去找一个从
	// 不来的东西——但对一个确实到了的文件说「它失败了」同样有害，所以每组为自己
	// 说话，未确认的发送绝不借用「确凿」的措辞。
	var lines []string
	if failed > 0 {
		lines = append(lines, mediaSendFailedText)
	}
	if unknown > 0 {
		lines = append(lines, mediaSendUnknownText)
	}
	if len(lines) > 0 {
		o.tellUser(ctx, to, strings.Join(lines, "\n"))
	}
}

// tellUser 往会话里放一句话，尽力而为。每个调用方都已走在「有东西出错了」的路上，
// 所以这里的失败只记日志就丢弃、不上抛——已经没有别的可试了。
func (o *Outbound) tellUser(ctx context.Context, to attachmentTarget, text string) {
	if o.senders == nil {
		return
	}
	sender := o.senders.get(to.InstallationID)
	if sender == nil {
		return
	}
	if err := sender.sendTextCtx(ctx, to.ChatID, to.ChatType, text); err != nil {
		o.logger.WarnContext(ctx, "wecom outbound: could not tell the user about the file",
			"error", err, "installation_id", uuidStringPub(to.InstallationID))
	}
}

// sendAttachment 把一个文件从对象存储送进会话，并报告对它归宿的已知程度。错误是
// 给日志的；状态是告诉用户的东西。
func (o *Outbound) sendAttachment(ctx context.Context, sender *wsSender, row db.Attachment, to attachmentTarget) (deliveryState, error) {
	// 记录下来的尺寸在取任何字节之前先查。超大附件无论如何都会被拒——
	// readObject 会复查实际读到的量，因为列是元数据、对象才是真相——但先把 40 MB
	// 从存储里读出来再拒，是没有受益人的活。
	if row.SizeBytes > maxMediaUploadBytes {
		return deliveryDefinitelyFailed, fmt.Errorf("attachment is %d bytes: %w", row.SizeBytes, errMediaUploadTooLarge)
	}
	data, err := o.readObject(ctx, row.Url)
	if err != nil {
		return deliveryDefinitelyFailed, err
	}
	kind := wecomMediaKind(row.ContentType, row.Filename, len(data))
	name := outboundMediaName(row.Filename, row.ContentType)

	mediaID, err := sender.uploadMedia(ctx, outboundMedia{
		Kind:     kind,
		Filename: name,
		Data:     data,
	})
	if err != nil {
		// 一次失败的上传从未产出 media_id，所以从未有消息被寻址到会话——包括失败
		// 是「finish 那步自己的 ack 丢了」的情况。文件确凿不在这里。
		return deliveryDefinitelyFailed, fmt.Errorf("upload %s: %w", kind, err)
	}
	// 视频是唯一在 media_id 之外还带字段的类型，两个字段都必填。可说的只有文件自
	// 己的名字——附件行不携带标题、agent 的话已经在上面的消息里了。
	err = sender.sendMedia(ctx, to.ChatID, to.ChatType, mediaSend{
		Kind:        kind,
		MediaID:     mediaID,
		Title:       strings.TrimSuffix(name, path.Ext(name)),
		Description: name,
	})
	return sendOutcome(err), err
}

// sendOutcome 读一次媒体推送的错误、看它对消息说了什么。
//
// 唯一要紧的区分：被拒是企微在回答，而「没有回答」不是回答。errAckTimeout 表示
// 帧到了 socket、裁定一直没来，这留下「消息可能已送达」的可能。
//
// 传输层失败也落在同一侧，理由相同、并不更弱。errWriteAttempted 标记的是「进入
// WriteMessage 之后」报出的错误；过了那一点，帧可能已经到对端——半关连接会对写
// 方报 "broken pipe"，而读者其实已有那些字节。只有写之前就失败的（marshal 错误、
// 连接拒绝设 deadline）才可证未送达。
//
// context 错误也落在同一侧，而且比希望的不那么精确。wsSender.request 无论 context
// 是在帧写出之前结束、还是在等裁定期间结束，都返回同一个 ctx.Err()，所以从这里
// 无法区分两者。把这一切都读成 unknown 是代价最小的方向：unknown 永不被重发、且
// 用两头都成立的话来描述，所以一次「从未发生」的发送只是少认领了，而不是一次「确
// 实发生」的发送被否认。
func sendOutcome(err error) deliveryState {
	switch {
	case err == nil:
		return deliveryDelivered
	case errors.Is(err, errAckTimeout),
		errors.Is(err, errWriteAttempted),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return deliveryUnknown
	default:
		return deliveryDefinitelyFailed
	}
}

// readObject 把整个文件拉进内存。它必须是整份：上传在第一批分片发出之前就要声明
// total_size 与 total_chunks，所以这个没法流式。
func (o *Outbound) readObject(ctx context.Context, rawURL string) ([]byte, error) {
	key := o.objects.KeyFromURL(rawURL)
	if key == "" {
		return nil, fmt.Errorf("wecom: attachment is not an object this deployment stores")
	}
	rc, err := o.objects.GetReader(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	defer rc.Close()
	// 多一个字节的余量，好把「恰好读到上限」和「后面还有」区分开。上限是平台的、
	// 不是组帧的，所以读到那里就停，而不是停在分片协议能表达的 50 MB——多出的
	// 30 MB 只会在内存里待上「足够被拒」那么久。
	data, err := io.ReadAll(io.LimitReader(rc, maxMediaUploadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	if len(data) > maxMediaUploadBytes {
		return nil, errMediaUploadTooLarge
	}
	return data, nil
}

// wecomMediaKind 决定告诉企微「这个文件是什么」。
//
// 内容类型打头，因为那是上传者声明的。当它说不出有用的东西——空、或代表「字节」
// 的 octet-stream——时，文件名扩展名是更好的猜测。文件超过某类型上限时降级成
// file 发，而不是发出去被拒。
func wecomMediaKind(contentType, filename string, size int) mediaMsgType {
	ct := baseContentType(contentType)
	if ct == "" || ct == "application/octet-stream" {
		ct = baseContentType(mime.TypeByExtension(path.Ext(filename)))
	}
	switch {
	case strings.HasPrefix(ct, "image/") && size <= maxOutboundImageBytes:
		return mediaTypeImage
	case strings.HasPrefix(ct, "video/") && size <= maxOutboundVideoBytes:
		return mediaTypeVideo
	// 语音只收 AMR。mp3 当语音发会被拒，而当文件发至少点开还能放。
	case ct == "audio/amr" && size <= maxOutboundVoiceBytes:
		return mediaTypeVoice
	default:
		return mediaTypeFile
	}
}

// baseContentType 丢掉内容类型可能带的参数，让 "text/csv; charset=utf-8" 以
// "text/csv" 参与比较。
func baseContentType(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if semi := strings.IndexByte(s, ';'); semi >= 0 {
		s = strings.TrimSpace(s[:semi])
	}
	return s
}

// outboundMediaName 是接收方在文件卡片上看到的名字。它被收成单一路径段——名字
// 要上线，而存下来的文件名不保证是一段——没有扩展名时补一个，因为那是企微判断格
// 式的唯一线索。
func outboundMediaName(filename, contentType string) string {
	name := cleanMediaFilename(filename)
	if name == "" {
		name = "attachment"
	}
	if path.Ext(name) == "" {
		if ext := mediaExtension(baseContentType(contentType)); ext != "" {
			name += ext
		}
	}
	return name
}

// chatDoneMessageID 从 chat:done payload 里取出 assistant 消息 id（类型化
// payload，或经序列化往返后的 map 形态）。这一轮上每个附件都绑在它上面。
func chatDoneMessageID(payload any) string {
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		return p.MessageID
	case map[string]any:
		if s, ok := p["message_id"].(string); ok {
			return s
		}
	}
	return ""
}

// attachmentSlots 限定同时读对象的附件投递数量。
//
// 进程级、不按安装：堆是进程级的，跑多个机器人的部署上按安装设上限只会相乘。每
// 次投递在上传期间持有一个对象，所以这个数决定「峰值驻留附件字节」。
var attachmentSlots = make(chan struct{}, maxConcurrentAttachmentDeliveries)

const (
	// maxConcurrentAttachmentDeliveries 是多少对象可以同时在飞。刻意取小：每个对
	// 象最大可达平台文件上限，而它们共用的 socket 是每机器人一条长连接，更多并发
	// 买来的是排队而不是吞吐。
	maxConcurrentAttachmentDeliveries = 2

	// maxPendingAttachmentDeliveries 约束「已发现文件、正等槽位」的投递。超过它
	// 投递被拒、用户被知会：回答的文本已经到他们手里、附件还在对象存储里，沉默
	// 会让他们等一个不来的文件。
	maxPendingAttachmentDeliveries = 32

	// maxAdmittedAttachmentDeliveries 约束 goroutine 本身、连带它们在「对轮次还一
	// 无所知」时跑的附件查询。取 pending 上限的两倍是作为余量、不是推导量：一轮在
	// 其整个生命周期里持有 admission、却只在查询发现文件后才占 pending 槽，所以一
	// 批「带文件」的积压会先顶到 pending 上限——这个顺序让面向用户的拒发停留在那
	// 条能点出真文件的路径上。
	//
	// 所以顶到本上限并不表示 pending 满了：仍在查询中的轮次持有 admission、不占
	// pending 槽；找不到文件的轮次终其一生持有 admission、从不认领 pending 槽。
	// TestDeliverAttachments_AdmissionBoundsTheLookupStage 是其中的第一例——它把每
	// 个 goroutine 停在查询里、以 pending 为零顶到 admitted 上限。
	maxAdmittedAttachmentDeliveries = 2 * maxPendingAttachmentDeliveries
)

// claimAttachmentSlot 占下一个 pending 槽，或在积压已满时报告。
func (o *Outbound) claimAttachmentSlot() bool {
	o.pendingMu.Lock()
	defer o.pendingMu.Unlock()
	if o.pendingAttachments >= maxPendingAttachmentDeliveries {
		return false
	}
	o.pendingAttachments++
	return true
}

func (o *Outbound) releaseAttachmentSlot() {
	o.pendingMu.Lock()
	defer o.pendingMu.Unlock()
	o.pendingAttachments--
}

// admitAttachmentDelivery 保留启动一个投递 goroutine 的权利，或在「已在跑的大多」
// 时报告。在 spawn 之前占：之后，goroutine 和它的查询已经越过任何它能约束的东西
// 了。
func (o *Outbound) admitAttachmentDelivery() bool {
	o.pendingMu.Lock()
	defer o.pendingMu.Unlock()
	if o.admittedAttachments >= maxAdmittedAttachmentDeliveries {
		return false
	}
	o.admittedAttachments++
	return true
}

func (o *Outbound) releaseAttachmentAdmission() {
	o.pendingMu.Lock()
	defer o.pendingMu.Unlock()
	o.admittedAttachments--
}
