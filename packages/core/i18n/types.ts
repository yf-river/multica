// 本文件提供 i18n 的基础类型：当前唯一支持的语言是简体中文（zh-Hans）。

/** 支持的语言列表——目前只有简体中文。 */
export type SupportedLocale = "zh-Hans";

/** 默认语言：简体中文。 */
export const DEFAULT_LOCALE: SupportedLocale = "zh-Hans";

/** 一份语言包的资源结构：命名空间 → key → 文案。 */
export type LocaleResources = Record<string, Record<string, unknown>>;
