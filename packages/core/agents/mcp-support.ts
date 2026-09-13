// 支持 MCP 配置的运行时提供商集合：这些后端的执行器会读取 `agent.mcp_config` 并
// 把 MCP 服务器转发给底层 CLI。MCP 配置页面对其它提供商一律隐藏，免得用户存下一个
// 运行时会静默忽略的值。请与 `server/pkg/agent/` 里读取 `ExecOptions.McpConfig` 的
// 后端、以及 `server/internal/daemon/execenv/` 里那些为不经 ExecOptions 收 MCP 配置的
// CLI 物化 MCP 配置的逐任务 preparer 保持同步。
const MCP_SUPPORTED_PROVIDERS = new Set([
  "claude",
  "codebuddy",
  "codearts",
  "codex",
  "cursor",
  "grok",
  "hermes",
  "kimi",
  "reasonix",
  "dsh",
  "kiro",
  "opencode",
  "qoder",
  "qoderclicn",
  "qwen",
  "qwenpaw",
  "mcode",
  "traecli",
  "dim",
  "omp",
]);

/** 该运行时提供商的后端是否真的读取并转发 MCP 配置。 */
export function providerSupportsMcpConfig(provider: string | undefined | null): boolean {
  if (!provider) return false;
  return MCP_SUPPORTED_PROVIDERS.has(provider);
}
