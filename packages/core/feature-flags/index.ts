// 本文件是 @multica/core/feature-flags 模块的对外出口（barrel）。
//
// 保持清单最小化——每新增一个导出，就多一份要在整个 monorepo 里维持的契约。
// 只有当真实调用方出现时才往里加。

export type {
  Decision,
  EvalContext,
  PercentRollout,
  Provider,
  Reason,
  Rule,
} from "./types";

export { FeatureFlagService } from "./service";
export { StaticProvider } from "./static-provider";
export { ChainProvider } from "./chain-provider";
export {
  BILLING_WORKSPACE_SUBSCRIPTIONS_FLAG,
  COMPOSIO_MCP_APPS_FLAG,
  PLUGINS_V1_FLAG,
} from "./keys";
export {
  FeatureFlagsProvider,
  useFeatureFlagService,
  useFlag,
  useVariant,
} from "./context";

// 哈希辅助函数也对外导出，供测试以及想绕过 Provider 直接复用分桶逻辑的调用方
// （这种情况很少；出现通常意味着该调用方应该改用 Service）。
export { bucketFor, inPercent } from "./hash";
