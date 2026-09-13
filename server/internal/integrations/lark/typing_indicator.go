// typing_indicator.go：管理飞书“正在处理（Typing）”表情反应的生命周期，给
// 入站消息一个视觉上的处理中反馈。
package lark

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// typingEmoji 是飞书用于“处理中”指示的 emoji_type，在消息上渲染成一个小
// 打字动画徽标。
const typingEmoji = "Typing"

// typingIndicatorMaxAge 是“消息多老就跳过打字指示”的年龄上限。防止 WebSocket
// 重连重放旧事件时出现过期的“处理中”徽标。两分钟上限既能约束重放窗口，又允许
// 短暂网络中断后自愈。
const typingIndicatorMaxAge = 2 * time.Minute

// TypingIndicatorState 保存移除一个反应所需的标识，以及当初是用哪个安装的
// app 凭据添加它的。installation id 在 add 时刻就记录，因为那是它一定还能
// 解析出来的最后一刻：它可由会话的 channel_chat_session_binding 行反查，
// 而删除会话会在触发 cancel 的同时丢掉那一行，且 cancel 还在发往 Patcher
// 的路上。
type TypingIndicatorState struct {
	MessageID      string
	ReactionID     string
	InstallationID pgtype.UUID

	// installSnapshot 是加反应那一刻的安装行快照，专为“光有 id 已经不够”的
	// 一种场景预留：运行时拆除会在同一个事务里既删安装行又取消任务
	// （handler/runtime.go、DeleteChannelInstallationsBySystemRuntimeAgents），
	// 等 cancel 到达 Clear 时已经没有行可查了。
	//
	// 它只是兜底，不是首选。实时查询能拿到 add 与 clear 之间发生的凭据轮换，
	// 快照拿不到，所以只在行确实不存在时才查它。
	//
	// 它也不削弱“state map 里绝不存放解密后 secret”的不变量：这里持有的与
	// 数据库里是同一个加密 blob，DecryptAppSecret 依然在 clear 时才执行。
	installSnapshot Installation
}

// TypingIndicatorQueries 是 manager 需要的窄 DB 访问面。
type TypingIndicatorQueries interface {
	GetLarkInstallation(ctx context.Context, id pgtype.UUID) (Installation, error)
}

// TypingIndicatorManager 负责飞书入站消息“处理中”反应的全生命周期。消息被
// 成功摄入时，它加一个 Typing 反应；运行结束——无论是有回复、失败还是被取消
// ——它为那个会话清掉反应。
//
// manager 可并发使用。它优雅地容忍缺失/过期的状态：对已有反应的消息再加反应
// 只是多追加一条状态；清理没有跟踪状态的会话是无操作。
type TypingIndicatorManager struct {
	client      APIClient
	credentials CredentialsResolver
	queries     TypingIndicatorQueries
	log         *slog.Logger

	mu     sync.RWMutex
	states map[string][]*TypingIndicatorState // key = chat_session_id string
}

// NewTypingIndicatorManager 构造一个 manager。所有依赖必须非 nil；manager 对
// 空 client / credentials / queries 直接 panic。
func NewTypingIndicatorManager(client APIClient, credentials CredentialsResolver, queries TypingIndicatorQueries, log *slog.Logger) *TypingIndicatorManager {
	if log == nil {
		log = slog.Default()
	}
	return &TypingIndicatorManager{
		client:      client,
		credentials: credentials,
		queries:     queries,
		log:         log,
		states:      make(map[string][]*TypingIndicatorState),
	}
}

// Add 给指定消息发送一个 Typing 反应，并把状态记录到该会话名下。它是同步
// 的——是否放进 detached goroutine 由调用方决定。错误只记录日志并被吞掉。
//
// createTime 是飞书的 epoch 毫秒字符串（InboundMessage.CreateTime）。超过
// typingIndicatorMaxAge 的消息被静默跳过，这样 WebSocket 重放和过期重连不会
// 在早已结束的会话上冒出误导性的“处理中”徽标。
func (m *TypingIndicatorManager) Add(ctx context.Context, inst Installation, chatSessionID pgtype.UUID, messageID string, createTime string) {
	if messageID == "" {
		return
	}
	if isMessageTooOld(createTime) {
		m.log.Debug("lark typing indicator: message too old, skipping",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"create_time", createTime,
		)
		return
	}
	creds, err := m.resolveCredentials(inst)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to resolve credentials",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"err", err,
		)
		return
	}

	reactionID, err := m.client.AddMessageReaction(ctx, AddReactionParams{
		InstallationID: creds,
		MessageID:      messageID,
		EmojiType:      typingEmoji,
	})
	if err != nil {
		m.log.Warn("lark typing indicator: add reaction failed",
			"chat_session_id", uuidString(chatSessionID),
			"message_id", messageID,
			"err", err,
		)
		return
	}

	key := uuidString(chatSessionID)
	m.mu.Lock()
	m.states[key] = append(m.states[key], &TypingIndicatorState{
		MessageID:       messageID,
		ReactionID:      reactionID,
		InstallationID:  inst.ID,
		installSnapshot: inst,
	})
	m.mu.Unlock()

	m.log.Debug("lark typing indicator: reaction added",
		"chat_session_id", key,
		"message_id", messageID,
		"reaction_id", reactionID,
	)
}

// Clear 移除该会话名下所有被跟踪的 Typing 反应并清掉状态条目。它是同步的，
// 保证反应在 agent 的回复发出前就消失，给用户一个干净的画面切换。单次删除
// 失败只记日志，不中断循环。
//
// 凭据取自已记录状态的安装，而不是会话的 binding，因为 clear 可能比 binding
// 活得久：删除会话会在同一个事务里既删 binding 行又取消该会话的任务，而到达
// Patcher 的 task:cancelled 事件是在该事务提交之后才广播的。此时再查 binding
// 会查不到——而状态又被从这里拿走，就没什么可清的了。安装行在会话删除后依然
// 存活。
//
// 但安装行不会熬过运行时拆除（拆除在同一事务里把它们删掉）——所以每条状态
// 同时携带 add 时刻的安装快照，仅在行确实不存在时使用。见 TypingIndicatorState。
func (m *TypingIndicatorManager) Clear(ctx context.Context, chatSessionID pgtype.UUID) {
	key := uuidString(chatSessionID)
	m.mu.Lock()
	states := m.states[key]
	delete(m.states, key)
	m.mu.Unlock()

	if len(states) == 0 {
		return
	}

	// 同一会话的反应通常共享一个安装，所以解析出的凭据做了 memo；
	// 若会话中途被重绑到另一个安装，仍会通过当初加反应的那个 app 清理所有
	// 反应。记 nil 条目表示该安装解析失败，避免每个反应都重试一次。
	resolved := make(map[string]*InstallationCredentials, 1)
	for _, s := range states {
		if s.ReactionID == "" {
			continue
		}
		instKey := uuidString(s.InstallationID)
		creds, seen := resolved[instKey]
		if !seen {
			creds = m.credentialsForInstallation(ctx, key, s.InstallationID, s.installSnapshot)
			resolved[instKey] = creds
		}
		if creds == nil {
			continue
		}
		if err := m.client.DeleteMessageReaction(ctx, DeleteReactionParams{
			InstallationID: *creds,
			MessageID:      s.MessageID,
			ReactionID:     s.ReactionID,
		}); err != nil {
			m.log.Warn("lark typing indicator: delete reaction failed",
				"chat_session_id", key,
				"message_id", s.MessageID,
				"reaction_id", s.ReactionID,
				"err", err,
			)
			continue
		}
		m.log.Debug("lark typing indicator: reaction removed",
			"chat_session_id", key,
			"message_id", s.MessageID,
			"reaction_id", s.ReactionID,
		)
	}
}

// credentialsForInstallation loads an installation row and decrypts its app
// secret. Nil means the clear cannot proceed for that installation; the reason
// is already logged. The decrypted secret exists only from here on, never in the
// state map.
// credentialsForInstallation 加载安装行并解密其 app secret。返回 nil 表示
// 该安装无法继续清理（原因已记日志）。解密后的 secret 只存在于此刻，绝不落
// 进 state map。
func (m *TypingIndicatorManager) credentialsForInstallation(ctx context.Context, sessionKey string, id pgtype.UUID, snapshot Installation) *InstallationCredentials {
	inst, err := m.queries.GetLarkInstallation(ctx, id)
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows) && snapshot.ID.Valid:
		// 行不存在了——在这条路径上意味着运行时拆除在取消这些任务的同一个
		// 事务里删掉了它。它加的反应还留在消息上，而这是唯一能把它摘下来的
		// 东西了。
		m.log.Debug("lark typing indicator: installation gone, clearing from the snapshot taken at add time",
			"chat_session_id", sessionKey,
			"installation_id", uuidString(id),
		)
		inst = snapshot
	default:
		m.log.Warn("lark typing indicator: failed to lookup installation for clear",
			"chat_session_id", sessionKey,
			"installation_id", uuidString(id),
			"err", err,
		)
		return nil
	}
	creds, err := m.resolveCredentials(inst)
	if err != nil {
		m.log.Warn("lark typing indicator: failed to resolve credentials for clear",
			"chat_session_id", sessionKey,
			"installation_id", uuidString(id),
			"err", err,
		)
		return nil
	}
	return &creds
}

// isMessageTooOld 把飞书事件里的 epoch 毫秒字符串（create_time）解析成时间，
// 判断该消息是否已超过 typingIndicatorMaxAge。空串或解析失败按“不老”处理，
// 避免把刚好到达的新消息误判成重放。
func isMessageTooOld(createTime string) bool {
	if createTime == "" {
		return false
	}
	ms, err := strconv.ParseInt(createTime, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.UnixMilli(ms)) > typingIndicatorMaxAge
}

// resolveCredentials 从 Installation 行解密 app secret，组装成调飞书 API 用
// 的 InstallationCredentials（tenant_key 存在则一并带上）。
func (m *TypingIndicatorManager) resolveCredentials(inst Installation) (InstallationCredentials, error) {
	secret, err := m.credentials.DecryptAppSecret(inst)
	if err != nil {
		return InstallationCredentials{}, err
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: secret,
		Region:    RegionOrDefault(inst.Region),
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}
	return creds, nil
}
