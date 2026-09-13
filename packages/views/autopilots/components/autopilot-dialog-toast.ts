// autopilot 创建/更新对话框"部分成功"toast 文案的集中格式化：i18n 键与 `{reason}`
// 占位符收在同一处、可被单测覆盖。没有它，`autopilot-dialog-i18n.test.ts` 里
// 的翻译契约可能通过、而对话框调用处悄悄传错变量名，把字面 `{{reason}}` 发给用户。

import type { TFunction } from "i18next";

/**
 * 生成"部分成功"toast 文案：调度保存成功但触发器创建失败时，向用户解释计划已存、
 * 触发未建。
 * @param t autopilots 命名空间的翻译函数。
 * @param kind 创建还是更新。
 * @param reason 服务器给的失败原因；有则拼进文案。
 * @returns 完整 toast 文案。
 */
export function formatSchedulePartialFailureToast(
  t: TFunction<"autopilots">,
  kind: "create" | "update",
  reason: string | null,
): string {
  if (reason) {
    return kind === "create"
      ? t(($) => $.dialog.toast_create_partial_with_reason, { reason })
      : t(($) => $.dialog.toast_update_partial_with_reason, { reason });
  }
  return kind === "create"
    ? t(($) => $.dialog.toast_create_partial)
    : t(($) => $.dialog.toast_update_partial);
}
