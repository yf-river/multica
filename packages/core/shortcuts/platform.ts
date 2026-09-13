// 本文件负责识别宿主操作系统平台（macOS / Windows / Linux），供快捷键的
// 按键语义与展示使用。

export type ShortcutPlatform = "macos" | "windows" | "linux" | "unknown";

let configuredPlatform: ShortcutPlatform | null = null;

/** 把 UA 字符串片段归一成平台枚举。 */
function normalizePlatform(value: string): ShortcutPlatform {
  const platform = value.toLowerCase();
  if (platform.includes("mac") || platform.includes("darwin")) return "macos";
  if (platform.includes("win")) return "windows";
  if (platform.includes("linux") || platform.includes("x11")) return "linux";
  return "unknown";
}

/** 探测浏览器暴露出来的宿主操作系统。 */
export function detectShortcutPlatform(): ShortcutPlatform {
  if (typeof navigator === "undefined") return "unknown";
  const nav = navigator as Navigator & {
    userAgentData?: { platform?: string };
  };
  // 有些隐私加固的浏览器把 `platform` 暴露成空字符串。若用空值合并运算符就会
  // 停在那里、丢掉仍然有用的 userAgent，导致 Mac 上的 Command 被当成
  // Windows/Super。
  const signals = [nav.userAgentData?.platform, nav.platform, nav.userAgent];
  for (const signal of signals) {
    if (!signal?.trim()) continue;
    const platform = normalizePlatform(signal);
    if (platform !== "unknown") return platform;
  }
  return "unknown";
}

/** 显式配置平台（例如桌面端宿主告诉内核它在 macOS 上跑）。 */
export function configureShortcutPlatform(
  platform: ShortcutPlatform | null | undefined,
): void {
  configuredPlatform = platform ?? null;
}

/** 取当前平台：优先用显式配置，否则回退到浏览器探测。 */
export function getShortcutPlatform(): ShortcutPlatform {
  return configuredPlatform ?? detectShortcutPlatform();
}
