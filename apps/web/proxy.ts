import { environmentCookieName } from "@multica/core/platform/environment";
/**
 * Next.js 运行时 proxy（中间件）：请求进入时做三件事——
 * 1) 按 runtime-urls 把后端路径（/api、/uploads、/ws、/docs 等）改写到真实上游；
 * 2) 把旧的、不带 workspace slug 的旧路由（/issues…）重定向到 /{slug}/…；
 * 3) 对已登录用户把根路径 `/` 重定向到其最近工作区（官方营销域名除外）。
 */
import { NextResponse, type NextRequest } from "next/server";
import { runtimeRewriteDestination } from "./config/runtime-urls";
import { isOfficialMarketingHost } from "./lib/public-host";

// URL 重构（pre-#1131）之前存在过的旧工作区路由段。任何以这些作为「第一段」的
// URL 都是旧链接：要改写成 /{slug}/{route}/…，否则旧书签、深链以及回滚重放过的
// 用户会 404。
const LEGACY_ROUTE_SEGMENTS = new Set([
  "issues",
  "projects",
  "agents",
  "squads",
  "inbox",
  "my-issues",
  "autopilots",
  "runtimes",
  "skills",
  "settings",
  "usage",
]);

// Next.js 16 把 `middleware` 改名成 `proxy`。API 面（NextRequest / NextResponse /
// cookies / matcher）完全一致，唯一的行为差异是运行时：proxy 强制走 nodejs，
// 不能再选择 edge。
/**
 * 请求代理入口。
 *
 * @param req - 收到的请求。
 * @returns 改写 / 重定向 / 原样放行的响应。
 */
export function proxy(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const runtimeDestination = runtimeRewriteDestination(pathname, process.env);
  if (runtimeDestination) {
    const url = new URL(runtimeDestination);
    url.search = req.nextUrl.search;
    return NextResponse.rewrite(url);
  }

  const hasSession = req.cookies.has(environmentCookieName("multica_logged_in", process.env.MULTICA_ENV_ID));
  const lastSlug = req.cookies.get(environmentCookieName("last_workspace_slug", process.env.MULTICA_ENV_ID))?.value;

  // --- 旧 URL 重定向：/issues/... → /{slug}/issues/... ---
  // 迁移到 slug 体系之前打进来的旧书签/客户端，由于路由挪到了 [workspaceSlug] 之下，
  // 不改写就会 404。
  const firstSegment = pathname.split("/")[1] ?? "";
  if (LEGACY_ROUTE_SEGMENTS.has(firstSegment)) {
    const url = req.nextUrl.clone();

    if (!hasSession) {
      url.pathname = "/login";
      return NextResponse.redirect(url);
    }

    if (lastSlug) {
      // 保留深链路径与查询串：/issues/abc → /{lastSlug}/issues/abc
      url.pathname = `/${lastSlug}${pathname}`;
      return NextResponse.redirect(url);
    }

    // 已登录但没有 cookie（从未打开过工作区，或 cookie 被清过）。根路径不是正确的
    // 去处：下面的根路径规则即使在有会话时也会把 `/` 留给官方营销域名做公开站，
    // 弹过去只会死循环式落在落地页而非进入应用。/login 自己会拿着 workspace 列表
    // 解析已登录访客——包括待处理邀请和「还没有 workspace」的情况——再 replace 到
    // 正确目的地。深链路径与查询串被丢弃而不作为 `next` 传递：它们本身是旧段，
    // 传回去又会在旧路由逻辑里绕一圈。
    url.pathname = "/login";
    url.search = "";
    return NextResponse.redirect(url);
  }

  // --- 根路径：把已登录用户重定向到其最近工作区 ---
  // 官方云主机还同时服务公开营销站。访问 https://multica.ai/ 即便本地桌面/运行时
  // 会话带着新鲜 cookie，也必须保持是公开站导航；像 /acme/issues 这样的显式应用
  // 路由以及旧 /issues 仍路由到工作区应用。
  if (
    pathname === "/" &&
    hasSession &&
    lastSlug &&
    !isOfficialMarketingHost(req.nextUrl.hostname)
  ) {
    const url = req.nextUrl.clone();
    url.pathname = `/${lastSlug}/issues`;
    return NextResponse.redirect(url);
  }

  return NextResponse.next();
}

/** 该 proxy 匹配哪些路径（与运行时改写/重定向目标一致，其余路径直接放行）。 */
export const config = {
  matcher: [
    "/v1/:path*",
    "/api/:path*",
    "/auth/:path*",
    "/uploads/:path*",
    "/docs/:path*",
    "/ws",
    "/((?!api|v1|_next/static|_next/image|favicon.ico|.*\\.).*)",
  ],
};
