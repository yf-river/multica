// 文件职责：实现 `agent mcp` 子命令组——管理「工作区 MCP server 库」到某个
// agent 的指派（list/add/enable/disable/remove），镜像 `agent skills` 的形态。
// 单独放一个文件，是为了保持 cmd_agent.go 的行号稳定（multica 自举 agent 的
// source map 引用了 cmd_agent.go 里的行号）。
package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// agentMcpCmd 是 `agent mcp` 分组命令。工作区库里的 MCP 条目在指派给 agent 之前
// 不生效；每个指派自带开关。这与 agent 自己的 mcp_config（`agent update
// --mcp-config`，存放 agent 私有 server）相互独立，claim 时合并、重名时 agent
// 自己的条目优先。
var agentMcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage which workspace MCP servers an agent uses",
	Long: "Assigns MCP servers from the workspace library to an agent. A library " +
		"entry does nothing until it is added to an agent here, and each " +
		"assignment carries its own on/off toggle — the same shape as workspace " +
		"skills.\n\n" +
		"This is separate from the agent's OWN mcp_config ('agent update " +
		"--mcp-config'), which holds servers private to that agent. Both are " +
		"merged at claim time; the agent's own entry wins on a name collision.",
}

// agentMcpListCmd 列出派给某 agent 的工作区 MCP server。
var agentMcpListCmd = &cobra.Command{
	Use:   "list <agent-id>",
	Short: "List the workspace MCP servers assigned to an agent",
	Args:  exactArgs(1),
	RunE:  runAgentMcpList,
}

// agentMcpAddCmd 给 agent 加一条（默认启用的）server 指派；重复加是 no-op。
var agentMcpAddCmd = &cobra.Command{
	Use:   "add <agent-id> <server-id>",
	Short: "Give a workspace MCP server to an agent",
	Long: "Assigns a workspace MCP server to this agent, enabled. Take the server " +
		"id from 'multica workspace mcp list'. Adding one twice is a no-op.",
	Args: exactArgs(2),
	RunE: runAgentMcpAdd,
}

var agentMcpEnableCmd = &cobra.Command{
	Use:   "enable <agent-id> <server-id>",
	Short: "Turn an assigned MCP server back on for this agent",
	Args:  exactArgs(2),
	RunE:  runAgentMcpEnable,
}

var agentMcpDisableCmd = &cobra.Command{
	Use:   "disable <agent-id> <server-id>",
	Short: "Turn an assigned MCP server off for this agent",
	Long: "Stops the agent from receiving this server without dropping the " +
		"assignment, so turning it back on later is one command.",
	Args: exactArgs(2),
	RunE: runAgentMcpDisable,
}

// agentMcpRemoveCmd 移除指派（工作区库条目本身与其他 agent 的指派不受影响）。
var agentMcpRemoveCmd = &cobra.Command{
	Use:   "remove <agent-id> <server-id>",
	Short: "Take a workspace MCP server away from an agent",
	Long: "Removes the assignment. The workspace library entry itself is " +
		"untouched and other agents keep theirs.",
	Args: exactArgs(2),
	RunE: runAgentMcpRemove,
}

func init() {
	agentCmd.AddCommand(agentMcpCmd)
	agentMcpCmd.AddCommand(agentMcpListCmd)
	agentMcpCmd.AddCommand(agentMcpAddCmd)
	agentMcpCmd.AddCommand(agentMcpEnableCmd)
	agentMcpCmd.AddCommand(agentMcpDisableCmd)
	agentMcpCmd.AddCommand(agentMcpRemoveCmd)

	for _, cmd := range []*cobra.Command{
		agentMcpListCmd, agentMcpAddCmd, agentMcpEnableCmd, agentMcpDisableCmd, agentMcpRemoveCmd,
	} {
		cmd.Flags().String("output", "table", "Output format: table or json")
	}
}

// agentMcpPath 拼 agent 的 MCP 指派端点路径，parts 里每段再 URL 转义一层
// （如 server-id、enabled）。
func agentMcpPath(agentID string, parts ...string) string {
	path := "/api/agents/" + url.PathEscape(agentID) + "/mcp-servers"
	for _, part := range parts {
		path += "/" + url.PathEscape(part)
	}
	return path
}

// runAgentMcpList 执行 `agent mcp list <agent-id>`：列出该 agent 的 MCP 指派。
func runAgentMcpList(cmd *cobra.Command, args []string) error {
	agentID := strings.TrimSpace(args[0])
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var servers []workspaceMcpServer
	if err := client.GetJSON(ctx, agentMcpPath(agentID), &servers); err != nil {
		return fmt.Errorf("list agent mcp servers: %w", err)
	}
	return printWorkspaceMcpServers(cmd, servers)
}

// runAgentMcpAdd 执行 `agent mcp add <agent-id> <server-id>`：加一条启用中的指派。
func runAgentMcpAdd(cmd *cobra.Command, args []string) error {
	agentID, serverID := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var servers []workspaceMcpServer
	body := map[string]any{"server_id": serverID}
	if err := client.PostJSON(ctx, agentMcpPath(agentID), body, &servers); err != nil {
		return fmt.Errorf("add agent mcp server: %w", err)
	}
	return printWorkspaceMcpServers(cmd, servers)
}

// runAgentMcpEnable 启用某条指派。
func runAgentMcpEnable(cmd *cobra.Command, args []string) error {
	return setAgentMcpEnabled(cmd, args, true)
}

// runAgentMcpDisable 停用某条指派（保留指派关系，便于一键再开）。
func runAgentMcpDisable(cmd *cobra.Command, args []string) error {
	return setAgentMcpEnabled(cmd, args, false)
}

// setAgentMcpEnabled 是 enable/disable 的共用实现，PATCH 一条指派的 enabled 位。
func setAgentMcpEnabled(cmd *cobra.Command, args []string, enabled bool) error {
	agentID, serverID := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var servers []workspaceMcpServer
	body := map[string]any{"enabled": enabled}
	if err := client.PutJSON(ctx, agentMcpPath(agentID, serverID, "enabled"), body, &servers); err != nil {
		return fmt.Errorf("update agent mcp server: %w", err)
	}
	return printWorkspaceMcpServers(cmd, servers)
}

// runAgentMcpRemove 执行 `agent mcp remove <agent-id> <server-id>`：移除指派。
func runAgentMcpRemove(cmd *cobra.Command, args []string) error {
	agentID, serverID := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var servers []workspaceMcpServer
	if err := client.DeleteJSONResponse(ctx, agentMcpPath(agentID, serverID), &servers); err != nil {
		return fmt.Errorf("remove agent mcp server: %w", err)
	}
	return printWorkspaceMcpServers(cmd, servers)
}
