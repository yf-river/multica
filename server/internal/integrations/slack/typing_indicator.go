// typing_indicator.go：入站消息的「处理中」指示灯——agent 干活时在用户消息上加
// 个 👀 表情，跑完再摘掉。
//
// Slack 没有飞书那种动态的 "typing" 反应，这里约定用一个所有工作区都内置的表情
// 👀（"eyes"，含义「看见了，正在办」）当指示灯。生命周期：消息被摄取时 Add 打上
// 表情；agent 这次运行无论以哪种方式结束——ChatDone、TaskFailed 还是
// TaskCancelled——Clear 都会把它摘掉。镜像 lark.TypingIndicatorManager：状态按
// chat_session_id 记在进程内存里，Clear 时才从库里现解 bot 令牌（add 与 clear 之
// 间内存里只留安装 id，绝不存令牌）；所有失败只记日志、绝不向上抛——指示灯是
// 尽力而为的装饰，不能挡路或搞挂真正的回复。
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// typingEmoji 是 agent 工作期间盖在用户消息上的 Slack 反应名，当「处理中」指示
// 灯用。改这一个常量就能整体换掉指示灯。注意：装的 Slack 应用需要 reactions:write
// 权限，反应才加得上去；没权限时加反应只是失败并被记日志，不影响业务。
const typingEmoji = "eyes"

// typingIndicatorMaxAge 限制「多老的消息还会被打灯」：超过这个时间就不打，免得
// Socket Mode 重连时把旧事件回放出来的、早就聊完的对话又盖上「处理中」的章。
// 与飞书保持一致。
const typingIndicatorMaxAge = 2 * time.Minute

// reactionAPI 是指示灯需要的极简 Slack 反应操作面。*slack.Client 直接满足它；
// 测试注入假实现。
type reactionAPI interface {
	AddReactionContext(ctx context.Context, name string, item slack.ItemRef) error
	RemoveReactionContext(ctx context.Context, name string, item slack.ItemRef) error
}

// typingState 是摘反应所需的全部信息：Slack 按「表情名 + item 引用」定位反应、
// 没有反应 id 可存，所以这里记 (channel, 消息 ts) 这一对，外加当初打灯用的是哪个
// 安装的 bot 令牌。安装 id 在 Add 那一刻记录，因为那是它【必然还查得到】的最后
// 时机——它藏在会话的 channel_chat_session_binding 里，而删除会话会连带删掉那行
// 绑定，可删除触发的取消事件此刻还在赶往本管理器的路上。
// configSnapshot 是打灯当时该安装的加密 config 快照，用于唯一一种「光有 id 不够」
// 的情形：运行时销毁会在取消任务的那同一事务里删掉安装行（handler/runtime.go 的
// DeleteChannelInstallationsBySystemRuntimeAgents），等取消到达 Clear 时，库里
// 已经没有那行了。
//
// 快照只是【兜底】而非首选——实时查询能感知 add 与 clear 之间发生的凭据轮换，
// 快照不能；所以只在行真的没了时才用它。它持有的仍是数据库里同款加密 blob，
// bot 令牌只在清除发生的这一刻被解密。
type typingState struct {
	ChannelID      string
	MessageTS      string
	InstallationID pgtype.UUID
	configSnapshot []byte
}

// TypingIndicatorQueries 是管理器 Clear 时解析安装 bot 令牌所需的窄 DB 面。
// *db.Queries 满足它。
type TypingIndicatorQueries interface {
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
}

// TypingIndicatorManager 管理入站 Slack 消息的「处理中」反应：消息被摄取时加一个
// 👀，agent 的运行无论怎么结束——EventChatDone、EventTaskFailed 或
// EventTaskCancelled——都把它摘掉。
//
// 镜像 lark.TypingIndicatorManager：状态以 chat_session_id 为键记在进程内存；
// Clear 时从库里重新解析 bot 令牌（add 与 clear 之间 map 里只留安装 id、绝不存
// 令牌）；所有失败只记日志并吞掉——指示灯是尽力而为，绝不能阻塞或搞挂真正的回复。
type TypingIndicatorManager struct {
	q       TypingIndicatorQueries
	decrypt Decrypter
	log     *slog.Logger
	newAPI  func(creds credentials) reactionAPI

	mu     sync.RWMutex
	states map[string][]typingState // 键 = chat_session_id 的字符串形态
}

// NewTypingIndicatorManager 基于生成查询与 bot 令牌解密器构造管理器。Slack API
// 客户端按调用现建：用安装解密后的 bot 令牌（xoxb-），与出站发送器同款做法。
func NewTypingIndicatorManager(q TypingIndicatorQueries, decrypt Decrypter, logger *slog.Logger) *TypingIndicatorManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &TypingIndicatorManager{
		q:       q,
		decrypt: decrypt,
		log:     logger,
		newAPI:  func(c credentials) reactionAPI { return slack.New(c.BotToken) },
		states:  make(map[string][]typingState),
	}
}

// Add 给刚摄取的这条消息打上反应，并把状态记到该聊天会话名下。inst 是已解析的
// 安装行，它的 Config blob 装着加密的 bot 令牌。本方法同步执行——Router 在一个
// 脱离的、有超时的 goroutine 里调用它。错误只记日志并吞掉。
func (m *TypingIndicatorManager) Add(ctx context.Context, inst db.ChannelInstallation, sessionID pgtype.UUID, channelID, messageTS string) {
	if channelID == "" || messageTS == "" {
		return
	}
	if isMessageTooOld(messageTS) {
		m.log.Debug("slack typing indicator: message too old, skipping",
			"chat_session_id", util.UUIDToString(sessionID), "message_ts", messageTS)
		return
	}
	creds, err := decodeCredentials(inst.Config, m.decrypt)
	if err != nil {
		m.log.Warn("slack typing indicator: decode credentials failed",
			"chat_session_id", util.UUIDToString(sessionID), "err", err)
		return
	}
	if err := m.newAPI(creds).AddReactionContext(ctx, typingEmoji, slack.NewRefToMessage(channelID, messageTS)); err != nil {
		m.log.Warn("slack typing indicator: add reaction failed",
			"chat_session_id", util.UUIDToString(sessionID), "message_ts", messageTS, "err", err)
		return
	}
	key := util.UUIDToString(sessionID)
	m.mu.Lock()
	m.states[key] = append(m.states[key], typingState{ChannelID: channelID, MessageTS: messageTS, InstallationID: inst.ID, configSnapshot: inst.Config})
	m.mu.Unlock()
}

// Clear 摘掉该聊天会话名下所有被跟踪的反应并清掉状态。对每条状态，bot 令牌都从
// 安装行重新解析——add 与 clear 之间内存里不存任何解密后的令牌。个别失败只记
// 日志、不中断整个循环。全程尽力而为。
//
// 安装行是直接从状态里读的、而不是经会话绑定现查，因为一次 Clear 可能晚于那条
// 绑定的寿命：删除聊天会话时，绑定行会在取消该会话任务的那【同一事务】里被删，
// 而到达本管理器的 task:cancelled 事件是在那事务提交【之后】才广播的。按绑定查会
// 扑空；而状态此刻又已经从这里取走，一旦扑空就再没有东西可清了。安装行比会话
// 活得更久。
func (m *TypingIndicatorManager) Clear(ctx context.Context, sessionID pgtype.UUID) {
	key := util.UUIDToString(sessionID)
	m.mu.Lock()
	states := m.states[key]
	delete(m.states, key)
	m.mu.Unlock()
	if len(states) == 0 {
		return
	}

	// 同一会话的反应通常来自同一个安装，所以把解析出的客户端按安装缓存一下；若
	// 会话中途重绑到别的安装，仍然通过各自「打灯的那个应用」把所有反应摘掉。nil
	// 条目表示那个安装解析失败了，缓存住以免每条反应都白试一次。
	apis := make(map[string]reactionAPI, 1)
	for _, s := range states {
		instKey := util.UUIDToString(s.InstallationID)
		api, resolved := apis[instKey]
		if !resolved {
			var err error
			api, err = m.apiForInstallation(ctx, s.InstallationID, s.configSnapshot)
			if err != nil {
				m.log.Warn("slack typing indicator: resolve installation for clear failed",
					"chat_session_id", key, "installation_id", instKey, "err", err)
			}
			apis[instKey] = api
		}
		if api == nil {
			continue
		}
		if err := api.RemoveReactionContext(ctx, typingEmoji, slack.NewRefToMessage(s.ChannelID, s.MessageTS)); err != nil {
			m.log.Warn("slack typing indicator: remove reaction failed",
				"chat_session_id", key, "message_ts", s.MessageTS, "err", err)
		}
	}
}

// apiForInstallation 加载一行安装并把它的加密 config 变成反应客户端。解密出的
// bot 令牌只在这条调用里存活。
func (m *TypingIndicatorManager) apiForInstallation(ctx context.Context, id pgtype.UUID, snapshot []byte) (reactionAPI, error) {
	config := snapshot
	inst, err := m.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          id,
		ChannelType: string(TypeSlack),
	})
	switch {
	case err == nil:
		config = inst.Config
	case errors.Is(err, pgx.ErrNoRows) && len(snapshot) > 0:
		// 行没了。走到这里通常就是运行时销毁在取消这批任务的那个事务里把它删了；
		// 反应还留在消息上，快照是唯一还能把它摘下来的东西。
	default:
		return nil, fmt.Errorf("lookup installation: %w", err)
	}
	creds, err := decodeCredentials(config, m.decrypt)
	if err != nil {
		return nil, fmt.Errorf("decode credentials: %w", err)
	}
	return m.newAPI(creds), nil
}

// Register 订阅所有能结束一次运行的 task-lifecycle 事件，这样无论运行怎么结束，
// 反应都会被摘掉。出站回复订阅器只处理 EventChatDone，所以另两种结束只有这里会
// 摘反应。
//
// EventTaskCancelled 必须订阅：否则被取消的运行会让 👀 永远留在用户消息上——
// 取消不发布 chat-done、也不发布 task-failed，除了这里没有任何地方会去摘它。
//
// task:cancelled 由 CancelTask、其后的排队补取消、agent/issue 级批量取消、运行时
// 与成员吊销、以及删除聊天会话各自逐行广播一次。其中「删除聊天会话」以前什么都不
// 广播：BroadcastCancelledTasks 曾通过任务的 chat_session 反查 workspace，而那一行
// 正是它自己的事务刚删掉的；没有 workspace 的事件在到达总线前就被丢弃。现在改为
// 从调用方拿 workspace。
//
// 仍有两个洞，且都不是「漏了订阅」：
//
// 归档 agent 会取消其任务但不逐行广播——理由是 agent:archived 事件已经让每个客户
// 端的任务列表整体失效（handler/agent.go 的 ArchiveAgent）。而没有任何客户端刷新
// 会顺手摘掉 Slack 消息上的反应，所以运行中归档 agent 会把 👀 留在原地。
//
// 另一个洞：结束事件跑得比加反应还快时就什么都清不掉——Add 要等 Slack 调用返回
// 之后才记状态，此时 Clear 看到的是空 map，等反应真正加上去，已经没有东西来摘它
// 了。Router 在脱离的 goroutine 里加反应，因此取消或极快的运行会先到。这个问题先
// 于 task:cancelled 就存在（chat-done 与 task-failed 同样会抢跑），要彻底修需要给
// 每个会话一个代数（generation），让 Add 的调用返回时可以核对——那是另一处独立
// 改动。
//
// 启动时对一条新总线调用一次；务必注册在出站订阅器之前，这样 EventChatDone 到来
// 时反应先摘、回复后发（总线投递是同步的、按订阅顺序执行）。
func (m *TypingIndicatorManager) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, m.handleEvent)
	bus.Subscribe(protocol.EventTaskFailed, m.handleEvent)
	bus.Subscribe(protocol.EventTaskCancelled, m.handleEvent)
}

func (m *TypingIndicatorManager) handleEvent(e events.Event) {
	sessionID, ok := chatSessionIDFromEvent(e)
	if !ok {
		// issue / autopilot 任务不带 chat_session——没有可摘的反应。
		return
	}
	// 总线投递是同步的，给反应调用加超时，让一个卡死的 Slack HTTP 请求不至于把
	// 发布侧钉住。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.Clear(ctx, sessionID)
}

// chatSessionIDFromEvent 从 task-lifecycle 事件里找回聊天会话 id。EventChatDone
// 会把它放上信封；EventTaskFailed 只在广播负载 map 里带它（且仅限聊天任务），所以
// 两处都要查。EventTaskCancelled 的各发布方都同时设这两处。
func chatSessionIDFromEvent(e events.Event) (pgtype.UUID, bool) {
	if e.ChatSessionID != "" {
		if id, err := util.ParseUUID(e.ChatSessionID); err == nil && id.Valid {
			return id, true
		}
	}
	if m, ok := e.Payload.(map[string]any); ok {
		if s, _ := m["chat_session_id"].(string); s != "" {
			if id, err := util.ParseUUID(s); err == nil && id.Valid {
				return id, true
			}
		}
	}
	return pgtype.UUID{}, false
}

// isMessageTooOld 判断一条 Slack 消息 ts（"<秒>.<微秒>"）是否老过
// typingIndicatorMaxAge。畸形或空的 ts 一律视为新鲜（不跳过）——宁可多打个灯，
// 也别漏掉一条真消息。
func isMessageTooOld(ts string) bool {
	if ts == "" {
		return false
	}
	secs, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(0, int64(secs*float64(time.Second)))) > typingIndicatorMaxAge
}
