"use client";

// 本文件：ListLoadMoreFooter —— 无限滚动问题视图（看板/列表/泳道）的列尾统一页脚。
// 把散落在三个视图里的同一套 错误/还有更多/已到底 三分支收拢到一处，
// 保证各状态的表现与文案始终一致。

import { useT } from "../../i18n";
import { InfiniteScrollSentinel } from "./infinite-scroll-sentinel";

/** 服务端"行分支"每页大小（见 use-issue-status-branches /
 * use-issue-group-branches）。一列的总行数不超过它时根本不会翻页，
 * 因此列尾不需要"没有更多了"的标记——列本身已自证完整。 */
const PAGINATED_THRESHOLD = 50;

/**
 * 无限滚动问题视图（看板 / 列表 / 泳道）统一的列尾页脚。把同一条
 * 错误/还有更多/已到底 三分支在四个副本里收拢为一份，让状态与文案保持一致：
 *  - 出错      → 显示"重试"
 *  - 还有更多  → 哨兵 + "加载中…"文字（光秃秃的转圈会被当成"卡死了"）
 *  - 已到底    → 淡化的"没有更多了"标记——但只对真正翻过页的列（总数超过一页）
 *                显示
 *  - 列表很短  → 什么都不显示
 */
export function ListLoadMoreFooter({
  hasMore,
  isLoading,
  total,
  onLoadMore,
  isError = false,
  onRetry,
}: {
  hasMore: boolean;
  isLoading: boolean;
  total: number;
  onLoadMore: () => void;
  isError?: boolean;
  onRetry?: () => void;
}) {
  const { t } = useT("issues");

  if (isError && onRetry) {
    return (
      <button
        type="button"
        className="w-full py-2 text-caption text-destructive hover:underline"
        onClick={onRetry}
      >
        {t(($) => $.table.load_more_failed_retry)}
      </button>
    );
  }

  if (hasMore) {
    return (
      <InfiniteScrollSentinel
        onVisible={onLoadMore}
        loading={isLoading}
        label={t(($) => $.table.loading_branch)}
      />
    );
  }

  if (total > PAGINATED_THRESHOLD) {
    return (
      <div className="py-2 text-center text-caption text-muted-foreground">
        {t(($) => $.table.no_more)}
      </div>
    );
  }

  return null;
}
