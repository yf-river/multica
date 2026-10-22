package upstream

// ExecutorSystemKey 标记平台托管的同步执行体。全平台只有一条，换运行时只改绑定，
// 不再按运行时前缀新建第二条。
const ExecutorSystemKey = "upstream_sync"

// ExecutorSystemKeyPrefix 是旧版按运行时派生的前缀，仅用于识别迁移前遗留的执行体。
const ExecutorSystemKeyPrefix = "upstream_sync:"
