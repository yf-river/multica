"use client";

// 收件箱单行条目：头像 + 展示标题 + 正文标签 + 活动徽标/时间，支持选中、
// 修饰键新开任务、右键菜单与行内归档按钮。
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { StatusIcon } from "../../issues/components/status-icon";
import {
  IssueAgentActivityIndicator,
} from "../../issues/components/issue-agent-activity-indicator";
import { ActorAvatar } from "../../common/actor-avatar";
import { Archive, ArchiveRestore } from "lucide-react";
import type { InboxItem } from "@multica/core/types";
import type { InboxView } from "./inbox-view";
import { InboxDetailLabel } from "./inbox-detail-label";
import { getInboxDisplayTitle } from "./inbox-display";
import { useInboxContextMenu } from "./inbox-context-menu";
import { useStatusLabel } from "../../issues/utils/status-label";
import { InboxRowMenu } from "./inbox-row-menu";
import { handleRowActivationKey } from "../../common/row-actions-menu";
import { useT } from "../../i18n";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import { resolveClickIntent, useIntentNavigate } from "../../navigation";

// 返回本地化「相对时间」格式化函数的 Hook——之前静态 `timeAgo` 的 i18n 版本。
// 返回函数（而不是字符串）是为了让调用处用法完全不变：`timeAgo(dateStr)`。
/** 返回「把时间字符串格式化成几秒/分钟/小时/天前」的函数。 */
export function useTimeAgo() {
  const { t } = useT("inbox");
  return (dateStr: string): string => {
    const diff = Date.now() - new Date(dateStr).getTime();
    const minutes = Math.floor(diff / 60000);
    if (minutes < 1) return t(($) => $.list.time.just_now);
    if (minutes < 60) return t(($) => $.list.time.minutes, { count: minutes });
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return t(($) => $.list.time.hours, { count: hours });
    const days = Math.floor(hours / 24);
    return t(($) => $.list.time.days, { count: days });
  };
}

/**
 * 收件箱单行条目：整体是一行可点的选择行，点击进详情，修饰键点击在新标签页
 * 打开对应任务；行内含归档/取消归档按钮与紧凑操作菜单。
 *
 * @param item 收件箱条目
 * @param view 当前列表视图（决定归档按钮方向与未读点显示）
 * @param isSelected 是否选中
 * @param onClick 普通点击（进入详情/选中）
 * @param onAction 归档/取消归档回调——永远是当前视图的反向操作
 */
export function InboxListItem({
  item,
  view,
  isSelected,
  onClick,
  onAction,
}: {
  item: InboxItem;
  view: InboxView;
  isSelected: boolean;
  onClick: () => void;
  // 主列表归档、归档列表取消归档——行操作永远是当前视图的反向操作，
  // 因此两个列表共用这一行组件。
  onAction: () => void;
}) {
  const { t } = useT("inbox");
  const timeAgo = useTimeAgo();
  // 收件箱是跨工作区的界面，所以状态目录按条目「自己的」工作区读，而不是路由的。
  // (MUL-6243)
  const { categoryOf: statusCategoryOf, colorOf: statusColorOf } =
    useIssueStatuses(item.workspace_id);
  const statusLabelOf = useStatusLabel(item.workspace_id);
  const openContextMenu = useInboxContextMenu();
  // 空安全的 slug（不用会抛错的 useWorkspacePaths）：行在测试中也要能渲染，
  // 且可能出现在工作区路由之外；没有 slug 时修饰键打开的提示就干脆不启用。
  const slug = useWorkspaceSlug();
  const issueHref =
    slug && item.issue_id
      ? paths.workspace(slug).issueDetail(item.issue_id)
      : null;
  const intentNavigate = useIntentNavigate();
  const displayTitle = getInboxDisplayTitle(item);
  const isArchivedView = view === "archived";
  // 归档刻意保留 `read` 不动，这样取消归档能还原真实的未读状态；否则归档行会
  // 带着一个用户在本视图里无法清除的未读标记。这里只是先压住这个提示。
  const showUnread = item.read !== true && !isArchivedView;
  const ActionIcon = isArchivedView ? ArchiveRestore : Archive;
  const actionLabel = isArchivedView
    ? t(($) => $.list.unarchive_tooltip)
    : t(($) => $.list.archive_tooltip);
  const actorType = item.actor_type ?? item.recipient_type;
  // 图标是按「状态类别」画的，单看图标分不清「In Review」和自定义的
  // 「Human Review」——在同一类别内两个状态间移动时，这行看起来像素级不变，
  // 用户会以为是收件箱没刷新（MUL-6395）。颜色承载自定义状态自己的身份，
  // 状态变更的正文标签也正是这么渲染的。内置状态的 `colorOf` 返回 null，
  // 落在它自己的语义色上。
  const statusColor = item.issue_status ? statusColorOf(item.issue_status) : null;

  return (
    // 用 div 而不是 <button>：行内还带着自己的控件（归档按钮、紧凑菜单），
    // <button> 里嵌可交互元素是屏幕阅读器够不到的非法 HTML。`role="button"`
    // 加 Enter/Space 处理器保留行自己的键盘行为。
    <div
      role="button"
      tabIndex={0}
      onClick={(e) => {
        // 普通点击保留主-详情结构的选中；修饰键点击引用了任务的行时，
        // 把该任务开成独立标签页。
        if (issueHref) {
          const intent = resolveClickIntent(e);
          if (intent !== "push") {
            intentNavigate(issueHref, intent);
            return;
          }
        }
        onClick();
      }}
      onKeyDown={(e) => handleRowActivationKey(e, onClick)}
      onAuxClick={(e) => {
        if (e.defaultPrevented || e.button !== 1 || !issueHref) return;
        e.preventDefault();
        intentNavigate(issueHref, "background-tab");
      }}
      // 右键打开列表的共享菜单（已读/未读、归档）。`select-none` 对应 Base UI
      // 自己触发器原本合进来的样式，保证右键某行不会开始一段文本选择。
      onContextMenu={
        openContextMenu ? (e) => openContextMenu(item, e) : undefined
      }
      className={`group flex w-full cursor-default select-none items-center gap-3 rounded-md px-2 py-2.5 text-left outline-none transition-colors focus-visible:ring-1 focus-visible:ring-ring ${
        isSelected
          ? "bg-accent"
          : "hover:bg-accent/50 data-[popup-open]:bg-accent/50"
      }`}
    >
      <ActorAvatar
        actorType={actorType}
        actorId={item.actor_id ?? item.recipient_id}
        size="lg"
        enableHoverCard
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-1.5">
            {showUnread && (
              <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-brand" />
            )}
            <span
              className={`truncate text-body ${showUnread ? "font-medium" : "text-muted-foreground"}`}
            >
              {displayTitle}
            </span>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {/* 纯指针的小提示：悬停时显示；行内任意处键盘聚焦时也显示，保证 Tab
                可达。触屏两者都没有，所以对不能悬停的指针保持隐藏——下面那个
                紧凑菜单承载同样的操作。 */}
            <button
              type="button"
              title={actionLabel}
              aria-label={actionLabel}
              onClick={(e) => {
                e.stopPropagation();
                onAction();
              }}
              className="hidden rounded p-0.5 text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring [@media(hover:hover)]:group-hover:inline-flex [@media(hover:hover)]:group-focus-within:inline-flex"
            >
              <ActionIcon className="h-3.5 w-3.5" />
            </button>
            <InboxRowMenu item={item} view={view} />
            {item.issue_status && (
              // 只给图标、和其它任务行一致——但颜色不是名字，这行又没有空间放
              // 看板卡/列表行那种 CustomStatusChip。`title` 就是这个名字，
              // 和上面归档按钮用的提示是同一个套路。
              <span
                title={statusLabelOf(item.issue_status)}
                className="flex shrink-0 items-center"
              >
                <StatusIcon
                  status={item.issue_status}
                  category={statusCategoryOf(item.issue_status)}
                  color={statusColor}
                  className="h-3.5 w-3.5 shrink-0"
                />
              </span>
            )}
          </div>
        </div>
        <div className="mt-0.5 flex items-center justify-between gap-2">
          <p className={`min-w-0 overflow-hidden text-ellipsis whitespace-nowrap text-caption ${showUnread ? "text-muted-foreground" : "text-muted-foreground"}`}>
            <InboxDetailLabel item={item} />
          </p>
          <div className="flex shrink-0 items-center gap-1.5">
            {/* 只放徽标、不放悬停卡（MUL-5189）。「有 Agent 在处理」在排查收件箱
                时值得一看；它背后的卡片只多给已流逝时长，而这不会改变你要不要
                点开这行。左侧已经有头像的悬停卡，这里再来一个弹层大多是噪音。 */}
            {item.issue_id && (
              <IssueAgentActivityIndicator
                issueId={item.issue_id}
                hoverCard={false}
              />
            )}
            <span className={`text-caption ${showUnread ? "text-muted-foreground" : "text-muted-foreground"}`}>
              {timeAgo(item.created_at)}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
