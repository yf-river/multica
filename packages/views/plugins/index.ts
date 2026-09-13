/**
 * plugins（插件）模块的公共出口。
 * 集中导出插件面板/弹窗挂载组件、surface 宿主桥与文档构造、hook 手动操作、
 * MCP 授权审批等 UI 与逻辑。
 */
export { PluginPanelSection } from "./plugin-panel-section";
export { PluginSurfaceFrame } from "./plugin-surface-frame";
export { buildSurfaceFrameDocument } from "./surface-document";
export {
  PluginHookMenuItems,
  collectManualHookActions,
  pluginHookActionKey,
  usePluginHookActions,
  useRunPluginHook,
} from "./plugin-hook-actions";
export type { PluginHookAction } from "./plugin-hook-actions";
export {
  PluginModalMenuItems,
  PluginModalSurface,
  collectModalSurfaces,
  pluginModalKey,
  usePluginModalSurfaces,
} from "./plugin-modal-surface";
export type { PluginModalTarget } from "./plugin-modal-surface";
export { PluginHookActivity, PluginScheduleActivity, summarizeInvocations } from "./plugin-hook-activity";
export { PluginMCPApproval, mcpHooks, initialSelection } from "./plugin-mcp-approval";
