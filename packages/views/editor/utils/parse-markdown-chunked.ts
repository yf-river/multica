import type { JSONContent } from "@tiptap/core";

/**
 * 源文本超过该大小时，ContentEditor 改为分块解析 markdown，而不是一次
 * 全量解析。`@tiptap/markdown` 走 `marked`，其分词器对文档长度是 O(n²)
 * （实测：533KB 纯文本 → 61.8s 解析，而之后那次 ProseMirror setContent
 * 只要 40ms）。全文档解析是瓶颈；一份典型 22KB 结构化工单的 trace 显示
 * 单次解析要好几百毫秒，所以单次解析路径只留给真正小的文档。
 */
export const MARKDOWN_CHUNK_THRESHOLD = 8_000;

// 更小的解析窗口很关键，因为分词成本大致随每块长度的平方增长。4KB 让
// 常见工单描述保持廉价，同时又不至于造出几百个微小的 ProseMirror 文档。
export const MARKDOWN_CHUNK_SIZE = 4_000;

/** markdown 管理器的最小接口：能单独把一段 markdown 解析成 JSONContent。 */
export interface MarkdownManagerLike {
  parse(markdown: string): JSONContent;
}

/**
 * 把 markdown 分块解析成 ProseMirror JSON 文档，避开 marked 的 O(n²)。
 *
 * 切成 k 块并各自独立解析，成本降到 O(n²/k)——marked 每次只在单个小块内
 * 扫描。切分点只会落在「围栏代码块之外」的空行上，因此每一块都是一串完整
 * 的块级节点；把各块的文档拼起来，与单次解析产生的文档一致。
 *
 * 已知限制：跨越切分边界的「松散」列表（项之间有空行的列表）可能渲染成
 * 两个相邻列表。相比一分钟的卡死这是可接受的权衡，而且只有超过阈值的大
 * 文档才可能触达。
 */
export function parseMarkdownChunked(
  manager: MarkdownManagerLike,
  markdown: string,
  chunkSize = MARKDOWN_CHUNK_SIZE,
): JSONContent {
  const lines = markdown.split("\n");
  const chunks: string[] = [];
  let current: string[] = [];
  let currentLen = 0;
  let inFence = false;

  for (const line of lines) {
    // 追踪围栏代码块，保证切分永远不会落在代码块中间。
    if (/^\s*(```|~~~)/.test(line)) inFence = !inFence;
    current.push(line);
    currentLen += line.length + 1;

    // 只有当前块足够大、且正处于段落边界（空行）、又不在围栏里时才切分。
    if (currentLen >= chunkSize && !inFence && line.trim() === "") {
      chunks.push(current.join("\n"));
      current = [];
      currentLen = 0;
    }
  }
  if (current.length) chunks.push(current.join("\n"));

  const merged: JSONContent = { type: "doc", content: [] };
  for (const chunk of chunks) {
    const doc = manager.parse(chunk);
    if (doc.content) merged.content!.push(...doc.content);
  }
  return merged;
}
