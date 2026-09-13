"use client";

// 本文件：HiddenColumnsPanel / HiddenColumnRow —— "隐藏列"侧栏。
// 看板类视图（看板 board 与泳道 swimlane）把被用户隐藏的状态列收进右侧这个面板，
// 每行对应一个状态、可一键恢复。面板外壳是唯一的事实来源，各行计数由调用方
// 通过 renderRow 槽位自定（如看板取服务端状态 facet 的精确总数，泳道用内存里
// 已加载问题的总数）。

import { statusCategoryOfKey } from "@multica/core/issues";
import { Eye, MoreHorizontal } from "lucide-react";
import type { IssueStatusCategory } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import { StatusIcon } from "./status-icon";
import { useT } from "../../i18n";

/**
 * "隐藏列"侧栏（由看板类视图渲染）的统一外壳。
 *
 * 各使用方通过 {@link renderRow} 槽位自行渲染每行的计数——看板读服务端
 * 状态 facet 的精确总数，泳道用由已加载问题推导出的内存总数。
 * 外壳集中在这里，可以让未来的新视图（日历/时间线等）不至于再造一份拷贝。
 */
export function HiddenColumnsPanel({
  hiddenStatuses,
  renderRow,
}: {
  hiddenStatuses: IssueStatusCategory[];
  renderRow: (status: IssueStatusCategory) => React.ReactNode;
}) {
  const { t } = useT("issues");
  return (
    <div className="flex w-[240px] shrink-0 flex-col">
      <div className="mb-2 flex items-center gap-2 px-1">
        <span className="text-body font-medium text-muted-foreground">
          {t(($) => $.board.hidden_columns_label)}
        </span>
      </div>
      <div className="flex-1 space-y-0.5">
        {hiddenStatuses.map((status) => renderRow(status))}
      </div>
    </div>
  );
}

/**
 * {@link HiddenColumnsPanel} 里的一行。纯展示——`total` 由调用方自行计算
 *（缓存查询或内存聚合都行），本组件只负责排版与"恢复该列"按钮。
 */
export function HiddenColumnRow({
  status,
  total,
}: {
  status: IssueStatusCategory;
  total?: number;
}) {
  const { t } = useT("issues");
  const viewStoreApi = useViewStoreApi();
  return (
    <div className="flex items-center justify-between rounded-lg px-2.5 py-2 hover:bg-muted/50">
      <div className="flex items-center gap-2">
        <StatusIcon status={status} className="h-3.5 w-3.5" />
        <span className="text-body">{t(($) => $.status[statusCategoryOfKey(status)])}</span>
      </div>
      <div className="flex items-center gap-1.5">
        {total !== undefined && (
          <span className="text-caption text-muted-foreground">{total}</span>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={t(($) => $.board.show_column)}
                className="rounded-full text-muted-foreground"
              >
                <MoreHorizontal className="size-3.5" />
              </Button>
            }
          />
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              onClick={() => viewStoreApi.getState().showStatus(status)}
            >
              <Eye className="size-3.5" />
              {t(($) => $.board.show_column)}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
}
