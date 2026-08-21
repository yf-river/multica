/**
 * Open a URL in the user's default browser. SSR-safe: no-op if `window` is
 * not defined.
 */
export function openExternal(url: string): void {
  if (typeof window === "undefined") return;
  window.open(url, "_blank", "noopener,noreferrer");
}
