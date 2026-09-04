import {
  render,
  type RenderOptions,
  type RenderResult,
} from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { ReactElement, ReactNode } from "react";
import { RESOURCES } from "../locales";
import { TEST_EN_RESOURCES } from "./i18n-resources";

type RenderArgs = Omit<RenderOptions, "wrapper"> & {
  locale?: string;
};

export function renderWithI18n(
  ui: ReactElement,
  options: RenderArgs = {},
): RenderResult {
  const { locale = "en", ...rest } = options;
  function Wrapper({ children }: { children: ReactNode }) {
    return (
      <I18nProvider locale={locale} resources={{ ...RESOURCES, ...TEST_EN_RESOURCES }}>
        {children}
      </I18nProvider>
    );
  }
  return render(ui, { wrapper: Wrapper, ...rest });
}

export { RESOURCES };
