import {
  render,
  type RenderOptions,
  type RenderResult,
} from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ReactElement, ReactNode } from "react";
import { RESOURCES } from "../locales";
import type { SupportedLocale } from "@multica/core/i18n";

type RenderArgs = Omit<RenderOptions, "wrapper"> & {
  locale?: SupportedLocale;
};

export function TestI18nProvider({
  children,
  locale = "zh-Hans",
}: {
  children: ReactNode;
  locale?: SupportedLocale;
}) {
  return (
    <I18nProvider locale={locale} resources={RESOURCES}>
      {children}
    </I18nProvider>
  );
}

export function renderWithI18n(
  ui: ReactElement,
  options: RenderArgs = {},
): RenderResult {
  const { locale = "zh-Hans", ...rest } = options;
  function Wrapper({ children }: { children: ReactNode }) {
    return <TestI18nProvider locale={locale}>{children}</TestI18nProvider>;
  }
  return render(ui, { wrapper: Wrapper, ...rest });
}
