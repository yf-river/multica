import { defineI18n } from "fumadocs-core/i18n";

export const i18n = defineI18n({
  languages: ["zh"],
  defaultLanguage: "zh",
  hideLocale: "default-locale",
  parser: "dot",
});

export type Lang = (typeof i18n.languages)[number];
