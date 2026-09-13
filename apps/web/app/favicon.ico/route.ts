/**
 * 处理旧的 `/favicon.ico` 请求，把它们重定向到真正的 SVG favicon。
 *
 * 这里的 `Location` 故意用相对路径。`Response.redirect()` 只接受绝对 URL，
 * 这会强制基于 `request.url` 来解析——而在自托管部署里，`request.url` 携带的是服务器自己的监听地址，
 * 不是对外公开的域名。`Dockerfile.web` 设置了 `HOSTNAME=0.0.0.0` / `PORT=3000`，
 * 裸跑 `next start` 也默认如此，于是重定向会变成 `http://0.0.0.0:3000/favicon.svg`：
 * 对任何客户端都不可达，还泄露了内部绑定地址。相对 `Location` 符合 RFC 7231 §7.1.2，
 * 会基于客户端实际使用的 origin 解析，因此无需信任 `Host` / `X-Forwarded-Host`。
 */

/**
 * 让这个 handler 避开预渲染缓存路径。
 *
 * 过去读取 `request.url` 会隐式地把该路由标记为动态。去掉那个参数后，Next 检测不到任何动态因素，
 * 就把它当成可缓存路由，并尝试存储响应——而响应体是零字节，预渲染 LRU 直接拒绝：
 *
 *   Failed to update prerender cache for /favicon.ico
 *   Error: LRUCache: calculateSize returned 0, but size must be > 0.
 *
 * 每个浏览器都会主动请求 `/favicon.ico`，所以每个部署里每一次请求都会打一条这样的错误，
 * 而对应的缓存写入永远不可能成功。强制动态恢复了旧版 `request.url` 读取碰巧带来的「按请求处理」语义，
 * 又不会重新引入绝对 URL。别在「这个 handler 没参数」的清理里顺手删掉这行。
 */
export const dynamic = "force-dynamic";

/**
 * 处理 GET 请求。
 *
 * @returns 一个 308 永久重定向响应，把请求指向 `/favicon.svg`。
 */
export function GET() {
  return new Response(null, {
    status: 308,
    headers: { Location: "/favicon.svg" },
  });
}
