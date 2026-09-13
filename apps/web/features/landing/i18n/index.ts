/** 官网 landing i18n 的统一出口：语言 Provider/钩子、字典类型与文档链接工具。 */

/** 语言 Provider 与 useLocale 钩子（见 context.tsx）。 */
export { LocaleProvider, useLocale } from "./context";
/** 文档链接按语言解析与语言判断工具（见 types.ts）。 */
export {
  docsHrefForLocale,
  isZhLocale,
} from "./types";
/** 字典与语言类型定义。 */
export type { LandingDict, Locale } from "./types";
