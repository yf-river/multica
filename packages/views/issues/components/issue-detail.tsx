"use client";
import {InsightsSurface} from "../../insights/insights-surface";

// 本文件：IssueDetail —— 单个问题（issue）的详情页核心组件。
// 负责渲染标题、描述、子任务列表、活动/评论时间线、右侧属性边栏，
// 并处理标题/描述的自动保存、滚动恢复、评论高亮跳转、子任务批量选择等交互。

import {
  issueBehavesAs,
  issueBehavesAsAny,
  issueStatusCategory,
  statusCategoryOfKey,
} from "@multica/core/issues";
import { useStatusLabel } from "../utils/status-label";
import { priorityLabel } from "../utils/priority-label";
import { issueHighlightMementoKey } from "./issue-highlight-memento";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useState, useEffect, useCallback, useMemo, useRef, Fragment, type ReactNode } from "react";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import { useDefaultLayout, usePanelRef } from "react-resizable-panels";
import { AppLink, useBackOrReplace } from "../../navigation";
import {
  Archive,
  Calendar,
  CalendarClock,
  CalendarDays,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  CircleCheck,
  Milestone,
  MoreHorizontal,
  PanelRight,
  Pin,
  PinOff,
  Plus,
  SlidersHorizontal,
  Tag,
  Unlink,
  Users,
} from "lucide-react";
import { BreadcrumbHeader, type BreadcrumbSegment } from "../../layout/breadcrumb-header";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import { ResizablePanelGroup, ResizablePanel, ResizableHandle } from "@multica/ui/components/ui/resizable";
import { Sheet, SheetContent } from "@multica/ui/components/ui/sheet";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { ContentEditor, type ContentEditorRef, TitleEditor, type TitleEditorRef, useFileDropZone, FileDropOverlay, useLazyEditor, useEditorUpload, ImageSequenceProvider } from "../../editor";
import { collectImageSequence, type ImageSequenceBlock } from "@multica/core/attachments/image-sequence";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { Popover, PopoverTrigger, PopoverContent } from "@multica/ui/components/ui/popover";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Command, CommandInput, CommandList, CommandEmpty, CommandGroup, CommandItem } from "@multica/ui/components/ui/command";
import { AvatarGroup, AvatarGroupCount } from "@multica/ui/components/ui/avatar";
import { ActorAvatar } from "../../common/actor-avatar";
import { PropRow } from "../../common/prop-row";
import { PropertyIcon } from "../../common/property-icon";
import type { Attachment, Issue, IssueProperty, IssueStatus, IssueStatusCategory, IssuePriority, ListIssuesCache, TimelineEntry, UpdateIssueRequest } from "@multica/core/types";
import { contentReferencesAttachment } from "@multica/core/types";
import { STATUS_CONFIG } from "@multica/core/issues/config";
import { commentLandingTarget } from "@multica/core/issues/comment-deletion";
import { formatDateOnly, isPastDateOnly } from "@multica/core/issues/date";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { toast } from "sonner";
import { errorCode } from "@multica/core/api";
import { StatusIcon, PriorityIcon, StatusPicker, PriorityPicker, StagePicker, StartDatePicker, DueDatePicker, AssigneePicker, LabelPicker } from ".";
import { maxSiblingStage } from "./pickers/stage-picker";
import { CustomPropertyValueEditor, CustomPropertyValueDisplay } from "./pickers/custom-property-picker";
import { Switch } from "@multica/ui/components/ui/switch";
import { IssueActionsDropdown, useIssueActions, IssueActionsContextMenu, IssueContextMenuProvider } from "../actions";
import { LabelChip } from "../../labels/label-chip";
import { IssueAgentActivityIndicator } from "./issue-agent-activity-indicator";
import { SubIssuesAgentWorkingChip } from "./sub-issues-agent-working-chip";
import { ProjectPicker } from "../../projects/components/project-picker";
import { CommentCard } from "./comment-card";
import { lastCommentIdByTaskId } from "./run-diff";
import { SourceContextBadge } from "./source-context-viewer";
import { RevisionConflictCompare } from "./revision-conflict-compare";
import { CommentInput } from "./comment-input";
import { CurrentIssueRenderContextProvider } from "../current-issue-render-context";
import { ResolvedThreadBar } from "./resolved-thread-bar";
import { getShortcut, shortcutMatchesEvent } from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";
import { ThreadMinimap } from "./thread-minimap";
import { ThreadNavPanel, mentionsUser, type ThreadNavThread } from "./thread-nav-panel";
import { collectThreadReplies, deriveThreadResolution } from "./thread-utils";
import { IssueAgentHeaderChip } from "./issue-agent-header-chip";
import { ExecutionLogSection } from "./execution-log-section";
import { TAPDSourceBadge } from "./tapd-source-badge";
import { QuickActionsSection } from "./quick-actions-section";
import { PluginPanelSection } from "../../plugins";
import { PullRequestList } from "./pull-request-list";
import { useGitHubSettings } from "@multica/core/github";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useRecentContextStore } from "@multica/core/chat";
import { useModalStore } from "@multica/core/modals";
import { flattenIssueBuckets, issueDetailOptions, issueKeys, childIssuesOptions, childIssueProgressOptions, issueAttachmentsOptions } from "@multica/core/issues/queries";
import { projectDetailOptions } from "@multica/core/projects/queries";
import { ProjectIcon } from "../../projects/components/project-icon";
import { issueLabelsOptions } from "@multica/core/labels";
import { propertyListOptions } from "@multica/core/properties";
import { memberListOptions, agentListOptions } from "@multica/core/workspace/queries";
import {
  selectExpandedResolved,
  useRecentIssuesStore,
  useResolvedExpandStore,
  useSubIssuesCollapseStore,
  useSubIssueDisplayStore,
  SUB_ISSUE_ROW_PROPERTY_KEYS,
  type SubIssueRowProperties,
  type SubIssueRowPropertyKey,
} from "@multica/core/issues/stores";
import { BatchActionToolbar } from "./batch-action-toolbar";
import {
  IssueSurfaceSelectionProvider,
  useCreateIssueSurfaceSelection,
  useIssueSurfaceSelection,
} from "../surface/selection-context";
import { useIssueTimeline } from "../hooks/use-issue-timeline";
import { useIssueReactions } from "../hooks/use-issue-reactions";
import { useIssueSubscribers } from "../hooks/use-issue-subscribers";
import { ReactionBar } from "@multica/ui/components/common/reaction-bar";
import { useLocale, useTimeAgo } from "../../i18n";
import {
  useRestoredViewState,
  useViewStateWriter,
} from "../../platform";
import { cn } from "@multica/ui/lib/utils";
import { PAGE_GUTTER } from "../../layout/page-header";

import { ProgressRing } from "./progress-ring";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";
import { useIssueDetailScrollRestore } from "../hooks/use-issue-detail-scroll-restore";
import { useInPageFind } from "../hooks/use-in-page-find";
import { useStickyComposer } from "../hooks/use-sticky-composer";
import { FindBar } from "./find-bar";
import {
  AnimatedRightSidebar,
  getAnimatedRightSidebarInitialOpen,
  rightSidebarPanelMotionProps,
  useAnimatedRightSidebarState,
  useRightSidebarShortcut,
} from "../../layout/animated-right-sidebar";

export { issueHighlightMementoKey } from "./issue-highlight-memento";

// 订阅按钮与"取消订阅"菜单触发器共用的样式，保证两者在视觉上是同一个控件——
// 它们占据同一个位置，只是点击后行为不同。
const SUBSCRIPTION_ACTION_CLASS =
  "text-caption text-muted-foreground hover:text-foreground transition-colors disabled:pointer-events-none disabled:opacity-50";

function SubscriberPopoverContent({
  members,
  agents,
  subscribers,
  toggleSubscriber,
  togglesDisabled,
  t,
}: {
  members: { user_id: string; name: string }[];
  agents: { id: string; name: string; archived_at?: string | null }[];
  subscribers: { user_type: string; user_id: string }[];
  toggleSubscriber: (id: string, type: "member" | "agent", subscribed: boolean) => void;
  /**
   * 这些复选框的数据都来自 `subscribers`，而它在查询完成前默认是空数组——
   * 也就是说，查询未完成时所有人都会被渲染成"未订阅"。此时点击并不是无害的空操作：
   * 显式订阅会把目标用户的 reason 改写为 'manual'，并清掉任何 opt-out 作用域
   * （见 server/pkg/db/queries/subscriber.sql），从而悄悄丢弃掉一次委托订阅或某人主动的退订。
   * 所以这些行要等真实结果回来，而不是只看进行中的 mutation（MUL-5714）。
   */
  togglesDisabled: boolean;
  t: ActivityT;
}) {
  const [search, setSearch] = useState("");
  const q = search.trim().toLowerCase();

  const uniqueMembers = members.filter((m, i, arr) => arr.findIndex((x) => x.user_id === m.user_id) === i);
  const activeAgents = agents.filter((a) => !a.archived_at);

  const filteredMembers = q
    ? uniqueMembers.filter((m) => m.name.toLowerCase().includes(q) || matchesPinyin(m.name, q))
    : uniqueMembers;
  const filteredAgents = q
    ? activeAgents.filter((a) => a.name.toLowerCase().includes(q) || matchesPinyin(a.name, q))
    : activeAgents;

  return (
    <PopoverContent align="end" className="w-64 p-0">
      <Command shouldFilter={false}>
        <CommandInput
          placeholder={t(($) => $.detail.change_subscribers_placeholder)}
          value={search}
          onValueChange={setSearch}
        />
        <CommandList className="max-h-64">
          {filteredMembers.length === 0 && filteredAgents.length === 0 && (
            <CommandEmpty>{t(($) => $.detail.no_subscribers_results)}</CommandEmpty>
          )}
          {filteredMembers.length > 0 && (
            <CommandGroup heading={t(($) => $.detail.members_group)}>
              {filteredMembers.map((m) => {
                const sub = subscribers.find((s) => s.user_type === "member" && s.user_id === m.user_id);
                const isSubbed = !!sub;
                return (
                  <CommandItem
                    key={`member-${m.user_id}`}
                    onSelect={() => toggleSubscriber(m.user_id, "member", isSubbed)}
                    disabled={togglesDisabled}
                    className="flex items-center gap-2.5"
                  >
                    <Checkbox checked={isSubbed} className="pointer-events-none" />
                    <ActorAvatar actorType="member" actorId={m.user_id} size="md" />
                    <span className="truncate flex-1">{m.name}</span>
                  </CommandItem>
                );
              })}
            </CommandGroup>
          )}
          {filteredAgents.length > 0 && (
            <CommandGroup heading={t(($) => $.detail.agents_group)}>
              {filteredAgents.map((a) => {
                const sub = subscribers.find((s) => s.user_type === "agent" && s.user_id === a.id);
                const isSubbed = !!sub;
                return (
                  <CommandItem
                    key={`agent-${a.id}`}
                    onSelect={() => toggleSubscriber(a.id, "agent", isSubbed)}
                    disabled={togglesDisabled}
                    className="flex items-center gap-2.5"
                  >
                    <Checkbox checked={isSubbed} className="pointer-events-none" />
                    <ActorAvatar actorType="agent" actorId={a.id} size="md" showStatusDot />
                    <span className="truncate flex-1">{a.name}</span>
                  </CommandItem>
                );
              })}
            </CommandGroup>
          )}
        </CommandList>
      </Command>
    </PopoverContent>
  );
}

function shortDate(date: string | null, locale: string): string {
  if (!date) return "—";
  return formatDateOnly(date, { month: "short", day: "numeric" }, locale);
}

type ActivityT = ReturnType<typeof useT<"issues">>["t"];

/**
 * 给活动流里的状态键标出可读名称。`resolveLabel` 是工作区目录解析器，用来给自定义状态取名；
 * 没有它——或者该状态已被删除——内置状态仍会拿到它的 i18n 名称，其余情况回退为原始键值。(MUL-6243)
 */
function statusLabel(
  status: string,
  t: ActivityT,
  resolveLabel?: (statusKey: string) => string,
): string {
  if (resolveLabel) return resolveLabel(status);
  if (status in STATUS_CONFIG) {
    return t(($) => $.status[statusCategoryOfKey(status)]);
  }
  return status;
}

function formatActivity(
  entry: TimelineEntry,
  t: ActivityT,
  locale: string,
  resolveActorName?: (type: string, id: string) => string,
  resolveStatusLabel?: (statusKey: string) => string,
): string {
  const details = (entry.details ?? {}) as Record<string, string>;
  switch (entry.action) {
    case "created":
      return t(($) => $.activity.created);
    case "status_changed":
      return t(($) => $.activity.status_changed, {
        from: statusLabel(details.from ?? "?", t, resolveStatusLabel),
        to: statusLabel(details.to ?? "?", t, resolveStatusLabel),
      });
    case "priority_changed":
      return t(($) => $.activity.priority_changed, {
        from: priorityLabel(details.from ?? "?", t),
        to: priorityLabel(details.to ?? "?", t),
      });
    case "assignee_changed": {
      const isSelfAssign = details.to_type === entry.actor_type && details.to_id === entry.actor_id;
      if (isSelfAssign) return t(($) => $.activity.self_assigned);
      const toName = details.to_id && details.to_type && resolveActorName
        ? resolveActorName(details.to_type, details.to_id)
        : null;
      if (toName) return t(($) => $.activity.assigned_to, { name: toName });
      if (details.from_id && !details.to_id) return t(($) => $.activity.removed_assignee);
      return t(($) => $.activity.changed_assignee);
    }
    case "start_date_changed": {
      if (!details.to) return t(($) => $.activity.start_date_removed);
      const formatted = formatDateOnly(details.to, { month: "short", day: "numeric" }, locale);
      return t(($) => $.activity.start_date_set, { date: formatted });
    }
    case "due_date_changed": {
      if (!details.to) return t(($) => $.activity.due_date_removed);
      const formatted = formatDateOnly(details.to, { month: "short", day: "numeric" }, locale);
      return t(($) => $.activity.due_date_set, { date: formatted });
    }
    case "title_changed":
      return t(($) => $.activity.title_renamed, {
        from: details.from ?? "?",
        to: details.to ?? "?",
      });
    case "description_updated":
      return t(($) => $.activity.description_updated);
    case "task_completed":
      return t(($) => $.activity.task_completed, { count: entry.coalesced_count ?? 1 });
    case "task_failed":
      return t(($) => $.activity.task_failed, { count: entry.coalesced_count ?? 1 });
    case "squad_leader_evaluated": {
      const reason = details.reason?.trim();
      switch (details.outcome) {
        case "action":
          return reason
            ? t(($) => $.activity.squad_leader_action_reason, { reason })
            : t(($) => $.activity.squad_leader_action);
        case "no_action":
          return reason
            ? t(($) => $.activity.squad_leader_no_action_reason, { reason })
            : t(($) => $.activity.squad_leader_no_action);
        case "failed":
          return reason
            ? t(($) => $.activity.squad_leader_failed_reason, { reason })
            : t(($) => $.activity.squad_leader_failed);
        default:
          return t(($) => $.activity.squad_leader_evaluated);
      }
    }
    default:
      return entry.action ?? "";
  }
}


// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// 无回复线程的稳定引用。如果内联写 `[]`，每次渲染都会新建一个数组，
// 从而破坏 CommentCard / ResolvedThreadBar 上的 React.memo 优化。
const EMPTY_REPLIES: TimelineEntry[] = [];

// ---------------------------------------------------------------------------
// 边栏属性的渐进式展示
// ---------------------------------------------------------------------------
//
// 边栏里展示的属性分两组：
//   - 核心属性：始终渲染（状态 / 负责人 / 项目）
//   - 可选属性：只有当 issue 在该字段有值、或用户本次会话通过"+ 添加属性"显式加进来时
//     才渲染（优先级 / 阶段 / 开始日期 / 截止日期 / 标签）
//
// 父任务不在任何一组里——它在 Properties 块下方有自己独立的一节，
// 只有当 issue 确实有父任务时才渲染。
//
// `OPTIONAL_PROP_KEYS` 是开放集合——新增一个可选字段意味着：
// 在这里追加一项、在下方 JSX switch 里接好对应行、再加一个语言包 key。
// 选择器、可见性规则、添加属性菜单全都由这一份列表驱动。
// `stage` 只对子任务有意义（相对于它的同级子任务），所以它的行和
// "添加属性"入口在渲染处都受 `issue.parent_issue_id` 控制——它仍保留在这个列表里，
// 是为了让初始化和可见性逻辑与其他可选属性走同一套机制。
const OPTIONAL_PROP_KEYS = ["priority", "stage", "start_date", "due_date", "labels"] as const;
type OptionalPropKey = (typeof OPTIONAL_PROP_KEYS)[number];

function isOptionalPropSet(
  issue: Issue,
  key: OptionalPropKey,
  attachedLabelsCount: number,
): boolean {
  switch (key) {
    case "priority":
      return !!issue.priority;
    case "stage":
      return issue.stage !== null && issue.stage !== undefined;
    case "start_date":
      return !!issue.start_date;
    case "due_date":
      return !!issue.due_date;
    case "labels":
      return attachedLabelsCount > 0;
  }
}

/**
 * 把一个父任务的子任务按阶段（stage）分组排序，用于展示：
 * 有阶段的组按 stage 升序排列，未分组（stage === null）的组放最后。
 * 调用方只有在确实存在阶段时，才给每个组渲染阶段表头。
 * @param children 父任务的直接子任务列表
 * @returns 按阶段分好组的子任务列表（含一个 stage 为 null 的未分组项）
 */
export function groupSubIssuesByStage(
  children: Issue[],
): { stage: number | null; items: Issue[] }[] {
  const byStage = new Map<number, Issue[]>();
  const unstaged: Issue[] = [];
  for (const c of children) {
    if (c.stage != null) {
      const arr = byStage.get(c.stage);
      if (arr) arr.push(c);
      else byStage.set(c.stage, [c]);
    } else {
      unstaged.push(c);
    }
  }
  const groups: { stage: number | null; items: Issue[] }[] = [...byStage.keys()]
    .sort((a, b) => a - b)
    .map((s) => ({ stage: s, items: byStage.get(s) as Issue[] }));
  if (unstaged.length > 0) groups.push({ stage: null, items: unstaged });
  return groups;
}

// 按元素引用做浅比较判断两个数组是否相等。用途：当"当前这条线程"没有变化时，
// 复用上一次渲染的每线程回复切片——即使外层的 `timeline` 数组因为某个无关线程的
// WS 事件被重建了，也不会影响本条线程的复用。
function shallowEqualEntries(a: TimelineEntry[], b: TimelineEntry[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

// 交给 <Virtuoso> 使用的扁平化逐项结构。Virtuoso 需要一个扁平数组，
// 每个元素对应一行要渲染的内容；我们保留 `timelineView.groups` 的分组逻辑
// （连续同人活动仍会折叠成一个活动组行），但把它投射成一个可辨识联合类型，
// 方便 itemContent 分发器按 kind 分支渲染。
type TimelineItem =
  | { kind: "comment"; id: string; entry: TimelineEntry }
  | { kind: "resolved-bar"; id: string; entry: TimelineEntry }
  | { kind: "activity-group"; id: string; entries: TimelineEntry[] };

type RawTimelineGroup = {
  type: "comment" | "activities";
  entries: TimelineEntry[];
};

function flattenGroups(
  groups: ReadonlyArray<RawTimelineGroup>,
  expandedResolved: ReadonlySet<string>,
): TimelineItem[] {
  const out: TimelineItem[] = [];
  for (const group of groups) {
    if (group.type === "comment") {
      const entry = group.entries[0]!;
      const isResolved = !!entry.resolved_at;
      const isExpanded = expandedResolved.has(entry.id);
      out.push(
        isResolved && !isExpanded
          ? { kind: "resolved-bar", id: entry.id, entry }
          : { kind: "comment", id: entry.id, entry },
      );
    } else {
      out.push({
        kind: "activity-group",
        id: group.entries[0]!.id,
        entries: group.entries,
      });
    }
  }
  return out;
}

function TimelineSkeleton() {
  return (
    <div className="mt-4 flex flex-col gap-3">
      {[0, 1].map((i) => (
        <div key={i} className="flex gap-3 p-4">
          <Skeleton className="h-10 w-10 shrink-0 rounded-full" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-4 w-32" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-4/5" />
          </div>
        </div>
      ))}
    </div>
  );
}

// 即便尾部的活动块处于展开状态，我们仍把它的内容截断到最近的 N 条——
// 一个包含 50 次状态变更的块，对评论区造成的淹没程度和 50 个各含 1 条的块一样严重。
// 更早的条目会折叠在"显示 N 条更多活动"这一行后面，点击后原地展开。
const LAST_ACTIVITY_BLOCK_VISIBLE_LIMIT = 8;

// 活动块的可折叠包装组件。较旧的块默认折叠成一行"N 条活动"摘要，
// 避免时间线被状态/优先级/负责人的频繁变更刷屏；尾部的块保持展开，
// 因为它通常能回答"刚刚发生了什么？"。展开状态由父组件持有，
// 这样在 Virtuoso 滚动导致的挂载/卸载之后状态还能保留。
function ActivityBlock({
  entries,
  expanded,
  onToggle,
  truncateOlder,
  showOlder,
  onToggleShowOlder,
  getActorName,
  resolveStatusLabel,
  resolveStatusCategory,
  resolveStatusColor,
  t,
  timeAgo,
  locale,
}: {
  entries: TimelineEntry[];
  expanded: boolean;
  onToggle: () => void;
  // 仅对尾部块生效：为 true 时，正文只显示最近 LAST_ACTIVITY_BLOCK_VISIBLE_LIMIT 条，
  // 更早的条目折叠在"显示 N 条更多活动"的内联开关后面。
  truncateOlder: boolean;
  showOlder: boolean;
  onToggleShowOlder: () => void;
  getActorName: (type: string, id: string) => string;
  resolveStatusLabel: (statusKey: string) => string;
  resolveStatusCategory: (statusKey: string) => IssueStatusCategory;
  /** 自定义状态自己的 `#rrggbb` 颜色；内置状态或未知键返回 null。 */
  resolveStatusColor: (statusKey: string) => string | null;
  t: ActivityT;
  timeAgo: (dateStr: string) => string;
  locale: string;
}) {
  if (!expanded) {
    const count = entries.length;
    return (
      <div className="pb-3 px-4 max-md:px-3">
        <button
          type="button"
          onClick={onToggle}
          className="flex items-center gap-1.5 text-caption text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronRight className="h-3 w-3 shrink-0" />
          <span>{t(($) => $.activity.activity_count, { count })}</span>
        </button>
      </div>
    );
  }
  const hiddenOlderCount =
    truncateOlder && !showOlder && entries.length > LAST_ACTIVITY_BLOCK_VISIBLE_LIMIT
      ? entries.length - LAST_ACTIVITY_BLOCK_VISIBLE_LIMIT
      : 0;
  const visibleEntries =
    hiddenOlderCount > 0 ? entries.slice(-LAST_ACTIVITY_BLOCK_VISIBLE_LIMIT) : entries;
  // 在默认的截断状态下隐藏"收起 N 条活动"的折叠表头。用户只是扫一眼近期活动时，
  // "显示 N 条更多"链接是唯一需要的控件——叠放两行箭头看起来像嵌套折叠，
  // 徒增视觉噪音。一旦用户显式展开更早条目，表头会重新出现，
  // 好让他们能把整个块再折回成一条计数行。
  const showHeader = hiddenOlderCount === 0;
  return (
    <div className="pb-3 px-4 max-md:px-3 flex flex-col gap-3">
      {showHeader && (
        <button
          type="button"
          onClick={onToggle}
          className="flex items-center gap-1.5 text-caption text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronDown className="h-3 w-3 shrink-0" />
          <span>{t(($) => $.activity.activity_count, { count: entries.length })}</span>
        </button>
      )}
      {hiddenOlderCount > 0 && (
        <button
          type="button"
          onClick={onToggleShowOlder}
          className="flex items-center gap-1.5 text-caption text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronRight className="h-3 w-3 shrink-0" />
          <span>{t(($) => $.activity.show_more_activities, { count: hiddenOlderCount })}</span>
        </button>
      )}
      {visibleEntries.map((entry) => {
        const details = (entry.details ?? {}) as Record<string, string>;
        const isStatusChange = entry.action === "status_changed";
        const isPriorityChange = entry.action === "priority_changed";
        const isStartDateChange = entry.action === "start_date_changed";
        const isDueDateChange = entry.action === "due_date_changed";

        let leadIcon: React.ReactNode;
        if (isStatusChange && details.to) {
          leadIcon = (
            <StatusIcon
              status={details.to as IssueStatus}
              category={resolveStatusCategory(details.to ?? "")}
              color={resolveStatusColor(details.to ?? "")}
              className="h-4 w-4 shrink-0"
            />
          );
        } else if (isPriorityChange && details.to) {
          leadIcon = <PriorityIcon priority={details.to as IssuePriority} className="h-4 w-4 shrink-0" />;
        } else if (isStartDateChange) {
          leadIcon = <CalendarClock className="h-4 w-4 shrink-0 text-muted-foreground" />;
        } else if (isDueDateChange) {
          leadIcon = <Calendar className="h-4 w-4 shrink-0 text-muted-foreground" />;
        } else {
          leadIcon = (
            <ActorAvatar
              actorType={entry.actor_type}
              actorId={entry.actor_id}
              name={entry.actor_name}
              avatarUrl={entry.actor_avatar_url}
              profileRequiresDirectoryEntry
              size="sm"
            />
          );
        }

        return (
          <div key={entry.id} className="flex items-center text-caption text-muted-foreground">
            <div className="mr-2 flex w-4 shrink-0 justify-center">
              {leadIcon}
            </div>
            <div className="flex min-w-0 flex-1 items-center gap-1">
              <span className="shrink-0 font-medium">
                {entry.actor_name || getActorName(entry.actor_type, entry.actor_id)}
              </span>
              <span className="truncate">{formatActivity(entry, t, locale, getActorName, resolveStatusLabel)}</span>
              {(entry.coalesced_count ?? 1) > 1 &&
                entry.action !== "task_completed" &&
                entry.action !== "task_failed" && (
                  <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-caption font-medium tabular-nums text-muted-foreground">
                    {t(($) => $.activity.coalesced_badge, { count: entry.coalesced_count ?? 1 })}
                  </span>
                )}
              <Tooltip>
                <TooltipTrigger
                  render={
                    <span className="ml-auto shrink-0 cursor-default">
                      {timeAgo(entry.created_at)}
                    </span>
                  }
                />
                <TooltipContent side="top">
                  {new Date(entry.created_at).toLocaleString(locale)}
                </TooltipContent>
              </Tooltip>
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// SubIssueRow —— 子任务列表项，支持内联编辑状态与负责人
// ---------------------------------------------------------------------------

function SubIssueRow({
  child,
  childProgress,
  rowProps,
  customProperties,
}: {
  child: Issue;
  /** 该子任务自己作为父任务时的进度（它本身也可能有子任务）。 */
  childProgress?: { done: number; total: number };
  /** 用户级展示偏好：该行要显示哪些内置字段。 */
  rowProps: SubIssueRowProperties;
  /** 用户选择在行上展示的工作区自定义属性。 */
  customProperties: IssueProperty[];
}) {
  const { t } = useT("issues");
  const locale = useLocale();
  const paths = useWorkspacePaths();
  const updateIssue = useUpdateIssue();
  const { selectedIds, toggle: toggleSelected } = useIssueSurfaceSelection();
  const selected = selectedIds.has(child.id);
  // Category, not key: a custom status in the done/cancelled categories is
  // finished work and has to strike through like any other. (MUL-6243)
  const isDone = issueBehavesAsAny(child, ["done", "cancelled"]);
  const labels = rowProps.labels ? (child.labels ?? []) : [];
  const customPropsWithValue = customProperties.filter(
    (p) => child.properties?.[p.id] !== undefined,
  );

  const handleUpdate = useCallback(
    (updates: Partial<UpdateIssueRequest>) => {
      updateIssue.mutate(
        {
          id: child.id,
          ...updates,
        },
        {
          onError: (err) =>
            toast.error(
              err instanceof Error && err.message
                ? err.message
                : t(($) => $.detail.update_failed),
            ),
        },
      );
    },
    [child.id, updateIssue, t],
  );

  // AppLink 只包住标题/标识区。选择器和复选框都是它的兄弟节点，
  // 所以点击它们不会触发导航——不需要 stopPropagation 技巧，
  // 也不会挡住原生复选框/选择器的触发。
  // 在行的任意位置右键，会打开共享的问题操作菜单（优先级、标签、日期、删除等），
  // 和列表行使用的是同一个菜单。
  return (
    <IssueActionsContextMenu issue={child}>
      <div
        className={cn(
          "flex items-center gap-2.5 px-3 py-2 hover:bg-accent/50 transition-colors group/row",
          selected && "bg-accent/30",
        )}
      >
        {/* 优先级图标 ⇄ 复选框的切换槽位，和主列表行一致：悬停/聚焦时，
            优先级图标让位给选择复选框。用 opacity（而不是 display）切换，
            是为了保证复选框始终能被键盘 Tab 到。 */}
        <div className="relative flex h-4 w-4 shrink-0 items-center justify-center">
          {rowProps.priority && (
            <PriorityIcon
              priority={child.priority}
              className={cn(
                "transition-opacity",
                selected
                  ? "opacity-0"
                  : "group-hover/row:opacity-0 group-focus-within/row:opacity-0",
              )}
            />
          )}
          <input
            type="checkbox"
            checked={selected}
            onChange={() => toggleSelected(child.id)}
            aria-label={t(($) => $.detail.select_sub_issue_aria, {
              identifier: child.identifier,
            })}
            className={cn(
              "absolute inset-0 cursor-pointer accent-primary transition-opacity",
              selected
                ? "opacity-100"
                : "opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100",
            )}
          />
        </div>
        <StatusPicker
          status={child.status}
          onUpdate={handleUpdate}
          align="start"
          trigger={
            <StatusIcon
              status={child.status}
              className="h-[15px] w-[15px] shrink-0"
            />
          }
        />
        <AppLink
          href={paths.issueDetail(child.id)}
          className="flex min-w-0 flex-1 items-center gap-2.5"
        >
          <span className="text-micro text-muted-foreground tabular-nums font-medium shrink-0">
            {child.identifier}
          </span>
          <IssueAgentActivityIndicator issueId={child.id} />
          <span className="flex min-w-0 flex-1 items-center gap-1.5">
            <span
              className={cn(
                "text-body truncate",
                isDone
                  ? "text-muted-foreground"
                  : "group-hover/row:text-foreground",
              )}
            >
              {child.title}
            </span>
            {labels.length > 0 && (
              <span className="hidden max-w-[200px] shrink-0 items-center gap-1 overflow-hidden md:inline-flex">
                {labels.slice(0, 2).map((label) => (
                  <LabelChip key={label.id} label={label} />
                ))}
                {labels.length > 2 && (
                  <span className="text-micro text-muted-foreground">
                    +{labels.length - 2}
                  </span>
                )}
              </span>
            )}
            {customPropsWithValue.length > 0 && (
              <span className="hidden max-w-[260px] shrink-0 items-center gap-1 overflow-hidden md:inline-flex">
                {customPropsWithValue.slice(0, 3).map((property) => (
                  <span
                    key={property.id}
                    className="inline-flex max-w-[120px] items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground"
                  >
                    <PropertyIcon property={property} className="size-3 text-micro" />
                    <CustomPropertyValueDisplay
                      property={property}
                      value={child.properties?.[property.id]}
                    />
                  </span>
                ))}
              </span>
            )}
            {rowProps.childProgress && childProgress && childProgress.total > 0 && (
              <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5">
                <ProgressRing
                  done={childProgress.done}
                  total={childProgress.total}
                  size={11}
                />
                <span className="text-micro text-muted-foreground tabular-nums font-medium">
                  {childProgress.done}/{childProgress.total}
                </span>
              </span>
            )}
          </span>
        </AppLink>
        {rowProps.dueDate && child.due_date && (
          <DueDatePicker
            dueDate={child.due_date}
            onUpdate={handleUpdate}
            align="end"
            trigger={
              <span
                className={cn(
                  "flex shrink-0 items-center gap-1 text-caption tabular-nums",
                  !isDone && isPastDateOnly(child.due_date)
                    ? "text-destructive"
                    : "text-muted-foreground",
                )}
              >
                <CalendarDays className="size-3" />
                {shortDate(child.due_date, locale)}
              </span>
            }
          />
        )}
        {rowProps.assignee && (
          <AssigneePicker
            assigneeType={child.assignee_type}
            assigneeId={child.assignee_id}
            onUpdate={handleUpdate}
            align="end"
            trigger={
              child.assignee_type && child.assignee_id ? (
                <ActorAvatar
                  actorType={child.assignee_type}
                  actorId={child.assignee_id}
                  size="sm"
                  className="shrink-0"
                />
              ) : (
                <span
                  aria-hidden
                  className="h-5 w-5 rounded-full border border-dashed border-muted-foreground/30 shrink-0"
                />
              )
            }
          />
        )}
      </div>
    </IssueActionsContextMenu>
  );
}

// ---------------------------------------------------------------------------
// SubIssueDisplayPopover —— 用户级"行上要显示哪些属性"的开关
// ---------------------------------------------------------------------------

// 复用主显示面板的 `card_*` 文案——属性名完全一致，
// 所以子任务控件不需要自己的语言包 key。
const SUB_ISSUE_ROW_PROPERTY_LABEL_KEY: Record<
  SubIssueRowPropertyKey,
  "card_priority" | "card_labels" | "card_child_progress" | "card_due_date" | "card_assignee"
> = {
  priority: "card_priority",
  labels: "card_labels",
  childProgress: "card_child_progress",
  dueDate: "card_due_date",
  assignee: "card_assignee",
};

function SubIssueDisplayPopover({
  workspaceProperties,
}: {
  workspaceProperties: IssueProperty[];
}) {
  const { t } = useT("issues");
  const rowProperties = useSubIssueDisplayStore((s) => s.rowProperties);
  const rowPropertyIds = useSubIssueDisplayStore((s) => s.rowPropertyIds);
  const toggleRowProperty = useSubIssueDisplayStore((s) => s.toggleRowProperty);
  const toggleRowPropertyId = useSubIssueDisplayStore((s) => s.toggleRowPropertyId);

  return (
    <Popover>
      <Tooltip>
        <PopoverTrigger
          render={
            <TooltipTrigger
              render={
                <button
                  type="button"
                  className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
                  aria-label={t(($) => $.display.tooltip)}
                >
                  <SlidersHorizontal className="h-3.5 w-3.5" />
                </button>
              }
            />
          }
        />
        <TooltipContent side="bottom">{t(($) => $.display.tooltip)}</TooltipContent>
      </Tooltip>
      <PopoverContent align="end" className="w-56 p-0">
        <div className="px-3 py-2.5">
          <div className="space-y-2">
            {SUB_ISSUE_ROW_PROPERTY_KEYS.map((key) => (
              <label
                key={key}
                className="flex cursor-pointer items-center justify-between"
              >
                <span className="text-body">
                  {t(($) => $.display[SUB_ISSUE_ROW_PROPERTY_LABEL_KEY[key]])}
                </span>
                <Switch
                  size="sm"
                  checked={rowProperties[key]}
                  onCheckedChange={() => toggleRowProperty(key)}
                />
              </label>
            ))}
            {workspaceProperties.map((property) => (
              <label
                key={property.id}
                className="flex cursor-pointer items-center justify-between gap-3"
              >
                <span className="flex min-w-0 items-center gap-1.5 truncate text-body">
                  <PropertyIcon property={property} className="size-3.5 text-caption" />
                  <span className="truncate">{property.name}</span>
                </span>
                <Switch
                  size="sm"
                  checked={rowPropertyIds.includes(property.id)}
                  onCheckedChange={() => toggleRowPropertyId(property.id)}
                />
              </label>
            ))}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}

// ---------------------------------------------------------------------------
// 组件 Props
// ---------------------------------------------------------------------------

interface IssueDetailProps {
  issueId: string;
  onDelete?: () => void;
  /** 通过工具栏按钮把问题标记为已完成之后调用。 */
  onDone?: () => void;
  defaultSidebarOpen?: boolean;
  layoutId?: string;
  /** 设置后，详情页会自动滚动到该评论并短暂高亮它。 */
  highlightCommentId?: string;
  /**
   * 自增此值可在"已经挂载"的详情页上重放 `highlightCommentId` 的落地跳转。
   * 重新挂载本身会重放（全新挂载 + 已清除的 memento 记录）；
   * 这个 token 用于既没有重新挂载、也不能保证重新渲染的那条路径——
   * 即重复点击已经打开的通知行（见 InboxPage.handleSelect）。
   * 自增既重新武装落地守卫，也强制一次重新渲染来重读被清除的 memento 记录。
   */
  highlightRequestToken?: number;
  /**
   * 头部最左侧的插槽，用来替换移动端的边栏触发器。把本详情页嵌入一层深度的宿主
   * （比如手机上的收件箱）会在这里传入自己的"返回"控件，而不是在我们上方再叠一条
   * 48px 的栏——这个头部已经渲染的面包屑描述的是问题所属的容器，
   * 而不是读者来源的界面，所以只有宿主才知道该如何描述这段来路。
   */
  leadingAction?: ReactNode;
}

// ---------------------------------------------------------------------------
// 未找到（Not found）
// ---------------------------------------------------------------------------

/**
 * 导出此组件是为了让 `IssueDetailRoute` 在标识符对应不到任何 issue 时渲染它。
 * 此时路由绝不能回退到 `IssueDetail`：那样会在已经失败的查询上再挂一个 observer，
 * 触发重新请求并无限循环地重启 resolve/remount 周期。
 * @param showBackLink 是否显示"返回"链接
 * @param leading 宿主提供的返回控件（对应 IssueDetailProps.leadingAction）
 */
export function IssueNotFound({
  showBackLink = true,
  leading,
}: {
  showBackLink?: boolean;
  /**
   * 宿主提供的返回方式，对应 `IssueDetailProps.leadingAction`。把整屏都交给本组件的宿主
   * （比如手机上的收件箱）已经没有自己的栏可用，所以这个状态必须承担返回路径，否则就没有返回入口。
   */
  leading?: ReactNode;
}) {
  const { t } = useT("issues");
  const backOrReplace = useBackOrReplace();
  const paths = useWorkspacePaths();

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      {leading && (
        <div className={cn("flex h-12 shrink-0 items-center gap-2 border-b", PAGE_GUTTER)}>{leading}</div>
      )}
      <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-3 text-body text-muted-foreground">
        <p>{t(($) => $.detail.not_found)}</p>
        {showBackLink && (
          <Button variant="outline" size="sm" onClick={() => backOrReplace(paths.issues())}>
            <ChevronLeft className="mr-1 h-3.5 w-3.5" />
            {t(($) => $.detail.back)}
          </Button>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 加载骨架屏
// ---------------------------------------------------------------------------

/**
 * 导出此组件是为了让 `IssueDetailRoute` 在把标识符 URL 解析成 issue 期间展示相同的骨架屏——
 * 这两种等待对用户来说无法区分，渲染同一个骨架屏能保持体验一致。
 * @param leading 宿主提供的返回控件（可选）
 */
export function IssueDetailSkeleton({ leading }: { leading?: ReactNode } = {}) {
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      {/* 返回路径从第一帧就是真实可用的，而不是等 issue 加载完成：
          把自身栏位让给 `leadingAction` 的宿主，在骨架屏占屏期间没有别的东西可提供。 */}
      <div className={cn("flex h-12 shrink-0 items-center gap-2 border-b", PAGE_GUTTER)}>
        {leading ?? (
          <>
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-4 w-4" />
            <Skeleton className="h-4 w-24" />
          </>
        )}
      </div>
      <div className="flex flex-1 min-h-0">
        {/* 和下方已加载滚动容器的 scrollbar-gutter 一致，避免真实内容挂载时骨架列横向偏移。 */}
        <div className="flex-1 overflow-y-auto [scrollbar-gutter:stable_both-edges]">
          {/* 内边距与已加载的列完全一致（见其注释），避免真实内容挂载时骨架屏横向回流。 */}
          <div className="mx-auto w-full max-w-4xl px-3 py-6 space-y-6 md:px-8 md:py-8">
            <Skeleton className="h-8 w-3/4" />
            <div className="space-y-2">
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-5/6" />
              <Skeleton className="h-4 w-2/3" />
            </div>
            <Skeleton className="h-px w-full" />
            <div className="space-y-3">
              <Skeleton className="h-4 w-20" />
              <div className="flex items-start gap-3">
                <Skeleton className="h-8 w-8 shrink-0 rounded-full" />
                <div className="flex-1 space-y-2">
                  <Skeleton className="h-4 w-32" />
                  <Skeleton className="h-16 w-full rounded-lg" />
                </div>
              </div>
            </div>
          </div>
        </div>
        <div className="hidden md:block w-80 border-l p-4 space-y-5">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="flex items-center gap-2">
              <Skeleton className="h-3 w-16 shrink-0" />
              <Skeleton className="h-5 w-24" />
            </div>
          ))}
          <Skeleton className="h-px w-full" />
          {Array.from({ length: 3 }).map((_, i) => (
            <div key={i} className="flex items-center gap-2">
              <Skeleton className="h-3 w-16 shrink-0" />
              <Skeleton className="h-4 w-28" />
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// IssueDetail —— 问题详情页主体
// ---------------------------------------------------------------------------

/**
 * 问题详情页组件：渲染单个 issue 的完整详情（标题、描述、子任务、时间线/评论、右侧属性边栏），
 * 并处理标题/描述自动保存、滚动恢复、评论高亮跳转、子任务批量选择等交互。
 * @param issueId 要展示的 issue 的 id
 * @param onDelete 删除完成后的回调（可选）
 * @param onDone 标记为已完成后的回调（可选）
 * @param defaultSidebarOpen 桌面端边栏默认是否展开
 * @param layoutId 用于持久化面板布局的 id
 * @param highlightCommentId 需要自动滚动并高亮的评论 id（可选）
 * @param highlightRequestToken 重放高亮跳转的令牌（可选）
 * @param leadingAction 头部最左侧的自定义控件（可选）
 */
export function IssueDetail({ issueId, onDelete, onDone, defaultSidebarOpen = true, layoutId = "multica_issue_detail_layout", highlightCommentId, highlightRequestToken, leadingAction }: IssueDetailProps) {
  const { t } = useT("issues");
  const locale = useLocale();
  const timeAgo = useTimeAgo();
  const id = issueId;
  const user = useAuthStore((s) => s.user);
  const paths = useWorkspacePaths();
  const openModal = useModalStore((state) => state.open);

  // 问题导航 —— 从 React Query 的列表缓存里读取
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  // 工作区 owner 和 admin 可以管理任何人写的评论（对应后端 `comment.go:507-512`）。
  // 在这里算好，避免每条评论渲染时都要重复推导。
  const currentUserRole =
    members.find((m) => m.user_id === user?.id)?.role ?? null;
  const canModerateComments =
    currentUserRole === "owner" || currentUserRole === "admin";
  const findCachedListIssue = useCallback(
    (targetId: string | null | undefined) => {
      if (!targetId) return undefined;
      const cachedLists = queryClient.getQueriesData<ListIssuesCache>({
        queryKey: issueKeys.list(wsId),
      });
      for (const [, cached] of cachedLists) {
        const match = cached
          ? flattenIssueBuckets(cached).find((item) => item.id === targetId)
          : undefined;
        if (match) return match;
      }
      return undefined;
    },
    [queryClient, wsId],
  );
  const { getActorName } = useActorName();
  const resolveStatusLabel = useStatusLabel(wsId);
  // 图标集合是按"类别"（CATEGORY）来的（MUL-6243），所以一条自定义状态变更记录
  // 会画出和它旁边内置状态一样的图标——"In Review → Awaiting Response" 这样一行
  // 看起来像什么都没动。颜色才是承载自定义状态自身身份的东西，收件箱行和
  // 状态变更详情标签都是这样渲染的。`colorOf` 保证内置状态继续用它的语义 token，
  // 而不是目录里的种子十六进制色值。
  const { categoryOf: resolveStatusCategory, colorOf: resolveStatusColor } =
    useIssueStatuses(wsId);
  // 描述的自动保存是有意不做条件限制的（没有显式提交；编辑器在序列化前已经去掉了 `blob:`，
  // 并在之后的保存时绑定 id）。但它仍需要失败提示，否则一次失败的上传
  // 会直接抹掉自己的占位符，文件就无声无息地消失了。
  const { uploadWithToast } = useEditorUpload();
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: layoutId,
  });
  const sidebarRef = usePanelRef();
  const isMobile = useIsMobile();
  const desktopSidebarInitialOpen = getAnimatedRightSidebarInitialOpen(
    defaultSidebarOpen,
    defaultLayout,
  );
  const {
    open: desktopSidebarOpen,
    visualOpen: desktopSidebarVisualOpen,
    motionEnabled: desktopSidebarMotionEnabled,
    beginToggle: beginDesktopSidebarToggle,
    handleResize: handleDesktopSidebarResize,
  } = useAnimatedRightSidebarState(desktopSidebarInitialOpen);
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);

  useEffect(() => {
    if (isMobile) {
      setMobileSidebarOpen(false);
    }
  }, [isMobile]);
  const sidebarOpen = isMobile ? mobileSidebarOpen : desktopSidebarOpen;
  const [propertiesOpen, setPropertiesOpen] = useState(true);
  const [detailsOpen, setDetailsOpen] = useState(true);
  const [parentIssueOpen, setParentIssueOpen] = useState(true);
  const [pullRequestsOpen, setPullRequestsOpen] = useState(true);
  const [metadataOpen, setMetadataOpen] = useState(false);
  const githubSettings = useGitHubSettings();

  // 每个 issue、每次会话各自维护的"当前在边栏 Properties 区可见的可选属性"集合。
  // 切换到不同 issue 时，用"已经设值的字段"做初始化种子；"+ 添加属性"会加一项进来，
  // 但清空值不会移除它（避免编辑→清空时行来回闪烁）。切换到另一个 issue 时重置。
  const [visibleOptionalProps, setVisibleOptionalProps] = useState<Set<OptionalPropKey>>(
    () => new Set(),
  );
  // 自定义属性走同一套渐进式展示机制，但按属性定义 id 而不是静态键联合类型来标识。
  const [visibleCustomProps, setVisibleCustomProps] = useState<Set<string>>(() => new Set());
  const [autoOpenCustomProp, setAutoOpenCustomProp] = useState<string | null>(null);
  // 挂载后要立即自动打开的可选属性（用户刚从"+ 添加属性"选中它，
  // 我们希望直接把它送进编辑状态）。由匹配这个 key 的那一行消费，用完即清空。
  const [autoOpenProp, setAutoOpenProp] = useState<OptionalPropKey | null>(null);
  // "+ 添加属性"弹出层的受控状态。Base UI 的 Popover 不会在点击选项后自动关闭
  // （它不是 Menu 原语），所以弹出层会留在新自动打开的选择器后面——两个弹出层叠在一起。
  // 我们在 `addOptionalProp` 里显式关闭它。
  const [addPropPopoverOpen, setAddPropPopoverOpen] = useState(false);
  // Virtuoso 的 `customScrollParent` 需要 HTMLElement，而不是 ref。普通的
  // `useRef.current` 在填充时不会触发重新渲染，所以 Virtuoso 的 prop 永远收不到元素。
  // 用回调 ref + state 解决：setState 触发重新渲染，把元素交给 Virtuoso。
  const [scrollContainerEl, setScrollContainerEl] = useState<HTMLDivElement | null>(null);
  // 底部评论输入框。在把刚发布的评论滚进视野时测量它的高度，
  // 好让评论底部落在输入框上方而不是被它挡住。
  const composerRef = useRef<HTMLDivElement | null>(null);
  // 每次打开任务都展示最新消息；评论深链仍交给高亮逻辑定位。
  const scrollContainerKey = `main:${id}`;
  // 这个 issue 的评论高亮深链是否已经在这里落地过（能跨标签页卸载存活，
  // 与下方 didHighlightRef 不同）：一次只是恢复视图的重新挂载，不能再重跑跳转。
  //
  // 落地 effect 通过 ref 读取它，而不是通过依赖数组读取：记录落地会写入这个值本身，
  // 如果作为 effect 依赖，那次写入会在跳转后的一次提交里重新触发 effect——
  // 而它的清理函数会在第一帧运行前就取消居中 rAF 循环和高亮渐隐。所有需要重新评估落地的
  // 路径，都已经通过其他依赖（挂载、条目到达、token 自增）重新跑了 effect，
  // 且它之前的那次渲染会刷新这个 ref。
  const consumedHighlightId = useRestoredViewState(issueHighlightMementoKey(id));
  const consumedHighlightRef = useRef(consumedHighlightId);
  consumedHighlightRef.current = consumedHighlightId;
  const writeViewState = useViewStateWriter();
  const rightSidebarShortcutTargetRef = useRef<HTMLDivElement | null>(null);
  const attachScrollContainer = useCallback(
    (el: HTMLDivElement | null) => {
      rightSidebarShortcutTargetRef.current = el;
      setScrollContainerEl(el);
    },
    [],
  );
  const [pendingPostedCommentId, setPendingPostedCommentId] = useState<string | null>(null);
  const scrollToTimelineBottom = useCallback((commentId: string) => {
    setPendingPostedCommentId(commentId);
  }, []);
  const [highlightedId, setHighlightedId] = useState<string | null>(null);
  // 用户偏好：是否把底部评论栏钉在滚动视口里。在 `md` 断点以下无论偏好如何都会关闭——见 hook。
  const stickyComposer = useStickyComposer();

  // 每次会话各自维护：用户临时展开过哪些"已解决"的线程。不持久化（和 Linear 一致）——
  // 刷新后全部收拢回横条。用 store 承载（而非组件 state），
  // 这样命令面板的"折叠/展开全部评论"命令能从页面外部驱动它。
  const expandedResolved = useResolvedExpandStore(selectExpandedResolved(id));
  const setResolvedExpanded = useResolvedExpandStore((s) => s.setExpanded);
  const toggleResolvedExpand = useCallback((commentId: string, expand: boolean) => {
    setResolvedExpanded(id, commentId, expand);
    // 收拢时线程会变矮，视口会跳到它下面的内容；用最小的位移把刚折叠的线程拉回视野。
    // rAF 等折叠落地后再测量。
    if (!expand) {
      requestAnimationFrame(() =>
        document.getElementById(`comment-${commentId}`)?.scrollIntoView({ block: "nearest" }),
      );
    }
  }, [id, setResolvedExpanded]);
  const clearResolvedExpand = useCallback((commentId: string) => {
    setResolvedExpanded(id, commentId, false);
  }, [id, setResolvedExpanded]);

  // 每次会话各自维护的活动块展开覆盖。默认规则是"只展开尾部块"
  // （由下方 timelineView.groups 计算）；这两个集合记录与默认不一致的用户点击。
  // 之所以需要两个集合，是因为"默认"会在新活动块追加时翻转——
  // 如果没有显式的收拢覆盖，一个被手动收拢的旧块会在它不再是尾部块时重新展开（反之亦然）。
  // 不持久化，与上面已解决线程的行为一致。
  const [expandedActivityIds, setExpandedActivityIds] = useState<Set<string>>(() => new Set());
  const [collapsedActivityIds, setCollapsedActivityIds] = useState<Set<string>>(() => new Set());
  // 用户显式选择在尾部块里也展示更早（末尾 8 条之前）条目的块 id 集合。
  // 与展开/收拢集合相互独立，这样"收拢再展开"能保留"显示全部"的选择，
  // 并且当新评论落在该块之后、使其失去尾部位置时，这个选择也能保留。
  const [showOlderActivityIds, setShowOlderActivityIds] = useState<Set<string>>(() => new Set());
  const toggleActivityBlock = useCallback((id: string, currentlyExpanded: boolean) => {
    if (currentlyExpanded) {
      setCollapsedActivityIds((prev) => {
        const next = new Set(prev);
        next.add(id);
        return next;
      });
      setExpandedActivityIds((prev) => {
        if (!prev.has(id)) return prev;
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    } else {
      setExpandedActivityIds((prev) => {
        const next = new Set(prev);
        next.add(id);
        return next;
      });
      setCollapsedActivityIds((prev) => {
        if (!prev.has(id)) return prev;
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    }
  }, []);
  const showOlderActivities = useCallback((id: string) => {
    setShowOlderActivityIds((prev) => {
      if (prev.has(id)) return prev;
      const next = new Set(prev);
      next.add(id);
      return next;
    });
  }, []);
  const didHighlightRef = useRef<string | null>(null);
  // 最近一次看到的 highlightRequestToken；自增会重新武装 didHighlightRef，
  // 让下方的落地 effect 在已挂载的详情页上重放。
  const lastHighlightRequestTokenRef = useRef(highlightRequestToken);

  // 来自 React Query 的 issue 数据——用详情查询，若可用则用列表缓存做种子。
  // 只有存在 description 时才做种子；列表 API 不返回 description，
  // 所以不完整的一行列表数据不能伪装成已水合的详情。
  const { data: issue = null, isLoading: issueLoading, refetch: refetchIssue } = useQuery({
    ...issueDetailOptions(wsId, id),
    // 列表行和"问题已创建"的实时载荷有意省略了仅详情才有的 source-context 快照。
    // 它们仍可能通过 initialData 为这个查询做种子，所以详情视图挂载时
    // 始终和权威的详情端点做一次对齐。没有这个的话，全局 Infinity staleTime
    // 会一直把 source context 藏到整页刷新为止。
    refetchOnMount: "always",
    initialData: () => {
      const cached = findCachedListIssue(id);
      return cached?.description != null ? cached : undefined;
    },
  });
  const openCommentSubIssue = useCallback((commentId: string) => {
    if (!issue) return;
    openModal("quick-create-issue", {
      anchor_comment_id: commentId,
      parent_issue_id: issue.id,
      parent_issue_identifier: issue.identifier,
      ...(issue.project_id ? { project_id: issue.project_id } : {}),
      ...(issue.assignee_type && issue.assignee_id
        ? { assignee_type: issue.assignee_type, assignee_id: issue.assignee_id }
        : {}),
    });
  }, [issue, openModal]);

  // 记录最近访问
  const recordVisit = useRecentIssuesStore((s) => s.recordVisit);
  const recordRecentContext = useRecentContextStore((s) => s.recordVisit);
  useEffect(() => {
    if (issue) {
      recordVisit(wsId, issue.id);
      recordRecentContext(wsId, {
        type: "issue",
        id: issue.id,
        label: issue.identifier,
        subtitle: issue.title,
        status: issue.status,
      });
    }
  }, [issue?.id, wsId]); // eslint-disable-line react-hooks/exhaustive-deps

  // 当 issue 从"已加载"变为"缺失"时触发一次 `onDelete`。删除走的是 shell 层模态框，
  // 所以调用方（如收件箱）无法被直接通知——改为由详情页观察自身缓存被清空后执行回调。
  // 当没有提供回调时（独立路由），通过操作菜单的 `onDeletedFallbackPath` 导航返回。
  const hadIssueRef = useRef(false);
  const firedDeleteCallbackRef = useRef(false);
  useEffect(() => {
    if (issue) {
      hadIssueRef.current = true;
      firedDeleteCallbackRef.current = false;
      return;
    }
    if (
      hadIssueRef.current &&
      !issueLoading &&
      !firedDeleteCallbackRef.current &&
      onDelete
    ) {
      firedDeleteCallbackRef.current = true;
      onDelete();
    }
  }, [issue, issueLoading, onDelete]);

  // 自定义 hooks —— 封装时间线、反应、订阅者逻辑
  const {
    timeline, loading: timelineLoading,
    submitComment, submitReply,
    editComment, deleteComment, toggleResolveComment, toggleReaction: handleToggleReaction,
  } = useIssueTimeline(id, user?.id);

  // 1. Run diffs are shown on comments, but owned by runs: each run's card hangs
  //    under its newest comment only. Derived once from the full timeline (roots
  //    and replies) so no card has to scan the thread itself.
  // 2. The map is stabilized like `threadReplies`: reactions or edits produce a new
  //    timeline but the same mapping, and a fresh prop here would re-render every
  //    memoized CommentCard on the page.
  const prevRunDiffRef = useRef<Map<string, string>>(new Map());
  const runDiffCommentIds = useMemo(() => {
    const fresh = lastCommentIdByTaskId(timeline);
    const previous = prevRunDiffRef.current;
    if (
      previous.size === fresh.size &&
      [...fresh].every(([taskId, commentId]) => previous.get(taskId) === commentId)
    ) {
      return previous;
    }
    prevRunDiffRef.current = fresh;
    return fresh;
  }, [timeline]);

  // 解决/取消解决必须总是清掉会话内的展开记录，这样重新解决一条已经展开的线程
  // 会把它折回横条（展开集合只按 commentId 作键，不按解决状态）。没有这个包装的话，
  // "展开 → 取消解决 → 解决"的序列会在第二次解决后仍让线程保持展开。
  const handleResolveToggle = useCallback(
    (commentId: string, resolved: boolean) => {
      // 任何解决状态变更都要把线程折回：清掉线程 ROOT 的展开记录
      // （展开状态按 root id 作键，但解决目标可能是回复）。沿 parent_id 向上走到 root。
      const byId = new Map(timeline.map((e) => [e.id, e]));
      let cur = byId.get(commentId);
      while (cur?.parent_id && byId.get(cur.parent_id)) cur = byId.get(cur.parent_id)!;
      clearResolvedExpand(cur?.id ?? commentId);
      toggleResolveComment(commentId, resolved);
    },
    [timeline, clearResolvedExpand, toggleResolveComment],
  );

  // 记忆化的时间线分组。每次渲染都从最新 timeline 重建"每父级"映射，
  // 然后把每条线程的回复子树预展平成一个专属的 `threadReplies` 切片（按 root 存储）。
  // 切片通过 `prevThreadRepliesRef` 相对上一次渲染做稳定化：如果某条线程的扁平列表
  // 和上一次浅比较相等，就复用上一次的数组，这样 CommentCard / ResolvedThreadBar 上的
  // React.memo 才能短路。没有这个的话，每个 WS 事件（包括无关线程上的反应、编辑、AI 流式输出）
  // 都会给每张卡片一个全新的 prop 引用，迫使所有线程子树齐步重渲染。
  const prevThreadRepliesRef = useRef<Map<string, TimelineEntry[]>>(new Map());
  const timelineView = useMemo(() => {
    // 分组：顶层 = 活动 + 根评论；回复按父级 id 归桶，并在 CommentCard 内部嵌套渲染。
    // 不需要孤儿救援：timeline 是全量拉取的，所以每条回复的父级总在同一个数组里。
    const topLevel = timeline.filter(
      (e) => e.type === "activity" || !e.parent_id,
    );
    const repliesByParent = new Map<string, TimelineEntry[]>();
    for (const e of timeline) {
      if (e.type === "comment" && e.parent_id) {
        const list = repliesByParent.get(e.parent_id) ?? [];
        list.push(e);
        repliesByParent.set(e.parent_id, list);
      }
    }

    // 预展平每条顶层评论的线程子树（父评论 + 按渲染顺序的所有后代）。
    // 当线程没变化时复用上一次的数组引用，让无关的 CommentCard 保持 memo。
    const prevThreadReplies = prevThreadRepliesRef.current;
    const threadReplies = new Map<string, TimelineEntry[]>();
    for (const root of topLevel) {
      if (root.type !== "comment") continue;
      const fresh = collectThreadReplies(root.id, repliesByParent);
      const previous = prevThreadReplies.get(root.id);
      threadReplies.set(
        root.id,
        previous && shallowEqualEntries(previous, fresh) ? previous : fresh,
      );
    }
    prevThreadRepliesRef.current = threadReplies;

    // 合并同一个执行者 + 同一种动作的连续活动。
    // - task_completed / task_failed：无时间限制（它们会跨多次运行重复出现）
    // - 其他所有动作：在 2 分钟窗口内合并
    // - squad_leader_evaluated：永不合并；结果/原因是审计数据
    const COALESCE_MS = 2 * 60 * 1000;
    const NO_TIME_LIMIT_ACTIONS = new Set(["task_completed", "task_failed"]);
    const NEVER_COALESCE_ACTIONS = new Set(["squad_leader_evaluated"]);
    const coalesced: TimelineEntry[] = [];
    for (const entry of topLevel) {
      if (entry.type === "activity") {
        const prev = coalesced[coalesced.length - 1];
        if (
          !NEVER_COALESCE_ACTIONS.has(entry.action!) &&
          prev?.type === "activity" &&
          prev.action === entry.action &&
          prev.actor_type === entry.actor_type &&
          prev.actor_id === entry.actor_id &&
          (NO_TIME_LIMIT_ACTIONS.has(entry.action!) ||
            Math.abs(new Date(entry.created_at).getTime() - new Date(prev.created_at).getTime()) <= COALESCE_MS)
        ) {
          coalesced[coalesced.length - 1] = { ...entry, coalesced_count: (prev.coalesced_count ?? 1) + 1 };
          continue;
        }
      }
      coalesced.push(entry);
    }

    // 把连续的活动归到一组，好让连接线正确渲染
    const groups: { type: "activities" | "comment"; entries: TimelineEntry[] }[] = [];
    for (const entry of coalesced) {
      if (entry.type === "activity") {
        const last = groups[groups.length - 1];
        if (last?.type === "activities") {
          last.entries.push(entry);
        } else {
          groups.push({ type: "activities", entries: [entry] });
        }
      } else {
        groups.push({ type: "comment", entries: [entry] });
      }
    }

    return { threadReplies, groups };
  }, [timeline]);

  // 交给 <Virtuoso> 的扁平数组。当 timelineView.groups 变化（时间线事件）
  // 或 expandedResolved 翻转（用户切换已解决线程）时重新计算。
  // 放在 useMemo 里，保证无关重渲染时 Virtuoso 的数据引用保持稳定。
  const items = useMemo<TimelineItem[]>(
    () => flattenGroups(timelineView.groups, expandedResolved),
    [timelineView.groups, expandedResolved],
  );

  // 页面内查找（Cmd/Ctrl+F）。`items.length` 是内容信号，在评论增删时触发匹配重算；
  // 文本编辑由 hook 自己的 MutationObserver 捕获。打开查找会强制时间线在下方扁平渲染，
  // 让每条评论都进 DOM 以便匹配。
  const find = useInPageFind({
    container: scrollContainerEl,
    contentKey: items.length,
    enabled: !!issue,
  });
  // 导航到另一个 issue 时关闭查找栏（并清除高亮）——web 路由是复用本组件而不是重新挂载。
  const closeFind = find.closeFind;
  useEffect(() => {
    closeFind();
  }, [id, closeFind]);

  // 尾部活动块的 id——唯一默认展开的那个。
  const lastActivityGroupId = useMemo(() => {
    for (let i = timelineView.groups.length - 1; i >= 0; i--) {
      const g = timelineView.groups[i]!;
      if (g.type === "activities") return g.entries[0]!.id;
    }
    return null;
  }, [timelineView.groups]);

  // 回复评论 id → 根评论 id 的映射，这样指向某条回复的深链
  // （回复在 CommentCard 内部，而不是扁平 items 数组里）能回退为把根线程滚进视野。
  // 没有这个的话，指向回复的收件箱通知会落到 items[-1] 上直接短路。
  const replyToRoot = useMemo(() => {
    const map = new Map<string, string>();
    for (const [rootId, replies] of timelineView.threadReplies) {
      for (const reply of replies) {
        map.set(reply.id, rootId);
      }
    }
    return map;
  }, [timelineView.threadReplies]);

  // 深链目标在扁平 items 数组里的下标。根评论是直接 findIndex 命中；
  // 回复 id 则查它所属的根。
  const targetIdx = useMemo(() => {
    if (!highlightCommentId) return -1;
    const direct = items.findIndex((it) => it.id === highlightCommentId);
    if (direct >= 0) return direct;
    const rootId = replyToRoot.get(highlightCommentId);
    if (!rootId) return -1;
    return items.findIndex((it) => it.id === rootId);
  }, [items, highlightCommentId, replyToRoot]);

  // 每条评论线程一个条目（含已折叠的解决横条），跳过活动组。
  // 从 Virtuoso 渲染的同一个扁平 `items` 数组派生，所以顺序始终和页面一致。
  // 两个线程导航器——右边缘轨道和头部面板——都从这一处派生，
  // 因此它们对"有哪些线程、顺序是什么"永远不会不一致。
  //
  // resolved 标志来自 `deriveThreadResolution`，而不是 `resolved-bar` 的 kind：
  // 那个 kind 只覆盖当前处于折叠状态的根解决，会漏掉回复的解决，
  // 并且会在用户展开一条已解决线程时立刻翻转为 false。
  const minimapThreads = useMemo<ThreadNavThread[]>(
    () =>
      items.flatMap((it) => {
        if (it.kind !== "comment" && it.kind !== "resolved-bar") return [];
        const replies = timelineView.threadReplies.get(it.id) ?? EMPTY_REPLIES;
        const currentUserId = user?.id ?? "";
        // "@me" 表示这条线程跟当前读者有关：他发起过、回复过，或在某处被 @ 过。
        // 作者身份也算——你在里面说过话的线程是你理应跟进的；
        // 如果只算字面意义的 @ 提及，会把其中大部分都漏掉。
        const involvesMe =
          currentUserId !== "" &&
          ([it.entry, ...replies].some(
            (entry) =>
              (entry.actor_type === "member" && entry.actor_id === currentUserId) ||
              mentionsUser(entry.content, currentUserId),
          ));
        return [
          {
            id: it.id,
            entry: it.entry,
            resolved: deriveThreadResolution(it.entry, replies).kind !== "none",
            replyCount: replies.length,
            involvesMe,
          },
        ];
      }),
    [items, timelineView.threadReplies, user?.id],
  );

  // 当时间线走"扁平渲染"（深链或页内查找）时没有 Virtuoso 实例——
  // 此时 minimap 跳转直接驱动滚动容器本身。
  const isFlatTimeline = !!highlightCommentId || find.open;
  const virtuosoRef = useRef<VirtuosoHandle>(null);
  // 把刚发布的那条评论滚进视野，让它的底部刚好停在吸底评论区上方（绝不被它挡住）。
  // 回复嵌在它所属根评论的 CommentCard 里，所以滚动目标是装着它的那条顶层行。
  // 扁平与虚拟化两条时间线需要各自的滚动驱动，因此拆成下面两个 effect——之所以拆开，
  // 是因为只有扁平那条依赖 `scrollContainerEl`；把该依赖加进虚拟化 effect 的话，
  // 一次容器重测就会在半路上取消掉正在进行的滚动。

  // 虚拟化模式：滚动由 Virtuoso 接管。第一次跳转会按估算高度先渲染出行；
  // 测出真实高度后，第二次跳转再用真实高度对齐。额外的 `offset` 把行的底部
  // 抬离吸底评论区，避免被它盖住。
  useEffect(() => {
    if (!pendingPostedCommentId || isFlatTimeline) return;
    const rootId = replyToRoot.get(pendingPostedCommentId) ?? pendingPostedCommentId;
    const index = items.findIndex((item) => item.id === rootId);
    if (index < 0) return;
    let timer: number | undefined;
    const scrollEnd = () => {
      const composerHeight = composerRef.current?.getBoundingClientRect().height ?? 0;
      virtuosoRef.current?.scrollToIndex({ index, align: "end", offset: composerHeight });
    };
    const frame = requestAnimationFrame(() => {
      scrollEnd();
      timer = window.setTimeout(() => {
        scrollEnd();
        setPendingPostedCommentId(null);
      }, 100);
    });
    return () => {
      cancelAnimationFrame(frame);
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [pendingPostedCommentId, items, replyToRoot, isFlatTimeline]);

  // 扁平模式（查找栏 / 深链）没有 Virtuoso，只能手推 scrollTop：把行底对齐到
  // 评论区顶边。评论的异步排版（markdown、代码高亮、图片）会不断改变它的高度，
  // 所以每一帧都重新对齐，直到稳定（误差 1px 内）或约 0.5s 耗尽——
  // 与深链落地用的 rAF 稳定逻辑相同。
  useEffect(() => {
    if (!pendingPostedCommentId || !isFlatTimeline) return;
    const rootId = replyToRoot.get(pendingPostedCommentId) ?? pendingPostedCommentId;
    if (items.findIndex((item) => item.id === rootId) < 0) return;
    let rafId = 0;
    let frames = 0;
    let last = -1;
    const alignBottom = () => {
      const el = document.getElementById(`comment-${rootId}`);
      const container = scrollContainerEl;
      if (el && container) {
        const c = container.getBoundingClientRect();
        const e = el.getBoundingClientRect();
        const composerHeight = composerRef.current?.getBoundingClientRect().height ?? 0;
        const target = Math.max(0, container.scrollTop + (e.bottom - c.bottom) + composerHeight);
        container.scrollTop = target;
        if (Math.abs(target - last) > 1 && ++frames < 30) {
          last = target;
          rafId = requestAnimationFrame(alignBottom);
          return;
        }
      }
      setPendingPostedCommentId(null);
    };
    rafId = requestAnimationFrame(alignBottom);
    return () => cancelAnimationFrame(rafId);
  }, [pendingPostedCommentId, items, replyToRoot, isFlatTimeline, scrollContainerEl]);
  const jumpFlashTimerRef = useRef<number | null>(null);
  useEffect(
    () => () => {
      if (jumpFlashTimerRef.current !== null) window.clearTimeout(jumpFlashTimerRef.current);
    },
    [],
  );
  const jumpToThread = useCallback(
    (threadId: string) => {
      if (isFlatTimeline) {
        // 扁平模式挂载了每一行，锚点一定在 DOM 里。直接推容器的 scrollTop——
        // 绝不用原生 scrollIntoView，它会连带滚动桌面壳层 (#3929)。
        const el = document.getElementById(`comment-${threadId}`);
        const container = scrollContainerEl;
        if (!el || !container) return;
        const c = container.getBoundingClientRect();
        const e = el.getBoundingClientRect();
        container.scrollTop = Math.max(0, container.scrollTop + (e.top - c.top) - 16);
      } else {
        // 虚拟化模式：目标行可能还没挂载，所以按下标滚动、让 Virtuoso 去挂载它。
        // offset 留出一点顶部间距。
        const index = items.findIndex((it) => it.id === threadId);
        if (index < 0) return;
        virtuosoRef.current?.scrollToIndex({ index, align: "start", offset: -16 });
      }
      // 像收件箱深链那样给落点线程加一次闪烁，让眼睛在瞬间跳转后有个锚点。
      //（已折叠的解决横条不接收高亮 prop——对它们来说滚动本身就是反馈。）
      setHighlightedId(threadId);
      if (jumpFlashTimerRef.current !== null) window.clearTimeout(jumpFlashTimerRef.current);
      jumpFlashTimerRef.current = window.setTimeout(() => setHighlightedId(null), 2000);
    },
    [isFlatTimeline, items, scrollContainerEl],
  );

  // 头部线程导航器。`open`/`pinned` 放在这里而不是面板内部，是因为全局快捷键
  // 需要能"一打开就是钉住态"，也因为右缘轨道要用 `threadNavHoverId` 点亮
  // 面板指针停留的那一格——两个导航器共用同一套坐标系 (MUL-5755)。
  const [threadNavOpen, setThreadNavOpen] = useState(false);
  const [threadNavPinned, setThreadNavPinned] = useState(false);
  const [threadNavHoverId, setThreadNavHoverId] = useState<string | null>(null);
  const handleThreadNavOpenChange = useCallback((open: boolean, pinned: boolean) => {
    setThreadNavOpen(open);
    setThreadNavPinned(pinned);
    if (!open) setThreadNavHoverId(null);
  }, []);

  // 全局快捷键 Mod+Shift+O。作用域限于当前已挂载的问题详情，并像 Cmd+F 一样
  // 以可见性为门槛，因此在桌面端只有当前可见的那个标签页会拦截按键。
  useEffect(() => {
    if (minimapThreads.length === 0) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.repeat || isImeComposing(e)) return;
      if (!shortcutMatchesEvent(getShortcut("openThreadNav"), e)) return;
      if (!scrollContainerEl || scrollContainerEl.getClientRects().length === 0) return;
      e.preventDefault();
      // 快捷键是用户的明确动作，所以直接打开"钉住"状态。在悬浮预览上再按一次
      // 会把该预览钉住而非关闭，与点击按钮的行为保持一致。
      if (threadNavOpen && threadNavPinned) {
        handleThreadNavOpenChange(false, false);
      } else {
        handleThreadNavOpenChange(true, true);
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [
    handleThreadNavOpenChange,
    minimapThreads.length,
    scrollContainerEl,
    threadNavOpen,
    threadNavPinned,
  ]);

  const {
    reactions: issueReactions,
    toggleReaction: handleToggleIssueReaction,
  } = useIssueReactions(id, user?.id);

  const {
    subscribers, isSubscribed, subscriptionReason, subscriptionKnown,
    togglePending, subtreePending,
    toggleSubscribe: handleToggleSubscribe, toggleSubscriber,
    unsubscribeFromSubtree: handleUnsubscribeSubtree,
  } = useIssueSubscribers(id, user?.id);

  // 上传到该 issue 的附件清单。驱动描述编辑器"点击时重新签名下载"：
  // NodeView 会用这条列表去比对 `src`/`href`，在调用 `/api/attachments/{id}` 之前
  // 先解析出附件 id。
  const { data: issueAttachments } = useQuery(issueAttachmentsOptions(id));

  // 子任务相关的查询
  const parentIssueId = issue?.parent_issue_id;
  const { data: parentIssue = null } = useQuery({
    ...issueDetailOptions(wsId, parentIssueId ?? ""),
    enabled: !!parentIssueId,
    initialData: () => findCachedListIssue(parentIssueId),
  });

  // 面包屑里的"项目"段。以 issue 的 project_id 为准——同样的 URL 无论从哪条路
  // 径进来都渲染出同样的面包屑。
  const issueProjectId = issue?.project_id;
  const { data: breadcrumbProject = null } = useQuery({
    ...projectDetailOptions(wsId, issueProjectId ?? ""),
    enabled: !!issueProjectId,
  });
  const {
    data: childIssues = [],
    isSuccess: childIssuesLoaded,
    isFetching: childIssuesFetching,
  } = useQuery({
    ...childIssuesOptions(wsId, id),
    enabled: !!issue,
  });
  // 记录"这个 issue 到底有没有子任务"，区别于"我们还没查过"。childIssuesOptions
  // 把 refetchOnMount 设成了 "always"，所以每次挂载都会重新拉取缓存快照，
  // 仅凭 isSuccess 仍只能描述上一次访问的状态。下面的退订控件会根据这个答案
  // 选择不同的服务端写入，因此"默认值/过期的空数组"绝不能当成"没有子任务"(MUL-5714)。
  const childCountKnown = childIssuesLoaded && !childIssuesFetching;
  // 父任务的子任务——用于在标题下方的"是 … 的子任务"面包屑旁渲染"x/y"进度。
  const { data: parentChildIssues = [] } = useQuery({
    ...childIssuesOptions(wsId, parentIssueId ?? ""),
    enabled: !!parentIssueId,
  });
  // 工作区级的 父任务→(完成数/总数) 映射；让每行子任务不打开详情也能显示
  // 自己的嵌套进度环。与列表视图用的是同一条查询，通常已命中缓存。
  const { data: subIssueProgress } = useQuery({
    ...childIssueProgressOptions(wsId),
    enabled: childIssues.length > 0,
  });
  // 用户级"子任务行显示什么字段"的偏好（内置字段开关 + 主动勾选的工作区自定义属性）。
  // `subIssueCustomProps` 拿持久化的属性 id 去比对当前工作区里存活的、未归档的属性定义——
  // 来自其他工作区或已归档属性的 id 会被过滤掉。
  const subIssueRowProps = useSubIssueDisplayStore((s) => s.rowProperties);
  const subIssueRowPropertyIds = useSubIssueDisplayStore((s) => s.rowPropertyIds);
  // 用 store 而非 useState 承载，这样折叠状态能跨"离开该 issue 再返回"存续于本次会话内。
  const subIssuesCollapsed = useSubIssuesCollapseStore((s) =>
    s.collapsedIssueIds.has(issueId),
  );
  const setSubIssuesCollapsedFor = useSubIssuesCollapseStore(
    (s) => s.setCollapsed,
  );
  const setSubIssuesCollapsed = useCallback(
    (update: boolean | ((prev: boolean) => boolean)) => {
      const prev = useSubIssuesCollapseStore
        .getState()
        .collapsedIssueIds.has(issueId);
      setSubIssuesCollapsedFor(
        issueId,
        typeof update === "function" ? update(prev) : update,
      );
    },
    [issueId, setSubIssuesCollapsedFor],
  );

  // The sub-issue list owns its selection, reset per issue. Rows, select-all
  // and the inline batch toolbar must read the same one: a peek renders this
  // inside a surface whose own selection provider would otherwise win for the
  // toolbar while the rows wrote elsewhere.
  const subIssueSelection = useCreateIssueSurfaceSelection(`sub-issues:${id}`);
  const {
    selectedIds,
    select: selectIds,
    deselect: deselectIds,
  } = subIssueSelection;

  const childIssueIds = useMemo(() => childIssues.map((c) => c.id), [childIssues]);
  const childSelectedCount = childIssueIds.filter((cid) =>
    selectedIds.has(cid),
  ).length;
  const allChildrenSelected =
    childIssueIds.length > 0 && childSelectedCount === childIssueIds.length;
  const someChildrenSelected = childSelectedCount > 0;
  const handleToggleSelectAllChildren = useCallback(() => {
    if (allChildrenSelected) deselectIds(childIssueIds);
    else selectIds(childIssueIds);
  }, [allChildrenSelected, childIssueIds, deselectIds, selectIds]);

  const loading = issueLoading;

  // 深链落地。语义上等价于跳转 `#comment-${id}`：找到那个 id 的元素并滚到视野中。
  // 当设置了 `highlightCommentId` 时下方时间线走扁平渲染（不做虚拟化），
  // 所以本 effect 在 commit 后执行时，每条评论的 id 都已进 DOM。
  //
  // 若目标是"已折叠的已解决线程"里的一条回复：回复本身不在 items 中
  //（只有横条根在）。需要先把线程自动展开，等 items 重新扁平化后 effect 再跑一次。
  //
  // `scrollContainerEl` 放进依赖是因为：issue 查询未完成时本组件会提前 return 一个
  // 加载骨架。滚动容器 ref 只有在加载完成的那次渲染后才会被填充，
  // 所以它正是"时间线（以及深链目标 id）已经真正渲染出来"的信号。
  useEffect(() => {
    if (!highlightCommentId || items.length === 0) return;
    // 这是一次显式的重播请求（再次点击已打开的那条通知）：宿主已经清掉 memento 条目
    // 并递增了 token，所以重新武装落地守卫，然后照常执行跳转。
    if (lastHighlightRequestTokenRef.current !== highlightRequestToken) {
      lastHighlightRequestTokenRef.current = highlightRequestToken;
      didHighlightRef.current = null;
    }
    if (didHighlightRef.current === highlightCommentId) return;
    // 这条深链在本次挂载前已经落过地（memento 里留有记录——比如切标签页回来或
    // 页内返回）。此时恢复的滚动位置就是用户离开时的状态，跳转不能跟它抢。
    // 新一次的选择会清掉记录，所以这里的拦截只作用于*重复*落地。
    if (consumedHighlightRef.current === highlightCommentId) {
      didHighlightRef.current = highlightCommentId;
      return;
    }

    const rootId = replyToRoot.get(highlightCommentId);
    if (rootId && rootId !== highlightCommentId) {
      // 根评论已解决 → 整条线程是一根折叠横条。
      if (items[targetIdx]?.kind === "resolved-bar") {
        toggleResolvedExpand(rootId, true);
        return;
      }
      // 解决者是某条回复 → 其余回复折进"N 条评论"横条后面；
      // 若目标正是那些被折起的回复之一，就先展开。
      const rootItem = items[targetIdx];
      if (rootItem?.kind === "comment" && !expandedResolved.has(rootId)) {
        const resolution = deriveThreadResolution(
          rootItem.entry,
          timelineView.threadReplies.get(rootId) ?? EMPTY_REPLIES,
        );
        if (resolution.kind === "reply" && resolution.resolutionId !== highlightCommentId) {
          toggleResolvedExpand(rootId, true);
          return;
        }
      }
    }

    // A deleted reply renders nothing, so the id a notification or share link
    // carries has no anchor to scroll to and no row to flash. Land on the
    // comment above where it was; the id itself stays this landing's identity
    // for the guards and the memento below.
    const threadRootId = rootId ?? highlightCommentId;
    const landingId = commentLandingTarget(
      highlightCommentId,
      threadRootId,
      timelineView.threadReplies.get(threadRootId) ?? EMPTY_REPLIES,
    );
    const el = document.getElementById(`comment-${landingId}`);
    const container = scrollContainerEl;
    if (!el || !container) return;

    didHighlightRef.current = highlightCommentId;
    // 把这次落地记进 memento，让以后只是"恢复视图"的重挂载（切标签页回来）
    // 跳过跳转，而不是在恢复的滚动位置上再重播一遍。
    writeViewState(issueHighlightMementoKey(id), highlightCommentId);

    // 直接推容器自己的 scrollTop，把目标评论在它自己的滚动容器里居中——绝不用原生
    // scrollIntoView。规范规定原生 scrollIntoView 会滚动*每一个*可滚动的祖先：
    // 在冷启动挂载、时间线还在持续增长（agent 流式输出）时，内层滚动区自己无法完成
    // 居中，滚动就会向上传导，带动桌面壳层的 `overflow:hidden` 容器——把整页连同头部
    // 一起顶出屏幕顶部，且没有任何滚动条可以拉回来，直到一次 resize 重新排版 (#3929)。
    // 把滚动限定在 `container` 内可保持受控；跨帧反复居中，等异步高度（markdown、代码
    // 高亮、流式回复）稳定后即可精准落点——而不是像原生那样依赖祖先滚动。
    let rafId = 0;
    let frames = 0;
    let last = -1;
    const center = () => {
      const c = container.getBoundingClientRect();
      const e = el.getBoundingClientRect();
      const target = Math.max(
        0,
        container.scrollTop + (e.top - c.top) - (container.clientHeight - e.height) / 2,
      );
      container.scrollTop = target;
      // 内容仍在排版 → 居中偏移量还在变化；继续居中直到稳定（1px 内）或消耗约 0.5s 帧。
      if (Math.abs(target - last) > 1 && ++frames < 30) {
        last = target;
        rafId = requestAnimationFrame(center);
      }
    };
    rafId = requestAnimationFrame(center);

    setHighlightedId(landingId);
    const fade = window.setTimeout(() => setHighlightedId(null), 2500);
    return () => {
      cancelAnimationFrame(rafId);
      clearTimeout(fade);
    };
  }, [highlightCommentId, highlightRequestToken, id, writeViewState, items, targetIdx, scrollContainerEl, replyToRoot, expandedResolved, timelineView, toggleResolvedExpand]);

  const descEditorRef = useRef<ContentEditorRef>(null);
  const descriptionEditingRef = useRef(false);
  const [descriptionConflictDraft, setDescriptionConflictDraft] = useState<string | null>(null);
  const descriptionAttachmentIdsRef = useRef<string[]>([]);
  const descriptionSaveInFlightRef = useRef(false);
  const descriptionSaveIssueIdRef = useRef(id);
  const pendingDescriptionSaveRef = useRef<{
    markdown: string;
    baseMarkdown: string;
    attachmentIds: string[];
  } | null>(null);
  // 从一开始就保持描述编辑器处于挂载状态。与空的输入框外壳不同，一篇渲染完的长描述
  // 不能在 react-markdown 与 ProseMirror 之间来回切换——每块微小的行高差异会累积成
  // 可见的滚动/排版跳动。分块渲染 Markdown 的路径让这一份"急切"编辑器成本可控；
  // 标题与输入框仍保持懒加载。
  const titleEditorRef = useRef<TitleEditorRef>(null);
  const titleBaseRef = useRef<string | undefined>(issue?.title);
  const [titleConflictDraft, setTitleConflictDraft] = useState<string | null>(null);
  // 当有冲突的标题草稿被丢弃时递增。TitleEditor 只在挂载时读取 `defaultValue`，
  // 也不暴露命令式 setter，所以"重新挂载"是把服务端标题放回编辑器的唯一办法。
  const [titleResetToken, setTitleResetToken] = useState(0);
  useEffect(() => {
    setTitleConflictDraft(null);
    setDescriptionConflictDraft(null);
    titleBaseRef.current = undefined;
    descriptionAttachmentIdsRef.current = [];
    descriptionSaveInFlightRef.current = false;
    descriptionSaveIssueIdRef.current = id;
    pendingDescriptionSaveRef.current = null;
  }, [id]);
  const titleLazy = useLazyEditor({ editorRef: titleEditorRef, resetKey: id });
  const { isDragOver: descDragOver, dropZoneProps: descDropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((file) => descEditorRef.current?.uploadFile(file)),
  });
  // 描述编辑器里"尚未完成绑定"的上传。上传时不带 `issueId`（避免用户在 markdown
  // 里删掉该文件后产生孤儿附件），所以它们一开始是游离的，等下次保存描述时通过
  // `attachment_ids` 重新绑定。这段逻辑还驱动编辑器预览——在绑定往返完成前，
  // 文本/代码类附件就能先显示一个"眼睛"图标。
  const [descPendingAttachments, setDescPendingAttachments] = useState<Attachment[]>([]);
  const descPendingAttachmentsRef = useRef<Attachment[]>([]);
  // 记忆化是因为下方图片序列以该数组作为依赖键：没有它的话，concat 分支每次渲染都
  // 会给出一份新引用，导致整个 issue 的图片序列在每次渲染时都重建一遍。
  const descEditorAttachments = useMemo(
    () =>
      descPendingAttachments.length > 0
        ? [...(issueAttachments ?? []), ...descPendingAttachments]
        : issueAttachments,
    [issueAttachments, descPendingAttachments],
  );

  // 该 issue 里的每一张图片，按页面渲染顺序排列：先是描述，再是每条时间线评论
  // 及其嵌套的回复子树 (MUL-5752)。之所以取自 `items` 而非扁平时间线，是为了让回复
  // 紧挨着它渲染其下的根；之所以取自数据而非 DOM，是因为 Virtuoso 只挂载可见窗口。
  //
  // 当前折叠起来的已解决线程也照样贡献图片：它们属于该 issue，距上屏只差一次点击，
  // 若排除它们，计数器就会随"哪些线程恰好被折起"而漂移。
  const imageSequence = useMemo(() => {
    const blocks: ImageSequenceBlock[] = [
      { content: issue?.description, attachments: descEditorAttachments },
    ];
    for (const item of items) {
      if (item.kind === "activity-group") continue;
      blocks.push({
        content: item.entry.content,
        attachments: item.entry.attachments,
      });
      for (const reply of timelineView.threadReplies.get(item.entry.id) ?? []) {
        blocks.push({ content: reply.content, attachments: reply.attachments });
      }
    }
    return collectImageSequence(blocks);
  }, [issue?.description, descEditorAttachments, items, timelineView.threadReplies]);

  const handleDescriptionUpload = useCallback(
    async (file: File) => {
      const result = await uploadWithToast(file);
      if (result) {
        descPendingAttachmentsRef.current = [
          ...descPendingAttachmentsRef.current,
          result,
        ];
        setDescPendingAttachments(descPendingAttachmentsRef.current);
      }
      return result;
    },
    [uploadWithToast],
  );

  useEffect(() => {
    descPendingAttachmentsRef.current = [];
    setDescPendingAttachments([]);
  }, [id]);

  // 共享的 issue 操作（mutation、置顶、复制链接、派发模态框等）。
  // 必须在 `if (!issue)` 提前 return 之前调用，以保证 hook 顺序稳定。
  const actions = useIssueActions(issue);
  const handleUpdateField = actions.updateField;

  // 标签放在独立的查询里（不在 issue 主体上）——这里先取到数量，
  // 以便种子化逻辑决定"标签"这一可选行是否要为一个已带标签的 issue 显示。
  const { data: attachedLabels = [] } = useQuery(issueLabelsOptions(wsId, id));
  const attachedLabelsCount = attachedLabels.length;

  // 自定义属性目录。包含已归档的定义：issue 可能仍带有归档前写入的值，
  // 在有人清掉它之前，那一行必须保持可渲染（只读）。
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(wsId, true));

  // 子任务行只露出"存活"的定义：显示选择器只提供未归档属性，chip 也只渲染
  // 能在这里解析到的 id。
  const activeWorkspaceProperties = useMemo(
    () => workspaceProperties.filter((p) => p.archived !== true),
    [workspaceProperties],
  );
  const subIssueCustomProps = useMemo(
    () =>
      subIssueRowPropertyIds
        .map((pid) => activeWorkspaceProperties.find((p) => p.id === pid))
        .filter((p): p is IssueProperty => !!p),
    [subIssueRowPropertyIds, activeWorkspaceProperties],
  );

  // 初始化"可见的可选属性"集合：
  //   - 切换 issue 时，重置为当前已设置的那些字段
  //   - 同一 issue 内，增量吸收用户刚设置的字段
  //     （保证用户在一次会话里"编辑后又清空"，该行依然保持可见）
  // 移除只发生在切换 issue 时——清空字段永不触发移除。
  const seededIssueIdRef = useRef<string | null>(null);
  useEffect(() => {
    if (!issue) return;
    if (seededIssueIdRef.current !== issue.id) {
      seededIssueIdRef.current = issue.id;
      setAutoOpenProp(null);
      setAutoOpenCustomProp(null);
      const seed = new Set<OptionalPropKey>();
      for (const k of OPTIONAL_PROP_KEYS) {
        if (isOptionalPropSet(issue, k, attachedLabelsCount)) seed.add(k);
      }
      setVisibleOptionalProps(seed);
      setVisibleCustomProps(new Set(Object.keys(issue.properties ?? {})));
      return;
    }
    setVisibleOptionalProps((prev) => {
      let next = prev;
      for (const k of OPTIONAL_PROP_KEYS) {
        if (isOptionalPropSet(issue, k, attachedLabelsCount) && !next.has(k)) {
          if (next === prev) next = new Set(prev);
          next.add(k);
        }
      }
      return next;
    });
    setVisibleCustomProps((prev) => {
      let next = prev;
      for (const propertyId of Object.keys(issue.properties ?? {})) {
        if (!next.has(propertyId)) {
          if (next === prev) next = new Set(prev);
          next.add(propertyId);
        }
      }
      return next;
    });
  }, [issue, attachedLabelsCount]);

  const addOptionalProp = useCallback(
    (key: OptionalPropKey) => {
      setVisibleOptionalProps((prev) => {
        if (prev.has(key)) return prev;
        const next = new Set(prev);
        next.add(key);
        return next;
      });
      setAutoOpenProp(key);
      // 先收起"+ 添加属性"弹层，免得它叠在我们即将自动打开的选择器后面。
      setAddPropPopoverOpen(false);
    },
    [],
  );

  const addCustomProp = useCallback((propertyId: string) => {
    setVisibleCustomProps((prev) => {
      if (prev.has(propertyId)) return prev;
      const next = new Set(prev);
      next.add(propertyId);
      return next;
    });
    setAutoOpenCustomProp(propertyId);
    setAddPropPopoverOpen(false);
  }, []);

  // 在下一个渲染周期清掉"自动打开"标记：选择器通过 useState 初始化器只读取一次
  // `defaultOpen`，这样它们能保留挂载时捕获的开合状态，同时后续交互不会再次触发它。
  useEffect(() => {
    if (autoOpenProp === null) return;
    setAutoOpenProp(null);
  }, [autoOpenProp]);

  useEffect(() => {
    if (autoOpenCustomProp === null) return;
    setAutoOpenCustomProp(null);
  }, [autoOpenCustomProp]);

  const handleToggleSidebar = useCallback(() => {
    if (isMobile) {
      setMobileSidebarOpen((open) => !open);
      return;
    }

    const panel = sidebarRef.current;
    if (!panel) return;
    const nextOpen = panel.isCollapsed();
    beginDesktopSidebarToggle(nextOpen);
    window.requestAnimationFrame(() => {
      if (nextOpen) panel.expand();
      else panel.collapse();
    });
  }, [beginDesktopSidebarToggle, isMobile, sidebarRef]);

  useRightSidebarShortcut(rightSidebarShortcutTargetRef, handleToggleSidebar);

  useIssueDetailScrollRestore({
    restoreKey: `${wsId}:${id}`,
    scrollContainerEl,
    ready: !!issue && !loading && !timelineLoading,
    // 仅在评论深链仍有待执行的落地时禁用——那时滚动归跳转逻辑管。一旦落地被消费
    //（比如切标签页回来），本 hook 的重试循环本身就是恢复逻辑：一次性 ref 附着所赋的值
    // 会被一个"异步内容（markdown、图片）还没长到捕获高度"的容器截断。
    disabled: !!highlightCommentId && consumedHighlightId !== highlightCommentId,
    startAtBottom: true,
  });

  // 让身份值在与 issue 详情无关的更新里保持稳定，这样 context 消费者只会在
  // 所属 issue 的身份真正改变时重渲染。
  const resolvedIssueId = issue?.id;
  const resolvedIssueIdentifier = issue?.identifier;
  const currentIssueRenderContext = useMemo(
    () =>
      resolvedIssueId && resolvedIssueIdentifier
        ? { id: resolvedIssueId, identifier: resolvedIssueIdentifier }
        : null,
    [resolvedIssueId, resolvedIssueIdentifier],
  );

  if (loading) {
    return <IssueDetailSkeleton leading={leadingAction} />;
  }

  if (!issue) {
    return <IssueNotFound showBackLink={!onDelete} leading={leadingAction} />;
  }

  const persistDescriptionSave = (
    draft: { markdown: string; baseMarkdown: string; attachmentIds: string[] },
  ) => {
    descriptionSaveInFlightRef.current = true;
    handleUpdateField(
      {
        description: draft.markdown,
        description_base: draft.baseMarkdown,
        attachment_ids:
          draft.attachmentIds.length > 0 ? draft.attachmentIds : undefined,
      },
      {
        onSuccess: (serverIssue) => {
          if (descriptionSaveIssueIdRef.current !== id) return;
          setDescriptionConflictDraft(null);
          descriptionSaveInFlightRef.current = false;
          const pending = pendingDescriptionSaveRef.current;
          pendingDescriptionSaveRef.current = null;
          if (pending) {
            // 通常服务端接受的文档和我们提交的完全一致。若服务端补上了迟到的
            // 渠道媒体，就把提交的正文当作下一版编辑器基线：服务端能识别出唯一
            // 差异只是编辑器从未见过的媒体，从而再次把它保留下来。
            const nextBase = serverIssue.description === draft.markdown
              ? serverIssue.description
              : draft.markdown;
            persistDescriptionSave({ ...pending, baseMarkdown: nextBase });
          }
        },
        onError: (error) => {
          if (descriptionSaveIssueIdRef.current !== id) return;
          descriptionSaveInFlightRef.current = false;
          const pending = pendingDescriptionSaveRef.current;
          pendingDescriptionSaveRef.current = null;
          if (errorCode(error) === "revision_conflict") {
            const latest = pending ?? draft;
            descriptionAttachmentIdsRef.current = latest.attachmentIds;
            setDescriptionConflictDraft(latest.markdown);
          }
        },
      },
    );
  };

  const queueDescriptionSave = (
    draft: { markdown: string; baseMarkdown: string; attachmentIds: string[] },
  ) => {
    descriptionAttachmentIdsRef.current = draft.attachmentIds;
    if (descriptionSaveInFlightRef.current) {
      pendingDescriptionSaveRef.current = draft;
      return;
    }
    persistDescriptionSave(draft);
  };

  const sidebarContent = (
    <div className="space-y-5">
      {/* 属性（Properties）——边栏第一个可折叠区块 */}
      <div>
        <button
          type="button"
          className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${propertiesOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
          onClick={() => setPropertiesOpen(!propertiesOpen)}
        >
          {t(($) => $.detail.section_properties)}
          <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${propertiesOpen ? "rotate-90" : ""}`} />
        </button>
        {propertiesOpen && <div className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 pl-2">
          {/* 核心属性——始终渲染。 */}
          <PropRow label={t(($) => $.detail.prop_status)}>
            <StatusPicker status={issue.status} onUpdate={handleUpdateField} align="start" />
          </PropRow>
          <PropRow label={t(($) => $.detail.prop_assignee)}>
            <AssigneePicker assigneeType={issue.assignee_type} assigneeId={issue.assignee_id} onUpdate={handleUpdateField} align="start" />
          </PropRow>
          <PropRow label={t(($) => $.detail.prop_project)}>
            <ProjectPicker
              projectId={issue.project_id}
              onUpdate={handleUpdateField}
            />
          </PropRow>

          {/* 可选属性——仅当 issue 上已设置、或本次会话里通过"+ 添加属性"加入
              时才渲染。行顺序与 `OPTIONAL_PROP_KEYS` 一致。 */}
          {visibleOptionalProps.has("priority") && (
            <PropRow label={t(($) => $.detail.prop_priority)}>
              <PriorityPicker
                priority={issue.priority}
                onUpdate={handleUpdateField}
                align="start"
                defaultOpen={autoOpenProp === "priority"}
              />
            </PropRow>
          )}
          {issue.parent_issue_id != null && visibleOptionalProps.has("stage") && (
            <PropRow label={t(($) => $.detail.prop_stage)}>
              <StagePicker
                stage={issue.stage}
                onUpdate={handleUpdateField}
                maxStage={maxSiblingStage(parentChildIssues)}
                align="start"
                defaultOpen={autoOpenProp === "stage"}
              />
            </PropRow>
          )}
          {visibleOptionalProps.has("start_date") && (
            <PropRow label={t(($) => $.detail.prop_start_date)}>
              <StartDatePicker
                startDate={issue.start_date}
                onUpdate={handleUpdateField}
                defaultOpen={autoOpenProp === "start_date"}
              />
            </PropRow>
          )}
          {visibleOptionalProps.has("due_date") && (
            <PropRow label={t(($) => $.detail.prop_due_date)}>
              <DueDatePicker
                dueDate={issue.due_date}
                onUpdate={handleUpdateField}
                defaultOpen={autoOpenProp === "due_date"}
              />
            </PropRow>
          )}
          {visibleOptionalProps.has("labels") && (
            <PropRow label={t(($) => $.detail.prop_labels)}>
              <LabelPicker
                issueId={issue.id}
                align="start"
                defaultOpen={autoOpenProp === "labels"}
              />
            </PropRow>
          )}

          {/* 自定义属性——与内置可选属性相同的渐进式展示：当 issue 有该字段值、
              或用户本次会话加入了该属性时才渲染该行。已归档的定义以只读方式渲染，
              直到其值被清掉。 */}
          {workspaceProperties
            .filter(
              (p) =>
                issue.properties?.[p.id] !== undefined ||
                (!p.archived && visibleCustomProps.has(p.id)),
            )
            .map((p) => (
              <PropRow
                key={p.id}
                label={
                  <>
                    <PropertyIcon property={p} className="size-3.5 text-caption" />
                    <span className="truncate">{p.name}</span>
                  </>
                }
              >
                <CustomPropertyValueEditor
                  issue={issue}
                  property={p}
                  defaultOpen={autoOpenCustomProp === p.id}
                />
              </PropRow>
            ))}

          {/* "+ 添加属性"——弹出一个 Popover 列出尚未显示的字段。
              当所有可选字段都上屏后即隐藏。放在与上面相同的网格里作为整行，
              自带内边距，让视觉节奏跟随上方各行。 */}
          {(OPTIONAL_PROP_KEYS.some((k) => !visibleOptionalProps.has(k) && (k !== "stage" || issue.parent_issue_id != null)) ||
            workspaceProperties.some((p) => !p.archived && !visibleCustomProps.has(p.id) && issue.properties?.[p.id] === undefined)) && (
            <div className="col-span-2 mt-1">
              <Popover open={addPropPopoverOpen} onOpenChange={setAddPropPopoverOpen}>
                <PopoverTrigger
                  className="flex items-center gap-1.5 rounded-md px-2 py-1 -mx-2 text-caption text-muted-foreground hover:bg-accent/50 hover:text-foreground transition-colors"
                >
                  <Plus className="h-3 w-3 shrink-0" />
                  <span>{t(($) => $.detail.add_property_action)}</span>
                </PopoverTrigger>
                {/* 选项外观镜像右侧属性区各行的排版（text-caption、灰调图标），
                    且每项以所选 picker 会用的那个图标开头，让下拉列表像是
                    "下方会出现什么"的预览。 */}
                <PopoverContent align="start" className="w-44 p-1">
                  {OPTIONAL_PROP_KEYS.filter((k) => !visibleOptionalProps.has(k) && (k !== "stage" || issue.parent_issue_id != null)).map((k) => (
                    <button
                      key={k}
                      type="button"
                      onClick={() => addOptionalProp(k)}
                      className="flex w-full items-center gap-2 rounded-md px-2 py-1 text-caption text-foreground transition-colors hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
                    >
                      {k === "priority" && (
                        <PriorityIcon priority="important_not_urgent" inheritColor className="text-muted-foreground" />
                      )}
                      {k === "stage" && (
                        <Milestone className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      {k === "start_date" && (
                        <CalendarClock className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      {k === "due_date" && (
                        <CalendarDays className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      {k === "labels" && (
                        <Tag className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      )}
                      <span className="truncate">
                        {k === "priority" && t(($) => $.detail.prop_priority)}
                        {k === "stage" && t(($) => $.detail.prop_stage)}
                        {k === "start_date" && t(($) => $.detail.prop_start_date)}
                        {k === "due_date" && t(($) => $.detail.prop_due_date)}
                        {k === "labels" && t(($) => $.detail.prop_labels)}
                      </span>
                    </button>
                  ))}
                  {(() => {
                    const addable = workspaceProperties.filter(
                      (p) =>
                        !p.archived &&
                        !visibleCustomProps.has(p.id) &&
                        issue.properties?.[p.id] === undefined,
                    );
                    if (addable.length === 0) return null;
                    return (
                      <>
                        <div className="my-1 h-px bg-border" />
                        {addable.map((p) => (
                          <button
                            key={p.id}
                            type="button"
                            onClick={() => addCustomProp(p.id)}
                            className="flex w-full items-center gap-2 rounded-md px-2 py-1 text-caption text-foreground transition-colors hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
                          >
                            {p.icon ? (
                              <PropertyIcon property={p} className="size-3.5 text-caption" />
                            ) : (
                              <SlidersHorizontal className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                            )}
                            <span className="truncate">{p.name}</span>
                          </button>
                        ))}
                      </>
                    );
                  })()}
                </PopoverContent>
              </Popover>
            </div>
          )}
        </div>}
      </div>

      {/* 快捷操作——边栏里唯一"干点事"的区块，因此紧挨属性区下方、置于所有只读区块
          之上。当工作区没有对该成员可见的激活操作时不渲染任何内容。它不按调用权限过滤：
          成员能看到并点开一个他其实无权执行的操作，拒绝原因在执行时说明，
          而不是靠一条悄悄变短的列表。 */}
      <QuickActionsSection issueId={issue.id} />
      <PluginPanelSection issueId={issue.id} />

      {/* 父任务——独立区块，仅当 issue 确有父任务时渲染。设置父任务可从 issue 操作
          菜单进入；这张卡片只用于展示已有的父任务，不为没有父任务的 issue 白白占用
          边栏空间。 */}
      {parentIssue && (
        <div>
          <button
            type="button"
            className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${parentIssueOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
            onClick={() => setParentIssueOpen(!parentIssueOpen)}
          >
            {t(($) => $.detail.section_parent_issue)}
            <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${parentIssueOpen ? "rotate-90" : ""}`} />
          </button>
          {parentIssueOpen && <div className="pl-2">
            <div className="flex items-center gap-0.5 rounded-md px-2 -mx-2 hover:bg-accent/50 transition-colors group">
              <AppLink
                href={paths.issueDetail(parentIssue.id)}
                className="flex flex-1 min-w-0 items-center gap-1.5 py-1.5 text-caption"
              >
                <StatusIcon
                  status={parentIssue.status}
                  category={issueStatusCategory(parentIssue) ?? undefined}
                  className="h-3.5 w-3.5 shrink-0"
                />
                <span className="text-muted-foreground shrink-0">{parentIssue.identifier}</span>
                <span className="truncate group-hover:text-foreground">{parentIssue.title}</span>
              </AppLink>
              <button
                type="button"
                title={t(($) => $.actions.remove_parent_issue)}
                aria-label={t(($) => $.actions.remove_parent_issue)}
                onClick={() => actions.removeParent()}
                className="shrink-0 rounded p-1 text-muted-foreground opacity-0 transition-opacity hover:bg-accent hover:text-foreground focus-visible:opacity-100 group-hover:opacity-100"
              >
                <Unlink className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>}
        </div>
      )}

      {/* 拉取请求——工作区关闭 PR 边栏（或 GitHub 总开关关闭）时隐藏。两种情况下
          后端数据都保留，因此重新开启时区块能立刻恢复。 */}
      {githubSettings.prSidebar && (
        <div>
          <button
            type="button"
            className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${pullRequestsOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
            onClick={() => setPullRequestsOpen(!pullRequestsOpen)}
          >
            {t(($) => $.detail.section_pull_requests)}
            <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${pullRequestsOpen ? "rotate-90" : ""}`} />
          </button>
          {pullRequestsOpen && <div className="pl-2"><PullRequestList issueId={id} /></div>}
        </div>
      )}

      {/* 执行日志——进行中的运行 + 折叠的过往运行，每段都带自己的 token 消耗，
          区块标题上汇总 issue 总计。自包含：自己管理折叠状态与 WS 订阅。
          没有可展示的运行时会自行隐藏。 */}
      <ExecutionLogSection issueId={id} identifier={issue.identifier} />

      {/* 详情——创建人与时间戳。放在执行日志下方，因为它是边栏里最没人看的区块：
          issue 一经创建这些值就不再变化，而上方的日志才是人们真正过来查看的。 */}
      <div>
        <button
          type="button"
          className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${detailsOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
          onClick={() => setDetailsOpen(!detailsOpen)}
        >
          {t(($) => $.detail.section_details)}
          <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${detailsOpen ? "rotate-90" : ""}`} />
        </button>
        {detailsOpen && <div className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 pl-2">
          <PropRow label={t(($) => $.detail.prop_created_by)}>
            <ActorAvatar actorType={issue.creator_type} actorId={issue.creator_id} size="sm" enableHoverCard />
            <span className="cursor-pointer truncate">{getActorName(issue.creator_type, issue.creator_id)}</span>
          </PropRow>
          <PropRow label={t(($) => $.detail.prop_created)}>
            <span className="text-muted-foreground">{shortDate(issue.created_at, locale)}</span>
          </PropRow>
          <PropRow label={t(($) => $.detail.prop_updated)}>
            <span className="text-muted-foreground">{shortDate(issue.updated_at, locale)}</span>
          </PropRow>
        </div>}
      </div>

      {/* 曾经放在这里的独立"Token 用量"区块已被移除：它展示的 issue 总计与现在
          执行日志标题上携带的相同，却少了成本、也说不清是哪次运行花掉的。它的每个
          字段（输入 / 输出 / 缓存 / 运行次数）现在都收进了日志标题打开的用量对话框。
          它读取的 `/api/issues/:id/usage` 接口仍保留——CLI 的 `issue usage` 命令还在用。 */}

      {/* 元数据——面向 agent 的自由 KV 袋。这些值对人类几乎毫无意义，所以触发器
          这一行沿用同级区块标题（拉取请求 / 详情 / 父任务）的样式，但点击后打开的是
          展示原始 JSON 的对话框而非就地展开——负载可能很大，就地展开会把边栏其余
          内容往下推，很吵。 */}
      {Object.keys(issue.metadata ?? {}).length > 0 && (
        <>
          <button
            type="button"
            className="flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium text-muted-foreground transition-colors hover:bg-accent/70 hover:text-foreground"
            onClick={() => setMetadataOpen(true)}
          >
            {t(($) => $.detail.section_metadata)}
            <span className="tabular-nums">
              · {Object.keys(issue.metadata ?? {}).length}
            </span>
          </button>
          <Dialog open={metadataOpen} onOpenChange={setMetadataOpen}>
            <DialogContent className="max-w-2xl">
              <DialogHeader>
                <DialogTitle>{t(($) => $.detail.section_metadata)}</DialogTitle>
              </DialogHeader>
              <pre className="max-h-[60vh] overflow-auto rounded-md bg-muted p-3 font-mono text-caption">
                {JSON.stringify(issue.metadata ?? {}, null, 2)}
              </pre>
            </DialogContent>
          </Dialog>
        </>
      )}
    </div>
  );

  // 时间线两种渲染模式（扁平 / 虚拟化）共用的行渲染器。
  // 外层 `id="comment-..."` 是深链目标——等价于原生 `<a href="#comment-...">` 锚点。
  const renderItem = (_i: number, item: TimelineItem): React.ReactElement => {
    if (item.kind === "resolved-bar") {
      return (
        <div className="pb-3" id={`comment-${item.id}`}>
          <ResolvedThreadBar
            entry={item.entry}
            replies={timelineView.threadReplies.get(item.id) ?? EMPTY_REPLIES}
            onExpand={() => toggleResolvedExpand(item.id, true)}
          />
        </div>
      );
    }
    if (item.kind === "comment") {
      const isResolved = !!item.entry.resolved_at;
      return (
        <div className="pb-3" id={`comment-${item.id}`}>
          <CommentCard
            issueId={id}
            entry={item.entry}
            replies={timelineView.threadReplies.get(item.id) ?? EMPTY_REPLIES}
            currentUserId={user?.id}
            canModerate={canModerateComments}
            onReply={submitReply}
            onReplyAccepted={scrollToTimelineBottom}
            onEdit={editComment}
            onDelete={deleteComment}
            onToggleReaction={handleToggleReaction}
            onCreateSubIssue={openCommentSubIssue}
            onResolveToggle={handleResolveToggle}
            onCollapseResolved={isResolved ? () => toggleResolvedExpand(item.id, false) : undefined}
            expandedResolvedIds={expandedResolved}
            onResolvedExpandChange={toggleResolvedExpand}
            highlightedCommentId={highlightedId}
            runDiffCommentIds={runDiffCommentIds}
          />
        </div>
      );
    }
    // 活动组（activity-group）
    const expanded = expandedActivityIds.has(item.id)
      ? true
      : collapsedActivityIds.has(item.id)
        ? false
        : item.id === lastActivityGroupId;
    const truncateOlder = item.id === lastActivityGroupId;
    const showOlder = showOlderActivityIds.has(item.id);
    return (
      <ActivityBlock
        entries={item.entries}
        expanded={expanded}
        onToggle={() => toggleActivityBlock(item.id, expanded)}
        truncateOlder={truncateOlder}
        showOlder={showOlder}
        onToggleShowOlder={() => showOlderActivities(item.id)}
        getActorName={getActorName}
        resolveStatusLabel={resolveStatusLabel}
        resolveStatusCategory={resolveStatusCategory}
        resolveStatusColor={resolveStatusColor}
        t={t}
        timeAgo={timeAgo}
        locale={locale}
      />
    );
  };

  // 面包屑只展示最直接的那个容器，绝不虚构链条。project_id 与 parent_issue_id 相互
  // 正交（子任务可以住在与父任务不同的项目里），所以两者从不并排渲染：有父任务用
  // 父任务，否则用项目，再否则为空。项目仍会出现在右侧属性面板中。工作区名被刻意
  // 排除——"所有问题"只是一个视图，不是容器。
  const breadcrumbSegments: BreadcrumbSegment[] = parentIssue
    ? [{ href: paths.issueDetail(parentIssue.id), label: parentIssue.identifier }]
    : breadcrumbProject
      ? [
          {
            href: paths.projectDetail(breadcrumbProject.id),
            className: "flex items-center gap-1 min-w-0 max-w-72",
            label: (
              <>
                <ProjectIcon project={breadcrumbProject} size="sm" />
                <span className="min-w-0 truncate">{breadcrumbProject.title}</span>
              </>
            ),
          },
        ]
      : [];

  const conversationContent = (
    // 承载本 issue 图片共用的唯一查看器——见 ImageSequenceProvider。包裹整根内容列，
    // 让描述编辑器的图片与时间线的图片共享同一条序列。
    <CurrentIssueRenderContextProvider value={currentIssueRenderContext}>
    <ImageSequenceProvider items={imageSequence}>
    <div className="relative flex h-full min-w-0 flex-1 flex-col">
        {/* 页内查找栏——悬浮在内容列右上角（面包屑头下方），位于滚动容器之外，
            因此时间线滚动时它纹丝不动，自身文本也不会被查找遍历。z-30：必须压过
            钉在时间线 top-0 的一切吸顶控件（评论头 z-10、解决折叠条 z-20）——
            z 相等时，DOM 靠后的吸顶条会盖在查找栏上，让它的关闭按钮变成孤儿
            (MUL-4414)。在桌面端它还会往内让开线程轨道的右缘竖条，避免打开的
            查找栏盖住最顶部的刻度。 */}
        {find.open && (
          <FindBar
            find={find}
            className={cn("absolute top-14 z-30", isMobile ? "right-4" : "right-10")}
          />
        )}
        <BreadcrumbHeader
          leading={leadingAction}
          segments={breadcrumbSegments}
          leaf={
            <AppLink
              href={paths.issueDetail(issue.id)}
              className="flex min-w-0 transition-opacity hover:opacity-80"
            >
              <span className="truncate font-medium text-foreground">
                {issue.identifier} {issue.title}
              </span>
            </AppLink>
          }
          actions={
            <>
            {/* "agent 工作中"实时 chip，位于右侧按钮组最左，避免与标题重叠
                （标题会截断让位）。没有 agent 在跑时它自行隐藏。 */}
            <IssueAgentHeaderChip issueId={id} />
            {/* 线程导航器。排在操作按钮最左，因为它导航的是文档内容，而它右侧
                的一切都作用于 issue 本身。移动端与右缘轨道一起隐藏：面板在移动端
                其实可用，但要单手操作得用 sheet 而非 popover——那是另一项改动。 */}
            {!isMobile && (
              <ThreadNavPanel
                threads={minimapThreads}
                onJump={jumpToThread}
                onHoverThread={setThreadNavHoverId}
                open={threadNavOpen}
                pinned={threadNavPinned}
                onOpenChange={handleThreadNavOpenChange}
              />
            )}
            {onDone && !issueBehavesAsAny(issue, ["done", "cancelled"]) && (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      className="text-muted-foreground"
                      onClick={() => { handleUpdateField({ status: "done" }); onDone?.(); }}
                    >
                      <CircleCheck />
                    </Button>
                  }
                />
                <TooltipContent side="bottom">{t(($) => $.detail.mark_done_tooltip)}</TooltipContent>
              </Tooltip>
            )}
            {onDone && issueBehavesAs(issue, "done") && (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      className="text-muted-foreground"
                      onClick={() => { onDone(); }}
                    >
                      <Archive />
                    </Button>
                  }
                />
                <TooltipContent side="bottom">{t(($) => $.detail.archive_tooltip)}</TooltipContent>
              </Tooltip>
            )}
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className={cn("text-muted-foreground", actions.isPinned && "text-foreground")}
                    onClick={actions.togglePin}
                  >
                    {actions.isPinned ? <PinOff /> : <Pin />}
                  </Button>
                }
              />
              <TooltipContent side="bottom">{actions.isPinned ? t(($) => $.detail.unpin_tooltip) : t(($) => $.detail.pin_tooltip)}</TooltipContent>
            </Tooltip>
            <IssueActionsDropdown
              issue={issue}
              align="end"
              // 当父组件传了 `onDelete` 时，删除通过上面的 effect 检测并跳过导航；
              // 否则删除模态框把我们带回来源列表，兜底是"所有问题"。
              onDeletedFallbackPath={onDelete ? undefined : paths.issues()}
              trigger={
                <Button variant="ghost" size="icon-sm" className="text-muted-foreground">
                  <MoreHorizontal />
                </Button>
              }
            />
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant={sidebarOpen ? "secondary" : "ghost"}
                    size="icon-sm"
                    className={sidebarOpen ? "" : "text-muted-foreground"}
                    aria-label={t(($) => $.detail.sidebar_tooltip)}
                    onClick={handleToggleSidebar}
                  >
                    <PanelRight />
                  </Button>
                }
              />
              <TooltipContent side="bottom">{t(($) => $.detail.sidebar_tooltip)}</TooltipContent>
            </Tooltip>
            </>
          }
        />

        {/* scrollbar-gutter both-edges：在使用经典（占空间）滚动条时——Mac 接鼠标或
            "始终显示"、Windows、Linux——全局 `scrollbar-width: thin` 只从右侧挖走约
            11px，让居中列读起来左边 32px、右边 43px 不对称 (MUL-4404)。左右镜像
            沟槽恢复对称；使用悬浮式滚动条的平台不预留空间，渲染不受影响。 */}
        <div
          ref={attachScrollContainer}
          data-tab-scroll-root={scrollContainerKey}
          className="relative flex-1 overflow-y-auto [scrollbar-gutter:stable_both-edges]"
        >
        {/* 左右留白：32px 在桌面列上是舒适的阅读边距，却占掉 393px 手机屏的 16%，
            所以 `md` 以下收窄到 12px。`max-md:pb-chat-launcher` 在滚动末端给
            launcher 的角落预留位置：`md` 以下评论区不再吸底（见 `useStickyComposer`），
            所以当读者滚到底部时它恰好落在这里——正是 launcher 悬浮的那个角落。 */}
        <div className="mx-auto w-full max-w-4xl px-3 py-6 max-md:pb-chat-launcher md:px-8 md:py-8">
          {titleLazy.active && (
            <div className={titleLazy.ready ? undefined : "hidden"}>
              <TitleEditor
                key={`title-${id}-${titleResetToken}`}
                ref={titleEditorRef}
                defaultValue={titleConflictDraft ?? issue.title}
                placeholder={t(($) => $.detail.title_placeholder)}
                className="w-full text-display-sm font-bold leading-snug tracking-tight"
                onReady={titleLazy.onReady}
                onBlur={(value) => {
                  const trimmed = value.trim();
                  if (trimmed && trimmed !== issue.title) {
                    handleUpdateField({
                      title: trimmed,
                      title_base: titleBaseRef.current,
                    }, {
                      onSuccess: (serverIssue) => {
                        setTitleConflictDraft(null);
                        titleBaseRef.current = serverIssue.title;
                      },
                      onError: (error) => {
                        if (errorCode(error) === "revision_conflict") {
                          setTitleConflictDraft(trimmed);
                        }
                      },
                    });
                  }
                }}
              />
            </div>
          )}
          {!titleLazy.ready && (
            <div
              role="button"
              tabIndex={0}
              className="w-full cursor-text text-display-sm font-bold leading-snug tracking-tight"
              onClick={(e) => {
                // 拖选文字（比如复制标题）不应唤起编辑器。
                const sel = window.getSelection();
                if (sel && !sel.isCollapsed) return;
                titleBaseRef.current = issue.title;
                titleLazy.activate({ x: e.clientX, y: e.clientY });
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  titleBaseRef.current = issue.title;
                  titleLazy.activate();
                }
              }}
            >
              <span className="inline-flex items-center gap-2">
                {issue.title}
                <TAPDSourceBadge issue={issue} variant="inline" />
              </span>
            </div>
          )}
          {titleConflictDraft !== null ? (
            <RevisionConflictCompare
              className="mt-2"
              title={t(($) => $.revision.compare_title)}
              serverLabel={t(($) => $.revision.server_version)}
              localLabel={t(($) => $.revision.local_version)}
              serverValue={issue.title}
              localValue={titleConflictDraft}
              serverAction={(
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    // 纯本地操作：服务端本来就保存着这个标题，所以丢弃不会写入任何
                    // 内容。重新挂载才是把它放回编辑器的动作（见 titleResetToken）。
                    setTitleConflictDraft(null);
                    titleBaseRef.current = issue.title;
                    setTitleResetToken((token) => token + 1);
                  }}
                >
                  {t(($) => $.revision.use_server)}
                </Button>
              )}
              localAction={(
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    const draft = titleConflictDraft.trim();
                    if (!draft) return;
                    handleUpdateField(
                      { title: draft, title_base: issue.title },
                      {
                        onSuccess: (serverIssue) => {
                          setTitleConflictDraft(null);
                          titleBaseRef.current = serverIssue.title;
                        },
                      },
                    );
                  }}
                >
                  {t(($) => $.revision.keep_local)}
                </Button>
              )}
            />
          ) : null}

          {parentIssue && !issue.source_context && (
            <AppLink
              href={paths.issueDetail(parentIssue.id)}
              className="mt-2 inline-flex max-w-full items-center gap-1.5 text-caption text-muted-foreground hover:text-foreground transition-colors group/parent"
            >
              <span className="font-medium shrink-0">{t(($) => $.detail.sub_issue_of)}</span>
              <StatusIcon
                  status={parentIssue.status}
                  category={issueStatusCategory(parentIssue) ?? undefined}
                  className="h-3.5 w-3.5 shrink-0"
                />
              <span className="tabular-nums shrink-0">{parentIssue.identifier}</span>
              <span className="truncate group-hover/parent:text-foreground">
                {parentIssue.title}
              </span>
              {parentChildIssues.length > 0 && (() => {
                const done = parentChildIssues.filter((c) => issueBehavesAs(c, "done")).length;
                return (
                  <span className="ml-1 inline-flex items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 shrink-0">
                    <ProgressRing done={done} total={parentChildIssues.length} size={11} />
                    <span className="tabular-nums text-micro font-medium">
                      {done}/{parentChildIssues.length}
                    </span>
                  </span>
                );
              })()}
            </AppLink>
          )}

          {issue.source_context && (
            <SourceContextBadge
              context={issue.source_context}
              parentIssue={parentIssue}
              parentProgress={parentChildIssues.length > 0
                ? {
                    done: parentChildIssues.filter((child) => issueBehavesAs(child, "done")).length,
                    total: parentChildIssues.length,
                  }
                : undefined}
              refetchIssue={async () => {
                const result = await refetchIssue();
                return result.isError ? null : result.data;
              }}
            />
          )}

          <ExternalSourceDetail issue={issue} />

          <div
            {...descDropZoneProps}
            className="relative mt-5 rounded-lg"
            onFocusCapture={() => {
              if (!descriptionEditingRef.current) {
                descriptionEditingRef.current = true;
              }
            }}
            onBlurCapture={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget)) {
                descriptionEditingRef.current = false;
              }
            }}
          >
            <ContentEditor
              ref={descEditorRef}
              key={id}
              value={descriptionConflictDraft ?? issue.description ?? ""}
              placeholder={t(($) => $.detail.desc_placeholder)}
              onUpdate={(md, baseMarkdown) => {
                // 把 markdown 里仍在引用的"待绑定上传"绑起来，让刷新后它们出现在
                // `issueAttachments` 里，编辑器对文本/代码的预览在重载后也依然有效。
                //
                // 用 `contentReferencesAttachment` 匹配，而不是 `md.includes(a.url)`：
                // 编辑器把持久的 `markdownLink`（`/api/attachments/<id>/download` /
                // `markdown_url`）写进正文，从不写原始存储地址 `a.url`。裸的
                // `md.includes(a.url)` 因此永远匹配不上，上传也就永远不会通过
                // `attachment_ids` 关联。重载后它从 `issueAttachments` 里消失，
                // 渲染器无法把它解析成新签名的 `download_url`；持久化的、带鉴权的
                // 下载接口在"origin 不是 API 主机"的客户端（例如移动端 webview）上
                // 无法以原生 <img> 加载——网页端还能靠 cookie/代理正常访问。
                // 评论/回复/聊天输入框都已是这样用 `contentReferencesAttachment`
                // 绑定（MUL-3130 / MUL-3192），此处与它们保持一致。
                const ids = descPendingAttachmentsRef.current
                  .filter((a) => contentReferencesAttachment(md, a))
                  .map((a) => a.id);
                queueDescriptionSave({
                  markdown: md,
                  baseMarkdown,
                  attachmentIds: ids,
                });
              }}
              onUploadFile={handleDescriptionUpload}
              debounceMs={1500}
              // 关闭 issue 弹层前必须保存用户最后看到的内容——没有这次 flush，
              // 一次粘贴后紧接着快速关闭会丢失图片 markdown 及其 attachment_ids
              // 绑定 (MUL-3254)。
              flushPendingOnUnmount
              currentIssueId={id}
              attachments={descEditorAttachments}
            />

            {descriptionConflictDraft !== null ? (
              <RevisionConflictCompare
                className="mt-3"
                title={t(($) => $.revision.compare_description)}
                serverLabel={t(($) => $.revision.server_version)}
                localLabel={t(($) => $.revision.local_version)}
                serverValue={issue.description || ""}
                localValue={descriptionConflictDraft}
                serverAction={(
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      // 编辑器是脏的——这正是冲突存在的原因——所以 `value` prop 无法
                      // 直接生效：ContentEditor 刻意拒绝覆盖未保存的内容。
                      // adoptContent 是显式的"采纳这份内容"通道，应用时不会触发更新，
                      // 因此丢弃永不产生写入。
                      descEditorRef.current?.adoptContent(issue.description || "");
                      descriptionAttachmentIdsRef.current = [];
                      pendingDescriptionSaveRef.current = null;
                      setDescriptionConflictDraft(null);
                    }}
                  >
                    {t(($) => $.revision.use_server)}
                  </Button>
                )}
                localAction={(
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      handleUpdateField(
                        {
                          description: descriptionConflictDraft,
                          description_base: issue.description || "",
                          attachment_ids:
                            descriptionAttachmentIdsRef.current.length > 0
                              ? descriptionAttachmentIdsRef.current
                              : undefined,
                        },
                        {
                          onSuccess: () => {
                            setDescriptionConflictDraft(null);
                          },
                        },
                      );
                    }}
                  >
                    {t(($) => $.revision.keep_local)}
                  </Button>
                )}
              />
            ) : null}

            <div className="flex items-center gap-1 mt-3">
              <ReactionBar
                reactions={issueReactions}
                currentUserId={user?.id}
                onToggle={handleToggleIssueReaction}
                getActorName={getActorName}
              />
              <FileUploadButton
                size="sm"
                multiple
                onSelect={(file) => descEditorRef.current?.uploadFile(file)}
              />
            </div>
            {descDragOver && <FileDropOverlay />}
          </div>

          {/* 子任务——Linear 风格 */}
          {childIssues.length === 0 && (
            <div className="mt-6">
              <button
                type="button"
                className="inline-flex items-center gap-1.5 text-caption text-muted-foreground hover:text-foreground transition-colors"
                onClick={() => actions.openCreateSubIssue()}
              >
                <Plus className="h-3.5 w-3.5" />
                <span>{t(($) => $.detail.add_sub_issues)}</span>
              </button>
            </div>
          )}
          {childIssues.length > 0 && (() => {
            const doneCount = childIssues.filter((c) => issueBehavesAs(c, "done")).length;
            return (
              // Provider 承载各子任务行共同委托的右键操作菜单（全局单例菜单，
              // 而非每行一个）。
              <IssueContextMenuProvider>
              <IssueSurfaceSelectionProvider selection={subIssueSelection}>
              <div className="mt-10 group/sub-issues">
                {/* 区块标题栏 */}
                <div className="flex items-center gap-2 mb-2">
                  <button
                    type="button"
                    onClick={() => setSubIssuesCollapsed((v) => !v)}
                    className="flex items-center gap-1.5 text-body font-medium text-foreground hover:text-foreground/80 transition-colors"
                  >
                    <ChevronDown
                      className={cn(
                        "h-3.5 w-3.5 text-muted-foreground transition-transform",
                        subIssuesCollapsed && "-rotate-90",
                      )}
                    />
                    <span>{t(($) => $.detail.sub_issues_label)}</span>
                  </button>
                  <div className="inline-flex items-center gap-1.5 rounded-full bg-muted/60 px-2 py-0.5">
                    <ProgressRing done={doneCount} total={childIssues.length} size={11} />
                    <span className="text-micro text-muted-foreground tabular-nums font-medium">
                      {doneCount}/{childIssues.length}
                    </span>
                  </div>
                  {/* 传 issue.id 而不是路由参数——接口要的是 UUID，而路由里可能是
                      人类可读的标识。 */}
                  <SubIssuesAgentWorkingChip parentIssueId={issue.id} />
                  <input
                    type="checkbox"
                    checked={allChildrenSelected}
                    ref={(el) => {
                      if (el) el.indeterminate = someChildrenSelected && !allChildrenSelected;
                    }}
                    onChange={handleToggleSelectAllChildren}
                    aria-label={t(($) => $.detail.select_all_sub_issues_aria)}
                    className={cn(
                      "ml-1 cursor-pointer accent-primary transition-opacity",
                      someChildrenSelected
                        ? "opacity-100"
                        : "opacity-0 group-hover/sub-issues:opacity-100 focus-visible:opacity-100",
                    )}
                  />
                  <div className="ml-auto flex items-center gap-0.5">
                    <SubIssueDisplayPopover workspaceProperties={activeWorkspaceProperties} />
                    <Tooltip>
                      <TooltipTrigger
                        render={
                          <button
                            type="button"
                            className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground transition-colors"
                            onClick={() => actions.openCreateSubIssue()}
                            aria-label={t(($) => $.detail.add_sub_issue_aria)}
                          >
                            <Plus className="h-4 w-4" />
                          </button>
                        }
                      />
                      <TooltipContent side="bottom">{t(($) => $.detail.add_sub_issue_tooltip)}</TooltipContent>
                    </Tooltip>
                  </div>
                </div>

                {/* 内联批量工具栏——有选中项时出现在行旁，而不是一条离得很远的
                    固定长条。 */}
                <BatchActionToolbar issues={childIssues} placement="inline" clearSelectionOnApply />

                {/* 子任务列表 */}
                {!subIssuesCollapsed && (() => {
                  const groups = groupSubIssuesByStage(childIssues);
                  const staged = childIssues.some((c) => c.stage != null);
                  return (
                    <div className="overflow-hidden rounded-lg border bg-card/30 divide-y divide-border/60">
                      {groups.map(({ stage: groupStage, items }) => (
                        <Fragment key={groupStage ?? "unstaged"}>
                          {staged && (
                            <div className="bg-muted/40 px-3 py-1 text-micro font-medium uppercase tracking-wider text-muted-foreground">
                              {groupStage == null
                                ? t(($) => $.stage.none)
                                : t(($) => $.stage.value, { n: groupStage })}
                            </div>
                          )}
                          {items.map((child) => (
                            <SubIssueRow
                              key={child.id}
                              child={child}
                              childProgress={subIssueProgress?.get(child.id)}
                              rowProps={subIssueRowProps}
                              customProperties={subIssueCustomProps}
                            />
                          ))}
                        </Fragment>
                      ))}
                    </div>
                  );
                })()}
              </div>
              </IssueSurfaceSelectionProvider>
              </IssueContextMenuProvider>
            );
          })()}

          <div className="my-8 border-t" />

          {/* 活动 / 评论 */}
          <div>
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-3">
                <h2 className="text-title-sm font-semibold">{t(($) => $.detail.activity_section)}</h2>
              </div>
              <div className="flex items-center gap-2">
                {/* "委托订阅"是指用户并未手动订阅——是他们的 agent 替他们创建了
                    这个 issue。把这一点讲明，才不会让人误以为产品在偷偷把用户
                    加到什么东西里 (MUL-5483)。 */}
                {isSubscribed && subscriptionReason === "delegated" && (
                  <Tooltip>
                    {/* 低调的表面，而非普通正文：这是解释状态的元信息，不能读起来
                        像紧挨"退订"的第二个操作。沿用共享的 caption 角色，而不是
                        临时凑的字号 (MUL-5451)。 */}
                    <TooltipTrigger className="cursor-default rounded-full bg-muted px-2 py-0.5 text-caption text-muted-foreground">
                      {t(($) => $.detail.delegated_subscription_badge)}
                    </TooltipTrigger>
                    <TooltipContent side="top" className="max-w-64">
                      {t(($) => $.detail.delegated_subscription_hint)}
                    </TooltipContent>
                  </Tooltip>
                )}
                {/* 订阅者查询未返回前什么都不渲染：默认的空列表会被解读成"谁都没
                    订阅"，渲染它就会向已订阅的人闪一下"订阅"，且落在那个时间窗内的
                    点击发出去的是订阅、而不是他想要的退订 (MUL-5714)。查询未决时，
                    不显示任何控件好过显示错误的控件。 */}
                {subscriptionKnown &&
                  (!isSubscribed || (childCountKnown && childIssues.length === 0) ? (
                    /* 当子树选项没有可覆盖的东西时，只有一个按钮、没有菜单。
                       这是纯根级路径（opt_out_scope='issue'），不是子树路径：
                       即便子任务数为零，两者也是不同的服务端写入，因为子树的
                       tombstone 还会让*未来的*子任务不再自动重订该用户
                       (server/pkg/db/queries/subscriber.sql)。拒绝一个 issue
                       绝不能悄悄把人从一个尚不存在的树里退订。当子任务数未知时
                       保留下方菜单——它永远不会替用户选择作用域。 */
                    <button
                      type="button"
                      onClick={handleToggleSubscribe}
                      disabled={togglePending || !user?.id}
                      className={SUBSCRIPTION_ACTION_CLASS}
                    >
                      {isSubscribed
                        ? t(($) => $.detail.unsubscribe)
                        : t(($) => $.detail.subscribe)}
                    </button>
                  ) : (
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        disabled={togglePending || subtreePending || !user?.id}
                        className={SUBSCRIPTION_ACTION_CLASS}
                      >
                        {t(($) => $.detail.unsubscribe)}
                      </DropdownMenuTrigger>
                      {/* 用 onClick 而非 onSelect：Base UI 的 Menu.Item 不暴露
                          onSelect（那是 Radix 的叫法）；因其 props 继承完整 div 属性集，
                          onSelect 能通过类型检查却悄悄落在 DOM 节点的原生"文本选中"
                          事件上——处理器永远不执行 (MUL-5710)。 */}
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem
                          onClick={handleToggleSubscribe}
                          disabled={togglePending}
                        >
                          {t(($) => $.detail.unsubscribe_this)}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          onClick={handleUnsubscribeSubtree}
                          disabled={subtreePending}
                        >
                          {t(($) => $.detail.unsubscribe_subtree)}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  ))}
                <Popover>
                  <PopoverTrigger className="cursor-pointer hover:opacity-80 transition-opacity">
                    {subscribers.length > 0 ? (
                      <AvatarGroup>
                        {subscribers.slice(0, 4).map((sub) => (
                          <ActorAvatar
                            key={`${sub.user_type}-${sub.user_id}`}
                            actorType={sub.user_type}
                            actorId={sub.user_id}
                            size="md"
                            enableHoverCard
                          />
                        ))}
                        {subscribers.length > 4 && (
                          <AvatarGroupCount>+{subscribers.length - 4}</AvatarGroupCount>
                        )}
                      </AvatarGroup>
                    ) : (
                      <span className="flex items-center justify-center h-6 w-6 rounded-full border border-dashed border-muted-foreground/30 text-muted-foreground">
                        <Users className="h-3 w-3" />
                      </span>
                    )}
                  </PopoverTrigger>
                  <SubscriberPopoverContent
                    members={members}
                    agents={agents}
                    subscribers={subscribers}
                    toggleSubscriber={toggleSubscriber}
                    togglesDisabled={
                      !subscriptionKnown || togglePending || !user?.id
                    }
                    t={t}
                  />
                </Popover>
              </div>
            </div>


            {/* "agent 工作中"的实时信号如今在头部（IssueAgentHeaderChip），这样它
                固定在同一个位置，不与内容列里的吸顶横幅抢空间。单任务的实时时间线与
                历史运行则在右面板的 ExecutionLogSection 展示。 */}

            {/* 时间线条目——通过 react-virtuoso 虚拟化，让首屏成本从 O(N) 降到
                O(视口)。在一条 500 评论的 issue 上，未虚拟化的 .map 会让页面冻结
                数秒（每张 CommentCard 挂载时都要跑 markdown 解析 + lowlight
                代码高亮）。

                customScrollParent 守卫：回调 ref 在首次 commit 后才填充。没有这个
                空值守卫，Virtuoso 会退回自己的滚动器，在 overflow-y-auto 里拿到
                0 高度，首屏就把总高度算错。 */}
            {timelineLoading && timelineView.groups.length === 0 ? (
              <TimelineSkeleton />
            ) : (
              // 两种渲染模式：
              //   - 设置了 `highlightCommentId`（来自收件箱深链）→ 扁平渲染。
              //     每条评论都挂载、每个高度都真实，上面的 useEffect 一执行
              //     `scrollIntoView`，目标 id 就在 DOM 里。没有虚拟化的估算误差、
              //     没有占位符回流漂移。代价是正比于 items.length 的冷挂载成本
              //     （每条评论做 markdown + lowlight），在深链场景下可接受——
              //     用户带着"就要落到某条"的明确意图而来。
              //   - `find.open`（页内 Cmd/Ctrl+F）→ 同样扁平渲染，让每条评论都进
              //     DOM，查找遍历才能匹配并高亮。同样的"明确意图换冷挂载"权衡。
              //   - 其余情况 → Virtuoso。浏览模式下，长时间线虚拟化在首屏性能上
              //     胜出。
              //
              // 这种拆分是刻意的：虚拟化与"精准落在目标上"有着根本对立的契约
              //（估算高度 vs 真实高度）。试图在同一条路径里同时满足两者，正是
              // 本 PR 要关闭的那些历史 bug 的来源。
              !highlightCommentId && !find.open ? (
                !scrollContainerEl ? (
                  // 回调 ref 填充期间先显示骨架屏，避免 IssueDetail 挂载与
                  // Virtuoso 挂载之间的空隙闪成一片空白。
                  <TimelineSkeleton />
                ) : (
                  <div className="mt-4">
                    <Virtuoso
                      key={`${wsId}:${id}`}
                      ref={virtuosoRef}
                      customScrollParent={scrollContainerEl}
                      data={items}
                      initialTopMostItemIndex={{ index: Math.max(0, items.length - 1), align: "end" }}
                      increaseViewportBy={{ top: 800, bottom: 800 }}
                      computeItemKey={(_i, item) => `${item.kind}:${item.id}`}
                      skipAnimationFrameInResizeObserver
                      // 刻意不设 followOutput。Virtuoso 会把它当成粘性的"是否在
                      // 底部"标志，在每次高度变化时把 scrollTop 重置为最大——
                      // issue 详情是文档形态，不是聊天窗口。
                      itemContent={renderItem}
                    />
                  </div>
                )
              ) : (
                <div className="mt-4">
                  {items.map((item, i) => (
                    <Fragment key={`${item.kind}:${item.id}`}>
                      {renderItem(i, item)}
                    </Fragment>
                  ))}
                </div>
              )
            )}

          </div>

          {/* 底部评论输入框——无头像、通栏宽。它是内容列的直接子元素（而非活动区块的）：
              吸底盒不能脱离自己的包含块，而 Activity 这个 div 只包住时间线——放在列
              这一层，`sticky bottom-0` 才能让输入框在整个滚动范围内钉住。

              吸底处理镜像 list-grid 的吸顶表头、底部边缘：卡片下方/周围是不透明的
              bg-background（否则它的圆角会漏出滚过的文字），上方一道 16px 渐变淡出
              （正好覆盖 mt-4 的间距，静止时压在页面背景上、不可见），pb-4 让卡片
              浮离视口边缘——-mb-4 把这内边距还给列的 py-8，使静止时的布局不位移。 */}
          <div
            ref={composerRef}
            className={cn(
              "mt-4",
              stickyComposer &&
                "sticky bottom-0 z-10 -mb-4 bg-background pb-4 before:pointer-events-none before:absolute before:inset-x-0 before:bottom-full before:h-4 before:bg-gradient-to-t before:from-background before:to-transparent",
            )}
          >
            {/* key={id}：web 的 /issues/[id] 路由在 issueId 变化时不会重新挂载，
                所以没有显式 key 的话，编辑器会保留上一个 issue 在内存里的内容，
                下一次按键就会把它冲进新 issue 的草稿键里。 */}
            <CommentInput
              key={id}
              issueId={id}
              onSubmit={submitComment}
              onAccepted={scrollToTimelineBottom}
            />
          </div>
        </div>
        </div>

        {/* 线程快速跳转轨道——贴着滚动容器的右缘、紧挨滚动条，正好在读者滚动时
            指针所在的位置 (MUL-4522)。right-3 是在两种滚动条模式下都成立的缩进：
            它避开经典滚动条约 11px 的沟槽；当沟槽为 0（悬浮式滚动条）时，轨道自身
            20px 的宽度刚好落在内容列 px-8 的留白上——既不盖滚动条也不盖正文。
            它还避开了面板边缘调整手柄 4px 的拖拽条。悬停预览线程，点击跳转。
            移动端隐藏：没有悬停，且沟槽太窄。 */}
        {!isMobile && (
          <ThreadMinimap
            threads={minimapThreads}
            scrollContainerEl={scrollContainerEl}
            onJump={jumpToThread}
            highlightedThreadId={threadNavHoverId}
            className="absolute bottom-0 right-3 top-12"
          />
        )}
      </div>
    </ImageSequenceProvider>
    </CurrentIssueRenderContextProvider>
  );
  const detailContent = <InsightsSurface issueId={id} title={`${issue.identifier} ${issue.title}`}>{conversationContent}</InsightsSurface>;


  if (isMobile) {
    return (
      <div className="flex flex-1 min-h-0">
        {detailContent}
        <Sheet open={mobileSidebarOpen} onOpenChange={setMobileSidebarOpen}>
          <SheetContent side="right" showCloseButton={false} className="w-[320px] overflow-y-auto p-4">
            {sidebarContent}
          </SheetContent>
        </Sheet>
      </div>
    );
  }

  return (
    <ResizablePanelGroup orientation="horizontal" className="flex-1 min-h-0" defaultLayout={defaultLayout} onLayoutChanged={onLayoutChanged}>
      <ResizablePanel id="content" minSize="50%">
        {detailContent}
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel
        id="sidebar"
        {...rightSidebarPanelMotionProps}
        data-right-sidebar-motion={desktopSidebarMotionEnabled ? "enabled" : undefined}
        defaultSize={desktopSidebarOpen ? 320 : 0}
        minSize={260}
        maxSize={420}
        collapsible
        groupResizeBehavior="preserve-pixel-size"
        panelRef={sidebarRef}
        onResize={handleDesktopSidebarResize}
      >
        <AnimatedRightSidebar open={desktopSidebarVisualOpen} motionEnabled={desktopSidebarMotionEnabled}>
          {sidebarContent}
        </AnimatedRightSidebar>
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}

function ExternalSourceDetail({ issue }: { issue: Issue }) {
  const { t } = useT("issues");
  const provider = String(issue.metadata.source_provider ?? "").toLowerCase();
  if (provider !== "tapd" && provider !== "gongfeng") return null;
  const url = String(issue.metadata.source_url ?? "");
  const status = String(issue.metadata.source_fetch_status ?? "");
  const title = String(issue.metadata.source_fetch_title ?? "");
  const summary = String(issue.metadata.source_fetch_summary ?? "");
  const error = String(issue.metadata.source_fetch_error ?? "");
  const statusText = (() => {
    switch (status) {
      case "fetched":
        return t(($) => $.external_source.statuses.fetched);
      case "fetch_failed":
        return t(($) => $.external_source.statuses.fetch_failed);
      case "blocked_missing_profile":
        return t(($) => $.external_source.statuses.blocked_missing_profile);
      case "pending_mcp_fetch":
        return t(($) => $.external_source.statuses.pending_mcp_fetch);
      default:
        return t(($) => $.external_source.statuses.unread);
    }
  })();
  return (
    <section className="mt-4 rounded-lg border border-surface-border bg-surface-subtle p-3">
      <div className="flex items-start gap-3">
        <TAPDSourceBadge issue={issue} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2 text-caption">
            <span className="font-semibold">
              {provider === "tapd"
                ? t(($) => $.external_source.tapd)
                : t(($) => $.external_source.gongfeng)}
            </span>
            <span className="text-muted-foreground">{statusText}</span>
          </div>
          {title && <p className="mt-1 text-body font-medium">{title}</p>}
          {summary && <p className="mt-1 whitespace-pre-wrap text-caption text-muted-foreground">{summary}</p>}
          {error && <p className="mt-1 text-caption text-destructive">{error}</p>}
          {url && (
            <a className="mt-2 inline-block max-w-full truncate text-caption text-primary hover:underline" href={url} target="_blank" rel="noreferrer">
              {t(($) => $.external_source.open)}
            </a>
          )}
        </div>
      </div>
    </section>
  );
}
