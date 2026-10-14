// 本文件提供路由图标的登记：每个工作区「页面」（集合/工具型界面）对应的
// 静态图标，以及「路径 → 图标名」的解析。
//
// 这里的「页面」指没有具体资源实例的集合或工具界面——Issues、Projects、
// Settings 等。它的图标是按 URL 路由段（`/{slug}/{segment}/...`）选取的稳定
// 静态选择。
//
// 这是侧边栏导航的真源（经 `resolveRouteIconName` / `routeIconForPath`）。
// 资源型导航也用同一登记做「页面级兜底」。
//
// 图标值是「名字」而不是 React 组件，让本模块保持无 React、在 `@multica/core`
// 里安全。名字 → 组件的注册表在 `packages/views/layout/route-icon-components.tsx`；
// 它的 `Record<RouteIconName, LucideIcon>` 类型让「缺组件」成为编译错误。

/** 导航页或资源类型能解析到的全部图标名。 */
export type RouteIconName =
  | "Inbox"
  | "MessageSquare"
  | "CircleUser"
  | "ListTodo"
  | "FolderKanban"
  | "Zap"
  | "Bot"
  | "Users"
  | "BarChart3"
  | "Monitor"
  | "Server"
  | "BookOpenText"
  | "Settings"
  | "ChefHat"
  | "Activity"
  | "Target"
  | "Heart"
  | "File"
  | "FileText"
  | "FileImage"
  | "FileCode"
  | "FileArchive"
  | "FileAudio"
  | "FileVideo"
  | "FileQuestion";

/** 页面的 i18n 文案 key（在 `layout.nav` 命名空间下）。 */
export type NavLabelKey =
  | "inbox"
  | "chat"
  | "my_issues"
  | "issues"
  | "projects"
  | "autopilots"
  | "agents"
  | "squads"
  | "usage"
  | "run_reviews"
  | "runtimes"
  | "mcp"
  | "quick_actions"
  | "skills"
  | "settings"
  | "cooking"
  | "health"
  | "meals"
  | "goals"
  | "life";

/** 每个工作区导航页的稳定标识。 */
export type WorkspacePageKey =
  | "inbox"
  | "chat"
  | "myIssues"
  | "issues"
  | "projects"
  | "autopilots"
  | "agents"
  | "squads"
  | "usage"
  | "runReviews"
  | "runtimes"
  | "mcp"
  | "quickActions"
  | "skills"
  | "settings"
  | "cooking"
  | "health"
  | "meals"
  | "goals"
  | "life";

/** 一个工作区导航页的注册条目。 */
export interface WorkspacePage {
  /** `/{slug}/{segment}/...` 里下标 1 的路由段。 */
  segment: string;
  /** 静态页面图标。 */
  icon: RouteIconName;
  /** `layout.nav.<navKey>` —— 本地化的页面名。 */
  navKey: NavLabelKey;
}

/**
 * 工作区导航页的唯一真源。保持与 paths.ts 里的导航目标和侧边栏导航分组对齐。
 */
export const WORKSPACE_PAGES: Record<WorkspacePageKey, WorkspacePage> = {
  inbox: { segment: "inbox", icon: "Inbox", navKey: "inbox" },
  chat: { segment: "chat", icon: "MessageSquare", navKey: "chat" },
  myIssues: { segment: "my-issues", icon: "CircleUser", navKey: "my_issues" },
  issues: { segment: "issues", icon: "ListTodo", navKey: "issues" },
  projects: { segment: "projects", icon: "FolderKanban", navKey: "projects" },
  autopilots: { segment: "autopilots", icon: "Zap", navKey: "autopilots" },
  agents: { segment: "agents", icon: "Bot", navKey: "agents" },
  squads: { segment: "squads", icon: "Users", navKey: "squads" },
  usage: { segment: "usage", icon: "BarChart3", navKey: "usage" },
  runReviews: { segment: "run-reviews", icon: "BarChart3", navKey: "run_reviews" },
  runtimes: { segment: "runtimes", icon: "Monitor", navKey: "runtimes" },
  mcp: { segment: "mcp", icon: "Server", navKey: "mcp" },
  quickActions: { segment: "quick-actions", icon: "Zap", navKey: "quick_actions" },
  skills: { segment: "skills", icon: "BookOpenText", navKey: "skills" },
  settings: { segment: "settings", icon: "Settings", navKey: "settings" },
  cooking: { segment: "cooking", icon: "ChefHat", navKey: "cooking" },
  health: { segment: "health", icon: "Activity", navKey: "health" },
  meals: { segment: "meals", icon: "ChefHat", navKey: "meals" },
  goals: { segment: "goals", icon: "Target", navKey: "goals" },
  life: { segment: "life", icon: "Heart", navKey: "life" },
};

/** 反向查找：路由段 → 页面 key。 */
const PAGE_BY_SEGMENT: Record<string, WorkspacePageKey> = Object.fromEntries(
  (Object.keys(WORKSPACE_PAGES) as WorkspacePageKey[]).map((key) => [
    WORKSPACE_PAGES[key].segment,
    key,
  ]),
);

/** 返回路由段对应的页面 key；没有匹配返回 null。 */
export function pageForSegment(segment: string): WorkspacePageKey | null {
  return PAGE_BY_SEGMENT[segment] ?? null;
}

/** 路径的路由段没有显式页面时用的兜底图标名。 */
export const DEFAULT_ROUTE_ICON_NAME: RouteIconName = "ListTodo";

/**
 * 解析工作区范围路径（或完整标签页 URL）的「页面级」图标名。
 *
 * 导航页路径是 `/{slug}/{segment}/...`，路由段在下标 1；忽略任何 search/hash
 * 后缀。子路由沿用父页面的图标。未知或过短的路径返回
 * {@link DEFAULT_ROUTE_ICON_NAME}，保证结果总是可渲染的图标名。
 *
 * 这是侧边栏/导航的入口。资源详情路由请用更丰富的展示解析器。
 *
 * @param path - 工作区范围路径或完整 URL
 * @returns 图标名
 */
export function resolveRouteIconName(path: string): RouteIconName {
  const pathname = path.split(/[?#]/)[0] ?? "";
  const segment = pathname.split("/").filter(Boolean)[1] ?? "";
  const page = pageForSegment(segment);
  return page ? WORKSPACE_PAGES[page].icon : DEFAULT_ROUTE_ICON_NAME;
}
