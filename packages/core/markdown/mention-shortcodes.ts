// 本文件把旧版「提及短代码」[ @ id="UUID" label="LABEL" ] 预处理成标准
// Markdown 链接形态 [@LABEL](mention://member/UUID)，让所有客户端只需处理
// 一种语法。
//
// 这些短代码存在于早期序列化格式留下的历史数据里。本函数把它们归一化，
// 让下游解析器（Web/桌面端 Tiptap 的 @tiptap/markdown、apps/mobile/lib/
// markdown/ 的移动端渲染器）只认一种语法。
//
// 这里对所有客户端都是唯一真源。移动端直接 import 本函数——移动端被允许从
// @multica/core/ 导入纯函数；Web/桌面端继续经由 @multica/ui/markdown 访问
// （它现在从这里转发导出），所以既有导入路径不受影响。
//
// 纯正则变换——无 IO、无全局状态。幂等：对同一输入跑两遍结果相同。
/**
 * 把旧版提及短代码转换成标准 Markdown 提及链接。
 *
 * @param text - 可能含旧短代码的正文
 * @returns 归一化后的正文（无旧短代码时原样返回）
 */
export function preprocessMentionShortcodes(text: string): string {
  if (!text.includes("[@ ")) return text;
  return text.replace(/\[@\s+([^\]]*)\]/g, (match, attrString: string) => {
    const attrs: Record<string, string> = {};
    const re = /(\w+)="([^"]*)"/g;
    let m;
    while ((m = re.exec(attrString)) !== null) {
      if (m[1] && m[2] !== undefined) attrs[m[1]] = m[2];
    }
    const { id, label } = attrs;
    if (!id || !label) return match;
    return `[@${label}](mention://member/${id})`;
  });
}
