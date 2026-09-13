/**
 * 客户案例内容的源码接入层：用 Fumadocs 从 @/.source 装载案例 MDX，
 * 只维护一套中文文案（dot 后缀的语言文件），并按请求 locale 提供查询入口。
 */
import { loader } from "fumadocs-core/source";
import { defineI18n } from "fumadocs-core/i18n";
import type { SupportedLocale } from "@multica/core/i18n";
import { useCases } from "@/.source";

// 案例内容使用「中文 + 点后缀」的 MDX 文件组织（*.zh.md）；公开路由仍不带语言前缀。
export const i18n = defineI18n({
  languages: ["zh"],
  defaultLanguage: "zh",
  hideLocale: "default-locale",
  parser: "dot",
});

/** 案例内容支持的语言（Fumadocs 的 i18n 分组键）。 */
export type UseCaseLang = (typeof i18n.languages)[number];

/**
 * 把请求 locale 映射到案例内容语言。
 *
 * @param locale - 请求 locale。
 * @returns 内容语言（当前恒为 zh）。
 */
export function getUseCaseLangForLocale(locale: SupportedLocale): UseCaseLang {
  void locale;
  return "zh";
}

/** Fumadocs 装载器：baseUrl 为公开路由前缀 /usecases。 */
export const useCasesSource = loader({
  baseUrl: "/usecases",
  source: useCases.toFumadocsSource(),
  i18n,
});

/** 按 locale 取全部案例页（用于列表页）。 */
export function getUseCasePagesForLocale(locale: SupportedLocale) {
  return useCasesSource.getPages(getUseCaseLangForLocale(locale));
}

/** 按 slug 路径 + locale 取单个案例页（用于详情页，找不到返回 undefined）。 */
export function getUseCasePageForLocale(
  slugs: string[],
  locale: SupportedLocale,
) {
  return useCasesSource.getPage(slugs, getUseCaseLangForLocale(locale));
}
