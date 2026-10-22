/**
 * 编辑器体系共用的链接处理工具。
 *
 * 使用者：content-editor（ProseMirror 点击处理器）、readonly-content
 * （react-markdown 链接组件）、link-hover-card（打开按钮）。
 */

import { isGlobalPath, isReservedSlug } from "@multica/core/paths";
import { isIssueIdentifier } from "@multica/ui/markdown";
import type { LinkClickIntent } from "../../navigation/click-intent";

/**
 * 顶层「工作区作用域」路由。用来识别那些写的时候没带工作区 slug 的
 * `/{route}/...` 路径——我们把当前 slug 拼到前面，让它在新的
 * /{slug}/{route}/... URL 结构下正确解析。
 *
 * 为什么用写死的白名单：这个启发式必须保守。像 "/acme/issues/abc"
 * 这种路径已经带 slug（第一段 "acme" 不是已知路由），不去动它是正确的。
 * 像 "/foo/bar" 这种第一段 "foo" 不是已知路由的路径是模糊的——我们不改写，
 * 认为作者是故意的。只有 "/issues/..." 这类路径才会被自动加前缀。
 */
const WORKSPACE_ROUTE_SEGMENTS = new Set([
  "usage",
  "issues",
  "projects",
  "autopilots",
  "agents",
  "squads",
  "chat",
  "inbox",
  "my-issues",
  "run-reviews",
  "runtimes",
  "skills",
  "settings",
  "mcp",
  "quick-actions",
  "upstream",
  "life",
  "cooking",
  "health",
  "meals",
  "goals",
  "companion",
]);

/**
 * 判断路径是否为「工作区作用域」的应用页面——`/{slug}/...` 形态，即第一段
 * 是某个工作区确实可能拥有的 slug。
 *
 * 应用源上还提供一些应用路由不管的东西：`/api/*`、`/uploads/*`（本地存储
 * 附件，由 web 代理）、`/_next/*`、`/favicon.ico`，以及前工作区路由。这些
 * 的第一段全都已经在保留 slug 列表里，所以用保留列表——也就是后端建
 * 工作区时强制的那一份——来回答这个问题，就不需要再维护一份必须和后端
 * 路由同步的独立黑名单了。
 */
function isWorkspaceScopedPath(pathname: string): boolean {
  const first = pathname.split("/")[1] ?? "";
  if (!first) return false;
  let segment: string;
  try {
    segment = decodeURIComponent(first);
  } catch {
    return false;
  }
  return !isReservedSlug(segment.toLowerCase());
}

/**
 * 把指向「本部署应用内工作区页面」的绝对 URL 换算成它在应用里的路径；
 * 其它一律返回 null。
 *
 * agent 或用户粘贴 `https://<app-host>/acme/issues/123` 与 `/acme/issues/123`
 * 是同一个目的地。不处理的话，即使 URL 属于当前部署也会被当成外部链接
 * （MUL-5208）。
 *
 * `appOrigin` 是部署的对外应用 URL，只有平台层知道。参见 `useAppOrigin()`。
 */
export function toInternalAppPath(
  href: string,
  appOrigin?: string | null,
): string | null {
  if (!appOrigin) return null;
  let target: URL;
  let app: URL;
  try {
    target = new URL(href);
    app = new URL(appOrigin);
  } catch {
    return null;
  }
  if (target.origin !== app.origin) return null;
  // 不透明源（file:、data:）之间比较是相等的；只有真实 web 源才能标识应用。
  if (target.protocol !== "http:" && target.protocol !== "https:") return null;
  if (!isWorkspaceScopedPath(target.pathname)) return null;
  return `${target.pathname}${target.search}${target.hash}`;
}

/** 链接指向的应用内实体页——有专属「小卡片」的两种。 */
export interface WorkspaceEntityRef {
  kind: "issue" | "project";
  /**
   * 实体 id，从路径解码而来。两种实体都是 UUID；issue 额外支持裸标识
   * （`MUL-123`）。调用方用 `isIssueIdentifier` 按形态分发：标识符在渲染成
   * 卡片前仍需先解析成真实 issue。
   */
  id: string;
  /**
   * 链接指明的工作区 slug；不带 slug 的旧形态（`/projects/<uuid>`）为
   * `null`，由 `openLink` 用当前工作区解析。凡是渲染工作区作用域数据的
   * 调用方，都必须拿非 null 的 slug 与当前工作区比对——实体本身只能在
   * 拥有它的工作区内被解析。
   */
  slug: string | null;
}

/** 路由段 → 实体类型。识别 `/issues/...` 与 `/projects/...` 两条路线。 */
const ENTITY_ROUTE_SEGMENTS: Record<string, WorkspaceEntityRef["kind"]> = {
  issues: "issue",
  projects: "project",
};

const UUID_RE =
  /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

/**
 * 该路径段是否指向 `kind` 类型的单个实体？
 *
 * project 只能用 UUID 寻址——它没有简写。issue 两种都有，其中标识符形态
 * 最重要：`copyLink` 和 issue 链接动作都拼 `paths.issueDetail(identifier || id)`，
 * issue 路由还会把 UUID URL 重写成标识符，所以用户从应用里/地址栏复制出来
 * 的往往是 `MUL-123`。这里只收 UUID 的话，人们真正会粘贴的那种形态反而
 * 会变成裸 URL。
 *
 * 长得像标识符的 id 只是候选而非命中：调用方拿它去当前工作区解析，
 * 解析不到就保留普通链接。
 */
function isEntityId(kind: WorkspaceEntityRef["kind"], id: string): boolean {
  return UUID_RE.test(id) || (kind === "issue" && isIssueIdentifier(id));
}

/** 安全地解码 URL 段，失败返回 null。 */
function decodeSegment(segment: string): string | null {
  try {
    return decodeURIComponent(segment);
  } catch {
    return null;
  }
}

/**
 * 解析不带自身 origin 的 href 时用的替身基准源。`.invalid` 顶级域被保留为
 * 永久不可解析（RFC 2606），所以不会有任何部署用它提供内容，任何 href 也
 * 无法借它冒充本应用。
 */
const RELATIVE_BASE = "https://link.invalid";

/**
 * 把链接解析成本部署内路径；指向别处一律返回 `null`。
 *
 * 用 `URL` 解析而不是测字符串前缀，答案才可信。开头带 `/` 不代表
 * “本站”：`//other.example/x` 与 `/\other.example/x` 都以它开头，但指的
 * 都是别的主机——那正是浏览器会去的地方。解析器能看到主机，前缀测试
 * 看不到，再加多少前缀也补不上这个洞——这就是这个检查的形态比它当前
 * 拒绝的用例更重要的原因。
 *
 * 没有 `appOrigin`（平台报不出）时，只有真正相对路径能解析成功：任何自带
 * 主机的链接都会在比对中落败，这是安全方向。
 */
function toSameOriginPath(
  href: string,
  appOrigin?: string | null,
): string | null {
  const base = appOrigin || RELATIVE_BASE;
  let target: URL;
  let expected: URL;
  try {
    target = new URL(href, base);
    expected = new URL(base);
  } catch {
    return null;
  }
  // 不透明源（`javascript:`、`data:`）字符串化后是 "null"，永远不可能
  // 等于真实源，所以它们也在这里被挡下。
  if (target.origin !== expected.origin) return null;
  return `${target.pathname}${target.search}${target.hash}`;
}

/**
 * 解析一个恰好指向本部署内某个 issue 或 project 实体页的链接；其它一律
 * 返回 `null`——外部 URL、列表页、更深的路由，以及带查询串或片段的链接。
 *
 * 同时接受站内相对路径与指向本部署应用源的绝对 URL。两者走同一个同源
 * 比较，所以两种形态不会对「什么算站内」产生分歧。
 */
export function parseWorkspaceEntityLink(
  href: string,
  appOrigin?: string | null,
): WorkspaceEntityRef | null {
  const path = toSameOriginPath(href, appOrigin);
  if (!path) return null;
  // 查询串或片段指向的是比实体页更具体的东西（保存的筛选、锚定的评论）。
  // 把它们折叠成纯实体引用等于静默丢弃这些信息。
  if (path.includes("?") || path.includes("#")) return null;

  const segments: string[] = [];
  for (const raw of path.split("/").filter(Boolean)) {
    const decoded = decodeSegment(raw);
    if (decoded === null) return null;
    segments.push(decoded);
  }

  // `/{slug}/{route}/{id}` ——应用内「复制链接」产生的标准形态。
  // `/{route}/{id}` ——不带 slug 的旧内容，按定义属于当前工作区，
  // 因为 `openLink` 会给它拼上当前 slug。
  let slug: string | null;
  let route: string | undefined;
  let id: string | undefined;
  if (segments.length === 3) {
    const [first, second, third] = segments;
    if (!first || isReservedSlug(first.toLowerCase())) return null;
    slug = first;
    route = second;
    id = third;
  } else if (segments.length === 2) {
    slug = null;
    [route, id] = segments;
  } else {
    return null;
  }

  const kind = route ? ENTITY_ROUTE_SEGMENTS[route] : undefined;
  if (!kind || !id || !isEntityId(kind, id)) return null;
  return { kind, id, slug };
}

/**
 * 打开链接——站内路径派发 multica:navigate，外部链接新开标签页。
 *
 * 提供了 `currentSlug` 且 `href` 是缺 slug 的工作区作用域路径时（比如
 * "/issues/abc" 而不是 "/{slug}/issues/abc"），会拼上前缀。这是给 URL
 * 重构之前创作的旧 markdown 内容准备的，也兼容未来用户粘贴时忘带 slug。
 *
 * `appOrigin` 让指向本部署的绝对 URL 与相对路径走同一条站内路由。
 *
 * `intent` 是用户的点击方式（见 `resolveClickIntent`）；应答
 * `multica:navigate` 的平台监听器负责执行——"push" 就地导航，其它新开
 * 标签页。外部链接忽略它：一律交给浏览器/系统浏览器。
 */
export function openLink(
  href: string,
  currentSlug?: string | null,
  appOrigin?: string | null,
  intent: LinkClickIntent = "push",
): void {
  const internalPath = href.startsWith("/")
    ? href
    : toInternalAppPath(href, appOrigin);
  if (internalPath) {
    let path = internalPath;
    if (currentSlug && !isGlobalPath(path)) {
      const firstSegment = path.split("/")[1];
      if (firstSegment && WORKSPACE_ROUTE_SEGMENTS.has(firstSegment)) {
        // 路径形如 /issues/abc（没带 slug）——拼上当前 slug。
        path = `/${currentSlug}${path}`;
      }
      // 否则第一段要么已经是 slug（"/acme/issues" 里的 "acme"），要么是
      // 未知的东西（如 "/foo"）。保持原样——用户写下的就是他们想去的。
    }
    window.dispatchEvent(
      new CustomEvent("multica:navigate", {
        detail: { path, disposition: intent },
      }),
    );
  } else {
    window.open(href, "_blank", "noopener,noreferrer");
  }
}

/** 判断 href 是否为 mention 协议链接（不应当普通链接打开）。 */
export function isMentionHref(href: string | null | undefined): href is string {
  return !!href && href.startsWith("mention://");
}
