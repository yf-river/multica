// 本文件是 core 包的对外出口（barrel），统一再导出顶层能力供外部使用。

export { useWorkspaceId } from "./hooks";
export { createQueryClient } from "./query-client";
export { QueryProvider } from "./provider";
