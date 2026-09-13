/**
 * 把斜杠命令的标签格式化成可显示的文本。
 *
 * @param label 命令项原始 label；非字符串或全空白时退回 "?"，
 *              保证下拉菜单不会出现空白项。
 */
export function formatSlashCommandLabel(label: unknown): string {
  return typeof label === "string" && label.trim().length > 0 ? label : "?";
}
