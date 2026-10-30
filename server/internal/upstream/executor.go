package upstream

import "strings"

// ExecutorSystemKey 是不带运行时的短键，只在查找时兼容更早版本可能写入的行；
// 项目执行体使用 executorKey，历史任务仍可通过旧键识别。
const ExecutorSystemKey = "upstream_sync"

// ExecutorSystemKeyPrefix 同时标识项目执行体与旧版运行时执行体。
const ExecutorSystemKeyPrefix = "upstream_sync:"

// ExecutorSystemKeyFor 解析旧版按运行时绑定的身份键。
func ExecutorSystemKeyFor(runtimeID string) string { return ExecutorSystemKeyPrefix + runtimeID }

// ExecutorOrphanPrefix 标记被替换下来的旧执行体：行保留（历史任务仍引用），
// 但不再被当作当前执行体，也不与新的键重复。
const ExecutorOrphanPrefix = "upstream_sync_orphan:"

// IsExecutorSystemKey 识别同步执行体：既认固定键，也认旧版按运行时派生的键。
func IsExecutorSystemKey(key string) bool {
	return key == ExecutorSystemKey || strings.HasPrefix(key, ExecutorSystemKeyPrefix)
}

func (s *Service) executorKey() string { return ExecutorSystemKeyPrefix + "project:" + s.Project() }
