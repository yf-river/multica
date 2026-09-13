/**
 * 代码语法高亮所用的 lowlight 实例与工具函数。
 *
 * 显式语言名正常走 baseLowlight 高亮；未标注或未知语言的代码块只按纯文本
 * 高亮（见 codeLowlight 注释）。
 */

import { common, createLowlight } from "lowlight";

const baseLowlight = createLowlight(common);

/**
 * 供 Tiptap 代码块使用的 lowlight：当代码围栏没有注册语言时，Tiptap 会调
 * `highlightAuto`。自动探测会拿每一套已注册语法对整块代码跑一遍，让大文档
 * 挂载很贵。这里保留接口，但把兜底做得确定且廉价：显式已知语言仍然高亮，
 * 未标注或未知的块保持纯文本。
 */
export const codeLowlight: ReturnType<typeof createLowlight> = {
  ...baseLowlight,
  highlightAuto(value) {
    return baseLowlight.highlight("plaintext", value);
  },
};

/**
 * 高亮一段代码。语言能识别则用对应语法，否则退化为纯文本。
 */
export function highlightCode(value: string, language?: string) {
  const normalizedLanguage = language?.trim().toLowerCase();

  return normalizedLanguage && baseLowlight.registered(normalizedLanguage)
    ? baseLowlight.highlight(normalizedLanguage, value)
    : baseLowlight.highlight("plaintext", value);
}
