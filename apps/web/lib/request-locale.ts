/** 请求级 locale 解析：当前仅支持 zh-Hans，用 React cache 按请求缓存结果。 */
import { cache } from "react";
import type { SupportedLocale } from "@multica/core/i18n";

/**
 * 取当前请求的语言（服务端使用，同一请求内多次调用只算一次）。
 *
 * @returns 当前语言（zh-Hans）。
 */
export const getRequestLocale = cache(
  async (): Promise<SupportedLocale> => "zh-Hans",
);
