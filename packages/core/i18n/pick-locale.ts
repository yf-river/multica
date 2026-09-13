// 本文件实现语言匹配：把候选语言列表解析成实际使用的语言。
// 目前产品只有简体中文一种语言，所以任何候选都直接落到默认语言。

import { DEFAULT_LOCALE, type SupportedLocale } from "./types";

/**
 * 从候选语言列表里匹配出一个受支持的语言。
 *
 * 目前只支持简体中文，因此总是返回 DEFAULT_LOCALE。参数保留是为了将来支持
 * 多语言时，调用方无需改动即可传入浏览器的 navigator.languages。
 *
 * @param _candidates - 候选语言列表（当前未使用）
 * @returns 匹配到的受支持语言
 */
export function matchLocale(_candidates: string[]): SupportedLocale {
  return DEFAULT_LOCALE;
}
