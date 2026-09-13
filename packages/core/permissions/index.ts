// 本文件是 permissions（权限）模块的对外入口（barrel）。
//
// 只导出视图层当前正在消费的内容。完整纯规则集在 `./rules`，直接对测试与
// 未来界面可用。往公共 API 加新规则时沿用同一「最小暴露」模式——有调用方才
// 导出。
export type {
  Decision,
  DecisionReason,
  PermissionContext,
} from "./types";

export { canAssignAgentToIssue, canEditAgent } from "./rules";

export {
  useAgentPermissions,
  useSkillPermissions,
} from "./use-resource-permissions";
export { useCurrentMember } from "./use-current-member";
