/**
 * 客户案例（usecases）相关页面所需的 i18n 静态文案与语言工具。
 *
 * 目前仅提供 zh-Hans 文案；同时把「按请求取 locale」与「按 locale 取文档链接」
 * 两个能力从各自模块统一转发出来，方便 case 页面一处引用。
 */

/** 复用 docs 文档站地址解析：按语言返回对应的官方文档根链接。 */
export { docsHrefForLocale } from "@/lib/docs-href";
import { getRequestLocale } from "@/lib/request-locale";

/** 解析当前请求的语言环境（服务端用）。 */
export const getUseCaseLocale = getRequestLocale;

/** 案例站通用界面文案的字段结构（目前只有 zh-Hans 一套）。 */
type UseCaseText = {
  indexTitle: string;
  indexSubtitle: string;
  indexMetadataTitle: string;
  indexMetadataDescription: string;
  cardReadMore: string;
  tableOfContents: string;
};

/** 案例站各页面的静态界面文案（按语言分键）。 */
export const useCaseText = {
  "zh-Hans": {
    indexTitle: "案例",
    indexSubtitle: "看看团队怎么用 Multica 把人和 agent 一起组织起来。",
    indexMetadataTitle: "案例",
    indexMetadataDescription:
      "看看团队怎么用 Multica 把人和 agent 一起组织起来。",
    cardReadMore: "阅读 →",
    tableOfContents: "目录",
  },
} as const satisfies Record<"zh-Hans", UseCaseText>;
