// 转录里 diff 代码的语法高亮。高亮以"整侧文本"为单位一次跑完——绝不逐行高亮——
// 这样多行字符串、注释、模板字符串能按它真正所属的语法单元整体着色。高亮好的树再
// 按换行拆开、在每行重新补全包裹它的外层 span，从而让"每行一个 diff 增删列"与
// "语法按块着色"同时成立。
//
// 与富文本代码块共用同一引擎（lowlight）和同一套 `.hljs-*` 类约定，所以一个 Rust
// 文件在转录里跟在评论里长得一样。

import { toHtml } from "hast-util-to-html";
import type { Element, ElementContent, Properties, Root, RootContent } from "hast";
import { highlightCode } from "../../editor/syntax-highlight";

/**
 * 文件扩展名 → lowlight 语法名。没有列出的扩展名在 `highlightCode` 里会按纯文本
 * 处理，所以这张表只需收录值得命名的语言——漏掉的只降级为无高亮文本，绝不会报错。
 */
const LANGUAGE_BY_EXTENSION: Record<string, string> = {
  bash: "bash",
  c: "c",
  cc: "cpp",
  cjs: "javascript",
  cpp: "cpp",
  cs: "csharp",
  css: "css",
  go: "go",
  h: "c",
  hpp: "cpp",
  htm: "xml",
  html: "xml",
  java: "java",
  js: "javascript",
  json: "json",
  jsx: "javascript",
  kt: "kotlin",
  lua: "lua",
  md: "markdown",
  mjs: "javascript",
  php: "php",
  pl: "perl",
  py: "python",
  r: "r",
  rb: "ruby",
  rs: "rust",
  scala: "scala",
  scss: "scss",
  sh: "bash",
  sql: "sql",
  swift: "swift",
  toml: "ini",
  ts: "typescript",
  tsx: "typescript",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
  zsh: "bash",
};

/**
 * 按扩展名挑出路径对应的语法名；返回 undefined 表示"按纯文本高亮"。
 * 取文件名最后一个点号后的部分做查表键，点号在开头（如 .env 这类隐藏文件）时
 * 认为没有扩展名。
 * @param path 文件路径。
 * @returns lowlight 语法名，或不支持时 undefined。
 */
export function languageForPath(path: string): string | undefined {
  const base = path.split("/").pop() ?? path;
  const dot = base.lastIndexOf(".");
  if (dot <= 0) return undefined;
  return LANGUAGE_BY_EXTENSION[base.slice(dot + 1).toLowerCase()];
}

/** 一段文本所处的"开放元素链"：从最外层到最内层依次排列（最内层在最后）。 */
type Ancestors = ReadonlyArray<Pick<Element, "tagName" | "properties">>;

// 把一段纯文本按祖先链从内到外逐层包上对应的标签，恢复它在整块高亮树里的嵌套位置。
function wrap(text: string, ancestors: Ancestors): ElementContent {
  let node: ElementContent = { type: "text", value: text };
  for (let i = ancestors.length - 1; i >= 0; i--) {
    const ancestor = ancestors[i];
    if (!ancestor) continue;
    node = {
      type: "element",
      tagName: ancestor.tagName,
      properties: (ancestor.properties ?? {}) as Properties,
      children: [node],
    };
  }
  return node;
}

// 深度遍历高亮树，按换行把树切成一列"行"，每行元素收集该行自己的子节点。
// 遍历时维护祖先链，跨行的节点（如多行字符串）到达新行时会重新打开祖先 span，
// 这样每行产出的 HTML 都是自洽、可独立渲染的片段。
function collect(
  nodes: ReadonlyArray<RootContent>,
  ancestors: Ancestors,
  lines: ElementContent[][],
): void {
  for (const node of nodes) {
    if (node.type === "text") {
      const parts = node.value.split("\n");
      for (let i = 0; i < parts.length; i++) {
        if (i > 0) lines.push([]);
        const part = parts[i];
        if (part === undefined || part.length === 0) continue;
        lines[lines.length - 1]?.push(wrap(part, ancestors));
      }
      continue;
    }
    if (node.type === "element") {
      collect(node.children, [...ancestors, { tagName: node.tagName, properties: node.properties }], lines);
    }
    // 注释/doctype 不可能出现在 lowlight 结果里，忽略它们让遍历保持全量，
    // 而无需为不可能出现的输入编造输出。
  }
}

/**
 * 把 `text` 作为一整块做语法高亮，返回整块 HTML；语法抛异常时返回 null，
 * 调用方可退回渲染无高亮的原文，而不是什么都不显示。
 * @param text 源码文本。
 * @param language lowlight 语法名，undefined 表示纯文本。
 * @returns 高亮 HTML，失败时 null。
 */
export function highlightBlock(text: string, language: string | undefined): string | null {
  try {
    return toHtml(highlightCode(text, language) as Root);
  } catch {
    return null;
  }
}

/**
 * 把 `text` 作为一整块高亮后，再按行拆开返回"每行一段 HTML"的数组。各行独立
 * 自洽，可直接嵌进 diff 的增删行容器；整块解析能保证跨行的语法单元（多行字符串、
 * 注释）着色正确。语法抛异常时返回 null。
 * @param text 源码文本。
 * @param language lowlight 语法名。
 * @returns 每行一段 HTML；失败时 null。
 */
export function highlightToLines(text: string, language: string | undefined): string[] | null {
  try {
    const tree = highlightCode(text, language) as Root;
    const lines: ElementContent[][] = [[]];
    collect(tree.children, [], lines);
    return lines.map((children) => toHtml({ type: "root", children }));
  } catch {
    return null;
  }
}
