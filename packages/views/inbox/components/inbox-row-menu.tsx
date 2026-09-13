"use client";

// 收件箱行的「⋯」紧凑操作菜单：与桌面端右键菜单同款操作，是触屏（没有悬停、
// 也没有右键）唯一能触达这些操作的地方。
import type { InboxItem } from "@multica/core/types";
import { RowActionsMenu } from "../../common/row-actions-menu";
import { useT } from "../../i18n";
import { useInboxRowActions } from "./inbox-context-menu";
import { useInboxItemActions } from "./inbox-item-actions";
import type { InboxView } from "./inbox-view";

/**
 * 行的紧凑操作菜单。它承载与桌面端右键菜单相同的操作，也是触屏指针下
 * 唯一能到达这些操作的方式——触屏既没有悬停也没有右键。
 *
 * 没有 `InboxContextMenuProvider` 时什么都不渲染：操作活在列表上，没有它
 * 的行也就没有任何可提供的操作。
 *
 * @param item 当前行条目
 * @param view 当前列表视图（收件箱 / 归档）
 */
export function InboxRowMenu({
  item,
  view,
}: {
  item: InboxItem;
  view: InboxView;
}) {
  const { t } = useT("inbox");
  const actions = useInboxRowActions();
  const groups = useInboxItemActions(item, view, actions);
  if (!actions) return null;

  return <RowActionsMenu label={t(($) => $.list.actions_aria)} groups={groups} />;
}
