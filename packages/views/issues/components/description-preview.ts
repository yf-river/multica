// 本文件：descriptionPreview —— 把问题描述 Markdown 摊平成单行纯文本预览。
// 预览面只展示一两行被 CSS clamp 截断的文本，但 clamp 是 CSS 层的：完整描述
// 仍挂进 DOM、进入看板每张卡片的无障碍树。这个长度上限是一道"DOM 体量"约束，
// 而非"行宽"承诺——它远高于任何预览面能显示的量，因此可见文本仍由 clamp 决定
//（见 #6633）。

import { stripChannelMediaMarkers } from "@multica/core/types";

// 预览面只显示一两行被 CSS 夹住的行，但夹住发生在 CSS 层：
// 完整描述仍挂载进 DOM、进入看板每张卡片的无障碍树。这个上限是
// DOM 体量约束而非行宽承诺——它远高于任何预览面能显示的量，
// 可见文本仍由 clamp 决定。（问题 #6633）
const PREVIEW_MAX_LENGTH = 300;

/**
 * 把描述 Markdown 摊平成单行纯文本预览。凡"问题旁边展示描述摘要"的
 * 界面都共用它。
 *
 * 频道媒体的出处是服务端持有的合并元数据，不是用户撰写的内容，
 * 因此最先剥离：它标注的那段图片 Markdown 在下面一行才被移除，
 * 若不先剥离，那条裸 HTML 注释会熬过所有后续清洗、变成可见的预览文本。
 */
export function descriptionPreview(markdown: string): string {
  const preview = (
    stripChannelMediaMarkers(markdown)
      // 先反转义反斜杠转义的标点，且这个顺序是"承重"的。编辑器把链接序列化成
      // `\[label\](url)`，因此下面链接规则见到的是 `\[label\]`，它的 `[^\]]+`
      // 捕获组会吞掉尾部反斜杠、放走头部那根——于是预览会读成 `\label\`。
      // 在任何结构性规则之后再反转义就太晚了，因为正是这些转义阻止了规则匹配。
      // 该字符类是 CommonMark 的可转义集合：全部 ASCII 标点。
      // 反斜杠若后跟其它字符（字母、空格），是正文里的字面反斜杠，原样保留。
      .replace(/\\([!"#$%&'()*+,\-./:;<=>?@[\\\]^_`{|}~])/g, "$1")
      // 目标组容忍一层成对括号，例如 `(https://en.wikipedia.org/wiki/Foo_(bar))`，
      // 或链接标签里的 URL 被自动加链后产生的嵌套链接——
      // 序列化成 `[label]([https://x](https://x))`。naive 的 `[^)]+`
      // 会在第一个 `)` 处停下、把外层的 `)` 晾在预览文本里——
      // 不要把它简化回 `[^)]+`。
      .replace(/!file\[[^\]]*\]\((?:[^()]|\([^()]*\))*\)/g, "")
      .replace(/!\[[^\]]*\]\((?:[^()]|\([^()]*\))*\)/g, "")
      .replace(/\[([^\]]+)\]\((?:[^()]|\([^()]*\))*\)/g, "$1")
      .replace(/[*_`~]+/g, "")
      .replace(/^[\s>#]+/gm, "")
      .replace(/\s+/g, " ")
      .trim()
  );

  if (preview.length <= PREVIEW_MAX_LENGTH) return preview;

  // 省略号占去预算的最后一个码元。按码元切片可能落在某个代理对的
  // 两半之间——那会渲染成一个孤零零的替换字符——因此跨切口的表情
  // 会整体丢弃，避免切坏。
  const maxContentLength = PREVIEW_MAX_LENGTH - 1;
  const lastCodeUnit = preview.charCodeAt(maxContentLength - 1);
  const endsOnHighSurrogate = lastCodeUnit >= 0xd800 && lastCodeUnit <= 0xdbff;
  const safeEnd = endsOnHighSurrogate ? maxContentLength - 1 : maxContentLength;

  return `${preview.slice(0, safeEnd).trimEnd()}…`;
}
