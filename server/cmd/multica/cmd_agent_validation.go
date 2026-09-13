// 文件职责：agent 相关 flag 的本地校验辅助，独立成小文件让校验逻辑与命令定义
// 分离、便于测试直接引用。
package main

import (
	"fmt"

	"github.com/multica-ai/multica/server/internal/agentconfig"
)

// validateAgentMaxConcurrentTasksFlag 校验 --max-concurrent-tasks 取值是否落在
// agentconfig 规定的合法区间，非法时报错并回显收到的值。
func validateAgentMaxConcurrentTasksFlag(value int32) error {
	if err := agentconfig.ValidateMaxConcurrentTasks(value); err != nil {
		return fmt.Errorf(
			"--max-concurrent-tasks %w (got %d)",
			err,
			value,
		)
	}
	return nil
}
