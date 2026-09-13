// senders_registry.go —— 一张进程级的「installation_id → 存活 wsSender」
// 小映射表。wecomChannel.Connect 在进入时登记、退出时清除；OutboundReplier
// 和 Outbound 按 installation id 查表，把 aibot_send_msg 压进入站循环所持有
// 的同一条 socket（aibot 没有 REST 出站通道，所有写出都走 WebSocket）。
// wecomChannel.Send 不是读者——它直接返回 ErrSendNotSupported。
//
// 为什么用注册表而不是把 sender 挂在 wecomChannel 上：OutboundReplier 在
// 启动时就用共享的 engine.Router 建好了，它没有按安装划分的 Channel 句柄。
// 引擎调 Replier.Reply 时传入的是 engine.ResolvedInstallation，里面只有
// installation id，没有 Channel。这张注册表就是中间的接缝：让启动时就存在的
// Replier 够得到每条安装的存活连接，而不用把 Channel 一路穿针引线进引擎。
package wecom

import (
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
)

// sendersRegistry 是一张 goroutine 安全的 installation_id → wsSender 映射。
// SendersRegistry 之所以要导出，只是为了让启动流程能在中继的分片读者启动前
// 先造出一张；其余方法一律不导出。
type SendersRegistry = sendersRegistry

type sendersRegistry struct {
	mu    sync.RWMutex
	byKey map[string]*wsSender
}

// newSendersRegistry 构造一张空注册表。
func newSendersRegistry() *sendersRegistry {
	return &sendersRegistry{byKey: make(map[string]*wsSender)}
}

// NewSendersRegistry 是公开构造函数，启动时用它把同一张注册表同时注入
// wecom 的 ChannelDeps（写侧）和 OutboundReplier（读侧）。保持导出是为了
// router.go 接线时不必去 import 一个未导出类型。
func NewSendersRegistry() *SendersRegistry { return newSendersRegistry() }

func (r *sendersRegistry) set(id pgtype.UUID, s *wsSender) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKey[util.UUIDToString(id)] = s
}

// clear 移除该安装的条目，但仅当 s 仍是这个 id 下登记的 sender 时才删。
// 正在退出的旧一代连接不能把接替自己的新连接挤掉：Connect 在进入时登记、
// 用 defer 在退出时清除，所以当租约在旧 socket 仍排空期间发生翻转时，新旧
// 两会短暂重叠，输家的 defer 会在赢家 set 之后才跑。此时若无条件 delete，
// 注册表会在一条健康连接尚在的情况下被清空，所有出站推送都解析成 nil——
// 机器人静默哑火，日志里却没有任何线索，直到下次重连碰巧重新登记。
//
// dingtalk_channel.go:74 用 `CompareAndSwap(c, nil)` 守护同样的交接；slack
// 和飞书根本没有注册表，因为它们的出站是 REST。WeCom 是唯一曾经无条件删除
// 的平台。
func (r *sendersRegistry) clear(id pgtype.UUID, s *wsSender) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := util.UUIDToString(id)
	if cur, ok := r.byKey[key]; ok && cur != s {
		return
	}
	delete(r.byKey, key)
}

// get 返回某条安装当前存活的 wsSender；没有任何连接被持有时返回 nil。
// 调用方必须把 nil 当作「连接未就绪」——Supervisor 可能正处在租约翻转后的
// 重连中。
func (r *sendersRegistry) get(id pgtype.UUID) *wsSender {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byKey[util.UUIDToString(id)]
}
