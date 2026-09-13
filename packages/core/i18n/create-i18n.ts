// 本文件负责创建 i18next 实例并做同步初始化。

import i18next, { type i18n as I18n } from "i18next";
import { initReactI18next } from "react-i18next";
import type { LocaleResources } from "./types";

// 服务端（RSC）和客户端都必须用「相同的 locale + 资源」调用本函数，否则会
// 出现 hydration 不一致。`initAsync: false` 强制同步初始化（i18next v25+ 里
// 由 `initImmediate` 改名而来）；`useSuspense: false` 避免 hydration 期间的
// 兜底渲染。
/**
 * 创建一个已用指定语言与资源初始化好的 i18next 实例。
 *
 * @param locale - 当前语言
 * @param resources - 语言 → 语言包资源的映射
 * @returns 初始化完成的 i18next 实例
 */
export function createI18n(
  locale: string,
  resources: Record<string, LocaleResources>,
): I18n {
  const instance = i18next.createInstance();
  instance.use(initReactI18next).init({
    lng: locale,
    fallbackLng: "zh-Hans",
    resources,
    interpolation: { escapeValue: false },
    initAsync: false,
    react: { useSuspense: false },
  });
  return instance;
}
