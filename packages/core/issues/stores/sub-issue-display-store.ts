// 议题详情页"子议题表格"要显示哪些列属性的个人偏好 store，全局持久化。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../../platform/storage";

/**
 * 议题详情页的子议题行显示哪些属性。
 *
 * 这是个人阅读偏好（像评论输入框的 sticky 开关），不是按视图的设置：每个议题的
 * 子议题面板共享这一份配置，用 `defaultStorage` 全局持久化。默认值与面板内置的
 * 行布局一致，用户改动前这份偏好是隐形的。
 *
 * `rowPropertyIds` 存的是工作区自定义属性定义 id。列表本身是全局的，而属性 id 是
 * 按工作区作用域的——渲染器拿当前工作区的属性表去解析这些 id，来自其它工作区的
 * id 在那个工作区里自然失效（与视图 store 的 `cardPropertyIds` 同样的容忍度）。
 */
export const SUB_ISSUE_ROW_PROPERTY_KEYS = [
  "priority",
  "labels",
  "childProgress",
  "dueDate",
  "assignee",
] as const;
/** 子议题行上可开关的属性 key。 */
export type SubIssueRowPropertyKey =
  (typeof SUB_ISSUE_ROW_PROPERTY_KEYS)[number];
/** 各内置属性是否显示。 */
export type SubIssueRowProperties = Record<SubIssueRowPropertyKey, boolean>;

/** 默认全部显示，与面板内置行布局一致。 */
export const DEFAULT_SUB_ISSUE_ROW_PROPERTIES: SubIssueRowProperties = {
  priority: true,
  labels: true,
  childProgress: true,
  dueDate: true,
  assignee: true,
};

interface SubIssueDisplayStore {
  /** 内置属性 key → 是否显示。 */
  rowProperties: SubIssueRowProperties;
  /** 额外显示的工作区自定义属性 id 列表。 */
  rowPropertyIds: string[];
  /** 切换某个内置属性是否显示。 */
  toggleRowProperty: (key: SubIssueRowPropertyKey) => void;
  /** 切换某个自定义属性 id 是否显示。 */
  toggleRowPropertyId: (propertyId: string) => void;
}

export const useSubIssueDisplayStore = create<SubIssueDisplayStore>()(
  persist(
    (set) => ({
      rowProperties: { ...DEFAULT_SUB_ISSUE_ROW_PROPERTIES },
      rowPropertyIds: [],
      toggleRowProperty: (key) =>
        set((s) => ({
          rowProperties: { ...s.rowProperties, [key]: !s.rowProperties[key] },
        })),
      toggleRowPropertyId: (propertyId) =>
        set((s) => ({
          rowPropertyIds: s.rowPropertyIds.includes(propertyId)
            ? s.rowPropertyIds.filter((id) => id !== propertyId)
            : [...s.rowPropertyIds, propertyId],
        })),
    }),
    {
      name: "multica_sub_issue_display",
      storage: createJSONStorage(() => defaultStorage),
      // 对 rowProperties 做深合并：未来版本新增的 key 默认按"显示"处理，而不是
      // undefined（persist 默认的浅合并会把整个对象替换成那份旧快照）。
      merge: (persisted, current) => {
        const p = (persisted ?? {}) as Partial<SubIssueDisplayStore>;
        return {
          ...current,
          ...p,
          rowProperties: {
            ...current.rowProperties,
            ...(p.rowProperties ?? {}),
          },
        };
      },
    },
  ),
);
