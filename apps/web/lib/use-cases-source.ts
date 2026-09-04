import { loader } from "fumadocs-core/source";
import { defineI18n } from "fumadocs-core/i18n";
import type { SupportedLocale } from "@multica/core/i18n";
import { useCases } from "@/.source";

// Use-case content uses Chinese dot-suffixed MDX files. The public route
// remains prefix-free.
export const i18n = defineI18n({
  languages: ["zh"],
  defaultLanguage: "zh",
  hideLocale: "default-locale",
  parser: "dot",
});

export type UseCaseLang = (typeof i18n.languages)[number];

export function getUseCaseLangForLocale(locale: SupportedLocale): UseCaseLang {
  void locale;
  return "zh";
}

export const useCasesSource = loader({
  baseUrl: "/usecases",
  source: useCases.toFumadocsSource(),
  i18n,
});

export function getUseCasePagesForLocale(locale: SupportedLocale) {
  return useCasesSource.getPages(getUseCaseLangForLocale(locale));
}

export function getUseCasePageForLocale(
  slugs: string[],
  locale: SupportedLocale,
) {
  return useCasesSource.getPage(slugs, getUseCaseLangForLocale(locale));
}
