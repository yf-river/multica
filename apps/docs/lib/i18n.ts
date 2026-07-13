import { defineI18n } from "fumadocs-core/i18n";

// Docs are Chinese-only. `hideLocale: "default-locale"` keeps public URLs
// prefix-free while the source loader reads `page.zh.mdx` / `meta.zh.json`.
export const i18n = defineI18n({
  languages: ["zh"],
  defaultLanguage: "zh",
  hideLocale: "default-locale",
  parser: "dot",
});

export type Lang = (typeof i18n.languages)[number];

export function isDocsLang(lang: string): lang is Lang {
  return (i18n.languages as readonly string[]).includes(lang);
}

export function resolveDocsLang(lang: string): Lang {
  return isDocsLang(lang) ? lang : i18n.defaultLanguage;
}
