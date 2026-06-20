import { loader } from "fumadocs-core/source";
import { defineI18n } from "fumadocs-core/i18n";
import type { SupportedLocale } from "@multica/core/i18n";
import { useCases } from "@/.source";

// Use-case content is Chinese-only after the locale-system cleanup.
export const i18n = defineI18n({
  languages: ["zh"],
  defaultLanguage: "zh",
  hideLocale: "default-locale",
  parser: "dot",
});

export type UseCaseLang = (typeof i18n.languages)[number];

export function getUseCaseLangForLocale(_locale: SupportedLocale): UseCaseLang {
  return "zh";
}

export const useCasesSource = loader({
  baseUrl: "/usecases",
  source: useCases.toFumadocsSource(),
  i18n,
});

export function getUseCasePagesForLocale(locale: SupportedLocale) {
  const lang = getUseCaseLangForLocale(locale);
  return useCasesSource.getPages(lang);
}

export function getUseCasePageForLocale(
  slugs: string[],
  locale: SupportedLocale,
) {
  const lang = getUseCaseLangForLocale(locale);
  return useCasesSource.getPage(slugs, lang);
}
