// 本文件：runtime 模型目录（model catalog）缓存。列出某台 runtime 支持哪些模型要走一趟
// 用户机器，这里用「过期即重新校验」（stale-while-revalidate）的缓存把大多数打开秒回。
// 头部的长注释是设计总述（两个窗口各司其职），随后的快照/接口/判决策略即本文件主体。
package handler

import (
	"context"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Runtime 模型目录缓存（过期即重新校验 stale-while-revalidate）
// ---------------------------------------------------------------------------
//
// 列出某台 runtime 的模型是一次到用户机器的往返：请求要等 daemon 的下一个心跳，daemon
// 再调用 provider CLI（或走 ACP 握手）才回报。即便有 pending 工作的推送提示（MUL-5444），
// 在填表时会反复打开的这个 UI（切换 runtime → 看模型 → 切回来）上依然是数秒延迟。
//
// 目录本身只在用户升级 CLI、登录不同账号或改 provider 配置时变化，是教科书级的
// stale-while-revalidate 对象：立刻用最后一次拿到的可靠快照应答，后台再刷新，让下一次
// 打开也是热的。
//
// 两个窗口分工不同，只有其中一个决定新鲜度：
//   - modelCatalogRevalidateAfter 是新鲜度旋钮。应答比它旧的快照会同时排一个后台刷新，
//     所以一次打开之后，CLI 升级就会收敛，无论服务窗口多长。
//   - modelCatalogServeWindow 只限制「没人用的快照」活多久，以及某人只打开过一次选择器、
//     之后再没回来时，答案能旧到什么程度。它刻意按天计：后台没有任何东西在保温，浏览器
//     自己的 react-query 缓存随标签页关闭而消失，Agent CLI 的升级节奏是按天算的——按分钟
//     计的窗口会让每天第一次打开都落空（正是这个缓存要消灭的数秒等待），却又换不来真正
//     的新鲜度。
//
// 窗口不是无界的，因为一次*失败*的上报刻意保留快照（瞬态发现失败不能把选择器清空）。
// 对发现一直失败的 runtime（CLI 卸载、已登出），过期是最终让陈旧目录退役的唯一机制。
//
// 只缓存成功、非空、`supported` 的目录。空列表几乎总是瞬态发现失败（CLI 未登录、超时）——
// 缓存它会把选择器钉死在空列表（与 daemon 端 agent.cachedDiscovery 同一套推理）。

const (
	// modelCatalogServeWindow 决定一条已缓存目录最长能应答 list-models 请求多久而无需等
	// daemon。刻意按天计（MUL-5444）：见上面新鲜度讨论——每份超过 modelCatalogRevalidateAfter
	// 的快照都会自己排一个刷新，所以这个窗口限制的是「无人使用条目的寿命」和「只打开一次
	// 的最坏情况」，不是活跃用户会遇到的陈旧度。
	modelCatalogServeWindow = 24 * time.Hour
	// modelCatalogRevalidateAfter 是超过该年龄、从缓存应答时还要顺带排一个后台刷新的阈值。
	// 这才是真正让目录保持诚实的旋钮；保持它够短。
	modelCatalogRevalidateAfter = 60 * time.Second
)

// ModelCatalogSnapshot 是某台 runtime 最后一次拿到的可靠模型列表。
type ModelCatalogSnapshot struct {
	RuntimeID string       `json:"runtime_id"`
	Models    []ModelEntry `json:"models"`
	Supported bool         `json:"supported"`
	StoredAt  time.Time    `json:"stored_at"`
}

// Age 报告快照是多久前采集的。
func (s *ModelCatalogSnapshot) Age(now time.Time) time.Duration {
	if s == nil {
		return 0
	}
	return now.Sub(s.StoredAt)
}

// ModelCatalogCache 按 runtime 保存最后一次成功的模型目录。两个方法在调用方眼里都是
// 尽力而为：Get 出错 = 「走慢路径答」，Put 出错 = 「下次打开会冷」。两者都不能导致
// 一个请求失败。
//
// 实现必须可并发安全使用。
type ModelCatalogCache interface {
	Get(ctx context.Context, runtimeID string) (*ModelCatalogSnapshot, error)
	Put(ctx context.Context, runtimeID string, models []ModelEntry, supported bool) error
	// Invalidate 删掉该 runtime 的任何快照。当缓存目录不再可信时使用（如 runtime 行已删除）。
	Invalidate(ctx context.Context, runtimeID string) error
}

// cacheableModelCatalog 判断一次完成的发现结果是否值得记住。`supported=false` 的 runtime
// 根本没有选择器；空目录按「瞬态失败」而不是权威的「这台 runtime 没有模型」处理。
//
// `fallback` 补上了上述两个检查留下的洞：好几个 provider 在发现失败时用非空的静态占位
// 目录应答，它能绕过空目录检查、被存成 last-known-good——于是一次瞬态失败就把 runtime
// 从未宣告过的目录钉满 24h 服务窗口。对 codebuddy 而言，占位目录与真实目录没有任何一个
// id 重合，每次选择都会选到一个 CLI 拒绝的 id（MUL-5549）。
func cacheableModelCatalog(models []ModelEntry, supported, fallback bool) bool {
	return supported && !fallback && len(models) > 0
}

// modelCatalogCacheAction 是一次完成的发现上报应对 runtime 的缓存目录采取的动作。
type modelCatalogCacheAction int

const (
	// modelCatalogCacheStore 把上报写为新 last-known-good。
	modelCatalogCacheStore modelCatalogCacheAction = iota
	// modelCatalogCacheDrop 丢弃任何快照：该上报是权威的，说明 runtime 不再宣告我们持有
	// 的那份目录。
	modelCatalogCacheDrop
	// modelCatalogCacheKeep 缓存原样不动：上报不具权威性，既不值得存储、也不构成丢弃已有
	// 真实目录的理由。
	modelCatalogCacheKeep
)

// modelCatalogCacheDecision 把一次完成的上报映射到对应缓存动作。
//
// fallback 情形是 MUL-5549 的修复点，刻意 Keep 而非 Drop：静态占位目录说明不了这台
// runtime 支持什么，让它把真实目录挤掉等于把一次瞬态发现失败变成降级。这与 `failed`
// 上报已有的处理一致——让瞬态失败期间仍用 last-known-good 应答，正是这个缓存存在的意义。
func modelCatalogCacheDecision(models []ModelEntry, supported, fallback bool) modelCatalogCacheAction {
	if fallback {
		return modelCatalogCacheKeep
	}
	if cacheableModelCatalog(models, supported, fallback) {
		return modelCatalogCacheStore
	}
	return modelCatalogCacheDrop
}

// cloneModelEntries 深拷贝一份目录，让内存后端交出去的值不会被调用方改到共享缓存上。
// 浅拷贝切片不够：ModelEntry 里带着 *ModelThinking（它又有自己的 level 切片）和
// ServiceTiers 切片，浅拷贝仍会别名指向缓存对象。Redis 后端走 JSON 往返天然免掉这份
// 拷贝；两个实现返回的值是否独立，行为必须一致。
func cloneModelEntries(models []ModelEntry) []ModelEntry {
	if models == nil {
		return nil
	}
	out := make([]ModelEntry, len(models))
	for i, m := range models {
		clone := m
		if m.Thinking != nil {
			thinking := *m.Thinking
			if m.Thinking.SupportedLevels != nil {
				thinking.SupportedLevels = append([]ThinkingLevel(nil), m.Thinking.SupportedLevels...)
			}
			clone.Thinking = &thinking
		}
		if m.ServiceTiers != nil {
			clone.ServiceTiers = append([]ModelServiceTier(nil), m.ServiceTiers...)
		}
		out[i] = clone
	}
	return out
}

// InMemoryModelCatalogCache 是单节点实现，自托管和测试够用；多节点部署应改用 Redis 后端，
// 让所有 API 副本共享一份热目录。
type InMemoryModelCatalogCache struct {
	mu        sync.Mutex
	entries   map[string]ModelCatalogSnapshot
	retainFor time.Duration
}

func NewInMemoryModelCatalogCache() *InMemoryModelCatalogCache {
	return &InMemoryModelCatalogCache{
		entries:   make(map[string]ModelCatalogSnapshot),
		retainFor: modelCatalogServeWindow,
	}
}

func (c *InMemoryModelCatalogCache) Get(_ context.Context, runtimeID string) (*ModelCatalogSnapshot, error) {
	if runtimeID == "" {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[runtimeID]
	if !ok {
		return nil, nil
	}
	if time.Since(entry.StoredAt) > c.retainFor {
		delete(c.entries, runtimeID)
		return nil, nil
	}
	// 拷贝一份返回，调用方改响应不会弄脏缓存。
	snapshot := entry
	snapshot.Models = cloneModelEntries(entry.Models)
	return &snapshot, nil
}

func (c *InMemoryModelCatalogCache) Put(_ context.Context, runtimeID string, models []ModelEntry, supported bool) error {
	// fallback=false: ReportModelListResult refuses to Put a fallback catalog
	// at all, so anything reaching a cache backend is a real discovery result.
	if runtimeID == "" || !cacheableModelCatalog(models, supported, false) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// 顺便做 GC，清掉过期条目，避免 map 随 runtime 来来去去无限膨胀。
	now := time.Now()
	for id, entry := range c.entries {
		if now.Sub(entry.StoredAt) > c.retainFor {
			delete(c.entries, id)
		}
	}

	c.entries[runtimeID] = ModelCatalogSnapshot{
		RuntimeID: runtimeID,
		Models:    cloneModelEntries(models),
		Supported: supported,
		StoredAt:  now,
	}
	return nil
}

func (c *InMemoryModelCatalogCache) Invalidate(_ context.Context, runtimeID string) error {
	if runtimeID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, runtimeID)
	return nil
}
