// Agent 模块的公共出口：类型、草稿、在线状态、权限、查询、常量、MCP 支持等统一从这里导出。
export * from "./types";
export * from "./draft";
export * from "./stored-draft";
export * from "./manual-draft-store";
export * from "./builder-protocol";
export * from "./derive-presence";
export * from "./failure-reason";
export * from "./effective-access";
export * from "./queries";
export * from "./use-agent-presence";
export * from "./use-update-agent-allowlist";
export * from "./use-agent-activity";
export * from "./use-workspace-presence-prefetch";
export * from "./constants";
export * from "./conversation-starters";
export * from "./use-customize-conversation-starters-href";
export * from "./visibility-label";
export * from "./use-workspace-agent-availability";
export * from "./mcp-support";
export * from "./runtime-binding";

/** 判断某个 Agent 任务状态是否代表"仍在活跃运行中"（排队/派发/等待/运行）。 */
export function isActiveAgentTaskStatus(status: string): boolean {
  return status === "queued" || status === "dispatched" || status === "waiting_local_directory" || status === "running";
}
