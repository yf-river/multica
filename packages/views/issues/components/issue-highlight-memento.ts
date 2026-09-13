// 本文件：issueHighlightMemento —— 问题行高亮"记忆"的 key 约定。
// 提供唯一的记忆 key 构造函数，供"从列表跳回某个问题后短暂高亮该行"的
// 滚动定位/高亮机制跨模块复用。

/** 构造问题高亮记忆条目的 key（如 `highlight:<issueId>`），
 *  用于按问题 id 读写"高亮过的行"这类会话级状态。 */
export function issueHighlightMementoKey(issueId: string): string {
  return `highlight:${issueId}`;
}
