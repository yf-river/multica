// ws_chunk_assembler.go：把飞书长连接上按 message_id 拆分多帧的大事件负载
// 缓存并拼接成完整 JSON。
package lark

import (
	"strconv"
	"sync"
	"time"
)

// chunkAssembler 按 message_id 缓冲飞书的多帧 data 负载，等所有分块到齐后
// 返回拼接字节。飞书把大事件负载拆成多个二进制 Frame，header 如下：
//
//	sum        — 总块数（>=2 表示多帧）
//	seq        — 本块在消息内的 0 起始序号
//	message_id — N 个分块共用的键
//
// SDK 参考实现（larksuite/oapi-sdk-go/v3/ws/client.go combine()）给半成品状态
// 用了 5 秒 TTL——更早的都视为被抛弃而丢弃。没有 TTL 的话，飞书侧在中间某一
// 块上丢包，缓冲的字节就会永远泄漏。
//
// 组装器可并发安全使用：单个实例服务所有 supervisor goroutine。状态只在进程
// 内存中——分块不会跨服务器重启保留（飞书在重连时会重新下发完整事件），所以
// 不需要持久化。
type chunkAssembler struct {
	ttl time.Duration
	now func() time.Time

	mu  sync.Mutex
	buf map[string]*chunkEntry
}

// chunkEntry 记录一条消息当前的拼接进度。
type chunkEntry struct {
	chunks   [][]byte // 按 seq 索引；nil 空位 = 还没到
	received int      // 已到非空块数量
	deadline time.Time
}

// newChunkAssembler 返回一个带指定半成品状态 TTL 的组装器。ttl 非正数时退回
// SDK 默认值（5 秒）。
func newChunkAssembler(ttl time.Duration, now func() time.Time) *chunkAssembler {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &chunkAssembler{
		ttl: ttl,
		now: now,
		buf: make(map[string]*chunkEntry),
	}
}

// admit 记录一个分块并返回：
//
//   - (payload, true)  —— 所有分块已到齐；payload 是按 seq 顺序拼接的字节，
//     该消息的缓冲条目已删除。
//   - (nil, false)     —— 半成品状态；调用方此时不应下发，也【不应】回 ACK
//     （镜像 SDK 行为：只在完整组装后 ACK，这样服务器可以重试整个事件）。
//
// admit 对畸形输入（sum<=0、seq<0、seq>=sum、重复 seq）直接返回 (nil, false)
// 并当作忽略该分块。生产上这些条件不会触发（飞书服务器侧已保证），但函数保持
// 防御——一个坏 header 绝不能污染下一条事件的缓冲。
func (a *chunkAssembler) admit(messageID string, sum, seq int, payload []byte) ([]byte, bool) {
	if messageID == "" || sum <= 0 || seq < 0 || seq >= sum {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	// 每次 admit 惰性 GC（单次 map 遍历，代价小），省得单独维护清扫
	// goroutine。规模受在途 message_id 数量限制，本身就很小（飞书会限制每条
	// 连接上的并发分块事件数）。
	a.gcExpiredLocked()

	entry, ok := a.buf[messageID]
	if !ok {
		entry = &chunkEntry{
			chunks:   make([][]byte, sum),
			deadline: a.now().Add(a.ttl),
		}
		a.buf[messageID] = entry
	}
	// 重复分块（网络重试 / 飞书乱序重发）：静默覆盖。飞书保证同一
	// (message_id, seq) 的负载字节稳定，所以重复 admit 不会改变最终拼接结果。
	if entry.chunks[seq] == nil {
		entry.received++
	}
	entry.chunks[seq] = append([]byte(nil), payload...)
	// 滑动 deadline：每来一个新块都顺延该消息的 TTL，因为飞书可能把一个多帧
	// 事件摊开在几百毫秒内；静态 5 秒针对的是“只收到第 0 块就没了”，而不是
	// “0..N-1 块在 4.9 秒内陆续到达”。
	entry.deadline = a.now().Add(a.ttl)

	if entry.received < len(entry.chunks) {
		return nil, false
	}

	total := 0
	for _, c := range entry.chunks {
		total += len(c)
	}
	out := make([]byte, 0, total)
	for _, c := range entry.chunks {
		out = append(out, c...)
	}
	delete(a.buf, messageID)
	return out, true
}

// gcExpired 移除已过 deadline 的条目。暴露给测试用；生产路径在 admit 里惰性执行。
func (a *chunkAssembler) gcExpired() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gcExpiredLocked()
}

// gcExpiredLocked 是 gcExpired 的实现，要求调用方已持锁。
func (a *chunkAssembler) gcExpiredLocked() int {
	now := a.now()
	n := 0
	for id, e := range a.buf {
		if now.After(e.deadline) {
			delete(a.buf, id)
			n++
		}
	}
	return n
}

// pendingCount 报告当前缓冲中“未组装完”的消息数。测试与运维看板会用到。
func (a *chunkAssembler) pendingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.buf)
}

// parseChunkHeaders 从 Frame 的 header 里提取分块元信息。缺失或不可解析的
// header 产出 (sum=0, seq=0, "")——connector 读成“单帧事件”，完全绕开组装器。
// 飞书文档称非分块事件不携带 sum（实际等于 1）；SDK 的 GetInt 在 header 缺失
// 时返回 0。
func parseChunkHeaders(f *Frame) (sum, seq int, messageID string) {
	if f == nil {
		return 0, 0, ""
	}
	if s := f.HeaderValue(FrameHeaderSumKey); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			sum = n
		}
	}
	if s := f.HeaderValue(FrameHeaderSeqKey); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			seq = n
		}
	}
	messageID = f.HeaderValue(FrameHeaderMessageIDKey)
	return
}
