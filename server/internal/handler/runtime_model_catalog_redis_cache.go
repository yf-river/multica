// 本文件：ModelCatalogCache 的 Redis 实现，让每个 API 副本共享同一份「每台 runtime 的
// 预热模型目录」。没有它，每个节点各存各的 map，用户在负载均衡后面来回跳节点时，大多数
// 打开操作仍要付一次完整的 daemon 往返（与 RedisModelListStore 同一套多节点考量）。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Key 布局——每台 runtime 一个普通字符串 key，TTL = 服务窗口，到期由 Redis 负责回收，
// 没有任何需要清扫的东西：
//
//	mul:runtime_model_catalog:<runtime_id> → JSON 编码的 ModelCatalogSnapshot
//
// 不需要 hash tag：这里每个操作都只碰单个 key，集群槽位共置没有收益（不同于要跑 Lua 脚本
// 的 pending 队列）。
const modelCatalogKeyPrefix = "mul:runtime_model_catalog:"

func modelCatalogKey(runtimeID string) string { return modelCatalogKeyPrefix + runtimeID }

// RedisModelCatalogCache 是 ModelCatalogCache 的多节点实现。
type RedisModelCatalogCache struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewRedisModelCatalogCache(rdb *redis.Client) *RedisModelCatalogCache {
	return &RedisModelCatalogCache{rdb: rdb, ttl: modelCatalogServeWindow}
}

func (c *RedisModelCatalogCache) Get(ctx context.Context, runtimeID string) (*ModelCatalogSnapshot, error) {
	if runtimeID == "" {
		return nil, nil
	}
	raw, err := c.rdb.Get(ctx, modelCatalogKey(runtimeID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get model catalog: %w", err)
	}
	var snapshot ModelCatalogSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		// 解码不了的快照与缓存未命中无法区分——直接丢掉，让下一次 Put 覆盖这个坏值。
		c.rdb.Del(ctx, modelCatalogKey(runtimeID))
		return nil, fmt.Errorf("decode model catalog: %w", err)
	}
	// 防一手「快照的存龄超过了服务窗口」：TTL 虽然来自同一常量，但配置变更或手工写入可能
	// 让两者分叉——按 StoredAt 年龄判断才是契约，TTL 只是手段。
	if snapshot.StoredAt.IsZero() || time.Since(snapshot.StoredAt) > c.ttl {
		return nil, nil
	}
	return &snapshot, nil
}

func (c *RedisModelCatalogCache) Put(ctx context.Context, runtimeID string, models []ModelEntry, unavailable []UnavailableModelEntry, supported bool) error {
	// fallback=false: ReportModelListResult refuses to Put a fallback catalog
	// at all, so anything reaching a cache backend is a real discovery result.
	if runtimeID == "" || !cacheableModelCatalog(models, supported, false) {
		return nil
	}
	snapshot := ModelCatalogSnapshot{
		RuntimeID:         runtimeID,
		Models:            models,
		UnavailableModels: unavailable,
		Supported:         supported,
		StoredAt:          time.Now(),
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal model catalog: %w", err)
	}
	if err := c.rdb.Set(ctx, modelCatalogKey(runtimeID), data, c.ttl).Err(); err != nil {
		return fmt.Errorf("persist model catalog: %w", err)
	}
	return nil
}

func (c *RedisModelCatalogCache) Invalidate(ctx context.Context, runtimeID string) error {
	if runtimeID == "" {
		return nil
	}
	if err := c.rdb.Del(ctx, modelCatalogKey(runtimeID)).Err(); err != nil {
		return fmt.Errorf("invalidate model catalog: %w", err)
	}
	return nil
}
