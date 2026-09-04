import { NextResponse, type NextRequest } from "next/server";
import { runtimeRewriteDestination } from "./config/runtime-urls";
import { isOfficialMarketingHost } from "./lib/public-host";

// Old workspace-scoped route segments that existed before the URL refactor
// (pre-#1131). Any URL with these as the FIRST segment is a legacy URL that
// needs to be rewritten to /{slug}/{route}/... so old bookmarks, deep links,
// and post-revert-and-reapply users don't hit 404.
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

// Next.js 16 renamed `middleware` → `proxy`. API surface (NextRequest /
// NextResponse / cookies / matcher) is identical; the only behavioral
// change is the runtime — proxy is forced to nodejs and cannot opt into
// edge.
export function proxy(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const runtimeDestination = runtimeRewriteDestination(pathname, process.env);
  if (runtimeDestination) {
    const url = new URL(runtimeDestination);
    url.search = req.nextUrl.search;
    return NextResponse.rewrite(url);
  }

  const hasSession = req.cookies.has("multica_logged_in");
  const lastSlug = req.cookies.get("last_workspace_slug")?.value;

  // --- Legacy URL redirect: /issues/... → /{slug}/issues/... ---
  // Old bookmarks and clients that hit us before the slug migration would
  // otherwise 404 since the route moved under [workspaceSlug].
  const firstSegment = pathname.split("/")[1] ?? "";
  if (LEGACY_ROUTE_SEGMENTS.has(firstSegment)) {
    const url = req.nextUrl.clone();

    if (!hasSession) {
      url.pathname = "/login";
      return NextResponse.redirect(url);
    }

    if (lastSlug) {
      // Preserve deep-link path + query: /issues/abc → /{lastSlug}/issues/abc
      url.pathname = `/${lastSlug}${pathname}`;
      return NextResponse.redirect(url);
    }

    // Logged-in but no cookie yet (never opened a workspace, or the cookie was
    // cleared). Root is the wrong destination: the root-path rule below leaves
    // `/` on the public site for the official marketing hosts even with a
    // session, so bouncing there dead-ends on the landing page instead of
    // reaching the app. /login already resolves an authenticated visitor
    // against their workspace list — including pending invitations and the
    // no-workspace-yet case — and replaces to the right destination. Deep-link
    // path and query are dropped rather than passed as `next`: they are legacy
    // segments themselves, so feeding one back would land here again.
    url.pathname = "/login";
    url.search = "";
    return NextResponse.redirect(url);
  }

  // --- Root path: redirect logged-in users to their last workspace ---
  // The official cloud host also serves the public marketing site. Visiting
  // https://multica.ai/ must remain a public-site navigation even when a local
  // desktop/runtime session has fresh auth cookies; explicit app routes such
  // as /acme/issues and legacy /issues still route to the workspace app.
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
