// 本文件：StatusHeading —— 状态分组表头（看板列头/列表分区头）。
// 展示该"状态类别"的图标、类别的可读名称与组内问题数量。

import { statusCategoryOfKey } from "@multica/core/issues";
import type { IssueStatus } from "@multica/core/types";
import { StatusIcon } from "./status-icon";
import { useT } from "../../i18n";

/**
 * 状态分组表头：图标 + 类别名（如"进行中"）+ 数量。
 * `status` 是某一"类别代表状态"，名称按类别映射翻译。
 */
export function StatusHeading({
  status,
  count,
}: {
  status: IssueStatus;
  count: number;
}) {
  const { t } = useT("issues");
  return (
    <div className="flex items-center gap-2">
      <span className="inline-flex items-center gap-1.5 text-caption font-semibold">
        <StatusIcon status={status} className="h-3 w-3" />
        {t(($) => $.status[statusCategoryOfKey(status)])}
      </span>
      <span className="text-caption text-muted-foreground">{count}</span>
    </div>
  );
}
