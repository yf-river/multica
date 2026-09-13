// 本文件登记「由本客户端发起的」工作区删除，供实时 `workspace:deleted` 处理器
// 区分「自己删的」与「别处删的」。

/**
 * 本客户端发起过 DELETE 的工作区 id 注册表。
 *
 * 在 useDeleteWorkspace.onMutate 里标记。实时 `workspace:deleted` 处理器会查它，
 * 对本客户端发起的删除直接空跑：mutation 流程自己负责存储清理与导航（两者都在
 * DELETE resolve 之后执行），若让处理器也反应，就会用它自己的整页跳转和这个流程
 * 的导航赛跑。
 *
 * 仅在 DELETE 失败时摘除标记——工作区仍存在，之后同一 id 被外部删除仍须正常
 * 处理。成功后该 id 就彻底没了，保留标记无论 WS 回显何时到达都能压掉我们这次
 * 删除的回声。
 *
 * 用模块级作用域而不是 React 状态，因为实时处理器运行在组件树之外。天然按标签页
 * 隔离：其他标签页/设备注册表为空，它们的处理器会正常处理该事件。
 */
const pendingDeletes = new Set<string>();

/** 标记某个工作区删除已由本客户端发起。 */
export function markWorkspaceDeletePending(workspaceId: string) {
  pendingDeletes.add(workspaceId);
}

/** 摘掉标记（DELETE 失败时用）。 */
export function unmarkWorkspaceDeletePending(workspaceId: string) {
  pendingDeletes.delete(workspaceId);
}

/** 判断该工作区的删除是否由本客户端发起。 */
export function isWorkspaceDeletePending(workspaceId: string): boolean {
  return pendingDeletes.has(workspaceId);
}
