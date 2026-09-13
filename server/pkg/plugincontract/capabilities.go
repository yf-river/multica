package plugincontract

// 本文件职责：定义「本 host 构建当前真正跑得动哪些能力」的清单
// （Capabilities / HostCapabilities），以及在安装前把 manifest 声明与这份
// 清单对账的 CheckCapabilities。宿主侧机制是分片落地的，能力门确保「声明了但
// 跑不了」的贡献在安装期就大声失败，而不是装了之后永远不触发。

import (
	"fmt"
	"sort"
	"strings"
)

// Capabilities 是本 host 构建当前真正跑得动的能力集。manifest schema 从首个
// 版本就完整定义，发布者永远不会追一个移动的契约；但 host 的机制是一片一片
// 落地的。manifest 声明了而这里没有列出的任何东西，都会在安装期大声失败——
// 一个被静默忽略的贡献会看起来「已安装」却永不触发。
//
// 要开启某项，请在同一份改动里同时落地它的运行时。
type Capabilities struct {
	SurfaceTypes  map[string]bool
	HookTriggers  map[string]bool
	HookTransport map[string]bool
	ResourceTypes map[string]bool
}

// HostCapabilities 是当前已上线的能力集。
//
// Action API + issue/sidebar surface 随 surface 运行时一起落地；hook 随 hook
// 引擎落地；agent trigger、mcp 传输与 skill resource 随 agent 集成一起落地。
func HostCapabilities() Capabilities {
	return Capabilities{
		// issue_panel 挂到 PluginPanelSection；modal 由 manual hook 动作打开。
		// sidebar_panel 保持关闭——host 没有它的落点，而开启一个 host 渲染
		// 不了的 surface，等于装一个「静默永不出现」的插件，这个门禁挡的
		// 正是它。
		SurfaceTypes: map[string]bool{
			SurfaceIssuePanel: true,
			SurfaceModal:      true,
		},
		// agent trigger 不是 host 驱动的调用点：hook 作为 MCP 工具呈现给
		// agent，由 agent 决定调不调。负责把它渲染成工具的 daemon 侧服务是
		// daemon/plugin_hook_mcp.go。
		HookTriggers: map[string]bool{
			TriggerUI:       true,
			TriggerManual:   true,
			TriggerEvent:    true,
			TriggerAgent:    true,
			TriggerSchedule: true,
		},
		// http 调用一个声明的端点；mcp 采纳外部 MCP server 的工具——这正是
		// 它带一个「按 schema 摘要钉住工具」的审批步骤的原因，见
		// service/plugin_mcp_transport.go。装插件在这里不是授权，批准那些
		// 工具才是。
		HookTransport: map[string]bool{TransportHTTP: true, TransportMCP: true},
		// skill resource 两个方向都不是调用——它是一份在安装时写进现有 skill
		// 表、卸载时移除的 SKILL.md。service.InstallSkillResources 干这件事。
		ResourceTypes: map[string]bool{ResourceSkill: true},
	}
}

// ErrCapabilityUnavailable 描述本构建还跑不了的贡献。
type ErrCapabilityUnavailable struct {
	Missing []string
}

func (e *ErrCapabilityUnavailable) Error() string {
	return fmt.Sprintf("this Multica version does not support: %s", strings.Join(e.Missing, ", "))
}

// CheckCapabilities 报告 manifest 声明的每项贡献里，host 跑不了的那些。
// 一次全部返回，让管理员看到完整差距，而不是一次一次安装才逐个发现。
func (m Manifest) CheckCapabilities(host Capabilities) error {
	missing := map[string]bool{}
	for _, surface := range m.Contributes.Surfaces {
		if !host.SurfaceTypes[surface.Type] {
			missing["surface "+surface.Type] = true
		}
	}
	for _, hook := range m.Contributes.Hooks {
		for _, trigger := range hook.Triggers {
			if !host.HookTriggers[trigger] {
				missing["hook trigger "+trigger] = true
			}
		}
		if !host.HookTransport[hook.Transport.Type] {
			missing["hook transport "+hook.Transport.Type] = true
		}
	}
	for _, resource := range m.Contributes.Resources {
		if !host.ResourceTypes[resource.Type] {
			missing["resource "+resource.Type] = true
		}
	}
	if len(missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	return &ErrCapabilityUnavailable{Missing: names}
}
