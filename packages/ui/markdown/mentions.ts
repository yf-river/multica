/**
 * 把旧版 mention 短码 [@ id="UUID" label="LABEL"] 转成标准 markdown 链接
 * 格式 [@LABEL](mention://member/UUID)。
 *
 * 这些短码存在于更早的 mention 序列化格式留下的旧数据库记录里。本函数把它们
 * 规范化，让下游解析器（Tiptap @tiptap/markdown、react-markdown）只需处理一种语法。
 *
 * 同步副本——必须与 packages/core/markdown/mention-shortcodes.ts 保持一致。
 * 移动端引入的是 core 的副本，因为 packages/ui/ 无法被移动端导入（见
 * apps/mobile/CLAUDE.md 的 Sharing Principles），而 packages/ui/ 也无法导入
 * packages/core/（见根 CLAUDE.md 的 Package Boundary Rules）。如果你改了这里的
 * 正则/行为，也要同步改 core 的副本——否则 web 和 mobile 渲染旧 mention 会不一致，
 * 破坏 "Counts must agree" 的一致性规则。
 */
export function preprocessMentionShortcodes(text: string): string {
  if (!text.includes("[@ ")) return text;
  return text.replace(
    /\[@\s+([^\]]*)\]/g,
    (match, attrString: string) => {
      const attrs: Record<string, string> = {};
      const re = /(\w+)="([^"]*)"/g;
      let m;
      while ((m = re.exec(attrString)) !== null) {
        if (m[1] && m[2] !== undefined) attrs[m[1]] = m[2];
      }
      const { id, label } = attrs;
      if (!id || !label) return match;
      return `[@${label}](mention://member/${id})`;
    },
  );
}
