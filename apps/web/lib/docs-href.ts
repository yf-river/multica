/** 文档站链接工具：当前所有语言一律指向站内 /docs（由运行时 proxy 改写）。 */
import type { SupportedLocale } from "@multica/core/i18n";

/**
 * 按语言解析官方文档根链接。
 *
 * @param _locale - 目标语言（当前忽略，语言仅中文）。
 * @returns 文档站根路径。
 */
export function docsHrefForLocale(_locale: SupportedLocale): string {
  return "/docs";
}
