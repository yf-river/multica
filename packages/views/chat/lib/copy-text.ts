// 聊天复制工具：把助手的 timeline 拆成可折叠的区域，并提取适合复制的文本。
import type { ChatMessage } from "@multica/core/types";
import type { ChatTimelineItem } from "@multica/core/chat";

/**
 * 把助手的 timeline 拆成三个区域，用于指挥家式的折叠：
 *   preface — 第一个 thinking/tool/error 条目之前的文本条目
 *   middle  — 从第一个到最后一个非文本条目（含首尾），包括夹在它们中间的文本条目
 *   final   — 最后一个非文本条目之后的文本条目
 *
 * UI 把 preface 渲染在外层折叠的上方，middle 渲染在折叠内部（每一行保留自己已有的
 * 内层 Collapsible），final 渲染在折叠下方。复制时拼接 preface + final——
 * 折叠里的内容被有意省略，与折叠收起时看到的内容一致。
 *
 * @param items - 助手的 timeline 条目列表。
 * @returns 拆分后的三个区域。
 */
export function splitTimeline(items: ChatTimelineItem[]): {
  preface: ChatTimelineItem[];
  middle: ChatTimelineItem[];
  final: ChatTimelineItem[];
} {
  const firstNonTextIdx = items.findIndex((i) => i.type !== "text");
  if (firstNonTextIdx === -1) {
    return { preface: [], middle: [], final: items };
  }
  let lastNonTextIdx = items.length - 1;
  while (lastNonTextIdx >= 0 && items[lastNonTextIdx]!.type === "text") {
    lastNonTextIdx--;
  }
  return {
    preface: items.slice(0, firstNonTextIdx),
    middle: items.slice(firstNonTextIdx, lastNonTextIdx + 1),
    final: items.slice(lastNonTextIdx + 1),
  };
}

/**
 * 复制动作放到剪贴板上的 Markdown 源码。设计上它只包含用户可见的答案——
 * 外层折叠内的任何内容（思考、工具调用、夹在中间的过渡文本）都会被丢弃。
 * 对于没有 timeline 的旧消息，以及全是非文本条目的病态情况，回退到 `message.content`，
 * 这样复制永远不会产生空字符串。
 *
 * @param message - 原始聊天消息。
 * @param timeline - 消息的 timeline 条目。
 * @returns 适合复制的纯文本（Markdown 源码）。
 */
export function extractCopyText(
  message: ChatMessage,
  timeline: ChatTimelineItem[],
): string {
  if (timeline.length === 0) return message.content ?? "";
  const { preface, final } = splitTimeline(timeline);
  const pieces = [...preface, ...final]
    .map((i) => i.content ?? "")
    .filter((s) => s.length > 0);
  if (pieces.length === 0) return message.content ?? "";
  return pieces.join("\n\n");
}
