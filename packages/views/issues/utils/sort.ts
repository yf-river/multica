// 客户端问题排序工具：按字段（优先级/开始/截止/创建时间/标题/位置/自定义属性）和方向
// 对问题数组排序的纯函数，供问题界面在本地小窗口内排序使用。
import type { Issue } from "@multica/core/types";
import { PRIORITY_ORDER } from "@multica/core/issues/config";
import type { SortField, SortDirection } from "@multica/core/issues/stores/view-store";
import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";

// 优先级顺序 → 权重表，用于按优先级排序。
const PRIORITY_RANK: Record<string, number> = Object.fromEntries(
  PRIORITY_ORDER.map((p, i) => [p, i])
);

/**
 * 对问题数组排序（会返回新数组，不修改原数组）。
 * @param issues - 要排序的问题。
 * @param field - 排序字段。
 * @param direction - 排序方向。
 * @returns 排序后的新数组。
 */
export function sortIssues(
  issues: Issue[],
  field: SortField,
  direction: SortDirection
): Issue[] {
  // `property:<id>` 按自定义属性值排序。数值按数字大小；日期值是纯 "YYYY-MM-DD"
  // 字符串，按字典序即时间序。方向只作用于「值的比较」——没有值的问题在两种方向下
  // 都排最后（整体 reverse 会让 desc 时把它们翻到最前）。
  const propertyId = propertyIdFromViewKey(field);
  if (propertyId) {
    const dir = direction === "desc" ? -1 : 1;
    return issues.toSorted((a, b) => {
      const av = a.properties?.[propertyId];
      const bv = b.properties?.[propertyId];
      const aMissing = av === undefined || Array.isArray(av);
      const bMissing = bv === undefined || Array.isArray(bv);
      if (aMissing && bMissing) return 0;
      if (aMissing) return 1;
      if (bMissing) return -1;
      if (typeof av === "number" && typeof bv === "number") return dir * (av - bv);
      return dir * String(av).localeCompare(String(bv));
    });
  }

  const sorted = issues.toSorted((a, b) => {
    switch (field) {
      case "priority":
        return (
          (PRIORITY_RANK[a.priority] ?? 99) -
          (PRIORITY_RANK[b.priority] ?? 99)
        );
      case "start_date": {
        if (!a.start_date && !b.start_date) return 0;
        if (!a.start_date) return 1;
        if (!b.start_date) return -1;
        return (
          new Date(a.start_date).getTime() - new Date(b.start_date).getTime()
        );
      }
      case "due_date": {
        if (!a.due_date && !b.due_date) return 0;
        if (!a.due_date) return 1;
        if (!b.due_date) return -1;
        return (
          new Date(a.due_date).getTime() - new Date(b.due_date).getTime()
        );
      }
      case "created_at":
        return (
          new Date(a.created_at).getTime() - new Date(b.created_at).getTime()
        );
      case "title":
        return a.title.localeCompare(b.title);
      case "position":
      default:
        return a.position - b.position;
    }
  });
  return direction === "desc" ? sorted.reverse() : sorted;
}
