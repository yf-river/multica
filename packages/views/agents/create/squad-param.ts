// 本文件提供创建流程路由 URL 的拼接工具：把必须跨屏存活的两个参数
//（squad、runtime）挂在路径上。

/**
 * 构造一条创建路由的 URL，带上必须跨屏存活的参数。
 *
 * `squad` 是打开整个流程的缘由，所以从方式选择页一路跟到用户所选的方法页。
 *
 * `runtime` 是“种子”而非“身份”：会话只有在持有消息或已保存配置后才进入会话
 * 列表，所以第一轮对话时，用户刚选的运行环境还没法从任何地方读回。它挂在 URL
 * 而不是组件 state 里，这样第一轮对话刷新页面时，系统仍知道对话要跑在哪台机器。
 */
export function createPathWithParams(
  path: string,
  params: { squad?: string | null; runtime?: string | null },
): string {
  const query = new URLSearchParams();
  if (params.squad) query.set("squad", params.squad);
  if (params.runtime) query.set("runtime", params.runtime);
  const suffix = query.toString();
  return suffix ? `${path}?${suffix}` : path;
}

/** 只带 squad 参数的简写。 */
export function withSquadParam(path: string, squadId: string | null): string {
  return createPathWithParams(path, { squad: squadId });
}
