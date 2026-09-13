// 本文件定义「运行时心跳活跃度」的存取接口与实现：用带 TTL 的 Redis key 记录某台
// runtime「刚刚心跳过」，让高频心跳路径不用每次去改写 agent_runtime.last_seen_at 数据库行。
// DB 行仍是状态流转的权威，也是存储不可用时的兜底。设计说明见下。
package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// LivenessStore 跟踪短命的「这台 runtime 最近心跳过」记录。它让心跳热路径写一个带 TTL 的
// Redis key 就行，不必每次重写 agent_runtime.last_seen_at。DB 行仍然是状态流转的权威，
// 也是该存储不可用时的兜底。
//
// 接口刻意做得很小、出错时无副作用：调用方拿到 Touch 的错误或 IsAlive 的 ok=false 时，
// 必须回退到纯 DB 行为（每次心跳都改写 last_seen_at；清扫器按 SQL 的过期窗口判断）。这样
// 无论 Redis 缺失还是亚健康，系统端到端都正确，且无需任何按调用点配置。
type LivenessStore interface {
	// Available 报告该存储是否接了真实后端。false 表示调用方应把 DB 当作唯一事实来源——
	// 不可用存储上的其它方法都是 no-op。
	Available() bool

	// Touch 为 runtimeID 记录一次新鲜心跳，带给定 TTL。后端失败时返回错误；调用方出错时
	// 应回退为一次 DB 心跳写入。
	Touch(ctx context.Context, runtimeID string, ttl time.Duration) error

	// IsAliveBatch 同时查询多个 runtime 的活跃度。返回的 map 覆盖每个输入 id（不活跃的
	// 一律 false）。ok=false 表示后端出错或不可用；调用方必须回退到 DB 过期窗口判断。
	IsAliveBatch(ctx context.Context, runtimeIDs []string) (alive map[string]bool, ok bool)

	// Forget 删掉 runtimeID 的活跃度记录。用于注销时以及清扫器确认某台 runtime 离线后。
	// 尽力而为：错误只记日志不返回，反正 TTL 也会回收这个 key。
	Forget(ctx context.Context, runtimeID string)
}

// noopLivenessStore 是默认实现——没接入任何 Redis client 时使用。所有方法都是 no-op；
// Available() 返回 false，让调用方知道要走 DB 路径。
type noopLivenessStore struct{}

// NewNoopLivenessStore 返回一个永远报告不可用的 LivenessStore。调用方应默认用它，在装配
// 阶段再换成真实实现。
func NewNoopLivenessStore() LivenessStore { return noopLivenessStore{} }

func (noopLivenessStore) Available() bool { return false }

func (noopLivenessStore) Touch(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (noopLivenessStore) IsAliveBatch(_ context.Context, _ []string) (map[string]bool, bool) {
	return nil, false
}

func (noopLivenessStore) Forget(_ context.Context, _ string) {}

// runtimeLivenessKeyPrefix 是 runtime 活跃度记录的 Redis key 前缀，与其它 runtime 存储的
// 命名空间保持一致（mul:update:*、mul:model_list:*、mul:local_skill_list:*）。
const runtimeLivenessKeyPrefix = "mul:runtime:hb:"

func runtimeLivenessKey(runtimeID string) string {
	return runtimeLivenessKeyPrefix + runtimeID
}

// RedisLivenessStore 每次 runtime 心跳写一个带 TTL 的 key。key 未过期就表示「这台 runtime
// 此刻活着」；清扫器在把 DB 里过期的 runtime 标为离线前会先问它。
type RedisLivenessStore struct {
	rdb *redis.Client
}

func NewRedisLivenessStore(rdb *redis.Client) *RedisLivenessStore {
	return &RedisLivenessStore{rdb: rdb}
}

func (s *RedisLivenessStore) Available() bool { return s != nil && s.rdb != nil }

func (s *RedisLivenessStore) Touch(ctx context.Context, runtimeID string, ttl time.Duration) error {
	if !s.Available() {
		return errors.New("redis liveness store: unavailable")
	}
	if runtimeID == "" {
		return errors.New("redis liveness store: empty runtime id")
	}
	if err := s.rdb.Set(ctx, runtimeLivenessKey(runtimeID), "1", ttl).Err(); err != nil {
		return fmt.Errorf("liveness touch: %w", err)
	}
	return nil
}

func (s *RedisLivenessStore) IsAliveBatch(ctx context.Context, runtimeIDs []string) (map[string]bool, bool) {
	if !s.Available() || len(runtimeIDs) == 0 {
		return map[string]bool{}, s.Available()
	}
	keys := make([]string, len(runtimeIDs))
	for i, id := range runtimeIDs {
		keys[i] = runtimeLivenessKey(id)
	}
	values, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		slog.Warn("liveness mget failed; falling back to DB",
			"error", err, "count", len(keys))
		return nil, false
	}
	out := make(map[string]bool, len(runtimeIDs))
	for i, id := range runtimeIDs {
		out[id] = values[i] != nil
	}
	return out, true
}

func (s *RedisLivenessStore) Forget(ctx context.Context, runtimeID string) {
	if !s.Available() || runtimeID == "" {
		return
	}
	if err := s.rdb.Del(ctx, runtimeLivenessKey(runtimeID)).Err(); err != nil {
		slog.Warn("liveness forget failed", "error", err, "runtime_id", runtimeID)
	}
}
