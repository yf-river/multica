"use client";

import { issueStatusCategory } from "@multica/core/issues";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Check,
  Clock,
  Copy,
  FileText,
  Link2,
  Loader2,
  MessageSquare,
  Plus,
  SearchIcon,
  ListChevronsDownUp,
  ListChevronsUpDown,
  Monitor,
  Moon,
  Sun,
  type LucideIcon,
} from "lucide-react";
import { Command as CommandPrimitive } from "cmdk";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type {
  MemberWithUser,
  SearchIssueResult,
  SearchProjectResult,
} from "@multica/core/types";
import { api } from "@multica/core/api";
import { partitionAggregatedSearchResults } from "@multica/core/search/cancelled-rank";
import {
  openCreateIssueWithPreference,
  selectRecentIssues,
  useCommentCollapseStore,
  useRecentIssuesStore,
  useResolvedExpandStore,
} from "@multica/core/issues/stores";
import { issueDetailOptions, issueTimelineOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core";
import { useWorkspacePaths, WORKSPACE_PAGES } from "@multica/core/paths";
import type { WorkspacePageKey, WorkspacePaths } from "@multica/core/paths";
import { useModalStore } from "@multica/core/modals";
import { createShortcutChord } from "@multica/core/shortcuts";
import { memberListOptions } from "@multica/core/workspace/queries";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { StatusIcon } from "../issues/components";
import { resolvedThreadRootIds, rootCommentIds } from "../issues/components/thread-utils";
import { ProjectIcon } from "../projects/components/project-icon";
import { useProjectStatusLabels } from "../projects/components/labels";
import { routeIconForPath } from "../layout/route-icon-components";
import { PROJECT_STATUS_CONFIG } from "@multica/core/projects/config";
import type { ProjectStatus } from "@multica/core/types";
import { ActorAvatar } from "../common/actor-avatar";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { useTheme } from "@multica/ui/components/common/theme-provider";
import { copyText } from "@multica/ui/lib/clipboard";
import {
  resolveClickIntent,
  useIntentNavigate,
  useNavigation,
  type LinkClickIntent,
} from "../navigation";
import { useT } from "../i18n";
import { matchesPinyin } from "../editor/extensions/pinyin-match";
import { HighlightText } from "./highlight-text";
import { useSearchStore } from "./search-store";

/**
 * search 模块：全局搜索命令面板（Command Palette）。
 * 由侧边栏搜索框或快捷键唤起的模态弹窗，可在同一处搜索并跳转到：
 *   - 页面（侧边栏注册的每个工作区页）
 *   - 快捷命令（新建 issue / 项目、复制当前 issue 链接、切换主题等）
 *   - 成员（按名称 / 邮箱 / 角色 / 拼音匹配）
 *   - issue 与项目（后端聚合搜索，按状态降级排序后分组展示）
 * 空查询时展示最近访问的 issue。
 */

// 调色板的「页面」分组由 WORKSPACE_PAGES 生成——侧边栏导航与桌面端标签栏
// 读的也是同一个注册表。它以前是手写列表，每新增一个页面就会悄悄过期：
// Chat、Autopilot、Squads、Analytics 已经上线侧边栏却无法从调色板打开
// （MUL-6272）。改为从注册表推导后，新工作区页一进注册表就会出现在调色板里。
//
// 页面 key 同时充当 WorkspacePaths 的方法名，所以 `p[key]()` 在渲染时按当前
// 工作区 slug 解析目标地址。因此每个 WorkspacePageKey 都必须是零参数路径构造器。

// 每个页面在本地化标签之外的额外查询别名。声明成完整的 Record，这样新增
// 一个工作区页时若没填关键词就会直接编译报错。
const PAGE_KEYWORDS: Record<WorkspacePageKey, string[]> = {
  inbox: ["inbox", "notifications", "收件箱", "通知"],
  chat: ["chat", "messages", "conversation", "聊天", "消息", "对话"],
  myIssues: ["my", "issues", "assigned", "mine", "我的", "任务"],
  issues: ["issues", "tasks", "bugs", "任务"],
  projects: ["projects", "kanban", "项目"],
  autopilots: ["autopilot", "autopilots", "automation", "schedule", "cron", "webhook", "自动化", "定时"],
  agents: ["agents", "bots", "ai", "智能体"],
  squads: ["squads", "teams", "小队", "团队"],
  usage: ["usage", "analytics", "stats", "metrics", "统计", "分析", "用量"],
  runReviews: ["run", "reviews", "history", "trace", "运行", "复盘", "历史", "轨迹"],
  runtimes: ["runtimes", "environments", "machines", "运行时"],
  skills: ["skills", "library", "技能"],
  settings: ["settings", "config", "preferences", "设置", "配置"],
  health: ["health", "身体", "健康", "身高", "体重", "指标"],
  life: ["life", "companion", "memory", "experiment", "人生", "搭子", "记忆", "实验"],
};

const NAV_PAGE_KEYS = Object.keys(WORKSPACE_PAGES) as WorkspacePageKey[];

// 刻意没有 `icon` 字段：与侧边栏导航一样，页面图标由 routeIconForPath 从
// 目标路径推导，保证所有导航界面对同一路由显示同一个图标。
interface NavPage {
  key: WorkspacePageKey;
  label: string;
  keywords: string[];
}

type ThemeValue = "light" | "dark" | "system";

/** 取成员名称首字母的缩写，用于无头像时的占位。 */
function memberInitials(name: string) {
  return name
    .split(" ")
    .map((word) => word[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);
}

// 页面与命令分组共用的行匹配器。
//
// 关键词按前缀而非子串匹配。它们是不可见的同义词，子串命中从外部看像 bug：
// 输 "s" 曾带出 Inbox（经 "notifications"），输 "i" 几乎带出所有页面。
// 可见标签仍然允许任意位置命中，因为那里用户能看见自己命中的是什么。
//
// 支持拼音的原因与成员搜索相同：中文界面下本地化标签是用户唯一能瞄准的
// 东西，所以输入 "renwu" 必须能命中「任务」。
function matchesRow(label: string, keywords: string[], query: string) {
  return (
    label.toLowerCase().includes(query) ||
    keywords.some((kw) => kw.startsWith(query)) ||
    matchesPinyin(label, query)
  );
}

/** 成员匹配：名称 / 邮箱任意位置命中，角色前缀命中，名称支持拼音。 */
function matchesMember(member: MemberWithUser, query: string) {
  return (
    member.name.toLowerCase().includes(query) ||
    member.email.toLowerCase().includes(query) ||
    (query.length >= 3 && member.role.startsWith(query)) ||
    matchesPinyin(member.name, query)
  );
}

/** 结果行左侧的 issue 指派人头像；没有指派人不渲染。 */
function IssueAssigneeAvatar({
  assigneeType,
  assigneeId,
}: {
  assigneeType?: string | null;
  assigneeId?: string | null;
}) {
  if (!assigneeType || !assigneeId) return null;
  return (
    <ActorAvatar
      actorType={assigneeType}
      actorId={assigneeId}
      size="sm"
      profileLink={false}
      className="shrink-0"
    />
  );
}

// 项目 / issue 结果行由三组渲染（项目、issues、已取消——见结果列表的分组
// 说明），所以行标记各自收在一个组件里，避免在每组里重复。
function ProjectResultRow({
  project,
  query,
  disabled,
  onSelect,
}: {
  project: SearchProjectResult;
  query: string;
  disabled?: boolean;
  onSelect: (value: string) => void;
}) {
  const projectStatusLabels = useProjectStatusLabels();
  const status = project.status as ProjectStatus;

  return (
    <CommandPrimitive.Item
      key={`project:${project.id}`}
      value={`project:${project.id}`}
      disabled={disabled}
      onSelect={onSelect}
      className="flex cursor-default select-none flex-col gap-1 rounded-lg px-3 py-2.5 text-body outline-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-selected:bg-accent"
    >
      <div className="flex items-center gap-2.5">
        <ProjectIcon project={project} size="md" />
        <span className="truncate">
          <HighlightText text={project.title} query={query} />
        </span>
        <span
          className={`ml-auto text-caption shrink-0 ${PROJECT_STATUS_CONFIG[status]?.color ?? "text-muted-foreground"}`}
        >
          {projectStatusLabels[status] ?? project.status}
        </span>
      </div>
      {project.match_source === "description" && project.matched_snippet && (
        <div className="flex items-start gap-2 pl-[26px]">
          <span className="text-caption text-muted-foreground truncate">
            <HighlightText text={project.matched_snippet} query={query} />
          </span>
        </div>
      )}
    </CommandPrimitive.Item>
  );
}

/** 搜索结果里的单个 issue 行：状态图标 + 编号 + 标题 + 命中片段。 */
function IssueResultRow({
  issue,
  query,
  disabled,
  onSelect,
}: {
  issue: SearchIssueResult;
  query: string;
  disabled?: boolean;
  onSelect: (value: string) => void;
}) {
  return (
    <CommandPrimitive.Item
      key={issue.id}
      value={issue.id}
      disabled={disabled}
      onSelect={onSelect}
      className="flex cursor-default select-none flex-col gap-1 rounded-lg px-3 py-2.5 text-body outline-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-selected:bg-accent"
    >
      <div className="flex items-center gap-2.5">
        <StatusIcon
          status={issue.status}
          category={issueStatusCategory(issue) ?? undefined}
          className="size-4 shrink-0"
        />
        <span className="text-caption text-muted-foreground shrink-0">
          {issue.identifier}
        </span>
        <span className="min-w-0 flex-1 truncate">
          <HighlightText text={issue.title} query={query} />
        </span>
        <IssueAssigneeAvatar
          assigneeType={issue.assignee_type}
          assigneeId={issue.assignee_id}
        />
      </div>
      {issue.matched_description_snippet && (
        <div className="flex items-start gap-2 pl-[26px]">
          <FileText className="size-3 shrink-0 text-muted-foreground mt-0.5" />
          <span className="text-caption text-muted-foreground truncate">
            <HighlightText
              text={issue.matched_description_snippet}
              query={query}
            />
          </span>
        </div>
      )}
      {issue.matched_comment_snippet && (
        <div className="flex items-start gap-2 pl-[26px]">
          <MessageSquare className="size-3 shrink-0 text-muted-foreground mt-0.5" />
          <span className="text-caption text-muted-foreground truncate">
            <HighlightText text={issue.matched_comment_snippet} query={query} />
          </span>
        </div>
      )}
    </CommandPrimitive.Item>
  );
}

/** 命令面板里一条可执行的快捷命令。 */
interface CommandItem {
  key: string;
  label: string;
  icon: LucideIcon;
  keywords: string[];
  trailing?: React.ReactNode;
  onSelect: () => void;
}

/** 一次搜索的结果集合（按展示时的查询快照存储）。 */
interface SearchResults {
  /**
   * 这些结果所回答的（已去掉首尾空格的）查询。结果行会比触发它们的查询
   * 活得更久——下一次请求在途时它们仍留在界面上——所以每份结果都带上它
   * 所回答的问题，凡是可以对某行执行动作的地方，都会先确认它仍匹配用户
   * 当前输入的内容。
   */
  query: string;
  issues: SearchIssueResult[];
  projects: SearchProjectResult[];
}

const NO_RESULTS: SearchResults = { query: "", issues: [], projects: [] };

// 所有分组共用的标题样式。标题走 cmdk 的 `heading` prop 而非自绘 div：
// cmdk 会把它渲染成 [cmdk-group-heading] 节点并把分组的 aria-labelledby
// 指向它，这样读屏软件能读出某行属于哪个分组。
const GROUP_CLASS =
  "p-2 [&_[cmdk-group-heading]]:px-3 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-caption [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-muted-foreground";

/**
 * 全局搜索命令面板（主体组件）。
 * 渲染一个覆盖层 Dialog：输入即搜索，结果按「页面 / 命令 / 成员 / 项目 /
 * issues / 已取消 / 最近」分组展示，选中后用记录的点击意图（cmd/ctrl 是否
 * 按下）在当前页或新标签页导航。
 */
export function SearchCommand() {
  const { t } = useT("search");
  // 页面名取自侧边栏自己的命名空间，而不是 `search.pages` 下的私有副本：
  // 每个页面只有一条翻译，调色板永远不会跟侧边栏在页面叫法上打架。
  const { t: tNav } = useT("layout");
  const navPages = useMemo<NavPage[]>(
    () =>
      NAV_PAGE_KEYS.map((key) => ({
        key,
        label: tNav(($) => $.nav[WORKSPACE_PAGES[key].navKey]),
        keywords: PAGE_KEYWORDS[key],
      })),
    [tNav],
  );
  const { pathname, getShareableUrl } = useNavigation();
  const intentNavigate = useIntentNavigate();
  const open = useSearchStore((s) => s.open);
  const setOpen = useSearchStore((s) => s.setOpen);

  // cmdk 的 onSelect 不携带事件，所以触发手势的修饰键在捕获阶段被记下来
  // （点结果行、输入框里按 Enter），再由下面的选择处理器消费：cmd/ctrl+
  // 点击或 Enter 会在新标签页打开结果，而不是原地跳转。
  const pendingIntentRef = useRef<LinkClickIntent>("push");
  const recordIntent = useCallback(
    (e: Pick<MouseEvent, "button" | "metaKey" | "ctrlKey" | "shiftKey">) => {
      pendingIntentRef.current = resolveClickIntent(e);
    },
    [],
  );
  const consumeIntent = useCallback(() => {
    const intent = pendingIntentRef.current;
    pendingIntentRef.current = "push";
    return intent;
  }, []);
  const wsId = useWorkspaceId();
  const recentItems = useRecentIssuesStore(selectRecentIssues(wsId));
  const p: WorkspacePaths = useWorkspacePaths();
  const { theme, setTheme } = useTheme();
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: open && !!wsId,
  });

  // 每个「最近访问」issue 都通过它的详情缓存解析。最近项通常已在详情缓存里
  // （因为用户打开过它们）；如果不在，就按 id 各发一次详情请求，让 Recent
  // 不依赖 issue 是否落在分页列表缓存内。
  //
  // 用 `open` 门控：调色板在整个会话期间一直挂载，store 最多存 20 个最近
  // id，所以不加门控的话，每次冷启动应用都会为一块用户可能根本不打开的面
  // 触发最多 20 个 issue 详情请求。
  const recentDetailQueries = useQueries({
    queries: open ? recentItems.map((item) => issueDetailOptions(wsId, item.id)) : [],
  });
  const recentIssues = useMemo(
    () =>
      recentDetailQueries.flatMap((q) => (q.data ? [q.data] : [])),
    [recentDetailQueries],
  );

  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResults>(NO_RESULTS);
  const [isLoading, setIsLoading] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const filteredPages = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return [];
    return navPages.filter((page) => matchesRow(page.label, page.keywords, q));
  }, [navPages, query]);

  // 判断当前路由是不是 issue 详情页——/{slug}/issues/{id}。
  // 其它路由返回 null，用于决定要不要展示 issue 专属命令。
  const currentIssueId = useMemo(() => {
    const match = pathname.match(/\/issues\/([^/]+)$/);
    const raw = match?.[1];
    return raw ? decodeURIComponent(raw) : null;
  }, [pathname]);
  const { data: currentIssue = null } = useQuery({
    ...issueDetailOptions(wsId, currentIssueId ?? ""),
    enabled: open && !!currentIssueId,
  });
  const queryClient = useQueryClient();

  const commands = useMemo<CommandItem[]>(() => {
    const activeThemeCheck = (value: ThemeValue) =>
      theme === value ? (
        <Check
          aria-label={t(($) => $.commands.current_theme_aria)}
          className="ml-auto size-4 shrink-0 text-muted-foreground"
        />
      ) : undefined;

    const items: CommandItem[] = [
      {
        key: "new-issue",
        label: t(($) => $.commands.new_issue),
        icon: Plus,
        keywords: ["new", "issue", "create", "add"],
        onSelect: () => {
          openCreateIssueWithPreference();
          setOpen(false);
        },
      },
      {
        key: "new-project",
        label: t(($) => $.commands.new_project),
        icon: Plus,
        keywords: ["new", "project", "create", "add"],
        onSelect: () => {
          useModalStore.getState().open("create-project");
          setOpen(false);
        },
      },
    ];

    if (currentIssueId && currentIssue) {
      const identifier = currentIssue.identifier;
      items.push(
        {
          key: "copy-issue-link",
          label: t(($) => $.commands.copy_issue_link),
          icon: Link2,
          keywords: ["copy", "link", "share", "url", identifier.toLowerCase()],
          onSelect: () => {
            void copyText(getShareableUrl(pathname)).then((ok) => {
              if (ok) toast.success(t(($) => $.toast.link_copied));
            });
            setOpen(false);
          },
        },
        {
          key: "copy-issue-identifier",
          label: t(($) => $.commands.copy_identifier, { identifier }),
          icon: Copy,
          keywords: ["copy", "id", "identifier", identifier.toLowerCase()],
          onSelect: () => {
            void copyText(identifier).then((ok) => {
              if (ok) toast.success(t(($) => $.toast.copied_identifier, { identifier }));
            });
            setOpen(false);
          },
        },
        {
          key: "fold-all-comments",
          label: t(($) => $.commands.fold_all_comments),
          icon: ListChevronsDownUp,
          keywords: ["fold", "collapse", "comments", "收起", "折叠", "评论"],
          onSelect: () => {
            // 只要 issue 页渲染过，时间线就已进缓存；ensureQueryData 只会在
            // 冷缓存时发请求。若仍加载不出来，说明屏幕上本就没有评论——
            // 隐藏该动作与可见状态一致。
            void queryClient
              .ensureQueryData(issueTimelineOptions(currentIssueId))
              .then((entries) => {
                useCommentCollapseStore
                  .getState()
                  .collapseAll(currentIssueId, rootCommentIds(entries));
                useResolvedExpandStore.getState().collapseAll(currentIssueId);
              })
              .catch(() => {});
            setOpen(false);
          },
        },
        {
          key: "unfold-all-comments",
          label: t(($) => $.commands.unfold_all_comments),
          icon: ListChevronsUpDown,
          keywords: ["unfold", "expand", "comments", "展开", "评论"],
          onSelect: () => {
            void queryClient
              .ensureQueryData(issueTimelineOptions(currentIssueId))
              .then((entries) => {
                useCommentCollapseStore.getState().expandAll(currentIssueId);
                useResolvedExpandStore
                  .getState()
                  .expandAll(currentIssueId, resolvedThreadRootIds(entries));
              })
              .catch(() => {});
            setOpen(false);
          },
        },
      );
    }

    items.push(
      {
        key: "theme-light",
        label: t(($) => $.commands.switch_to_light),
        icon: Sun,
        keywords: ["light", "theme", "appearance", "mode", "bright"],
        trailing: activeThemeCheck("light"),
        onSelect: () => {
          setTheme("light");
          setOpen(false);
        },
      },
      {
        key: "theme-dark",
        label: t(($) => $.commands.switch_to_dark),
        icon: Moon,
        keywords: ["dark", "theme", "appearance", "mode", "night"],
        trailing: activeThemeCheck("dark"),
        onSelect: () => {
          setTheme("dark");
          setOpen(false);
        },
      },
      {
        key: "theme-system",
        label: t(($) => $.commands.use_system_theme),
        icon: Monitor,
        keywords: ["system", "theme", "appearance", "mode", "auto"],
        trailing: activeThemeCheck("system"),
        onSelect: () => {
          setTheme("system");
          setOpen(false);
        },
      },
    );

    return items;
  }, [currentIssue, currentIssueId, getShareableUrl, pathname, queryClient, setOpen, setTheme, theme, t]);

  const filteredCommands = useMemo(() => {
    const q = query.trim().toLowerCase();
    // 无查询时只露出主创建动作。其它命令（切主题、复制、New Project）随用户
    // 输入逐渐浮现，空态空间留给 Recent。
    if (!q) return commands.filter((c) => c.key === "new-issue");
    return commands.filter((c) => matchesRow(c.label, c.keywords, q));
  }, [commands, query]);

  const filteredMembers = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return [];
    const wantsAllMembers =
      q.length >= 3 &&
      ("members".startsWith(q) ||
        "people".startsWith(q) ||
        "users".startsWith(q) ||
        "team".startsWith(q));
    return members
      .filter((member) => wantsAllMembers || matchesMember(member, q))
      .slice(0, 10);
  }, [members, query]);

  const hasResults =
    results.issues.length > 0 ||
    results.projects.length > 0 ||
    filteredMembers.length > 0;

  // 回答上一次查询的行在下一次请求在途时仍被渲染——这正是列表不会每敲一个
  // 字符就闪烁的原因——但它们必须不可操作：输入一变 cmdk 就会重选第一个合法
  // 项，一条仍在屏的过期行会把下一次 Enter 变成跳去用户已经敲过去的旧结果。
  const resultsAreStale = results.query !== query.trim();

  // 跨类型的「已取消」降级（MUL-5824）。两次搜索各自在服务端独立排序，所以
  // 分区只能在这里（聚合展示时）做。见结果列表上的渲染说明。
  const partitionedResults = useMemo(
    () =>
      partitionAggregatedSearchResults({
        issues: results.issues,
        projects: results.projects,
        // 分区描述的是这些行，所以以它们所回答的查询为 key——而不是用户
        // 后来又输入了什么。
        query: results.query,
      }),
    [results],
  );

  // 单次 ESC 关闭——捕获阶段会先于 base-ui Dialog 自己的处理器触发
  useEffect(() => {
    if (!open) return;
    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        setOpen(false);
      }
    };
    document.addEventListener("keydown", handleEsc, true);
    return () => document.removeEventListener("keydown", handleEsc, true);
  }, [open, setOpen]);

  // 卸载时清理防抖定时器与进行中的请求
  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
      if (abortRef.current) abortRef.current.abort();
    };
  }, []);

  // 弹窗关闭时重置内部状态
  useEffect(() => {
    if (!open) {
      setQuery("");
      setResults(NO_RESULTS);
      setIsLoading(false);
    }
  }, [open]);

  const search = useCallback((q: string) => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    if (abortRef.current) abortRef.current.abort();

    if (!q.trim()) {
      setResults(NO_RESULTS);
      setIsLoading(false);
      return;
    }

    setIsLoading(true);
    debounceRef.current = setTimeout(async () => {
      const controller = new AbortController();
      abortRef.current = controller;
      try {
        const [issueRes, projectRes] = await Promise.all([
          api.searchIssues({
            q: q.trim(),
            limit: 20,
            include_closed: true,
            signal: controller.signal,
          }),
          api.searchProjects({
            q: q.trim(),
            limit: 10,
            include_closed: true,
            signal: controller.signal,
          }),
        ]);
        if (!controller.signal.aborted) {
          setResults({
            query: q.trim(),
            issues: issueRes.issues,
            projects: projectRes.projects,
          });
          setIsLoading(false);
        }
      } catch {
        if (!controller.signal.aborted) {
          // 丢弃上一次查询留下的行，而不是让它们永久灰着留在屏幕上：
          // 原本要替换它们的请求永远不会来了。列表直接落到空态。
          setResults({ query: q.trim(), issues: [], projects: [] });
          setIsLoading(false);
        }
      }
    }, 300);
  }, []);

  const handleValueChange = useCallback(
    (value: string) => {
      setQuery(value);
      search(value);
    },
    [search],
  );

  const handleSelect = useCallback(
    (value: string) => {
      setOpen(false);
      const href = value.startsWith("project:")
        ? // value 形如 "project:<id>"——去掉前 8 个字符取出 id。
          p.projectDetail(value.slice(8))
        : p.issueDetail(value);
      intentNavigate(href, consumeIntent());
    },
    [intentNavigate, consumeIntent, setOpen, p],
  );

  const handlePageSelect = useCallback(
    (key: WorkspacePageKey) => {
      setOpen(false);
      intentNavigate(p[key](), consumeIntent());
    },
    [intentNavigate, consumeIntent, setOpen, p],
  );

  const handleMemberSelect = useCallback(
    (userId: string) => {
      intentNavigate(p.memberDetail(userId), consumeIntent());
      setOpen(false);
    },
    [intentNavigate, consumeIntent, setOpen, p],
  );

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent
        finalFocus={false}
        className="top-[20%] translate-y-0 overflow-hidden rounded-xl! p-0 sm:max-w-xl!"
        showCloseButton={false}
      >
        <DialogHeader className="sr-only">
          <DialogTitle>{t(($) => $.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.description)}
          </DialogDescription>
        </DialogHeader>
        <CommandPrimitive
          shouldFilter={false}
          onPointerDownCapture={recordIntent}
          onClickCapture={recordIntent}
          onKeyDownCapture={(e) => {
            if (e.key === "Enter") {
              recordIntent({
                button: 0,
                metaKey: e.metaKey,
                ctrlKey: e.ctrlKey,
                shiftKey: e.shiftKey,
              });
            }
          }}
          className="flex size-full flex-col overflow-hidden rounded-xl bg-popover text-popover-foreground"
        >
          {/* 搜索输入框 */}
          <div className="flex items-center gap-3 border-b px-4 py-3">
            <SearchIcon className="size-5 shrink-0 text-muted-foreground" />
            <CommandPrimitive.Input
              placeholder={t(($) => $.placeholder)}
              value={query}
              onValueChange={handleValueChange}
              onKeyDown={(e) => {
                // cmdk 的根处理器会拦截 Home/End 做列表导航；这里阻止冒泡，
                // 让浏览器把光标移动到文本开头/结尾。
                if (e.key === "Home" || e.key === "End") {
                  e.stopPropagation();
                }
              }}
              className="flex-1 bg-transparent text-body outline-none placeholder:text-muted-foreground"
            />
            <ShortcutKeycaps
              shortcut={createShortcutChord("Escape")}
              className="hidden shrink-0 sm:inline-flex"
            />
          </div>

          {/* 结果列表 */}
          <CommandPrimitive.List className="max-h-[min(400px,50vh)] overflow-y-auto overflow-x-hidden">
            {/* 页面分组——仅在查询命中时展示 */}
            {filteredPages.length > 0 && (
              <CommandPrimitive.Group
                heading={t(($) => $.groups.pages)}
                className={GROUP_CLASS}
              >
                {filteredPages.map((page) => {
                  const PageIcon = routeIconForPath(p[page.key]());
                  return (
                  <CommandPrimitive.Item
                    key={page.key}
                    value={`page:${page.key}`}
                    onSelect={() => handlePageSelect(page.key)}
                    className="flex cursor-default select-none items-center gap-2.5 rounded-lg px-3 py-2.5 text-body outline-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-selected:bg-accent"
                  >
                    <PageIcon className="size-4 shrink-0 text-muted-foreground" />
                    <span className="truncate">
                      <HighlightText text={page.label} query={query} />
                    </span>
                  </CommandPrimitive.Item>
                  );
                })}
              </CommandPrimitive.Group>
            )}

            {/* 命令分组——New Issue / New Project / 复制链接 / 主题，仅在查询命中时展示 */}
            {filteredCommands.length > 0 && (
              <CommandPrimitive.Group
                heading={t(($) => $.groups.commands)}
                className={GROUP_CLASS}
              >
                {filteredCommands.map((cmd) => (
                  <CommandPrimitive.Item
                    key={cmd.key}
                    value={`command:${cmd.key}`}
                    onSelect={cmd.onSelect}
                    className="flex cursor-default select-none items-center gap-2.5 rounded-lg px-3 py-2.5 text-body outline-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-selected:bg-accent"
                  >
                    <cmd.icon className="size-4 shrink-0 text-muted-foreground" />
                    <span className="truncate">
                      <HighlightText text={cmd.label} query={query} />
                    </span>
                    {cmd.trailing}
                  </CommandPrimitive.Item>
                ))}
              </CommandPrimitive.Group>
            )}

            {filteredMembers.length > 0 && (
              <CommandPrimitive.Group
                heading={t(($) => $.groups.members)}
                className={GROUP_CLASS}
              >
                {filteredMembers.map((member) => (
                  <CommandPrimitive.Item
                    key={member.user_id}
                    value={`member:${member.user_id}`}
                    onSelect={() => handleMemberSelect(member.user_id)}
                    className="flex cursor-default select-none items-center gap-2.5 rounded-lg px-3 py-2.5 text-body outline-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-selected:bg-accent"
                  >
                    <ActorAvatarBase
                      name={member.name}
                      initials={memberInitials(member.name)}
                      avatarUrl={resolvePublicFileUrl(member.avatar_url)}
                      size="md"
                    />
                    <div className="min-w-0 flex-1">
                      <div className="truncate">
                        <HighlightText text={member.name} query={query} />
                      </div>
                      <div className="truncate text-caption text-muted-foreground">
                        <HighlightText text={member.email} query={query} />
                      </div>
                    </div>
                  </CommandPrimitive.Item>
                ))}
              </CommandPrimitive.Group>
            )}

            {/*
              只在没有任何东西可看时显示 spinner。isLoading 在按键瞬间就翻
              转（300ms 防抖还没触发），所以每敲一个字符就把整个结果列表换成
              spinner，会让输入过程反复闪烁。保留上一次查询的行直到下个响应
              落地，是惯用取舍：它们短暂地晚一拍，而不是直接消失。
            */}
            {isLoading && !hasResults && (
              <div className="flex items-center justify-center py-10">
                <Loader2 className="size-5 animate-spin text-muted-foreground" />
              </div>
            )}

            {!isLoading &&
              query.trim() &&
              !hasResults &&
              filteredPages.length === 0 &&
              filteredCommands.length === 0 && (
                <CommandPrimitive.Empty className="py-10 text-center text-body text-muted-foreground">
                  {t(($) => $.empty.no_results)}
                </CommandPrimitive.Empty>
              )}

            {/*
              渲染顺序是跨类型的「已取消」分区（MUL-5824）：进行中的项目 →
              进行中的 issues → 一个收尾的 Cancelled 分组依次容纳已取消的
              项目再是已取消的 issues。项目与 issues 是两次各自独立排序的
              响应，所以仅按类型排序还不够——过去整个项目组会整块排在 issues
              组上方，导致一个已取消的项目成了列表第一行。把已取消的行集中到
              一个收尾分组，是唯一能让任一种类的已取消行都不会排在另一种类的
              进行中行前面的排布。直接命中仍留在各自的进行中分组
              （见 partitionAggregatedSearchResults）。
            */}
            {partitionedResults.liveProjects.length > 0 && (
              <CommandPrimitive.Group
                heading={t(($) => $.groups.projects)}
                className={GROUP_CLASS}
              >
                {partitionedResults.liveProjects.map((project) => (
                  <ProjectResultRow
                    key={`project:${project.id}`}
                    project={project}
                    query={results.query}
                    disabled={resultsAreStale}
                    onSelect={handleSelect}
                  />
                ))}
              </CommandPrimitive.Group>
            )}

            {partitionedResults.liveIssues.length > 0 && (
              <CommandPrimitive.Group
                heading={t(($) => $.groups.issues)}
                className={GROUP_CLASS}
              >
                {partitionedResults.liveIssues.map((issue) => (
                  <IssueResultRow
                    key={issue.id}
                    issue={issue}
                    query={results.query}
                    disabled={resultsAreStale}
                    onSelect={handleSelect}
                  />
                ))}
              </CommandPrimitive.Group>
            )}

            {partitionedResults.hasCancelled && (
              <CommandPrimitive.Group
                heading={t(($) => $.groups.cancelled)}
                className={GROUP_CLASS}
              >
                {partitionedResults.cancelledProjects.map((project) => (
                  <ProjectResultRow
                    key={`project:${project.id}`}
                    project={project}
                    query={results.query}
                    disabled={resultsAreStale}
                    onSelect={handleSelect}
                  />
                ))}
                {partitionedResults.cancelledIssues.map((issue) => (
                  <IssueResultRow
                    key={issue.id}
                    issue={issue}
                    query={results.query}
                    disabled={resultsAreStale}
                    onSelect={handleSelect}
                  />
                ))}
              </CommandPrimitive.Group>
            )}

            {!query.trim() && recentIssues.length > 0 && (
              <CommandPrimitive.Group
                heading={
                  <>
                    <Clock className="size-3" />
                    <span>{t(($) => $.groups.recent)}</span>
                  </>
                }
                className={`${GROUP_CLASS} [&_[cmdk-group-heading]]:flex [&_[cmdk-group-heading]]:items-center [&_[cmdk-group-heading]]:gap-2`}
              >
                {recentIssues.map((item) => (
                  <CommandPrimitive.Item
                    key={item.id}
                    value={item.id}
                    onSelect={handleSelect}
                    className="flex cursor-default select-none items-center gap-2.5 rounded-lg px-3 py-2.5 text-body outline-none data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50 data-selected:bg-accent"
                  >
                    <StatusIcon
                      status={item.status}
                      category={issueStatusCategory(item) ?? undefined}
                      className="size-4 shrink-0"
                    />
                    <span className="text-caption text-muted-foreground shrink-0">
                      {item.identifier}
                    </span>
                    <span className="min-w-0 flex-1 truncate">{item.title}</span>
                    <IssueAssigneeAvatar
                      assigneeType={item.assignee_type}
                      assigneeId={item.assignee_id}
                    />
                  </CommandPrimitive.Item>
                ))}
              </CommandPrimitive.Group>
            )}

            {!isLoading && !query.trim() && recentIssues.length === 0 && (
              <div className="px-5 py-4 text-center text-caption text-muted-foreground">
                {t(($) => $.empty.type_to_search)}
              </div>
            )}
          </CommandPrimitive.List>
        </CommandPrimitive>
      </DialogContent>
    </Dialog>
  );
}
