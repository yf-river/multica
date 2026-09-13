// 本文件提供把 i18next 实例挂进 React 组件树的 Provider。
// 用 useState 惰性创建，保证实例在组件生命周期内只建一次。

"use client";

import { useState, type ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import { createI18n } from "./create-i18n";
import type { LocaleResources } from "./types";

/** I18nProvider 的 props。 */
interface I18nProviderProps {
  /** 当前语言，例如 "zh-Hans"。 */
  locale: string;
  /** 语言 → 语言包资源的映射。 */
  resources: Record<string, LocaleResources>;
  /** 需要访问翻译能力的子树。 */
  children: ReactNode;
}

/**
 * 把 i18next 实例通过 react-i18next 的 Provider 提供给整棵子树。
 *
 * @param props - 语言、语言包与子组件
 */
export function I18nProvider({
  locale,
  resources,
  children,
}: I18nProviderProps) {
  const [instance] = useState(() => createI18n(locale, resources));
  return <I18nextProvider i18n={instance}>{children}</I18nextProvider>;
}
