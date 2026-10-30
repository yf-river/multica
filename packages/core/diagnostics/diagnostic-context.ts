// 本文件维护「当前诊断路由」以及把真实路径折叠成路由模板的逻辑。
//
// 路由模板能在把工作区 slug 和资源 ID 挡在遥测之外的同时，保留可用于
// 诊断的「屏幕级」分组。

let currentRoute: string | null = null;

/** 记录当前页面所在的路由模板（用于遥测上报前的去标识化）。 */
export function setDiagnosticRoute(next: string | null): void {
  currentRoute = typeof next === "string" && next.trim() ? next.trim().slice(0, 256) : null;
}

/** 读取当前记录的路由模板。 */
export function getDiagnosticRoute(): string | null {
  return currentRoute;
}

/** 清空当前记录的路由上下文（例如登出时）。 */
export function resetDiagnosticContext(): void {
  currentRoute = null;
}

// 已知的路由形态，与 `packages/core/paths/paths.ts`（所有导航都会经过的唯一
// 路径构造器）以及消费它的各路由层保持一致。桶匹配是按「结构」比对的：
// 一个 `:param` 槽位放的是任意占据该位置的片段——不管它是 UUID、issue key、
// `p1`、`skl_123` 还是百分号转义过的字符串。
//
// 不能靠片段长相去猜：那样会漏掉所有长得不像 ID 的 ID——项目、技能、agent、
// runtime、附件的 ID 都是任意字符串。`paths.ts` 还会做 URL 编码，所以含斜杠的
// ID 到达时是已被转义成一段的，绝不能误当成两段路径。
//
// 只加进 `paths.ts` 而没加进这里不会造成泄露：下面会把匹配不上的路径打码而
// 不是放行。diagnostic-context.test.ts 里的对齐测试会遍历真实的路径构造器，
// 一旦少加就会失败，所以这份清单不可能悄悄落后。
type RoutePattern = readonly string[];

// 工作区内的路由模板清单。
const WORKSPACE_ROUTES: readonly RoutePattern[] = [
  ["issues"],
  ["issues", ":id"],
  ["projects"],
  ["projects", ":id"],
  ["autopilots"],
  ["autopilots", ":id"],
  ["agents"],
  ["agents", "new"],
  ["agents", "new", "manual"],
  ["agents", "new", "ai"],
  ["agents", "new", "ai", ":sessionId"],
  ["agents", ":id"],
  ["members", ":id"],
  ["squads"],
  ["squads", ":id"],
  ["inbox"],
  ["chat"],
  ["my-issues"],
  ["usage"],
  ["insights"],
  ["run-reviews"],
  ["runtimes"],
  ["runtimes", ":id"],
  ["runtimes", ":id", "runtime", ":runtimeId"],
  ["skills"],
  ["skills", ":id"],
  ["settings"],
  ["mcp"],
  ["quick-actions"],
  ["upstream"],
  ["health"],
  ["meals"],
  ["goals"],
  ["cooking"],
  ["cooking", "recipes", ":id"],
  ["cooking", "tips"],
  ["cooking", "tips", ":id"],
  ["cooking", "favorites"],
  ["life"],
  ["companion"],
  ["attachments", ":id", "preview"],
];

// 全局（工作区之外）的路由模板清单。
const GLOBAL_ROUTES: readonly RoutePattern[] = [
 ["insights"],
  ["login"],
  ["signup"],
  ["logout"],
  ["workspaces", "new"],
  ["invite", ":id"],
  ["invitations"],
  ["onboarding"],
  ["auth", "callback"],
];

// 各模板的首段组成的集合，用于判断某段是「全局板块名」还是「工作区 slug」。
const WORKSPACE_SECTIONS = new Set(WORKSPACE_ROUTES.map((route) => route[0]!));
const GLOBAL_SECTIONS = new Set(GLOBAL_ROUTES.map((route) => route[0]!));

/**
 * 把具体路径折叠成它的路由模板：`/acme/issues/MUL-12` 会变成 `/:slug/issues/:id`。
 *
 * 诊断只需要知道用户当时在哪个「屏」，而模板能挡住工作区 slug 和资源 ID，
 * 同时让该字段可以按一个查询分组统计。
 *
 * 匹配不上任何已知路由的路径会用 `*` 打码而不是原样上报——识别不出某段是
 * ID 还是页面名，恰恰就是它绝不能外传的情况。
 *
 * @param path - 浏览器里的完整路径（含 query/hash 也能处理）
 * @returns 折叠后的路由模板字符串，如 `/:slug/issues/:id` 或 `/:slug/*`
 */
export function bucketDiagnosticPath(path: string): string {
  const [pathname = ""] = path.split(/[?#]/);
  const segments = pathname.split("/").filter(Boolean);
  if (segments.length === 0) return "/";

  const globalRoute = matchRoute(segments, GLOBAL_ROUTES);
  if (globalRoute) return `/${globalRoute.join("/")}`;

  // 保留 slug 意味着已知的全局板块永远不可能同时也是工作区，所以某个未匹配
  // 的路径若顶着已知全局板块名，就保留板块名、把其余部分打码。
  const first = segments[0]!;
  if (GLOBAL_SECTIONS.has(first)) return `/${first}/*`;

  // 其余都按工作区范围处理，首段就是 slug。
  const rest = segments.slice(1);
  if (rest.length === 0) return "/:slug";

  const scoped = matchRoute(rest, WORKSPACE_ROUTES);
  if (scoped) return `/:slug/${scoped.join("/")}`;

  const section = rest[0]!;
  return WORKSPACE_SECTIONS.has(section)
    ? `/:slug/${section}/*`
    : "/:slug/*";
}

/**
 * 找出一条路径命中的路由模板。`:param` 槽位接受任意单个片段，其余槽位必须
 * 逐字匹配。多条模板都命中时，字面量最多者胜出，这样 `/agents/new` 会被识别
 * 成「创建页」而不是「一个碰巧叫 new 的 agent」。
 *
 * @param segments - 拆分好的路径片段
 * @param patterns - 待匹配的路由模板集合
 * @returns 命中且字面量最多的模板；没有命中返回 null
 */
function matchRoute(
  segments: readonly string[],
  patterns: readonly RoutePattern[],
): RoutePattern | null {
  let best: RoutePattern | null = null;
  let bestLiterals = -1;

  for (const pattern of patterns) {
    if (pattern.length !== segments.length) continue;

    let literals = 0;
    let matches = true;
    for (let i = 0; i < pattern.length; i += 1) {
      const slot = pattern[i]!;
      if (slot.startsWith(":")) continue;
      if (slot !== segments[i]) {
        matches = false;
        break;
      }
      literals += 1;
    }

    if (matches && literals > bestLiterals) {
      best = pattern;
      bestLiterals = literals;
    }
  }

  return best;
}
