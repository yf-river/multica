"use client";

/**
 * mention 建议弹层：`@` 下拉列表的数据装配、分组、筛选与键盘交互。
 *
 * 职责：从 React Query 缓存读成员/agent/squad 与已缓存 issue，按最近提及
 * 排序合并；查询时再叠加服务端搜索结果；把「已取消（cancelled）」的条目
 * 降权、把直接命中的条目钉在前面；并把整份列表交给通用弹层渲染
 * （suggestion-popup.tsx）。选中（Enter / Tab / 点击）后会记录一次
 * mention 使用时间，供下次排序用。
 */

import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { QueryClient } from "@tanstack/react-query";
import { getCurrentWsId } from "@multica/core/platform";
import { flattenIssueBuckets, issueKeys } from "@multica/core/issues/queries";
import { issueStatusCategory } from "@multica/core/issues";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { useAuthStore } from "@multica/core/auth";
import { canAssignAgentToIssue } from "@multica/core/permissions";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { api } from "@multica/core/api";
import {
  isIssueDirectHit,
  isProjectDirectHit,
} from "@multica/core/search/cancelled-rank";
import { isImeComposing } from "@multica/core/utils";
import type {
  Issue,
  ListIssuesCache,
  MemberWithUser,
  Agent,
  Squad,
} from "@multica/core/types";
import { ListTodo } from "lucide-react";
import { ActorAvatar } from "../../common/actor-avatar";
import { StatusIcon } from "../../issues/components/status-icon";
import { ProjectIcon } from "../../projects/components/project-icon";
import { useT } from "../../i18n";
import { Badge } from "@multica/ui/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import type { IssueStatus, IssueStatusCategory, ProjectStatus } from "@multica/core/types";
import { PROJECT_STATUS_CONFIG } from "@multica/core/projects/config";
import type { SuggestionOptions } from "@tiptap/suggestion";
import { PluginKey } from "@tiptap/pm/state";
import {
  getRecencyMap,
  recordMentionUsage,
  sortUserItemsByRecency,
} from "./mention-recency";
import { matchesPinyin } from "./pinyin-match";
import {
  createSuggestionPopupRender,
} from "./suggestion-popup";
import {
  isPickerAcceptKey,
  pickerNavigationDirection,
} from "../../common/picker-keys";
import { isTriggerArmedAt } from "./suggestion-trigger-arming";
import { blockedReasonLabel } from "../../issues/blocked-trigger-copy";

// ---------------------------------------------------------------------------
// 类型定义
// ---------------------------------------------------------------------------

/** 一条可被 @ 选中的候选对象。 */
export interface MentionItem {
  id: string;
  label: string;
  type: "member" | "agent" | "squad" | "issue" | "project" | "all";
  /** 注入式上下文条目的可选分组提示。 */
  group?: "current" | "recent" | "search";
  /** 标签旁展示的次要文本（如 issue 标题）。 */
  description?: string;
  /** 供 StatusIcon 渲染的 issue 状态。 */
  status?: IssueStatus;
  /**
   * 该状态实际归属的类别，随 issue 载荷带下来，让列表自身不必去解析自定义
   * key。图标与「关闭/降权」规则都读它——直接比原始 key 会把自定义的
   * done 状态画得、排得像进行中的活一样。（MUL-6243）
   */
  statusCategory?: IssueStatusCategory;
  /** 供 ProjectIcon 渲染的项目 emoji/图标快照。 */
  icon?: string | null;
  /** 最近/当前项目渲染用的项目状态快照。 */
  projectStatus?: ProjectStatus;
  /** 出现时表示该目标保持可见但不可选中。 */
  disabledReason?: "agent_runtime_required";
}

interface MentionListProps {
  items: MentionItem[];
  query: string;
  command: (item: MentionItem) => void;
  includeProjectSearch?: boolean;
}

/** MentionList 通过 ref 暴露的键盘处理句柄。 */
export interface MentionListRef {
  onKeyDown: (props: { event: KeyboardEvent }) => boolean;
}

// ---------------------------------------------------------------------------
// 按分区给条目分组
// ---------------------------------------------------------------------------

/** 一个可视化分组：组名 + 条目列表。 */
interface MentionGroup {
  label: string;
  items: MentionItem[];
}

function groupItems(items: MentionItem[], query: string): MentionGroup[] {
  const current: MentionItem[] = [];
  const recent: MentionItem[] = [];
  const search: MentionItem[] = [];
  const users: MentionItem[] = [];
  const issues: MentionItem[] = [];
  const cancelled: MentionItem[] = [];

  for (const item of items) {
    if (isDemotedCancelled(item, query)) {
      cancelled.push(item);
    } else if (item.group === "current") {
      current.push(item);
    } else if (item.group === "recent") {
      recent.push(item);
    } else if (item.group === "search") {
      search.push(item);
    } else if (item.type === "issue" || item.type === "project") {
      issues.push(item);
    } else {
      users.push(item);
    }
  }

  const groups: MentionGroup[] = [];
  if (current.length > 0) groups.push({ label: "Current", items: current });
  if (recent.length > 0) groups.push({ label: "Recent", items: recent });
  if (search.length > 0) groups.push({ label: "Search", items: search });
  if (users.length > 0) groups.push({ label: "Users", items: users });
  if (issues.length > 0) groups.push({ label: "Issues", items: issues });
  // 取消组永远排最后：任何已取消的条目都不得排在活条目前面。
  if (cancelled.length > 0) groups.push({ label: "Cancelled", items: cancelled });
  return groups;
}

// ---------------------------------------------------------------------------
// MentionList——编辑器内渲染的弹层列表
// ---------------------------------------------------------------------------

const MAX_ITEMS = 20;
const SERVER_ISSUE_SEARCH_LIMIT = 20;
const SERVER_CONTEXT_SEARCH_LIMIT = 8;
const SERVER_SEARCH_DEBOUNCE_MS = 150;

/** 同一类型同一 id 的条目对应同一个 key。 */
function mentionItemKey(item: MentionItem): string {
  return `${item.type}:${item.id}`;
}

/** 合并多组条目并按键去重（先到先得）。 */
function mergeMentionItems(
  ...itemGroups: MentionItem[][]
): MentionItem[] {
  const seen = new Set<string>();
  const merged: MentionItem[] = [];

  for (const item of itemGroups.flat()) {
    const key = mentionItemKey(item);
    if (seen.has(key)) continue;
    seen.add(key);
    merged.push(item);
  }

  return merged;
}

/**
 * 已取消的工作是被放弃的工作：保持可触达，但绝不排在活匹配之前、也绝不
 * 占用活匹配的名额。
 *
 * 即使搜索 API 已把取消结果排最后，弹层仍需要这条规则的独立理由有两个：
 *
 * 1. 缓存行在服务端行**之前**合并，随后整份列表才截断到 MAX_ITEMS，所以
 *    一条本地缓存的已取消 issue 会越过所有后端排名的结果，把活跃工作挤出
 *    窗口。
 * 2. 上下文 mention 聚合了两个独立排序的响应——issue 然后是 project，都标
 *    `search`——所以已取消的 project 仍会排在活 issue 上方。按类型排序无法
 *    修复跨类型列表。
 *
 * 豁免项（与服务端及其它搜索界面一致）：
 *
 * - 直接命中。精确标识符、裸数字或完整标题意味着用户瞄准的就是那一条
 *   记录；降权它等于藏起用户要找的东西。通过 isIssueDirectHit /
 *   isProjectDirectHit 与后端规则共享，三个界面不会漂移。
 * - 精选的 `current` / `recent` 组。它们是有意的上下文而非相关度命中，
 *   "Current" 即使在看的 issue 本身已取消也必须扛过截断存活。
 */
function isDemotedCancelled(item: MentionItem, query: string): boolean {
  if (isPinnedAboveTruncation(item, query)) return false;
  if (item.type === "issue") return item.statusCategory === "cancelled";
  if (item.type === "project") return item.projectStatus === "cancelled";
  return false;
}

/**
 * 必须扛过 MAX_ITEMS 截断的行：精选上下文与直接命中。把直接命中从降权中
 * 豁免还不够——`slice(0, MAX_ITEMS)` 作用在合并后的列表上，一条落在 20 个
 * 缓存候选之后的直接命中仍会被切掉。把它钉到最前才是真正保证用户输入的
 * 记录完整可触达的方式，这也镜像了服务端——它本就把直接命中排第一。
 *
 * issue mention 行把标识符放在 `label`、标题放在 `description`；project 行
 * 把标题放在 `label`。
 */
function isPinnedAboveTruncation(item: MentionItem, query: string): boolean {
  if (item.group === "current" || item.group === "recent") return true;
  if (!query) return false;
  if (item.type === "issue") {
    return isIssueDirectHit(
      { identifier: item.label, title: item.description },
      query,
    );
  }
  if (item.type === "project") {
    return isProjectDirectHit({ title: item.label }, query);
  }
  return false;
}

/**
 * 在 MAX_ITEMS 截断**之前**应用的稳定三层分区，让已取消行让出的是名额
 * 而不只是位置：
 *
 *   钉置（精选上下文 + 直接命中）→ 活跃 → 已取消
 *
 * 每层内部相对顺序不动，其余排序交给后端排名负责。groupItems() 随后把
 * 已取消层渲染成收尾的小节。
 */
function demoteCancelledItems(items: MentionItem[], query: string): MentionItem[] {
  const pinned: MentionItem[] = [];
  const live: MentionItem[] = [];
  const cancelled: MentionItem[] = [];

  for (const item of items) {
    if (isPinnedAboveTruncation(item, query)) pinned.push(item);
    else if (isDemotedCancelled(item, query)) cancelled.push(item);
    else live.push(item);
  }

  return pinned.length > 0 || cancelled.length > 0
    ? [...pinned, ...live, ...cancelled]
    : items;
}

export const MentionList = forwardRef<MentionListRef, MentionListProps>(
  function MentionList({ items, query, command, includeProjectSearch = false }, ref) {
    const { t } = useT("editor");
    // 选中态按条目身份跟踪，而不是位置下标。列表会被 groupItems() 反复
    // 重分桶、还会异步增长（服务端搜索结果），所以下标不是稳定目标——
    // 下标 N 下的那一行会随列表重排而变化。selectedKey 把高亮钉在具体条目
    // 上；数值下标只是相对弹层实际渲染的同一份顺序（orderedItems）推导
    // 出来的。null 表示「还没显式选中」→ 默认高亮第一行。
    const [selectedKey, setSelectedKey] = useState<string | null>(null);
    const [serverItems, setServerItems] = useState<MentionItem[]>([]);
    const [isSearching, setIsSearching] = useState(false);
    const [searchedQuery, setSearchedQuery] = useState("");
    const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);
    const normalizedQuery = query.trim();

    useEffect(() => {
      const q = normalizedQuery;
      setServerItems([]);

      if (!q) {
        setIsSearching(false);
        setSearchedQuery("");
        return;
      }

      const wsId = getCurrentWsId();
      if (!wsId) {
        setIsSearching(false);
        setSearchedQuery(q);
        return;
      }

      let cancelled = false;
      const controller = new AbortController();
      setIsSearching(true);

      const timer = setTimeout(() => {
        void (async () => {
          try {
            if (includeProjectSearch) {
              const [issues, projects] = await Promise.all([
                api.searchIssues({
                  q,
                  limit: SERVER_CONTEXT_SEARCH_LIMIT,
                  include_closed: true,
                  signal: controller.signal,
                }),
                api.searchProjects({
                  q,
                  limit: SERVER_CONTEXT_SEARCH_LIMIT,
                  include_closed: true,
                  signal: controller.signal,
                }),
              ]);
              if (!cancelled && !controller.signal.aborted) {
                setServerItems([
                  ...issues.issues.map((issue) => ({ ...issueToMention(issue), group: "search" as const })),
                  ...projects.projects.map((project) => ({ ...projectToMention(project), group: "search" as const })),
                ]);
              }
            } else {
              const res = await api.searchIssues({
                q,
                limit: SERVER_ISSUE_SEARCH_LIMIT,
                include_closed: true,
                signal: controller.signal,
              });
              if (!cancelled && !controller.signal.aborted) {
                setServerItems(res.issues.map(issueToMention));
              }
            }
          } catch {
            // 请求被中断或网络出错：保留同步读到的缓存结果即可，不影响已有列表。
          } finally {
            if (!cancelled && !controller.signal.aborted) {
              setSearchedQuery(q);
              setIsSearching(false);
            }
          }
        })();
      }, SERVER_SEARCH_DEBOUNCE_MS);

      return () => {
        cancelled = true;
        clearTimeout(timer);
        controller.abort();
      };
    }, [includeProjectSearch, normalizedQuery]);

    const displayItems = useMemo(() => {
      const currentServerItems = searchedQuery === normalizedQuery ? serverItems : [];
      return demoteCancelledItems(
        mergeMentionItems(items, currentServerItems),
        normalizedQuery,
      ).slice(0, MAX_ITEMS);
    }, [items, normalizedQuery, searchedQuery, serverItems]);

    // 选中用的统一下标空间。groupItems() 会把 displayItems 重分桶
    // （current → recent → search → users → issues）；orderedItems 就是弹层
    // 自上而下渲染的原样。键盘导航、Enter、点击、高亮与滚动都索引它，
    // 所以高亮行永远等于确认项——不存在第二份「数据顺序」可以漂移。
    const groups = useMemo(
      () => groupItems(displayItems, normalizedQuery),
      [displayItems, normalizedQuery],
    );
    const orderedItems = useMemo(() => groups.flatMap((g) => g.items), [groups]);

    // 从钉住的身份推导数值下标。若选中的条目已不在列表里（查询把它滤掉
    // 了）或尚未选中任何项，就回退到第一行。这在重排与异步结果到达时能
    // 自愈，又从不强杀一个活跃中的选中。
    const selectedIndex = useMemo(() => {
      const firstSelectable = orderedItems.findIndex((item) => !item.disabledReason);
      if (selectedKey === null) return firstSelectable;
      const i = orderedItems.findIndex((it) => mentionItemKey(it) === selectedKey);
      return i === -1 || orderedItems[i]?.disabledReason
        ? firstSelectable
        : i;
    }, [orderedItems, selectedKey]);

    useEffect(() => {
      if (selectedIndex >= 0) {
        itemRefs.current[selectedIndex]?.scrollIntoView({ block: "nearest" });
      }
    }, [selectedIndex]);

    const selectItem = useCallback(
      (item: MentionItem | undefined) => {
        if (!item || item.disabledReason) return;
        const wsId = getCurrentWsId();
        if (wsId) recordMentionUsage(wsId, item);
        command(item);
      },
      [command],
    );

    useImperativeHandle(ref, () => ({
      onKeyDown: ({ event }) => {
        // 输入法正在组合——不要把 Enter/方向键当弹层动作拦截；
        // 那些键归输入法所有（Enter 提交组合等）。
        if (isImeComposing(event)) return false;
        // 方向键加 Ctrl+N/J/P/K 别名，即命令栏接受的组合——见
        // pickerNavigationDirection。
        const direction = pickerNavigationDirection(event);
        if (direction !== null) {
          const selectableIndexes = orderedItems.flatMap((item, index) =>
            item.disabledReason ? [] : [index],
          );
          if (selectableIndexes.length === 0) return true;
          const current = selectableIndexes.indexOf(selectedIndex);
          const delta =
            direction === "next" ? 1 : selectableIndexes.length - 1;
          const next =
            selectableIndexes[
              ((current === -1 ? 0 : current) + delta) %
                selectableIndexes.length
            ]!;
          setSelectedKey(mentionItemKey(orderedItems[next]!));
          return true;
        }
        // Enter 是规范确认键；纯 Tab 是附加别名（见 isPickerAcceptKey）。
        // Shift/修饰键 + Tab 落进焦点导航。
        if (isPickerAcceptKey(event)) {
          if (selectedIndex < 0) return true;
          selectItem(orderedItems[selectedIndex]);
          return true;
        }
        return false;
      },
    }));

    if (orderedItems.length === 0) {
      const isWaitingForServer =
        normalizedQuery !== "" &&
        (isSearching || searchedQuery !== normalizedQuery);

      return (
        <div className="rounded-md border bg-popover p-2 text-caption text-muted-foreground shadow-md">
          {isWaitingForServer
            ? t(($) => $.mention.searching)
            : t(($) => $.mention.no_results)}
        </div>
      );
    }

    const hasContextGroups = orderedItems.some((item) => item.group === "current" || item.group === "recent");
    const contextLayout = hasContextGroups;
    const groupLabel = (label: string): string => {
      if (label === "Current") return t(($) => $.mention.group_current);
      if (label === "Recent") return t(($) => $.mention.group_recent);
      if (label === "Search") return t(($) => $.mention.group_search);
      if (label === "Users") return t(($) => $.mention.group_users);
      if (label === "Issues") return t(($) => $.mention.group_issues);
      if (label === "Cancelled") return t(($) => $.mention.group_cancelled);
      return label;
    };

    // 建立扁平索引映射：globalIndex → item
    let globalIndex = 0;

    const renderRows = (group: MentionGroup): ReactNode =>
      group.items.map((item) => {
        const idx = globalIndex++;
        return (
          <MentionRow
            key={`${item.type}-${item.id}`}
            item={item}
            selected={idx === selectedIndex}
            onSelect={() => selectItem(item)}
            buttonRef={(el) => { itemRefs.current[idx] = el; }}
          />
        );
      });

    // 所有分组共用一个滚动容器。过去上下文布局只让 "Recent" 组可滚动，
    // 其余都是 `shrink-0`，于是混了上下文与搜索结果的查询会把 Recent 压向
    // 零高度，其未被裁剪的行就画到下面各组之上。改成单个 `overflow-y-auto`
    // 弹性列后各组只是堆叠、整份弹层滚动——没有组能塌到另一组上。上下文
    // 变体只在宽度 / max-height / 外框上有差异。
    return (
      <div
        className={cn(
          "flex flex-col overflow-y-auto overscroll-contain border bg-popover py-1",
          // 高度预算：钳到两者中较小的——设计最大值，或 floating-ui `size`
          // middleware 发布的视口感知 `--suggestion-available-height`
          // （见 suggestion-popup.tsx）。在弹层不受该控制器渲染时，变量回退
          // 到设计最大值。这是唯一的高度权威；不要在它之上再加第二个固定
          // max-height，否则列表可能溢出视口。
          contextLayout
            ? "max-h-[min(420px,var(--suggestion-available-height,420px))] w-96 rounded-lg shadow-xl"
            : "max-h-[min(300px,var(--suggestion-available-height,300px))] w-72 rounded-md shadow-md",
        )}
      >
        {groups.map((group) => (
          <div key={group.label}>
            <div className="px-3 py-2 text-micro font-semibold uppercase tracking-wide text-muted-foreground">
              {groupLabel(group.label)}
            </div>
            {renderRows(group)}
          </div>
        ))}
      </div>
    );
  },
);

// ---------------------------------------------------------------------------
// MentionRow——列表里的单个条目
// ---------------------------------------------------------------------------

function MentionRow({
  item,
  selected,
  onSelect,
  buttonRef,
}: {
  item: MentionItem;
  selected: boolean;
  onSelect: () => void;
  buttonRef: (el: HTMLButtonElement | null) => void;
}) {
  const { t } = useT("editor");
  const { t: issuesT } = useT("issues");
  if (item.type === "issue") {
    // 视觉上变暗已关闭（done/cancelled）的 issue，让它们和建议列表里的
    // 活跃项可区分——它们仍可选中。
    const isClosed =
      item.statusCategory === "done" || item.statusCategory === "cancelled";
    return (
      <button
        type="button"
        ref={buttonRef}
        className={`flex w-full items-center gap-2.5 px-3 py-2 text-left text-caption transition-colors ${
          selected ? "bg-accent" : "hover:bg-accent/50"
        } ${isClosed ? "opacity-60" : ""}`}
        onClick={onSelect}
      >
        <span className="flex h-7 w-7 shrink-0 items-center justify-center">
          {item.status ? (
            <StatusIcon
              status={item.status}
              category={item.statusCategory}
              className="h-3.5 w-3.5"
            />
          ) : (
            <ListTodo className="h-3.5 w-3.5 text-muted-foreground" />
          )}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-2">
            <span className="shrink-0 font-medium text-muted-foreground">{item.label}</span>
            {item.description && (
              <span
                className={`truncate text-foreground ${isClosed ? "line-through" : ""}`}
              >
                {item.description}
              </span>
            )}
          </span>
        </span>
      </button>
    );
  }

  if (item.type === "project") {
    const projectStatusCfg = item.projectStatus ? PROJECT_STATUS_CONFIG[item.projectStatus] : null;
    return (
      <button
        type="button"
        ref={buttonRef}
        className={`flex w-full items-center gap-2.5 px-3 py-2 text-left text-caption transition-colors ${
          selected ? "bg-accent" : "hover:bg-accent/50"
        }`}
        onClick={onSelect}
      >
        <span className="flex h-7 w-7 shrink-0 items-center justify-center">
          <ProjectIcon project={{ icon: item.icon ?? null }} size="sm" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate font-medium text-foreground">{item.label}</span>
          {item.description && (
            <span className="block truncate text-muted-foreground">
              {item.description}
            </span>
          )}
        </span>
        {projectStatusCfg && (
          <span className={`${projectStatusCfg.dotColor} ml-auto size-1.5 shrink-0 rounded-full`} />
        )}
      </button>
    );
  }

  const disabledMessage = item.disabledReason
    ? blockedReasonLabel(item.disabledReason, issuesT)
    : null;
  const button = (
    <button
      type="button"
      ref={buttonRef}
      aria-disabled={disabledMessage ? true : undefined}
      aria-label={
        disabledMessage ? `${item.label}: ${disabledMessage}` : undefined
      }
      className={`flex w-full items-center gap-2.5 px-3 py-1.5 text-left text-caption transition-colors ${
        selected ? "bg-accent" : disabledMessage ? "" : "hover:bg-accent/50"
      } ${disabledMessage ? "cursor-not-allowed opacity-50" : ""}`}
      onClick={onSelect}
    >
      <ActorAvatar
        actorType={item.type === "all" ? "member" : item.type}
        actorId={item.id}
        size="sm"
        showStatusDot
      />
      <span className="truncate font-medium">
        {item.type === "all" ? t(($) => $.mention.all_members) : item.label}
      </span>
      {item.type === "agent" && (
        <Badge variant="outline" className="ml-auto text-micro h-4 px-1.5">
          {t(($) => $.mention.agent_badge)}
        </Badge>
      )}
      {item.type === "squad" && (
        <Badge variant="outline" className="ml-auto text-micro h-4 px-1.5">
          {t(($) => $.mention.squad_badge)}
        </Badge>
      )}
    </button>
  );

  if (!disabledMessage) return button;

  return (
    <Tooltip>
      <TooltipTrigger render={button} />
      <TooltipContent side="top" className="max-w-72 text-caption">
        {disabledMessage}
      </TooltipContent>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// Suggestion 配置工厂
// ---------------------------------------------------------------------------

/** 把 issue 数据转成 mention 候选。 */
function issueToMention(
  i: Pick<Issue, "id" | "identifier" | "title" | "status"> & Partial<Pick<Issue, "status_category">>,
): MentionItem {
  return {
    id: i.id,
    label: i.identifier,
    type: "issue" as const,
    description: i.title,
    status: i.status as IssueStatus,
    statusCategory: issueStatusCategory(i) ?? undefined,
  };
}

/** 把 project 数据转成 mention 候选。 */
function projectToMention(p: { id: string; title: string; description?: string | null; icon?: string | null; status?: ProjectStatus }): MentionItem {
  return {
    id: p.id,
    label: p.title,
    type: "project" as const,
    description: p.description ?? undefined,
    icon: p.icon ?? null,
    projectStatus: p.status,
  };
}

/** 候选是否命中当前查询（子串 + 拼音匹配，label 与 description 都查）。 */
function matchesMentionQuery(item: MentionItem, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return (
    item.label.toLowerCase().includes(q) ||
    item.description?.toLowerCase().includes(q) === true ||
    matchesPinyin(item.label, q) ||
    (item.description ? matchesPinyin(item.description, q) : false)
  );
}

/** createMentionSuggestion 的选项。 */
interface MentionSuggestionOptions {
  mode?: "default" | "context";
  getContextItems?: () => MentionItem[];
}

/**
 * 装配 `@` mention 的 Tiptap Suggestion 配置。
 *
 * @param qc React Query 客户端，用于同步读成员/agent/squad 与缓存 issue。
 * @param options 见 MentionSuggestionOptions；"context" 模式会注入调用方
 *   提供的上下文条目并启用项目搜索。
 */
export function createMentionSuggestion(
  qc: QueryClient,
  options: MentionSuggestionOptions = {},
): Omit<
  SuggestionOptions<MentionItem>,
  "editor"
> {
  // 显式 key 传给 Tiptap Suggestion，并由共享弹层控制器派发
  // exitSuggestion(view, pluginKey) 时复用。
  const pluginKey = new PluginKey("mentionSuggestion");

  function buildSyncItems(query: string): MentionItem[] {
    // 命令式读工作区 id，因为这里跑在 TipTap 工厂作用域（React 渲染之外）。
    // getCurrentWsId() 是由 URL 驱动的工作区布局设置的非 React 单例。
    const wsId = getCurrentWsId();
    if (!wsId) return [];

    const members: MemberWithUser[] = qc.getQueryData(workspaceKeys.members(wsId)) ?? [];
    const agents: Agent[] = qc.getQueryData(workspaceKeys.agents(wsId)) ?? [];
    const squads: Squad[] = qc.getQueryData(workspaceKeys.squads(wsId)) ?? [];
    const listQueries = qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) });
    const cachedResponse = listQueries[0]?.[1];
    const cachedIssues: Issue[] = cachedResponse ? flattenIssueBuckets(cachedResponse) : [];

    // 命令式读当前用户身份——本工厂运行在 React 渲染之外，不能在这里以
    // hook 方式调 useAuthStore()。packages/core/auth/index.ts 里的 Proxy 会
    // 把 `.getState()` 转发给已注册的 store。用于在 @mention 列表里拦截
    // 个人 agent：成员不应看到（或自动补全）本就没法指派给自己的 agent。
    const userId = useAuthStore.getState().user?.id ?? null;
    const myRole =
      members.find((m) => m.user_id === userId)?.role ?? null;

    const q = query.toLowerCase();

    const allItem: MentionItem[] =
      "all members".includes(q) || "all".includes(q)
        ? [{ id: "all", label: "All members", type: "all" as const }]
        : [];

    const memberItems: MentionItem[] = members
      .filter((m) => m.name.toLowerCase().includes(q) || matchesPinyin(m.name, q))
      .map((m) => ({
        id: m.user_id,
        label: m.name,
        type: "member" as const,
      }));

    const agentItems: MentionItem[] = agents
      .filter(
        (a) =>
          !a.archived_at &&
          (a.name.toLowerCase().includes(q) || matchesPinyin(a.name, q)) &&
          canAssignAgentToIssue(a, { userId, role: myRole }).allowed,
      )
      .map((a) => ({
        id: a.id,
        label: a.name,
        type: "agent" as const,
        disabledReason: isAgentRuntimeBound(a)
          ? undefined
          : ("agent_runtime_required" as const),
      }));
    const activeAgentRuntimeBinding = new Map(
      agents
        .filter((agent) => !agent.archived_at)
        .map((agent) => [agent.id, isAgentRuntimeBound(agent)]),
    );

    const squadItems: MentionItem[] = squads
      .filter(
        (s) =>
          !s.archived_at &&
          (s.name.toLowerCase().includes(q) || matchesPinyin(s.name, q)),
      )
      .map((s) => ({
        id: s.id,
        label: s.name,
        type: "squad" as const,
        disabledReason:
          activeAgentRuntimeBinding.get(s.leader_id) === false
            ? ("agent_runtime_required" as const)
            : undefined,
      }));

    // 成员与 agent 共享一个已排序列表——最近提及的排在前面、不论类型，
    // 本设备上用户还没提过的按字母序兜底。
    const recency = getRecencyMap(wsId);
    const userItems = sortUserItemsByRecency(
      [...memberItems, ...agentItems, ...squadItems],
      recency,
    );

    // 缓存 issue 提供即时首屏；MentionList 会叠加服务端匹配结果，补齐
    // done/cancelled 及其它不在本缓存里的 issue。
    const issueItems: MentionItem[] = cachedIssues
      .filter(
        (i) =>
          i.identifier.toLowerCase().includes(q) ||
          i.title.toLowerCase().includes(q),
      )
      .map(issueToMention);

    return [...allItem, ...userItems, ...issueItems];
  }

  return {
    pluginKey,
    allowSpaces: true,
    // 只对用户真正敲下的 `@` 打开。Tiptap 只看文档内容匹配，没有这条的话，
    // 粘贴、拖放、撤销或服务端加载的 `@` 也会一样打开弹层（MUL-5429）。
    shouldShow: ({ editor, range }) => isTriggerArmedAt(editor, range.from),
    items: ({ query }) => {
      if (options.mode === "context") {
        const normalizedQuery = query.trim();
        const contextItems = (options.getContextItems?.() ?? []).filter((item) => matchesMentionQuery(item, query));
        if (!normalizedQuery) return contextItems;
        return mergeMentionItems(contextItems, buildSyncItems(query));
      }
      return buildSyncItems(query);
    },

    render: createSuggestionPopupRender<MentionItem, MentionItem, MentionListRef, MentionListProps>({
      pluginKey,
      component: MentionList,
      getProps: (props) => ({
        items: props.items,
        query: props.query,
        command: props.command,
        includeProjectSearch: options.mode === "context",
      }),
      onKeyDown: (ref, props) => ref?.onKeyDown(props) ?? false,
    }),
  };
}
