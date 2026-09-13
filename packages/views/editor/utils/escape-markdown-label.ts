/**
 * 转义会破坏 Markdown 链接/图片标签语法的字符：[ ] \ ( )。
 * 图片和文件卡片的 renderMarkdown 用它，防止原始文件名把
 * `![alt](url)` / `!file[name](url)` 的输出搞坏。
 */
export function escapeMarkdownLabel(text: string): string {
  return text.replace(/[[\]\\()]/g, (ch) => `\\${ch}`);
}
