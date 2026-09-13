/**
 * 富文本模块：remark 插件，修复 AI 生成的中文加粗尾部多余空格。
 * 例如把 `**中文。 **next` 修复成等价于 `**中文。** next` 的形式。
 */
import type { PhrasingContent, Root, Strong, Text } from "mdast";
import { visit } from "unist-util-visit";

// 中日韩文字符集合：汉字、平假名、片假名、韩文。
const CJK_SCRIPT = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}]/u;

function text(value: string): Text {
  return { type: "text", value };
}

function strong(value: string): Strong {
  return { type: "strong", children: [text(value)] };
}

function repairTextValue(value: string): PhrasingContent[] | null {
  const replacement: PhrasingContent[] = [];
  let consumed = 0;
  let cursor = 0;

  while (cursor < value.length - 1) {
    const opening = value.indexOf("**", cursor);
    if (opening === -1) break;

    const firstInner = value[opening + 2];
    if (firstInner == null || /\s|\*/u.test(firstInner)) {
      cursor = opening + 2;
      continue;
    }

    // 不要跨过另一个分隔符串去查找：那会把那段畸形范围之外的、合法或
    // 有意保持字面的 Markdown 重新解释掉。
    const closing = value.indexOf("**", opening + 2);
    if (closing === -1) break;

    const afterClosing = value[closing + 2];
    if (
      afterClosing == null ||
      /\s/u.test(afterClosing) ||
      (value[closing - 1] !== " " && value[closing - 1] !== "\t")
    ) {
      cursor = opening + 2;
      continue;
    }

    let whitespaceStart = closing - 1;
    while (
      whitespaceStart > opening + 2 &&
      (value[whitespaceStart - 1] === " " || value[whitespaceStart - 1] === "\t")
    ) {
      whitespaceStart--;
    }

    const inner = value.slice(opening + 2, whitespaceStart);
    if (!CJK_SCRIPT.test(inner) || !/\S/u.test(inner)) {
      cursor = opening + 2;
      continue;
    }

    if (opening > consumed) replacement.push(text(value.slice(consumed, opening)));
    replacement.push(strong(inner), text(value.slice(whitespaceStart, closing)));
    consumed = closing + 2;
    cursor = consumed;
  }

  if (replacement.length === 0) return null;
  if (consumed < value.length) replacement.push(text(value.slice(consumed)));
  return replacement;
}

/**
 * 在 Markdown 解析后恢复截图里常见的 AI 书写形式：
 * `**中文。 **next` 变成等价于 `**中文。** next`。
 *
 * 这个转换器刻意比原始字符串改写更窄。它只访问包含 CJK 字符的已解析文本
 * 节点，所以代码、合法的 Markdown 节点和非 CJK 文本都保持原语义。
 * 源文本与值相等的校验也排除了那些 AST 文本已不再匹配作者字面输入的
 * 转义 Markdown。
 */
export function remarkRepairCjkStrongTrailingWhitespace() {
  return (tree: Root, file: { value: unknown }) => {
    const source = String(file.value ?? "");

    visit(tree, "text", (node, index, parent) => {
      if (index == null || parent == null || parent.type === "strong") return;

      const start = node.position?.start.offset;
      const end = node.position?.end.offset;
      if (start == null || end == null || source.slice(start, end) !== node.value) {
        return;
      }

      const replacement = repairTextValue(node.value);
      if (!replacement) return;
      parent.children.splice(index, 1, ...replacement);
    });
  };
}
