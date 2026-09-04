"use client";

import { createContext, use, useMemo } from "react";
import { useConfigStore } from "@multica/core/config";
import { createZhDict } from "./zh";
import type { LandingDict, Locale } from "./types";

type LocaleContextValue = {
  locale: Locale;
  t: LandingDict;
};

const LocaleContext = createContext<LocaleContextValue | null>(null);

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

export function useLocale() {
  const ctx = use(LocaleContext);
  if (!ctx) throw new Error("useLocale must be used within LocaleProvider");
  return ctx;
}
