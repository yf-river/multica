"use client";

// 状态选项 hook：把「用户可选的/可筛的状态」摊成一张按规范分类顺序排列的平铺列表，
// 供状态选择器和状态筛选共用，保证两边永远一致。
import { useMemo } from "react";
import { ALL_STATUSES } from "@multica/core/issues/config";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { issueStatusColor } from "@multica/core/issue-statuses/queries";
import type { IssueStatus, IssueStatusCategory } from "@multica/core/types";
import { useStatusLabel } from "./status-label";

/** 一个状态选项。 */
export interface StatusOption {
  key: IssueStatus;
  /** 这个状态表现为哪个分类——决定它的图标和悬停色。 */
  category: IssueStatusCategory;
  label: string;
  /** 自定义状态为 `#rrggbb`；内置状态为 null，保留其 token 色。 */
  color: string | null;
}

/**
 * 用户可选/可筛的状态，摊成一张按规范分类顺序排好的平铺列表（MUL-6243, MUL-6399）。
 *
 * 分类以「每选项自带」的方式携带，而不是渲染成小标题：分类就是状态继承的行为，
 * 图标和悬停色已经说明了它；而且每分类一个小标题会把 7 行的列表变成 14 行半是空白的
 * 列表。
 *
 * 状态选择器和状态筛选共用这份数据，两者永不漂移——一边提供、另一边缺失某个状态，
 * 正是问题会变得找不到的方式。
 *
 * 归档状态默认排除：归档即退役一个「今后不再指派」的状态。只读的筛选可以通过
 * `includeArchivedKeys` 把某些具体当前 key 加回来，让既有问题依旧可查，而选择器不会
 * 提供一个退役的值让人去指派。
 */
const NO_ARCHIVED_STATUS_KEYS: readonly IssueStatus[] = [];

/**
 * 返回可选用/可筛选的状态选项列表。
 * @param wsId - 工作区 id。
 * @param includeArchivedKeys - 需要一并包含的已归档状态 key（通常给筛选用）。
 * @returns 平铺的状态选项列表。
 */
export function useStatusOptions(
  wsId: string,
  includeArchivedKeys: readonly IssueStatus[] = NO_ARCHIVED_STATUS_KEYS,
): StatusOption[] {
  const { statuses } = useIssueStatuses(wsId);
  const labelOf = useStatusLabel(wsId);

  return useMemo(
    () => {
      const includedArchived = new Set(includeArchivedKeys);
      return ALL_STATUSES.flatMap((category) => {
        const entries = statuses.filter(
          (entry) =>
            entry.category === category &&
            (!entry.archived_at || includedArchived.has(entry.key)),
        );
        // 这个分类在目录里没有行：请求还在路上，或该工作区早于种子数据。
        // 直接给出内置状态（它的 key 即分类），保证生命周期的每一步都不会缺席。
        if (entries.length === 0) {
          return [
            {
              key: category as IssueStatus,
              category,
              label: labelOf(category),
              color: null,
            },
          ];
        }
        return entries.map((e) => ({
          key: e.key as IssueStatus,
          category,
          label: labelOf(e.key),
          color: issueStatusColor(e),
        }));
      });
    },
    [includeArchivedKeys, labelOf, statuses],
  );
}
