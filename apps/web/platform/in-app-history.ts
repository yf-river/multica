/**
 * 「这一页后面还有 Multica 页面吗？」——给 Web 导航适配器判断能否后退。
 *
 * 需要后退的调用方（如删除 issue 后要回到打开它的列表）在当前页是「冷打开」
 * （分享链接、粘贴 URL、新标签页）时，不能退着退着退出应用。
 *
 * 只有浏览器能回答这个问题，而恰好有一个 API：Navigation API 的 entries() 只覆盖
 * 当前条目前后「同源且连续」的一段，所以从站外来时即便浏览器技术上还有地方可去，
 * canGoBack 也会是 false。history.length 不能替代它——那会把其它源的条目也算进去。
 *
 * 这里没有别的猜测。给 adapter 自己的 router.push 次数计数看起来可行，其实不行：
 * push 是一个「请求」而不是已提交的历史条目。当 canonical URL 未变化时 Next 会把它
 * 降级成 replaceState（app-router.js 里 `pendingPush && href !== canonicalUrl` 分支），
 * push 也可能在过渡中被覆盖或放弃。任何由「调用次数」推出来的计数都可能声称存在并不
 * 存在的历史——一个错误的 true 会把用户带出 Multica，这正是本模块要防的失败。
 * 因此浏览器答不上来时返回 false，由调用方走兜底路径。
 */

/** Navigation API 的最小形态——TypeScript 的 DOM lib 还没有它。 */
type NavigationApi = { canGoBack: boolean };

/**
 * 「后退一步是否仍在应用内」。浏览器没有 Navigation API 时一律 false——
 * 此时调用方导航到它的兜底地址，行为与这些浏览器在没有这套机制之前一致。
 *
 * @returns 应用内是否有可退的历史条目。
 */
export function canGoBackInApp(): boolean {
  if (typeof window === "undefined") return false;
  const navigation = (window as { navigation?: unknown }).navigation as
    | NavigationApi
    | undefined;
  return navigation?.canGoBack === true;
}
