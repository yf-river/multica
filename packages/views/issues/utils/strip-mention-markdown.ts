/**
 * 把提及的 markdown 语法剥成纯文本。
 *
 * 处理：
 * - 普通提及：`[@名字](mention://agent/id)` → `@名字`
 * - 名字里的转义括号：`[@David\[TF\]](mention://agent/id)` → `@David[TF]`
 * - 问题提及（没有 @）：`[MUL-123](mention://issue/id)` → `MUL-123`
 * - 不碰普通 markdown 链接：`[docs](https://...)` 原样保留
 * - 不碰反斜杠转义的提及：`\[@名字](mention://...)` 原样保留
 *
 * 正则在镜像 mention-extension.ts 里的分词器。
 * @param text - 含提及 markdown 的文本。
 * @returns 剥去提及语法后的纯文本。
 */
export function stripMentionMarkdown(text: string): string {
  return text.replace(
    /(?<![\\])\[(@?)((?:\\.|[^\]])+)\]\(mention:\/\/\w+\/[^)]+\)/g,
    (_, prefix: string, rawLabel: string) => {
      const label = rawLabel.replace(/\\\[/g, "[").replace(/\\\]/g, "]");
      return `${prefix}${label}`;
    },
  );
}
