/**
 * navigation 模块：跨平台（Web / 桌面）导航适配器的接口定义。
 * 不同的运行平台各自实现这套接口，再注入 NavigationProvider；共享视图只与
 * 该抽象打交道，从而在不感知底层路由库的前提下跳转、读当前地址、取可分享
 * 链接等。
 */

/** 平台导航能力的抽象接口。 */
export interface NavigationAdapter {
  push(path: string): void;
  replace(path: string): void;
  back(): void;
  pathname: string;
  searchParams: URLSearchParams;
  /**
   * 当前 URL fragment（含开头的 `#`），没有则为 `""`。它与 `pathname` /
   * `searchParams` 一起构成「我在哪」：那些要重建当前 URL 的共享视图（复制
   * 链接、反馈）必须保留像 `#comment-…` 这样的深链。请用 `currentPath()`
   * 组合三者，而不要在每个调用处自己拼接。
   */
  hash: string;
  /** 为某个应用路径返回绝对、可分享的 URL。 */
  getShareableUrl: (path: string) => string;
  /**
   * 可选：为某个路径预热路由资源 / RSC payload。调用方必须通过
   * `prefetch?.(href)` 触发。
   */
  prefetch?: (path: string) => void;
  /**
   * 可选：当前地址后面是否还有一个应用内页面——让 `back()` 落在 Multica
   * 内部而不是一步走出应用？只有平台自己答得了。无法回答的适配器就留空，
   * 调用方须把空当成 `false`。
   *
   * 请通过 `useBackOrReplace()` 读取，而不是直接调用。
   */
  canGoBack?: () => boolean;
  /**
   * 可选：沿历史前进一步，`back()` 的逆操作。保持可选是因为并非每个适配器
   * 都持有前向栈。调用方必须通过 `forward?.()` 触发。
   */
  forward?: () => void;
  /** 受支持时，在一个新的桌面标签页/窗口打开应用路径。 */
  openInNewTab?: (path: string, title?: string, options?: { activate?: boolean }) => void;
}
