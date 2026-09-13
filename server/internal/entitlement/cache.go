// cache.go 实现 entitlement 的「按 workspace 键控的 LRU 策略缓存」，以及缓存条目里
// 的时效语义（freshUntil 新鲜期 / staleUntil 陈旧观察期 / retryAfter 失败重试等待）。
package entitlement

import (
	"container/list"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// errVersionRegression 表示云端返回的订阅版本号低于本地缓存（乱序响应或回滚）。
var errVersionRegression = errors.New("entitlement subscription version regression")

// policySnapshot 是某个 workspace 某份策略的不可变快照。
type policySnapshot struct {
	policyRevision      int64
	subscriptionVersion int64
	cloudValidUntil     time.Time
	gates               map[GateName]Gate
}

// cacheEntry 是缓存里一条 workspace 策略及其时效窗口。
type cacheEntry struct {
	hasPolicy  bool // 是否真正持有策略（false 表示只有失败标记）
	policy     policySnapshot
	freshUntil time.Time // 在此之前策略是「新鲜」的，可正常 enforce
	staleUntil time.Time // 在 freshUntil 之后、此点之前只能降级为 observe
	retryAfter time.Time // 在此之前跳过刷新（失败退避）
}

// cacheItem 是链表节点里存的实际值（带 workspace 键，方便淘汰时回删 map）。
type cacheItem struct {
	workspaceID uuid.UUID
	entry       cacheEntry
}

// policyCache 是按 workspace 键控的 LRU。workspace 键绝不由响应数据推断而来，
// 防止一个租户的策略落进另一个租户的缓存槽位。
type policyCache struct {
	mu         sync.Mutex
	maxEntries int
	order      *list.List
	entries    map[uuid.UUID]*list.Element
}

func newPolicyCache(maxEntries int) *policyCache {
	return &policyCache{
		maxEntries: maxEntries,
		order:      list.New(),
		entries:    make(map[uuid.UUID]*list.Element, maxEntries),
	}
}

// get 按 workspace 取缓存；命中时把该条目挪到链表头（LRU 语义）。
func (c *policyCache) get(workspaceID uuid.UUID) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[workspaceID]
	if !ok {
		return cacheEntry{}, false
	}
	c.order.MoveToFront(elem)
	return elem.Value.(*cacheItem).entry, true
}

// put 写入缓存，返回实际存下的条目与可能的错误。版本守卫防止「乱序到达的订阅响应」
// 覆盖一份「在有界陈旧期内仍可用于观察」的快照；一旦该窗口结束，接受云端当前响应
// 能让服务从回滚中恢复，而不至于陷入重试风暴。
func (c *policyCache) put(workspaceID uuid.UUID, entry cacheEntry, receivedAt time.Time) (cacheEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[workspaceID]; ok {
		current := elem.Value.(*cacheItem).entry
		// 版本守卫：在有界陈旧观察窗口结束前，不允许乱序/回滚的订阅响应覆盖当前快照。
		// 窗口结束后接受云端当前响应，让服务能从回滚中恢复而不至于陷入重试风暴。
		guardVersions := current.hasPolicy && receivedAt.Before(current.staleUntil)
		if guardVersions && entry.policy.subscriptionVersion < current.policy.subscriptionVersion {
			return current, errVersionRegression
		}
		elem.Value.(*cacheItem).entry = entry
		c.order.MoveToFront(elem)
		return entry, nil
	}

	c.insert(workspaceID, entry)
	return entry, nil
}

// markFailure 记录一次刷新失败：在缓存里记下 retryAfter（没有缓存的 workspace 也会
// 插入一条纯失败标记，防止失败风暴）。
func (c *policyCache) markFailure(workspaceID uuid.UUID, retryAfter time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[workspaceID]; ok {
		item := elem.Value.(*cacheItem)
		item.entry.retryAfter = retryAfter
		c.order.MoveToFront(elem)
		return
	}
	c.insert(workspaceID, cacheEntry{retryAfter: retryAfter})
}

// insert 插入新条目并做 LRU 淘汰。调用方必须已持有 c.mu。
func (c *policyCache) insert(workspaceID uuid.UUID, entry cacheEntry) {
	elem := c.order.PushFront(&cacheItem{workspaceID: workspaceID, entry: entry})
	c.entries[workspaceID] = elem
	if c.order.Len() > c.maxEntries {
		oldest := c.order.Back()
		item := oldest.Value.(*cacheItem)
		delete(c.entries, item.workspaceID)
		c.order.Remove(oldest)
	}
}

func (c *policyCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
