// 本文件：task-run-labels —— 代理运行（AgentTask）的展示文案工具。
// 凡列出某个问题代理运行的界面（执行日志行、用量详情对话框）都共用这里的
// 标签；放在本文件而非任一组件里，正是为了两者不漂移——同一次运行在侧栏
// 与它打开的表格里必须读起来一致。

import type { AgentTask } from "@multica/core/types";
import { useT } from "../../i18n";
import { stripMentionMarkdown } from "../utils/strip-mention-markdown";

// 所有"列出某问题代理运行"的界面共用的展示标签：
// 执行日志行与用量详情对话框。放在这里而非其中任一组件里，
// 避免二者漂移——同一次运行在侧栏与打开的表格里必须读起来一样。

/**
 * 这次运行是由什么触发的，给出人话标签。
 *
 * 首选来源：任务创建当时拍下的权威快照（评论文本 / autopilot 标题）。
 * 它能扛住原文被改被删，且信息密度高——远胜一条结构化的标签。
 *
 * 重试任务在数据库侧继承了父任务的 trigger_summary（快照能跨尝试存活），
 * 但只显示继承摘要的行与其父任务无从区分。当设置了 parent_task_id 时，
 * 前面加"重试 #N"前缀，让重试即使带着继承的摘要也能一眼认出是重试。
 *
 * 回退链面向"快照字段上线前创建的旧任务"，或我们不做快照的来源
 *（直接指派 / 聊天）：按触发来源退化成简短的结构化标签。
 * 新任务（061 迁移之后）几乎总走快照路径。
 */
export function useTriggerText(task: AgentTask): string {
  const { t } = useT("issues");
  const isRetry = !!task.parent_task_id;
  const retryPrefix = isRetry
    ? task.attempt && task.attempt > 1
      ? t(($) => $.execution_log.trigger_retry_attempt_prefix, { attempt: task.attempt })
      : t(($) => $.execution_log.trigger_retry_prefix)
    : "";

  if (task.trigger_summary) return retryPrefix + stripMentionMarkdown(task.trigger_summary);
  if (isRetry) {
    return task.attempt && task.attempt > 1
      ? t(($) => $.execution_log.trigger_retry_attempt, { attempt: task.attempt })
      : t(($) => $.execution_log.trigger_retry);
  }
  if (task.autopilot_run_id) return t(($) => $.execution_log.trigger_autopilot);
  if (task.trigger_comment_id) return t(($) => $.execution_log.trigger_comment);
  // 由指派触发、且带交接（handoff）备注的运行：像评论触发展示文本那样
  // 把备注内联展示出来（超出由调用方截断），让该行读起来就是一条交接，
  // 而不是笼统的"初始运行"。
  if (task.handoff_note) {
    return retryPrefix + t(($) => $.execution_log.trigger_handoff_prefix) + stripMentionMarkdown(task.handoff_note);
  }
  return t(($) => $.execution_log.trigger_initial);
}

/** 运行状态的 i18n 可读标签（排队/派发/等待本地目录/运行中/完成/失败/取消）。 */
export function useStatusLabel(status: AgentTask["status"], failureReason?: string | null): string {
  const { t } = useT("issues");
  if (failureReason === "release_pause") return t(($) => $.execution_log.status_release_paused);
  switch (status) {
    case "waiting_plan": return t(($) => $.execution_log.status_waiting_plan);
    case "waiting_input": return t(($) => $.execution_log.status_waiting_input);
    case "queued": return t(($) => $.execution_log.status_queued);
    case "dispatched": return t(($) => $.execution_log.status_dispatched);
    case "waiting_local_directory":
      return t(($) => $.execution_log.status_waiting_local_directory);
    case "running": return t(($) => $.execution_log.status_running);
    case "completed": return t(($) => $.execution_log.status_completed);
    case "failed": return t(($) => $.execution_log.status_failed);
    case "cancelled": return t(($) => $.execution_log.status_cancelled);
    default: return status;
  }
}

/**
 * 已结束运行的墙钟耗时；任务从未开始或从未结束时返回 ""。
 * 刻意不为"运行中"任务计算：执行日志已经为它们走着一个实时计时器，
 * 旁边再放一个冻结的时长数字会互相矛盾。
 */
export function taskDurationMs(task: AgentTask): number | null {
  const startIso = task.started_at ?? task.dispatched_at;
  if (!startIso || !task.completed_at) return null;
  const start = new Date(startIso).getTime();
  const end = new Date(task.completed_at).getTime();
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return null;
  return end - start;
}
