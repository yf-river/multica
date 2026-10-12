// "立即运行"结果的 toast 归类（MUL-4525）。纯函数以便不挂载页面就能对每种运行
// 状态单测。
//
// 响应 schema 刻意接受任意状态字符串以向前兼容，所以成功必须是白名单——绝不是
// "除了 skipped/failed 都是成功"。未来或异常但可解析的状态（如 "blocked"、
// "deferred"）必须降级为错误 toast，绝不误报 "triggered"。

export type RunNowToastKind = "success" | "warning" | "error";

// 只有明确的启动状态算成功；其余都是需要向用户警告/报错的非成功结果。
export function runNowToastKind(status: string | undefined): RunNowToastKind {
  switch (status) {
    case "issue_created":
    case "running":
      return "success";
    case "skipped":
      // 准入被拦 / 目标未就绪——可恢复，属信息性提示。
      return "warning";
    case "failed":
      return "error";
    default:
      // 未知 / 未来状态：绝不宣称成功。
      return "error";
  }
}

// 说明非成功运行为何没触发的 i18n 键（位于 autopilots 的 `detail` 命名空间），
// 以服务器稳定的 reason_code 为键。未知/缺失的 code 降级为通用文案。
export type RunNowBlockedKey =
  | "run_blocked_invocation_not_allowed"
  | "run_blocked_runtime_offline"
  | "run_blocked_agent_runtime_required"
  | "run_blocked_target_unavailable"
  | "run_blocked_attribution"
  | "run_blocked_already_active"
  | "run_blocked_generic";

/**
 * 把服务器的 reason_code 映射到 i18n 键；未知 code 归为通用键。
 */
export function runNowBlockedKey(reasonCode: string | undefined): RunNowBlockedKey {
  switch (reasonCode) {
    case "invocation_not_allowed":
      return "run_blocked_invocation_not_allowed";
    case "runtime_offline":
      return "run_blocked_runtime_offline";
    // 是"未绑定"而非"离线"：智能体绑到某个运行时之前，不会有东西来认领这次运行
    // （MUL-5559）。
    case "agent_runtime_required":
      return "run_blocked_agent_runtime_required";
    case "target_unavailable":
      return "run_blocked_target_unavailable";
    case "attribution_blocked":
      return "run_blocked_attribution";
    case "already_active":
      return "run_blocked_already_active";
    default:
      return "run_blocked_generic";
  }
}
