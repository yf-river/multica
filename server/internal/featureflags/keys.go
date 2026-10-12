// Package featureflags 集中定义服务端各功能开关（feature flag）的键，并提供
// 读取开关状态的便捷封装与给前端下发公开开关列表的求值函数。
package featureflags

import (
	"context"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

const (
	// ComposioMCPApps 控制 Composio 应用管理 UI，以及——连同它依赖的 MUL-3963
	// permission_mode / invocation_targets 访问模型——agent 创建流程里对齐后的
	// Private / Public-to 选择器。该访问模型的存在是为了给 Composio 共享把关，
	// 所以两者用同一开关一起上线。
	ComposioMCPApps = "composio_mcp_apps"
	// PluginsV1 在第一个产品切片内部试用期间控制面向用户的 Plugin 目录与生命周期
	// 管理 API。刻意不控制已固定的 Task/Run 执行：关闭发现与管理能力绝不能改动
	// 一条已在飞行中的不可变执行清单。
	PluginsV1 = "plugins_v1"
)

// frontendPublicFlags 是需要暴露给前端求值的公开开关键集合。
var frontendPublicFlags = []string{
	ComposioMCPApps,
	PluginsV1,
}

// ComposioMCPAppsEnabled 快捷判断"Composio MCP 应用"开关是否开启。
func ComposioMCPAppsEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, ComposioMCPApps, false)
}

// PluginsV1Enabled 快捷判断"插件 V1"开关是否开启。
func PluginsV1Enabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, PluginsV1, false)
}

// EvaluateFrontendPublicFlags 一次性求值所有前端公开开关，返回 key→bool 的映射，
// 供启动时注入到前端。
func EvaluateFrontendPublicFlags(ctx context.Context, flags *featureflag.Service) map[string]bool {
	out := make(map[string]bool, len(frontendPublicFlags))
	for _, key := range frontendPublicFlags {
		out[key] = flags.IsEnabled(ctx, key, false)
	}
	return out
}
