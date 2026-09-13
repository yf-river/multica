/** 在浏览器中打开一个 URL。对 SSR 安全、对异步调用方的弹窗拦截也安全。 */
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
