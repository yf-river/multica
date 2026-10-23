package upstream

import "strings"

// ExecutorSystemKey 是不带运行时的短键，只在查找时兼容更早版本可能写入的行；
// 本版本的新建和改绑都写 ExecutorSystemKeyFor 生成的键，全平台仍只有一条执行体。
const ExecutorSystemKey = "upstream_sync"

// ExecutorSystemKeyPrefix 是执行体键的前缀。键仍按运行时派生：旧程序按同一个键查找
// 执行体，改键会让升级期间仍在服务的旧版另建一条 Agent 和自动化。
const ExecutorSystemKeyPrefix = "upstream_sync:"

// ExecutorSystemKeyFor 返回绑定到该运行时的执行体键。换运行时同步改键，
// 旧程序按新键也能找到同一个执行体。
func ExecutorSystemKeyFor(runtimeID string) string { return ExecutorSystemKeyPrefix + runtimeID }

// ExecutorOrphanPrefix 标记被替换下来的旧执行体：行保留（历史任务仍引用），
// 但不再被当作当前执行体，也不与新的键重复。
const ExecutorOrphanPrefix = "upstream_sync_orphan:"

// IsExecutorSystemKey 识别同步执行体：既认固定键，也认旧版按运行时派生的键。
func IsExecutorSystemKey(key string) bool {
	return key == ExecutorSystemKey || strings.HasPrefix(key, ExecutorSystemKeyPrefix)
}
