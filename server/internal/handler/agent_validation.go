// 本文件集中 agent 的 max_concurrent_tasks 字段校验与缺省值解析：把「是否显式
// 提供 / 是否为 JSON null」的判定留在 PATCH 的 rawFields 层面，值本身委托给
// agentconfig 包做范围校验。
package handler

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/multica-ai/multica/server/internal/agentconfig"
)

// validateAgentMaxConcurrentTasks 把 agent 专属的错误前缀包到 agentconfig 的
// 范围校验结果上，让 API 错误信息点名是哪个字段违规。
func validateAgentMaxConcurrentTasks(value int32) error {
	if err := agentconfig.ValidateMaxConcurrentTasks(value); err != nil {
		return fmt.Errorf("max_concurrent_tasks %w", err)
	}
	return nil
}

// defaultAndValidateAgentMaxConcurrentTasks 解析 PATCH 里的可选字段：字段缺失或
// 显式 JSON null → 落默认值；否则校验调用方提供的值。返回错误时 *value 不可用。
func defaultAndValidateAgentMaxConcurrentTasks(rawFields map[string]json.RawMessage, value *int32) error {
	raw, provided := rawFields["max_concurrent_tasks"]
	if !provided || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*value = agentconfig.DefaultMaxConcurrentTasks
		return nil
	}
	return validateAgentMaxConcurrentTasks(*value)
}
