import { fromMarkdown } from "mdast-util-from-markdown";
import type { Root, RootContent, Code } from "mdast";

/**
 * 富文本模块：流式 RichContent 的围栏代码闭合性闸门（MUL-4922）。
 *
 * 绝不能用作者仍在输入的围栏去实例化 Mermaid 图或沙箱 HTML iframe：流式
 * 中途源码语法不完整，Mermaid 会在每次击键时抛错，iframe 会被每秒创建和
 * 销毁几十次。只有*闭合*的围栏才能升级成富块。
 *
 * 本模块只回答一个问题——「这段源码里哪些围栏代码块是闭合的？」——并返回
 * 偏移量。它刻意不负责渲染。渲染只留在唯一的 ReactMarkdown 管线里；一个
 * 负责渲染的闸门会成为这次清理本要删除的第二个渲染器。
 *
 * 闭合性来自一次真正的 CommonMark 解析，而不是 `startsWith("```")` 扫描，
 * 所以缩进围栏、波浪线围栏、超过三个的标记、列表项内的围栏、嵌套围栏，
 * 都会按照真实 Markdown 解析器的方式来判断。
 */

// 开围栏：最多 3 个前导空格，然后是 >= 3 个反引号或波浪线。
const FENCE_OPEN_RE = /^ {0,3}(`{3,}|~{3,})/;

function isCodeNode(node: RootContent | Root): node is Code {
  return node.type === "code";
}

function collectCodeNodes(node: Root | RootContent, out: Code[]): void {
  if (isCodeNode(node)) {
    out.push(node);
    return;
  }
  const children = (node as { children?: RootContent[] }).children;
  if (children) {
    for (const child of children) collectCodeNodes(child, out);
  }
}

/**
 * 当 `raw`（某个 mdast `code` 节点的精确源码范围）以对它自己开围栏合法的
 * 闭围栏结尾时返回 true。
 *
 * CommonMark 要求闭围栏与开围栏使用相同字符、长度至少相等，所以 ```` 开
 * 而 ``` 闭仍是*未闭合*——较短的一串是内容，不是终止符。显式检查长度，
 * 是因为只匹配「一行 >=3 个标记」的正则会把它报成闭合，从而让半写的图
 * 到达 Mermaid。
 */
function endsWithClosingFence(raw: string): boolean {
  const lines = raw.split("\n");
  const openMatch = FENCE_OPEN_RE.exec(lines[0] ?? "");
  // 没有开围栏意味着是缩进（4 空格）代码块。它没有可悬空的围栏，也不带
  // info string，所以永远不会分发到富块——当作已了结处理。
  if (!openMatch?.[1]) return true;

  const openFence = openMatch[1];
  const marker = openFence[0] as "`" | "~";
  // 单行不可能既是开围栏又是闭围栏。
  if (lines.length < 2) return false;

  // mdast 可能把尾随换行算进节点范围，也可能不算。
  const lastLine = (lines[lines.length - 1] === "" ? lines[lines.length - 2] : lines[lines.length - 1]) ?? "";
  const closeMatch = /^ {0,3}([`~]+)[ \t]*$/.exec(lastLine);
  if (!closeMatch?.[1]) return false;

  const closeFence = closeMatch[1];
  return closeFence[0] === marker && closeFence.length >= openFence.length;
}

/**
 * 返回 `source` 里每个已闭合围栏代码块的起始偏移量集合。
 *
 * `source` 必须是交给 ReactMarkdown 的最终处理后的 Markdown——也就是
 * preprocess/highlight 已经改写过的那个字符串。对 pre-preprocess 原始文本
 * 计算的偏移会发生漂移，匹配到错误的节点。
 * @param source 最终处理后的 Markdown 源文本。
 */
export function computeClosedFenceOffsets(source: string): Set<number> {
  const closed = new Set<number>();
  if (!source) return closed;

  let tree: Root;
  try {
    tree = fromMarkdown(source);
  } catch {
    // 解析失败时不能升级任何东西：退回「全都不闭合」，展示源码而不是在
    // 坏输入上跑 Mermaid。
    return closed;
  }

  const codes: Code[] = [];
  collectCodeNodes(tree, codes);

  for (const node of codes) {
    const start = node.position?.start.offset;
    const end = node.position?.end.offset;
    if (start == null || end == null) continue;
    if (endsWithClosingFence(source.slice(start, end))) closed.add(start);
  }

  return closed;
}
