// 议题时间线（TimelineEntry[]）缓存的稳定升序排序。
import type { TimelineEntry } from "@multica/core/types";

/**
 * 对平铺的 TimelineEntry[] 缓存做稳定升序排序。
 *
 * 所有向议题时间线缓存追加内容的写入方，都必须经过这个辅助函数，
 * 这样即使 WebSocket 事件和 mutation 的 onSuccess 回调乱序到达，
 * 展示顺序仍然保持 `created_at` 升序（相同时间用 id 作打破平局的依据）。
 *
 * 原地改动的观察者（按 id 做 map / filter）不需要它——它们本就保留原有相对顺序。
 */
export function sortTimelineEntriesAsc(entries: TimelineEntry[]): TimelineEntry[] {
  entries.sort((a, b) => {
    if (a.created_at !== b.created_at) {
      return a.created_at < b.created_at ? -1 : 1;
    }
    return a.id < b.id ? -1 : 1;
  });
  return entries;
}
