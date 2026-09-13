package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// governedLifeTaskTimeout 是“受管辖的 Life 任务”允许执行的最长时间：这类任务用
// Life 上下文而非 provider 自有历史，超时阈值是硬性约定，不随任务配置放大。
const governedLifeTaskTimeout = 10 * time.Minute

// governedLifeMCPConfig 构造一份把当前 multica 可执行文件自己注册成 MCP server 的
// 配置（life 工具），供 Life/companion 类任务把受管辖的能力挂给 agent。
func governedLifeMCPConfig() (json.RawMessage, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve multica executable for Life MCP: %w", err)
	}
	config, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"life": map[string]any{"command": executable, "args": []string{"life", "mcp"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build Life MCP config: %w", err)
	}
	return config, nil
}

// taskExecutionTimeout 决定一次任务执行的超时上限。受管辖的 Life 任务有一个硬性的
// 最大执行时长：若配置值未设置（<=0）或比上限更长，就收敛到 governedLifeTaskTimeout，
// 防止这类任务被配置成无限期运行；其它任务沿用配置值（<=0 表示不设超时）。
func taskExecutionTimeout(configured time.Duration, governedLifeTask bool) time.Duration {
	if governedLifeTask && (configured <= 0 || configured > governedLifeTaskTimeout) {
		return governedLifeTaskTimeout
	}
	return configured
}
