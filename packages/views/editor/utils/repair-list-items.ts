import { TextSelection } from "@tiptap/pm/state";
import type { Editor } from "@tiptap/core";

/** ProseMirror JSON 节点的最小形态——够用来遍历和修补即可。 */
interface JsonNode {
  type?: string;
  content?: JsonNode[];
  [key: string]: unknown;
}

/**
 * 确保每个列表项都以段落开头。
 *
 * `listItem`/`taskItem` 的内容结构是 `paragraph block*`，所以合法列表项的
 * 第一个子节点必须是段落。`@tiptap/markdown` 解析空列表项时有两种违反：
 * 顶层空项（`"1. "`）会被解析成没有子节点的项；嵌套空项（`"1. \n   1. "`）
 * 会解析成唯一子节点是子列表的外层项。两者都不合 schema，且会让文档失去
 * 文本光标。这里先深度优先递归，再给任何不以段落开头的项前插一个空段落。
 * Attr（如任务项的 `checked`）会保留。
 */
function repairListItems(node: JsonNode): { node: JsonNode; changed: boolean } {
  let changed = false;
  let content = node.content;

  if (content) {
    const mapped = content.map((child) => {
      const result = repairListItems(child);
      if (result.changed) changed = true;
      return result.node;
    });
    if (changed) content = mapped;
  }

  if (node.type === "listItem" || node.type === "taskItem") {
    if (!content || content.length === 0 || content[0]?.type !== "paragraph") {
      content = [{ type: "paragraph" }, ...(content ?? [])];
      changed = true;
    }
  }

  return changed ? { node: { ...node, content }, changed } : { node, changed };
}

/**
 * 修复 markdown 解析出的空列表项，让文档重新拥有合法的文本光标。
 *
 * 在评论里输入 `1.`（或 `- `）后留下的文档，会以草稿 `"1. \n\n"` 持久化。
 * 重新挂载时 `@tiptap/markdown` 把那个空项解析成不含子节点、不合 schema
 * 的 `listItem`（见 {@link repairListItems}）；随后文档只剩 `AllSelection`
 * 而没有一个光标，浏览器就把插入符画到下一个块上——即被报告的那个 bug：
 * “输入 `1.`，切走工单再切回来，光标跳到列表项外面，怎么都移不回去。”
 * 注意这只影响*空*项的往返；非空项（`"1. buy milk"`）会逐字节原样往返。
 *
 * 传入 `preferredSelection`（外部重新解析前捕获的光标）时，会把它贴回
 * 最近的合法文本位置；否则光标落在第一个合法文本位置（列表项开头的段落）。
 * 若每个列表项本来就合法，则什么都不做，普通内容不受影响。
 *
 * @returns 是否真的修复了文档（因此也重置了光标）。
 */
export function repairEmptyListItems(
  editor: Editor,
  preferredSelection?: { from: number; to: number },
): boolean {
  if (editor.isDestroyed) return false;

  const { node: repaired, changed } = repairListItems(
    editor.getJSON() as JsonNode,
  );
  if (!changed) return false;

  // 损坏的解析留下了 `AllSelection`。若在选区仍存活的时重设内容，会让
  // ProseMirror 的 replace 把整个列表压成裸段落，所以要先把选区钉在真实位置。
  editor.view.dispatch(
    editor.state.tr
      .setSelection(TextSelection.near(editor.state.doc.resolve(0)))
      .setMeta("addToHistory", false),
  );

  // 用修好的树重建内容。这是对外部解析内容的规范化，不是用户的编辑操作：
  // `setMeta("addToHistory", false)` 让它不进撤销栈（否则 Ctrl-Z 会把坏项
  // 又还回来），`emitUpdate: false` 则避免把结果写回草稿/服务端造成自回写。
  editor
    .chain()
    .setMeta("addToHistory", false)
    .setContent(repaired, { emitUpdate: false })
    .run();

  const { doc } = editor.state;
  const size = doc.content.size;
  const clamp = (pos: number) => Math.min(Math.max(pos, 0), size);
  const anchor = preferredSelection ? clamp(preferredSelection.from) : 0;
  const head = preferredSelection ? clamp(preferredSelection.to) : 0;
  editor.view.dispatch(
    editor.state.tr
      .setSelection(TextSelection.between(doc.resolve(anchor), doc.resolve(head)))
      .setMeta("addToHistory", false),
  );
  return true;
}
