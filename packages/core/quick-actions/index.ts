// 本文件是 quick-actions（快捷动作）模块的对外出口（barrel）。

export { quickActionKeys, quickActionListOptions } from "./queries";
export {
  useCreateQuickAction,
  useUpdateQuickAction,
  useDeleteQuickAction,
  useRunQuickAction,
} from "./mutations";
