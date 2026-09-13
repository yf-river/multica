/**
 * 只读占位视图与编辑器之间的「文本锚点」光标映射。
 *
 * lazy-editor 切换过去时曾用原始像素坐标（`posAtCoords`）落光标，这假设
 * 只读渲染与 Tiptap 渲染逐像素一致。长文档里从不成立：逐块高度差异
 * （表格、代码头、mention 卡片）自上而下累积，且 NodeView/图片在 `onReady`
 * 之后还在回流——于是点击会落到目标好几行之外。文本锚点则完全绕开布局：
 * 记录点击的是**哪个**顶层块里的**第几个字符**，再把这个逻辑位置在
 * ProseMirror 文档里解出来。
 *
 * 偏移只数**非空白**字符。两个渲染器对容器块内的空白有分歧——react-markdown
 * 保留 HTML 源码里的元素间换行为文本节点（"URL: x\nTitle: y"），而
 * ProseMirror 的 textContent 无分隔拼接（"URL: xTitle: y"）——原始字符偏移
 * 会逐列表项/嵌套块漂移。但非空白字符序列两侧完全一致。单词边界上由此产生
 * 的空白歧义（"词末"还是"下一个词首"）由 `bias` 化解，它取自用户实际点击
 * 位置附近的内容。
 *
 * 已知漂移：两侧文本化结果不同的内容（mention 的显示名在只读渲染里是文本、
 * 在文档里是 atom——零文本）会让偏移在**那一个块内**偏；解析时会钳到块末，
 * 最坏情况光标落进同一块稍前的位置，绝不会跑到别的块。
 */

import type { Node as PMNode } from "@tiptap/pm/model";

/** 文本锚点：标识只读内容里的一个逻辑字符位置。 */
export interface TextAnchor {
  /** 顶层块的序号（只读根子元素 ↔ doc 子节点一一对应）。 */
  block: number;
  /** 该块内光标之前的非空白字符数。 */
  offset: number;
  /**
   * 当空白分隔第 offset 个非空白字符与下一个字符时，光标贴向哪一侧：
   * 1 = 恰好在下个非空白字符前（用户点了行/词首），
   * -1 = 恰好在上一个字符后（用户点了行/词尾）。
   */
  bias: 1 | -1;
}

const NON_WS = /\S/;

/** 数一段文本里的非空白字符个数。 */
function countNonWs(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) if (NON_WS.test(s.charAt(i))) n++;
  return n;
}

/**
 * 把只读渲染里的一个点击点解析成文本锚点。
 * `root` 是 markdown 容器，它的元素子节点与文档顶层块一一对应。
 * 该点落不出光标位置（API 不支持、点在内容下方的空白边距）时返回 null——
 * 调用方退回按坐标聚焦。
 */
export function anchorFromPoint(
  x: number,
  y: number,
  root: HTMLElement,
): TextAnchor | null {
  const doc = root.ownerDocument;
  let node: Node | null = null;
  let nodeOffset = 0;
  // 标准 API（Chromium/Firefox），然后是 WebKit 旧 API。
  if (typeof doc.caretPositionFromPoint === "function") {
    const p = doc.caretPositionFromPoint(x, y);
    if (p) {
      node = p.offsetNode;
      nodeOffset = p.offset;
    }
  } else if (typeof doc.caretRangeFromPoint === "function") {
    const r = doc.caretRangeFromPoint(x, y);
    if (r) {
      node = r.startContainer;
      nodeOffset = r.startOffset;
    }
  }
  if (!node || !root.contains(node) || node === root) return null;

  // 向上爬到顶层块（`root` 的直接子节点）。
  let blockEl: Node = node;
  while (blockEl.parentNode && blockEl.parentNode !== root) {
    blockEl = blockEl.parentNode;
  }
  if (blockEl.parentNode !== root || blockEl.nodeType !== Node.ELEMENT_NODE) {
    return null;
  }
  const block = Array.prototype.indexOf.call(root.children, blockEl);
  if (block < 0) return null;

  // 取块内光标前的文本。Range.toString() 同时处理文本节点光标与元素光标
  // （offset = 子节点下标），结果是该块 textContent 的前缀——用它还能偷看
  // 光标后紧邻的字符以定 bias。
  const range = doc.createRange();
  try {
    range.setStart(blockEl, 0);
    range.setEnd(node, nodeOffset);
  } catch {
    return { block, offset: 0, bias: 1 };
  }
  const before = range.toString();
  const nextChar = (blockEl.textContent ?? "").charAt(before.length);
  return {
    block,
    offset: countNonWs(before),
    bias: NON_WS.test(nextChar) ? 1 : -1,
  };
}

/**
 * 把文本锚点解析成 ProseMirror 位置。越界值会钳制（块 → 最后一块，
 * 偏移 → 块末），让过期或漂移的锚点仍落进预期块内而不是抛错。
 */
export function posFromAnchor(doc: PMNode, anchor: TextAnchor): number {
  if (doc.childCount === 0) return 0;
  if (anchor.block >= doc.childCount) return doc.content.size;
  const blockIndex = Math.max(0, anchor.block);

  let blockStart = 0;
  for (let i = 0; i < blockIndex; i++) blockStart += doc.child(i).nodeSize;
  const block = doc.child(blockIndex);
  const contentStart = blockStart + 1;

  const wanted = Math.max(0, anchor.offset);
  let seen = 0;
  // 光标位于第 `wanted` 个非空白字符之后（一个都还没过时，就是块内容起点）。
  let afterPrev = contentStart;
  let resolved: number | null = null;
  block.descendants((child, pos) => {
    if (resolved !== null) return false;
    if (!child.isText) return true;
    const text = child.text ?? "";
    for (let i = 0; i < text.length; i++) {
      if (!NON_WS.test(text.charAt(i))) continue;
      if (seen === wanted) {
        // 这是光标之后的第一个非空白字符。
        resolved = anchor.bias === 1 ? contentStart + pos + i : afterPrev;
        return false;
      }
      seen++;
      afterPrev = contentStart + pos + i + 1;
    }
    return true;
  });
  if (resolved !== null) return resolved;
  // 光标越过最后一个非空白字符：精确落在它之后；过冲（渲染器把文档没有的
  // 内容文本化，如 mention 标签）则钳到块内容末尾。
  return seen === wanted ? afterPrev : blockStart + block.nodeSize - 1;
}
