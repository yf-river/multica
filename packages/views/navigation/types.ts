export interface NavigationAdapter {
  push(path: string): void;
  replace(path: string): void;
  back(): void;
  pathname: string;
  searchParams: URLSearchParams;
  /**
   * Current fragment, including its leading `#`, or `""` when the location has
   * none. Part of "where am I" alongside `pathname` / `searchParams`: shared
   * views that rebuild the current URL (copy link, feedback) must keep a deep
   * link such as `#comment-…` intact. Compose the three with `currentPath()`
   * rather than concatenating them at each call site.
   */
  hash: string;
  /** Return an absolute, shareable URL for an application path. */
  getShareableUrl: (path: string) => string;
  /**
   * Optional: warm up route assets / RSC payload for a path. Callers must
   * invoke via `prefetch?.(href)`.
   */
  prefetch?: (path: string) => void;
  /**
   * Optional: is there an in-app page behind the current one, so that `back()`
   * lands somewhere inside Multica rather than stepping off the app? Only the
   * platform can answer. Adapters that cannot answer leave this undefined and
   * callers must treat that as `false`.
   *
   * Read it through `useBackOrReplace()` rather than calling it directly.
   */
  canGoBack?: () => boolean;
  /**
   * Optional: step forward through history, the inverse of `back()`. It remains
   * optional because not every adapter owns a forward stack. Callers must
   * invoke it via `forward?.()`.
   */
  forward?: () => void;
  /** Open an application path in a new desktop tab/window when supported. */
  openInNewTab?: (path: string, title?: string, options?: { activate?: boolean }) => void;
}
