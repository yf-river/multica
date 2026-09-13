// 本文件是 MCP 配置文件（mcpServers/mcp 两处容器）的纯数据模型与操作：
// 从任意来源对象里枚举/新增/删除“受管的 MCP 服务”，并识别其传输类型。

/** MCP 配置里服务可以存放的两个容器键（Claude 用 mcpServers，通用规范用 mcp）。 */
export type McpConfigContainer = "mcpServers" | "mcp";

/** 一个“受管”的 MCP 服务：名称、原始配置、所在容器、识别出的传输类型、启用状态。 */
export type ManagedMcpServer = {
  name: string;
  config: Record<string, unknown>;
  container: McpConfigContainer;
  transport: string;
  enabled: boolean;
};

/** 类型守卫：判断是否为普通对象（非 null、非数组）。 */
export function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

/** 从服务配置里推断传输类型：有 command / 标成 local|stdio → stdio；sse → sse；
 *  有 url / 标成 remote|http|streamable-http → http；否则 unknown。 */
export function mcpTransport(config: Record<string, unknown>): string {
  const type = typeof config.type === "string" ? config.type.toLowerCase() : "";
  if (config.command || type === "local" || type === "stdio") return "stdio";
  if (type === "sse") return "sse";
  if (
    config.url ||
    type === "remote" ||
    type === "http" ||
    type === "streamable-http"
  ) {
    return "http";
  }
  return "unknown";
}

/** 从配置对象中枚举全部受管 MCP 服务（两容器都扫，先到先得、按名称排序）。
 *  enable 状态默认 true，除非显式 enabled=false 或 disabled=true。 */
export function listManagedMcpServers(value: unknown): ManagedMcpServer[] {
  if (!isRecord(value)) return [];

  const out: ManagedMcpServer[] = [];
  const seen = new Set<string>();
  for (const container of ["mcpServers", "mcp"] as const) {
    const raw = value[container];
    if (!isRecord(raw)) continue;
    for (const [name, entry] of Object.entries(raw)) {
      if (seen.has(name) || !isRecord(entry)) continue;
      seen.add(name);
      out.push({
        name,
        config: entry,
        container,
        transport: mcpTransport(entry),
        enabled: entry.enabled !== false && entry.disabled !== true,
      });
    }
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

/** 新增或更新一个服务：若 previous 存在，先从它原所在容器删除，再写入新名/配置。 */
export function upsertManagedMcpServer(
  value: unknown,
  previous: ManagedMcpServer | null,
  name: string,
  config: Record<string, unknown>,
): Record<string, unknown> {
  const document = isRecord(value) ? { ...value } : {};
  const container: McpConfigContainer = previous?.container ?? "mcpServers";

  if (previous) {
    const previousMap = isRecord(document[previous.container])
      ? { ...(document[previous.container] as Record<string, unknown>) }
      : {};
    delete previousMap[previous.name];
    document[previous.container] = previousMap;
  }

  const target = isRecord(document[container])
    ? { ...(document[container] as Record<string, unknown>) }
    : {};
  target[name] = config;
  document[container] = target;
  return document;
}

/** 删除一个服务：容器删空则整个容器键一并移除；文档因此为空时返回 null。 */
export function removeManagedMcpServer(
  value: unknown,
  server: ManagedMcpServer,
): Record<string, unknown> | null {
  if (!isRecord(value)) return null;
  const document = { ...value };
  const container = isRecord(document[server.container])
    ? { ...(document[server.container] as Record<string, unknown>) }
    : {};
  delete container[server.name];

  if (Object.keys(container).length > 0) document[server.container] = container;
  else delete document[server.container];

  return Object.keys(document).length > 0 ? document : null;
}
