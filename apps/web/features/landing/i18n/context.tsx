"use client";

/**
 * 官网 landing 的轻量 i18n Provider：当前只提供一套 zh-Hans 文案，
 * 通过 React context 把字典与语言下发给所有营销组件，供 useLocale() 消费。
 */
import { createContext, use, useMemo } from "react";
import { useConfigStore } from "@multica/core/config";
import { createZhDict } from "./zh";
import type { LandingDict, Locale } from "./types";

/** context 值：当前语言 + 对应字典。 */
type LocaleContextValue = {
  locale: Locale;
  t: LandingDict;
};

const LocaleContext = createContext<LocaleContextValue | null>(null);

/**
 * 语言上下文 Provider。
 *
 * @param props.children - 子组件树。
 * @param props.initialLocale - 保留的初始语言参数；目前仅支持 zh-Hans，
 *   取字典时若允许注册开关（allowSignup）变化会重新生成 CTA 文案。
 * @returns 提供 locale + t 的上下文。
 */
export function LocaleProvider({
  children,
  initialLocale: _initialLocale,
}: {
  children: React.ReactNode;
  initialLocale?: Locale;
}) {
  const locale: Locale = "zh-Hans";
  const allowSignup = useConfigStore((state) => state.allowSignup);
  const t = useMemo(() => createZhDict(allowSignup), [allowSignup]);

  return (
    <LocaleContext.Provider value={{ locale, t }}>
      {children}
    </LocaleContext.Provider>
  );
}

/**
 * 读取当前语言字典（在 LocaleProvider 内调用）。
 *
 * @returns {locale, t}：当前语言与文案字典。
 */
export function useLocale() {
  const ctx = use(LocaleContext);
  if (!ctx) throw new Error("useLocale must be used within LocaleProvider");
  return ctx;
}
