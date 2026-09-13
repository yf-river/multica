"use client";

// 收件箱行的操作集：把「在新标签页打开 / 标为已读未读 / 归档（或取消归档）」
// 排成按分隔线分组的菜单项，供右键菜单与行内紧凑菜单共用。
import { Archive, ArchiveRestore, Check, CircleDot, ExternalLink } from "lucide-react";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import type { InboxItem } from "@multica/core/types";
import type { RowActionItem } from "../../common/row-actions-menu";
import { useIntentNavigate } from "../../navigation";
import { useT } from "../../i18n";
import type { InboxView } from "./inbox-view";

/** 菜单会调用的行级操作，都以收件箱条目 id 为参。 */
export interface InboxRowActions {
  onMarkRead: (id: string) => void;
  onMarkUnread: (id: string) => void;
  /**
   * 在主列表里归档、在归档列表里取消归档——与行内按钮同样的「当前视图的
   * 反向操作」。
   */
  onAction: (id: string) => void;
}

/**
 * 收件箱一行会提供的操作，按分隔线切成组。
 *
 * 桌面端右键菜单与「指针无法悬停时替代悬停按钮」的紧凑行菜单共用这份，因此
 * 二者绝不会漂移：一个没有悬停态的指针，拿到的操作恰好就是右键能给的操作。
 *
 * `actions` 可能是 null（没有 Provider）；这时这一行没有任何可提供的操作。
 *
 * @param item 当前行条目
 * @param view 当前列表视图
 * @param actions 行级操作集（可为 null）
 * @returns 分组后的菜单项
 */
export function useInboxItemActions(
  item: InboxItem,
  view: InboxView,
  actions: InboxRowActions | null,
): RowActionItem[][] {
  const { t } = useT("inbox");
  // 空安全的 slug（不用会抛错的 useWorkspacePaths）：让菜单在工作区路由之外也
  // 能渲染；没有 slug 时该条目不显示跳转而已。
  const slug = useWorkspaceSlug();
  const intentNavigate = useIntentNavigate();
  if (!actions) return [];

  const issueHref =
    slug && item.issue_id
      ? paths.workspace(slug).issueDetail(item.issue_id)
      : null;
  const isArchivedView = view === "archived";

  const groups: RowActionItem[][] = [];

  // 显式的「在新标签页打开」是前台打开——按导航规范的右键行为，焦点跟随新页。
  // 只有引用了任务的行才有地方可开。
  if (issueHref) {
    groups.push([
      {
        key: "open-in-new-tab",
        label: t(($) => $.context_menu.open_in_new_tab),
        icon: <ExternalLink className="h-4 w-4" />,
        onSelect: () => intentNavigate(issueHref, "foreground-tab"),
      },
    ]);
  }

  // 已读/未读开关只出现在主视图。归档行的未读状态刻意不显示（归档会保留 read，
  // 以便取消归档时还原真实状态），未读数也不计入归档项——所以在归档视图放这个
  // 开关只会报成功、屏幕上却毫无变化。
  if (!isArchivedView) {
    groups.push([
      item.read === true
        ? {
            key: "mark-unread",
            label: t(($) => $.context_menu.mark_unread),
            icon: <CircleDot className="h-4 w-4" />,
            onSelect: () => actions.onMarkUnread(item.id),
          }
        : {
            key: "mark-read",
            label: t(($) => $.context_menu.mark_read),
            icon: <Check className="h-4 w-4" />,
            onSelect: () => actions.onMarkRead(item.id),
          },
    ]);
  }

  groups.push([
    {
      key: "archive-toggle",
      label: isArchivedView
        ? t(($) => $.context_menu.unarchive)
        : t(($) => $.context_menu.archive),
      icon: isArchivedView ? (
        <ArchiveRestore className="h-4 w-4" />
      ) : (
        <Archive className="h-4 w-4" />
      ),
      onSelect: () => actions.onAction(item.id),
    },
  ]);

  return groups;
}
