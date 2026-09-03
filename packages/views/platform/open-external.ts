/** Open a URL in the browser. SSR-safe and popup-safe for async callers. */
export function openExternal(
  url: string,
  options?: { webTarget?: "new-tab" | "same-tab" },
): void {
  if (typeof window === "undefined") return;
  if (options?.webTarget === "same-tab") {
    window.location.assign(url);
    return;
  }
  window.open(url, "_blank", "noopener,noreferrer");
}
