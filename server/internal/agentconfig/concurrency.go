// Package agentconfig 提供 agent 配置相关的常量与校验逻辑。
package agentconfig

import "fmt"

const (
	// DefaultMaxConcurrentTasks 默认的"单个 agent 最多并发任务数"。
	DefaultMaxConcurrentTasks int32 = 6
	// MinMaxConcurrentTasks 该配置允许的最小值。
	MinMaxConcurrentTasks int32 = 1
	// MaxMaxConcurrentTasks 该配置允许的最大值。
	MaxMaxConcurrentTasks int32 = 50
)

// IsValidMaxConcurrentTasks 判断并发任务数是否落在 [1, 50] 合法区间内。
func IsValidMaxConcurrentTasks(value int32) bool {
	return value >= MinMaxConcurrentTasks && value <= MaxMaxConcurrentTasks
}

// ValidateMaxConcurrentTasks 校验并发任务数，非法时返回带边界的错误信息。
func ValidateMaxConcurrentTasks(value int32) error {
	if !IsValidMaxConcurrentTasks(value) {
		return fmt.Errorf(
			"must be between %d and %d",
			MinMaxConcurrentTasks,
			MaxMaxConcurrentTasks,
		)
	}
	return nil
}
