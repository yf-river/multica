/** 客户端操作系统粗分类（不保留原始 UA）。 */
export type CoarseClientOS =
  | "macos"
  | "windows"
  | "linux"
  | "ios"
  | "android"
  | "chromeos"
  | "unknown";

/**
 * 把浏览器给出的平台提示映射成粗粒度 OS 桶，不保留原始 user agent。
 *
 * @param nav - 可选的 navigator 子集（默认取全局 navigator，SSR 时为 undefined）。
 * @returns 命中的 OS 分类。
 */
export function detectWebOS(
  nav: Pick<Navigator, "platform" | "userAgent"> | undefined =
    typeof navigator === "undefined" ? undefined : navigator,
): CoarseClientOS {
  if (!nav) return "unknown";
  const hint = `${nav.platform} ${nav.userAgent}`.toLowerCase();
  if (/iphone|ipad|ipod/.test(hint)) return "ios";
  if (hint.includes("android")) return "android";
  if (hint.includes("cros")) return "chromeos";
  if (/mac|darwin/.test(hint)) return "macos";
  if (/win/.test(hint)) return "windows";
  if (/linux|x11/.test(hint)) return "linux";
  return "unknown";
}
