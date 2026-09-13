// 本文件定义「置顶（Pin）」相关类型：把议题/项目/视图固定到侧边栏顶部。

/** 可置顶的对象类型：议题、项目或（已保存的）视图。 */
export type PinnedItemType = "issue" | "project" | "view";

/**
 * 置顶项的元数据。标题/状态/编号/图标都不在这里——调用方通过
 * `issueDetailOptions` / `projectDetailOptions` 动态获取，这样侧边栏能随
 * `issue:updated` / `project:updated` 事件自动刷新，无需在 `pinKeys` 上做
 * 跨实体的缓存失效。
 */
export interface PinnedItem {
  id: string;
  workspace_id: string;
  user_id: string;
  item_type: PinnedItemType;
  item_id: string;
  position: number;
  created_at: string;
}

/** 新增置顶项的请求体。 */
export interface CreatePinRequest {
  item_type: PinnedItemType;
  item_id: string;
}

/** 重排置顶顺序的请求体：一次提交完整的新顺序列表。 */
export interface ReorderPinsRequest {
  items: { id: string; position: number }[];
}
