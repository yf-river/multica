import {
  preprocessLinks,
  preprocessMentionShortcodes,
  preprocessFileCards,
  preprocessIssueIdentifiers,
} from "@multica/ui/markdown";
import { stripChannelMediaMarkers } from "@multica/core/types";

/**
 * 在把 markdown 以 contentType: 'markdown' 载入 Tiptap 前，对字符串做预处理。
 *
 * 这是 @tiptap/markdown 解析前唯一施加的转换。它不做 HTML 转换——那是
 * 2026 年 4 月重构时删掉的旧 markdownToHtml.ts 管线的活。
 *
 * 对原始 Markdown 施加的 string→string 变换：
 * 1. 旧式 mention 短码 [@ id="..." label="..."] → [@Label](mention://member/id)
 *    （数据库里的旧序列化格式，读取时迁移）
 * 2. （仅只读）裸 issue 标识符 MUL-123 → [MUL-123](mention://issue/MUL-123)
 * 3. 裸 URL → 经 linkify-it 变 markdown 链接（这样能渲染成可点击的 Link 节点）
 * 4. 文件卡片语法（新版 !file[name](url) + 旧版 [name](cdnUrl)）→ HTML div，
 *    供 fileCard 节点解析
 *
 * Tiptap 编辑器与只读 react-markdown 渲染器共用此函数，保证两者 linkify
 * 行为一致。`autolinkIssueIdentifiers` 是唯一一处刻意的差异：它是选开
 * （OPT-IN）的，在可编辑的 Tiptap 路径上必须保持关闭——因为在那里改写裸
 * 标识符会造出一个 id 为标识符字符串（而非真实 UUID）的 mention 节点，
 * 污染保存下来的 markdown。只有只读渲染器（在渲染时才把标识符解析成
 * UUID）会传它。
 *
 * `cdnDomain` 作为显式参数，而不是在函数内命令式读 `configStore.getState()`。
 * CDN 配置在鉴权后异步到达，一次性读取会让这个转换悄悄变成依赖时机：
 * 配置到达前渲染的内容会永远把旧 CDN 链接留成普通锚点，因为没有东西会
 * 重跑这次转换。把值传进来，能让响应式的调用方（RichContent，订阅了该
 * store）把它放进 memo 依赖；一次性调用方（Tiptap 编辑器，只在载入时
 * 预处理一次）则仍可自己读 store。
 */
export function preprocessMarkdown(
  markdown: string,
  opts: { cdnDomain: string; autolinkIssueIdentifiers?: boolean },
): string {
  if (!markdown) return "";
  const { cdnDomain } = opts;
  // Channel-media 的来源标记是为乐观合并安全而持久化的，不是用户创作的内容。
  // 无论可编辑的 Tiptap 解析器还是只读渲染器看到它之前，都只移除这个带
  // 命名空间的注释；ContentEditor 另把原始受控值留作服务端合并基准。
  const visibleMarkdown = stripChannelMediaMarkers(markdown);
  const step1 = preprocessMentionShortcodes(visibleMarkdown);
  const step2 = opts?.autolinkIssueIdentifiers
    ? preprocessIssueIdentifiers(step1)
    : step1;
  const step3 = preprocessLinks(step2);
  const step4 = preprocessFileCards(step3, cdnDomain);
  return step4;
}
