"use client";

// 创建议题弹窗的"工具栏字段可见性"设置（Settings → Issue 项管理）：
// 记录两种创建模式下哪些可选字段常驻工具栏，哪些收进 ⋯ 溢出菜单。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/** 快速创建（Quick Create）弹窗工具栏上可选显示的字段。 */
export type QuickCreateField = "project" | "priority" | "due_date";
/** 手动创建弹窗工具栏上可选显示的字段。 */
export type ManualCreateField =
  | "status"
  | "priority"
  | "assignee"
  | "labels"
  | "project"
  | "due_date"
  | "start_date";

// 规范的字段顺序——设置页按这个顺序渲染各行，setter 也按它归一化持久化数组，
// 这样一串开关操作永远不会把同一组选择持久化成两种不同编码。
export const QUICK_CREATE_FIELDS: QuickCreateField[] = ["project", "priority", "due_date"];
export const MANUAL_CREATE_FIELDS: ManualCreateField[] = [
  "status",
  "priority",
  "assignee",
  "labels",
  "project",
  "due_date",
  "start_date",
];

/** 快速创建默认只显示"项目"字段。 */
export const DEFAULT_QUICK_CREATE_FIELDS: QuickCreateField[] = ["project"];
// 对齐手动弹窗的历史工具栏：这五个字段一直显示，而截止/开始日期藏在 ⋯ 溢出里。
export const DEFAULT_MANUAL_CREATE_FIELDS: ManualCreateField[] = [
  "status",
  "priority",
  "assignee",
  "labels",
  "project",
];

// 每种创建模式把哪些可选字段留在工具栏上。归 Settings → Issue 项管，两个创建弹窗
// 都读它；在这里被关掉的字段仍可从弹窗的 ⋯ 溢出里打开，且只要它持有值就总会
// 重新浮回工具栏。用按工作区隔离的存储（各工作区的项目/自定义属性不同）、又天然
// 按用户隔离（localStorage 是浏览器用户配置文件局部的）——与 quick-create 对
// actor/项目记忆的作用域一致。
interface IssueCreateSettingsState {
  quickCreateFields: QuickCreateField[];
  setQuickCreateFieldVisible: (field: QuickCreateField, visible: boolean) => void;
  manualCreateFields: ManualCreateField[];
  setManualCreateFieldVisible: (field: ManualCreateField, visible: boolean) => void;
}

/** 只改动 `field` 一项、其余按当前状态保留的开关助手。 */
function toggle<F extends string>(all: F[], current: F[], field: F, visible: boolean): F[] {
  return all.filter((f) => (f === field ? visible : current.includes(f)));
}

/** 创建议题弹窗工具栏字段可见性设置 store。 */
export const useIssueCreateSettingsStore = create<IssueCreateSettingsState>()(
  persist(
    (set) => ({
      quickCreateFields: DEFAULT_QUICK_CREATE_FIELDS,
      setQuickCreateFieldVisible: (field, visible) =>
        set((s) => ({
          quickCreateFields: toggle(QUICK_CREATE_FIELDS, s.quickCreateFields, field, visible),
        })),
      manualCreateFields: DEFAULT_MANUAL_CREATE_FIELDS,
      setManualCreateFieldVisible: (field, visible) =>
        set((s) => ({
          manualCreateFields: toggle(MANUAL_CREATE_FIELDS, s.manualCreateFields, field, visible),
        })),
    }),
    {
      name: "multica_issue_create_settings",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      merge: (persistedState, currentState) => {
        // 存储里缺字段（旧版/被清理）时回退到默认值，别让空数组清空工具栏。
        const persisted = (persistedState ?? {}) as Partial<IssueCreateSettingsState>;
        return {
          ...currentState,
          ...persisted,
          quickCreateFields: persisted.quickCreateFields ?? DEFAULT_QUICK_CREATE_FIELDS,
          manualCreateFields: persisted.manualCreateFields ?? DEFAULT_MANUAL_CREATE_FIELDS,
        };
      },
    },
  ),
);

// 切换工作区后重水合该工作区的字段可见性设置。
registerForWorkspaceRehydration(() => useIssueCreateSettingsStore.persist.rehydrate());
