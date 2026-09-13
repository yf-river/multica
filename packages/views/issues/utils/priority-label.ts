// 优先级显示标签工具：把优先级 VALUE 翻译成用户看到的本地化名称。纯函数——
// 便于问题活动格式化器等「手里攥着一个 t 的普通函数」与组件共用。
import { PRIORITY_CONFIG } from "@multica/core/issues/config";
import type { IssuePriority } from "@multica/core/types";
import { useT } from "../../i18n";

/** issues 命名空间的 `t`——所有优先级名称都住在这里。 */
export type IssuesT = ReturnType<typeof useT<"issues">>["t"];

/**
 * 为优先级 VALUE 命名。五个内置优先级从 key 本地化，所以非英文成员绝不会读到
 * `PRIORITY_CONFIG` 的英文种子值；客户端旧到不认识的取值则回退成原值，而不是渲染
 * 空白。状态侧对应的工具是 `useStatusLabel`。
 *
 * 做成纯函数而不是 hook，让只持有一个被递进来的 `t` 的问题活动格式化器
 * 能和自己去调 `useT("issues")` 的组件共享它。
 * @param priority - 优先级 key。
 * @param t - issues 命名空间的翻译函数。
 * @returns 本地化的优先级名称。
 */
export function priorityLabel(priority: string, t: IssuesT): string {
  if (priority in PRIORITY_CONFIG) {
    return t(($) => $.priority[priority as IssuePriority]);
  }
  return priority;
}
