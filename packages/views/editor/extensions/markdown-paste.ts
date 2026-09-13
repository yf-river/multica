/**
 * Markdown 粘贴扩展——确保粘贴的文本按 Markdown 解析。
 *
 * 问题：浏览器剪贴板可能同时含有 text/plain 和 text/html。ProseMirror 在
 * 两者都在时总是优先 text/html（parseFromClipboard 里写死：
 * `let asText = !html`）。从 VS Code、文本编辑器或 .md 文件复制时，操作系统
 * 会把文本包进 <pre>/<div> HTML 标签，ProseMirror 会把这些解析成代码块——
 * 这就错了。
 *
 * 解法：用 `handlePaste`（唯一一个对所有粘贴事件都触发、且能拿到原始
 * ClipboardEvent 的 ProseMirror prop）。先查 HTML 里有没有 `data-pm-slice`——
 * 这个属性是 ProseMirror 自己的剪贴板序列化器加的。有它，说明来源是另一个
 * ProseMirror 编辑器、其 HTML 结构正确——交给 ProseMirror 处理。否则把
 * text/plain 分类进三条路之一：
 * - native：交给 ProseMirror 或其它扩展处理
 * - literal：精确插入文本，不做 Markdown 解析
 * - markdown：把 text/plain 按 Markdown 解析
 *
 * 为什么不用 clipboardTextParser？它只在剪贴板没有 text/html 时才运行
 * （ProseMirror 源码：`let asText = !!text && !html`）。
 *
 * HTML/文本的分类刻意保守。富语义 HTML 应保持 native，这样链接、列表、
 * 强调与行内代码都能存活。编辑器产出的语法高亮包裹层
 * （<pre>/<code>/<span>/<div>）单凭它们自己不足以判 native——那些仍应按
 * Markdown 源码粘贴。
 */
import { Extension } from "@tiptap/core";
import { Plugin, PluginKey, type Transaction } from "@tiptap/pm/state";
import type { EditorView } from "@tiptap/pm/view";
import {
  Fragment,
  Slice,
  type Node as ProseMirrorNode,
} from "@tiptap/pm/model";

const LARGE_PASTE_TEXT_THRESHOLD = 50_000;
const SEMANTIC_RICH_HTML_SELECTOR = [
  "a[href]",
  "b",
  "blockquote",
  "del",
  "details",
  "em",
  "figcaption",
  "figure",
  "h1",
  "h2",
  "h3",
  "h4",
  "h5",
  "h6",
  "hr",
  "i",
  "img",
  "li",
  "mark",
  "ol",
  "s",
  "strong",
  "sub",
  "summary",
  "sup",
  "table",
  "tbody",
  "td",
  "tfoot",
  "th",
  "thead",
  "tr",
  "u",
  "ul",
].join(",");
const RAW_HTML_TAG_RE = /<(\/?[a-zA-Z][a-zA-Z0-9-]*)(?:\s[^>]*)?\/?>/g;

// CommonMark 把 <word> 一律当裸 HTML，不管 "word" 是不是真实 HTML 元素。
// 纯文本粘贴时，用户文本才是事实来源，所以在 Markdown 词法器能把它归成
// HTML 之前，先转义这些形似标签的片段。
function escapeRawHtmlTagsInSegment(segment: string): string {
  return segment.replace(
    RAW_HTML_TAG_RE,
    (match) => match.replaceAll("<", "&lt;").replaceAll(">", "&gt;"),
  );
}

/** 收集片段里所有形似 HTML 标签的文本（原样）。 */
function collectRawHtmlTagsInSegment(segment: string): string[] {
  return segment.match(RAW_HTML_TAG_RE) ?? [];
}

function escapeTagsOutsideCodeSpans(line: string): string {
  const parts: string[] = [];
  let i = 0;

  while (i < line.length) {
    if (line[i] === "`") {
      let count = 0;
      while (i + count < line.length && line[i + count] === "`") count++;
      const delimiter = "`".repeat(count);
      const afterOpener = i + count;

      let closerIdx = afterOpener;
      let found = false;
      while (closerIdx <= line.length - count) {
        const idx = line.indexOf(delimiter, closerIdx);
        if (idx === -1) break;
        if (
          (idx + count >= line.length || line[idx + count] !== "`") &&
          (idx === 0 || line[idx - 1] !== "`")
        ) {
          parts.push(line.slice(i, idx + count));
          i = idx + count;
          found = true;
          break;
        }
        closerIdx = idx + 1;
      }

      if (!found) {
        parts.push(escapeRawHtmlTagsInSegment(delimiter));
        i = afterOpener;
      }
      continue;
    }

    const nextBacktick = line.indexOf("`", i);
    const end = nextBacktick === -1 ? line.length : nextBacktick;
    parts.push(escapeRawHtmlTagsInSegment(line.slice(i, end)));
    i = end;
  }

  return parts.join("");
}

function collectTagsOutsideCodeSpans(line: string): string[] {
  const tags: string[] = [];
  let i = 0;

  while (i < line.length) {
    if (line[i] === "`") {
      let count = 0;
      while (i + count < line.length && line[i + count] === "`") count++;
      const delimiter = "`".repeat(count);
      const afterOpener = i + count;

      let closerIdx = afterOpener;
      let found = false;
      while (closerIdx <= line.length - count) {
        const idx = line.indexOf(delimiter, closerIdx);
        if (idx === -1) break;
        if (
          (idx + count >= line.length || line[idx + count] !== "`") &&
          (idx === 0 || line[idx - 1] !== "`")
        ) {
          i = idx + count;
          found = true;
          break;
        }
        closerIdx = idx + 1;
      }

      if (!found) {
        i = afterOpener;
      }
      continue;
    }

    const nextBacktick = line.indexOf("`", i);
    const end = nextBacktick === -1 ? line.length : nextBacktick;
    tags.push(...collectRawHtmlTagsInSegment(line.slice(i, end)));
    i = end;
  }

  return tags;
}

/**
 * 把代码围栏与行内代码 span 之外的形似 HTML 标签（`<...>`）转义成实体，
 * 避免纯文本粘贴的来源文本被 Markdown 词法器当成裸 HTML（分类策略见文件头）。
 */
export function escapeRawHtmlTagsOutsideCode(text: string): string {
  const lines = text.split("\n");
  let inFencedBlock = false;
  let fenceChar = "";
  let fenceLen = 0;

  const processed = lines.map((line) => {
    const fenceMatch = line.match(/^ {0,3}(`{3,}|~{3,})/);
    const fence = fenceMatch?.[1];
    if (fence) {
      if (!inFencedBlock) {
        inFencedBlock = true;
        fenceChar = fence.charAt(0);
        fenceLen = fence.length;
        return line;
      }
      const isClosingFence =
        fence.charAt(0) === fenceChar &&
        fence.length >= fenceLen &&
        /^ {0,3}(`{3,}|~{3,})[ \t]*$/.test(line);
      if (isClosingFence) {
        inFencedBlock = false;
        return line;
      }
    }

    if (inFencedBlock) return line;
    return escapeTagsOutsideCodeSpans(line);
  });

  return processed.join("\n");
}

function findRawHtmlTagsOutsideCode(text: string): string[] {
  const lines = text.split("\n");
  const tags: string[] = [];
  let inFencedBlock = false;
  let fenceChar = "";
  let fenceLen = 0;

  for (const line of lines) {
    const fenceMatch = line.match(/^ {0,3}(`{3,}|~{3,})/);
    const fence = fenceMatch?.[1];
    if (fence) {
      if (!inFencedBlock) {
        inFencedBlock = true;
        fenceChar = fence.charAt(0);
        fenceLen = fence.length;
        continue;
      }
      const isClosingFence =
        fence.charAt(0) === fenceChar &&
        fence.length >= fenceLen &&
        /^ {0,3}(`{3,}|~{3,})[ \t]*$/.test(line);
      if (isClosingFence) {
        inFencedBlock = false;
        continue;
      }
    }

    if (!inFencedBlock) {
      tags.push(...collectTagsOutsideCodeSpans(line));
    }
  }

  return tags;
}

type PasteMode = "native" | "literal" | "markdown";

interface PasteClassificationInput {
  text: string;
  html: string;
  hasFiles: boolean;
  isInsideCodeBlock: boolean;
}

function isJsonDocumentText(text: string): boolean {
  const trimmed = text.trim();
  if (!trimmed) return false;

  const startsLikeJson =
    (trimmed.startsWith("{") && trimmed.endsWith("}")) ||
    (trimmed.startsWith("[") && trimmed.endsWith("]"));
  if (!startsLikeJson) return false;

  try {
    JSON.parse(trimmed);
    return true;
  } catch {
    return false;
  }
}

function isStructuredPlainText(text: string): boolean {
  return isJsonDocumentText(text);
}

function hasRichStyle(style: string): boolean {
  const normalized = style.toLowerCase();
  return (
    /font-weight\s*:\s*(bold|[6-9]00)\b/.test(normalized) ||
    /font-style\s*:\s*italic\b/.test(normalized) ||
    /text-decoration[^;]*(line-through|underline)/.test(normalized)
  );
}

function countOccurrences(text: string, needle: string): number {
  if (!needle) return 0;
  let count = 0;
  let index = text.indexOf(needle);
  while (index !== -1) {
    count++;
    index = text.indexOf(needle, index + needle.length);
  }
  return count;
}

function htmlPreservesRawTagsFromPlainText(html: string, text: string): boolean {
  const tags = findRawHtmlTagsOutsideCode(text);
  if (tags.length === 0) return true;
  if (typeof DOMParser === "undefined") return false;

  const doc = new DOMParser().parseFromString(html, "text/html");
  const htmlText = doc.body?.textContent ?? "";
  const expectedCounts = new Map<string, number>();
  for (const tag of tags) {
    expectedCounts.set(tag, (expectedCounts.get(tag) ?? 0) + 1);
  }

  for (const [tag, expectedCount] of expectedCounts) {
    if (countOccurrences(htmlText, tag) < expectedCount) return false;
  }

  return true;
}

function hasSemanticRichHtml(html: string, text: string): boolean {
  if (!html.trim()) return false;
  if (typeof DOMParser === "undefined") return false;

  if (!htmlPreservesRawTagsFromPlainText(html, text)) return false;

  const doc = new DOMParser().parseFromString(html, "text/html");
  const { body } = doc;
  if (!body) return false;

  if (body.querySelector(SEMANTIC_RICH_HTML_SELECTOR)) return true;

  // 行内 <code> 承载有意义的富文本语义。<pre><code> 这一对通常只是编辑器
  // 的语法高亮包裹层，所以那类路径仍留给 Markdown 解析。
  for (const code of Array.from(body.querySelectorAll("code"))) {
    if (!code.closest("pre")) return true;
  }

  for (const el of Array.from(body.querySelectorAll<HTMLElement>("[style]"))) {
    if (hasRichStyle(el.getAttribute("style") ?? "")) return true;
  }

  return false;
}

function classifyPaste({
  text,
  html,
  hasFiles,
  isInsideCodeBlock,
}: PasteClassificationInput): PasteMode {
  if (hasFiles) return "native";
  if (!text) return "native";
  if (isInsideCodeBlock) return "literal";
  if (html && html.includes("data-pm-slice")) return "native";
  if (html && hasSemanticRichHtml(html, text)) return "native";
  if (text.length > LARGE_PASTE_TEXT_THRESHOLD) return "literal";
  if (isStructuredPlainText(text)) return "literal";
  return "markdown";
}

function canJoinOrderedLists(
  left: ProseMirrorNode,
  right: ProseMirrorNode,
): boolean {
  const leftType = left.attrs.type ?? "1";
  const rightType = right.attrs.type ?? "1";
  if (
    left.type.name !== "orderedList" ||
    right.type !== left.type ||
    leftType !== rightType
  ) {
    return false;
  }

  const parsedLeftStart = Number(left.attrs.start);
  const parsedRightStart = Number(right.attrs.start);
  const leftStart = Number.isFinite(parsedLeftStart) ? parsedLeftStart : 1;
  const rightStart = Number.isFinite(parsedRightStart) ? parsedRightStart : 1;
  const expectedContinuation = leftStart + left.childCount;

  // 有些富文本来源会把每个可见列表项塞进自己独立的 <ol> 且不带 start 值，
  // 解析出的 slice 于是变成一连串各自从 1 重开的单项列表。显式的续号值
  // 是同结构配更好的 HTML 提示。两种形态在我们的文档模型里都应合并成
  // 一个列表。
  return rightStart === 1 || rightStart === expectedContinuation;
}

function repairOrderedListsInFragment(fragment: Fragment): Fragment {
  const repaired: ProseMirrorNode[] = [];

  fragment.forEach((node) => {
    const content = node.isLeaf
      ? node.content
      : repairOrderedListsInFragment(node.content);
    const repairedNode = content.eq(node.content) ? node : node.copy(content);
    const previous = repaired.at(-1);

    if (previous && canJoinOrderedLists(previous, repairedNode)) {
      repaired[repaired.length - 1] = previous.copy(
        previous.content.append(repairedNode.content),
      );
      return;
    }

    repaired.push(repairedNode);
  });

  return Fragment.fromArray(repaired);
}

function repairFragmentedOrderedLists(slice: Slice): Slice {
  const content = repairOrderedListsInFragment(slice.content);
  if (content.eq(slice.content)) return slice;
  return new Slice(content, slice.openStart, slice.openEnd);
}

/**
 * 给事务盖上「这是一次粘贴的提交」标记，与 ProseMirror 自带的标记方式一致。
 *
 * `doPaste` 会往它派发的事务上盖 `paste` / `uiEvent`，但一旦某个
 * `handlePaste` prop 认领了事件它就提前返回——而本扩展是个几乎认领所有
 * 粘贴的兜底。因此下游凡是问「这段文本是粘贴来的吗？」的地方都会看到
 * 一个没有标记的事务，把粘贴当成了打字。`issueIdentifierAutolink` 读的
 * 正是这个：没有标记时它只会看光标前的 token，于是粘贴 `See MUL-2 now`
 * 什么都不会自动链接（MUL-5429）。
 *
 * ProseMirror 作者把这类 meta 描述为第三方代码区分用户事件所依赖的契约，
 * 任何自派发事务的自定义 `handlePaste` 都欠它们一笔。
 */
function dispatchPaste(view: EditorView, tr: Transaction): void {
  view.dispatch(tr.setMeta("paste", true).setMeta("uiEvent", "paste"));
}

/**
 * 创建「粘贴即按 Markdown 解析」的扩展：对剪贴板 text/plain 做分类
 * （native / literal / markdown，见文件头与 classifyPaste），把 Markdown
 * 类文本解析成文档内容插入；一切派发都经 dispatchPaste 打上 paste 标记，
 * 供 issueIdentifierAutolink 等下游区分粘贴与打字（MUL-5429）。
 */
export function createMarkdownPasteExtension() {
  return Extension.create({
    name: "markdownPaste",
    addProseMirrorPlugins() {
      const { editor } = this;
      return [
        new Plugin({
          key: new PluginKey("markdownPaste"),
          props: {
            handlePaste(view, event, slice) {
              if (!editor.markdown) return false;
              const clipboard = event.clipboardData;
              if (!clipboard) return false;

              const text = clipboard.getData("text/plain");
              const html = clipboard.getData("text/html");
              const { $from } = view.state.selection;
              const mode = classifyPaste({
                text,
                html,
                hasFiles: Boolean(clipboard.files?.length),
                isInsideCodeBlock: $from.parent.type.name === "codeBlock",
              });

              if (mode === "native") {
                // ProseMirror 自己产的剪贴板 HTML 已精确表达我们的文档结构。
                // 只修外部富 HTML——那里相邻的 <ol> 片段常代表一个可见列表。
                if (html && !html.includes("data-pm-slice")) {
                  const repaired = repairFragmentedOrderedLists(slice);
                  if (repaired !== slice) {
                    dispatchPaste(
                      view,
                      view.state.tr.replaceSelection(repaired).scrollIntoView(),
                    );
                    return true;
                  }
                }
                return false;
              }

              if (mode === "literal") {
                dispatchPaste(view, view.state.tr.insertText(text));
                return true;
              }

              // 其余情况（VS Code、文本编辑器、.md 文件、终端、网页）：
              // 把 text/plain 按 Markdown 解析。
              const preprocessed = escapeRawHtmlTagsOutsideCode(text);
              const json = editor.markdown.parse(preprocessed);
              const node = editor.schema.nodeFromJSON(json);

              // 安全网：输入非空但解析仍得到空文档时，退回字面插入。
              const first = node.content.firstChild;
              const parsedEmpty =
                node.content.childCount === 0 ||
                (node.content.childCount === 1 &&
                  first?.type.name === "paragraph" &&
                  first.content.size === 0);
              if (text.trim() && parsedEmpty) {
                dispatchPaste(view, view.state.tr.insertText(text));
                return true;
              }

              const parsedSlice = Slice.maxOpen(node.content);
              dispatchPaste(view, view.state.tr.replaceSelection(parsedSlice));
              return true;
            },
          },
        }),
      ];
    },
  });
}
