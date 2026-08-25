// Keep in sync with providers that consume ExecOptions.McpConfig or materialize it in execenv.
const MCP_SUPPORTED_PROVIDERS = new Set([
  "claude",
  "codex",
  "cursor",
  "hermes",
  "kimi",
  "kiro",
  "opencode",
  "openclaw",
]);

export function providerSupportsMcpConfig(provider: string | undefined | null): boolean {
  if (!provider) return false;
  return MCP_SUPPORTED_PROVIDERS.has(provider);
}
