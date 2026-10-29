/**
 * 运行时 URL 解析工具（供 next.config / proxy / 浏览器端共用）。
 *
 * 统一规则：API 基址一律按「后端 origin」清洗——不允许以 `/api` 结尾、只接受
 * http(s) URL——并在 dev 下提供指向本地后端/文档站的兜底地址。
 */
/** 环境变量字典：键 → 字符串值（可能缺失）。 */
type RuntimeEnv = Record<string, string | undefined>;

/** 去掉首尾空白与结尾 `/`；空值返回 undefined。 */
function cleanUrl(raw: string | undefined): string | undefined {
  const value = raw?.trim();
  if (!value) return undefined;
  return value.replace(/\/+$/, "");
}

/** 必须是合法 http(s) URL，否则返回 undefined。 */
function cleanHttpUrl(raw: string | undefined): string | undefined {
  const value = cleanUrl(raw);
  if (!value) return undefined;

  try {
    const url = new URL(value);
    if (url.protocol === "http:" || url.protocol === "https:") return value;
  } catch {
    return undefined;
  }

  return undefined;
}

// API 基址命名的是后端 ORIGIN，绝不能带 `/api` 端点：每个调用方都自带前缀
//（packages/core/api/client.ts 发 `/api/**`、头像解析 `/uploads/**`、实时连 `/ws`），
// 后端也在根路径统一服务这三类（server/cmd/server/router.go）。因此基址若以 `/api`
// 结尾，会拼出 `/api/api/**` 并让所有上传 404——这是最常见的自托管错误（#6619, MUL-5922）。
// 这里主动剥掉这一层后缀而非「尊重」它；其它路径前缀保留：反向代理完全可以合法地
// 把整个后端挂在 `https://host/multica` 之类的前缀下。
function stripApiPathSuffix(value: string): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    return value;
  }
  const pathname = url.pathname.replace(/\/+$/, "");
  if (!pathname.endsWith("/api")) return value;
  url.pathname = pathname.slice(0, -"/api".length);
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/+$/, "");
}

/** 清洗 API 基址：先做 http(s) 校验，再剥掉误配的 `/api` 后缀。 */
function cleanApiBaseUrl(raw: string | undefined): string | undefined {
  const value = cleanHttpUrl(raw);
  if (!value) return undefined;
  return stripApiPathSuffix(value);
}

/** 把相对/绝对 path 拼到基址后面（自动补 `/`）。 */
function appendPath(baseUrl: string, path: string): string {
  return `${baseUrl}${path.startsWith("/") ? path : `/${path}`}`;
}

/**
 * 解析远端后端地址：优先 REMOTE_API_URL，其次 NEXT_PUBLIC_API_URL。
 *
 * @param env - 环境变量字典。
 * @returns 清洗后的后端 origin，或 undefined。
 */
export function resolveRemoteApiUrl(env: RuntimeEnv): string | undefined {
  const explicitRemote = cleanApiBaseUrl(env.REMOTE_API_URL);
  if (explicitRemote) return explicitRemote;

  const publicApi = cleanApiBaseUrl(env.NEXT_PUBLIC_API_URL);
  if (publicApi) return publicApi;
  return undefined;
}

/** 解析文档站地址（DOCS_URL，http(s) 才接受）。 */
export function resolveDocsUrl(env: RuntimeEnv): string | undefined {
  return cleanHttpUrl(env.DOCS_URL);
}

// Dev-only 兜底：`next dev` 跑在开发者机器上，没配置时默认的 localhost
// 后端/文档端口（8080/4000）可以安全假设。构建与运行时 proxy 保持严格的解析器，
// 让预构建镜像永远不会去猜一个 origin（#4787）。
/**
 * dev 用的远端 API 地址：有配置用配置，否则回落到 localhost:8080（后端约定端口）。
 *
 * @param env - 环境变量字典。
 * @returns 一个始终可用的后端 origin。
 */
export function resolveDevRemoteApiUrl(env: RuntimeEnv): string {
  const configured = resolveRemoteApiUrl(env);
  if (configured) return configured;
  // Next 在求值 next.config.ts 之前会先把前端监听端口写进 process.env.PORT。
  // 若把这种通用变量当成后端端口，每次 dev 改写都会指回前端自己。只有后端专属的
  // 别名（BACKEND_PORT 等）在此进程里才是安全的兜底。
  const backendPort =
    env.BACKEND_PORT?.trim() ||
    env.API_PORT?.trim() ||
    env.SERVER_PORT?.trim() ||
    "8080";
  return `http://localhost:${backendPort}`;
}

/** dev 用的文档站地址：没配置就回落 localhost:4000。 */
export function resolveDevDocsUrl(env: RuntimeEnv): string {
  return resolveDocsUrl(env) ?? "http://localhost:4000";
}

// 与上面服务端解析器同样的严格程度。过去相对/非 http 值会被原样放行并成为 XHR 基址，
// 于是 `NEXT_PUBLIC_API_URL=/api` 产出 `/api/api/**` 与 `/api/uploads/**`（#6619）。
// 返回 undefined 让浏览器退回同源相对路径，这与「未设置」时的行为一致。
export function resolveBrowserApiBaseUrl(env: RuntimeEnv): string | undefined {
  return cleanApiBaseUrl(env.NEXT_PUBLIC_API_URL);
}

/** 解析浏览器端 WebSocket 地址：显式配置优先，否则从清洗后的 API 基址推导。 */
export function resolveBrowserWsUrl(env: RuntimeEnv): string | undefined {
  const explicit = cleanUrl(env.NEXT_PUBLIC_WS_URL);
  if (explicit) return explicit;

  const apiUrl = resolveBrowserApiBaseUrl(env);
  return apiUrl ? tryDeriveWsUrl(apiUrl) : undefined;
}

/**
 * 计算运行时（同源代理）对某条路径应改写到哪：文档站路径去 docs，其余后端路径
 * 去远端 API 基址；命中不到任何后端路径则返回 undefined（交给 Next.js 处理）。
 *
 * @param pathname - 请求路径。
 * @param env - 环境变量字典。
 * @returns 改写目标 URL，或 undefined。
 */
export function runtimeRewriteDestination(
  pathname: string,
  env: RuntimeEnv,
): string | undefined {
  const docsUrl = resolveDocsUrl(env);
  if (pathname === "/docs") {
    return docsUrl ? appendPath(docsUrl, "/docs") : undefined;
  }
  if (pathname.startsWith("/docs/")) {
    return docsUrl ? appendPath(docsUrl, pathname) : undefined;
  }

  if (pathname === "/api/version") return undefined;

  const remoteApiUrl = resolveRemoteApiUrl(env);
  if (!remoteApiUrl) return undefined;

  if (pathname === "/v1" || pathname.startsWith("/v1/")) {
    return appendPath(remoteApiUrl, pathname);
  }
  if (pathname === "/api" || pathname.startsWith("/api/")) {
    return appendPath(remoteApiUrl, pathname);
  }
  if (pathname === "/uploads" || pathname.startsWith("/uploads/")) {
    return appendPath(remoteApiUrl, pathname);
  }
  if (pathname === "/studio" || pathname.startsWith("/studio/")) {
    return appendPath(remoteApiUrl, pathname);
  }
  if (pathname === "/ws") {
    return appendPath(remoteApiUrl, "/ws");
  }
  // `multica setup self-host` 会探测 `{server-url}/health`，任何非 200 都视为
  // 「服务器不可达」。后端本来能服务它，但若同源反向代理把所有流量都转给 web
  // 镜像，探测会在 Next.js 路由层 404——于是对着健康的一整套栈做 setup 也会失败。
  // 因此像 /ws 一样把这个精确路径代理到后端。
  if (pathname === "/health") {
    return appendPath(remoteApiUrl, "/health");
  }
  if (isBackendAuthPath(pathname)) {
    return appendPath(remoteApiUrl, pathname);
  }

  return undefined;
}

/** 是否为需要代理到后端的认证路径（/auth/**），但 /auth/callback* 与 SSO 回调除外
 *  ——那些是 Next.js 自己处理的页面路由，不能代理。 */
function isBackendAuthPath(pathname: string): boolean {
  if (pathname === "/auth/callback") return false;
  if (pathname.startsWith("/auth/callback/")) return false;
  if (pathname === "/auth/hg-sso/callback") return false;
  if (pathname.startsWith("/auth/hg-sso/callback/")) return false;
  return pathname === "/auth" || pathname.startsWith("/auth/");
}

// `/ws` 是拼在 API 基址的 PATH 上，而不是它的 origin 上，这是刻意的：基址代表后端
// 挂在哪个前缀下，HTTP（`<base>/api/**`）与实时（`<base>/ws`）必须共用此前缀，
// 否则带前缀挂载的部署会「一边能用、另一边坏掉」。MUL-5922 那次回归——
// `NEXT_PUBLIC_API_URL=https://host/api` 推导出 `wss://host/api/ws`，而后端其实在
// 根路径服务 `/ws`——已在更上游的基址清洗里修好（见 stripApiPathSuffix），无需在此特判。
function tryDeriveWsUrl(apiUrl: string): string | undefined {
  let url: URL;
  try {
    url = new URL(apiUrl);
  } catch {
    return undefined;
  }
  if (url.protocol === "https:") url.protocol = "wss:";
  else if (url.protocol === "http:") url.protocol = "ws:";
  else return undefined;
  url.pathname = appendPath(url.pathname.replace(/\/+$/, ""), "/ws");
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/$/, "");
}
