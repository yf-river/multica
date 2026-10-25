// 被阻止的 @agent / @squad 触发结果的本地化文案。输入框预览 chip 和发送后的 toast 共用，
// 保证两处用同样的措辞描述同一个原因。`reason_code` 是线路上枚举安全的代码；它映射出的
// 文案绝不暴露目标身份——目标名字由调用方从用户自己 @ 出来的标记里提供。
//
// 文案不能断言代码本身没携带的原因。`invocation_not_allowed` 在服务端是刻意模糊的
// （见 dispatch/reason.go）：它同时覆盖「你无权调用该目标」和「这个 id 在这里解析不到任何东西」。
// 如果文案只怪权限，会让人因为一个其实只是 mention uuid 打错了的问题，跑去排查智能体的可见性设置
// （MUL-5548），所以两种文案都把这两种可能都点出来。
//
// `agent_runtime_required` 和 `runtime_offline` 也是同理才分开的（MUL-5559）：
// 未绑定的智能体根本没有机器可重连，用「运行时离线」的文案会让用户去找一台不存在的电脑。
// 它真正需要的修复是把智能体绑定到某个运行时。
//
// `runtime_unusable` 是这一族的第三个成员（MUL-6164）：机器是可达的，但上面的智能体 CLI
// 无法执行，用「离线」文案会让用户去重连一个本来就已连接的东西。它需要的修复是在那台机器上
// 重装，服务端留在问题上的系统评论会带精确的命令。
import type { useT } from "../i18n";

type IssuesT = ReturnType<typeof useT<"issues">>["t"];

/**
 * 返回完整的阻断原因句子——用于提示框等有空间展开说明的地方。
 * @param reasonCode - 服务端返回的阻断原因代码。
 * @param t - issues 命名空间的翻译函数。
 * @returns 对应的本地化文案。
 */
export function blockedReasonLabel(reasonCode: string, t: IssuesT): string {
  switch (reasonCode) {
    case "invocation_not_allowed":
      return t(($) => $.comment.trigger_blocked_invocation_not_allowed);
    case "target_unavailable":
      return t(($) => $.comment.trigger_blocked_target_unavailable);
    case "runtime_offline":
      return t(($) => $.comment.trigger_blocked_runtime_offline);
    case "runtime_unusable":
      return t(($) => $.comment.trigger_blocked_runtime_unusable);
    case "agent_runtime_required":
      return t(($) => $.comment.trigger_blocked_agent_runtime_required);
    case "runtime_access_denied":
      return t(($) => $.comment.trigger_blocked_runtime_access_denied);
    default:
      return t(($) => $.comment.trigger_blocked_generic);
  }
}

/**
 * 返回简短的阻断原因徽标——用于行内 chip 和 toast，那里目标名字已经交代了「是谁」，
 * 原因只需用一两个词说明「为什么」。
 * @param reasonCode - 服务端返回的阻断原因代码。
 * @param t - issues 命名空间的翻译函数。
 * @returns 对应的简短本地化文案。
 */
export function blockedShortReasonLabel(reasonCode: string, t: IssuesT): string {
  switch (reasonCode) {
    case "invocation_not_allowed":
      return t(($) => $.comment.trigger_blocked_short_invocation_not_allowed);
    case "target_unavailable":
      return t(($) => $.comment.trigger_blocked_short_target_unavailable);
    case "runtime_offline":
      return t(($) => $.comment.trigger_blocked_short_runtime_offline);
    case "runtime_unusable":
      return t(($) => $.comment.trigger_blocked_short_runtime_unusable);
    case "agent_runtime_required":
      return t(($) => $.comment.trigger_blocked_short_agent_runtime_required);
    case "runtime_access_denied":
      return t(($) => $.comment.trigger_blocked_short_runtime_access_denied);
    default:
      return t(($) => $.comment.trigger_blocked_short_generic);
  }
}
