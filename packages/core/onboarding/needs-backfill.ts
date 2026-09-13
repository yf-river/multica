// 本文件决定是否向「已完成引导的老用户」追问获客来源（source-backfill），
// 以及触发该追问的阈值常量。

import type { User } from "../types";
import type { QuestionnaireAnswers } from "./types";

/**
 * 用户用 X / ESC / 点击外部关掉 backfill 弹窗的最多次数；超过后视为永久
 * 关闭，弹窗不再出现。
 *
 * 提交和显式 Skip 永远是终局（都会写服务端）。计数只为「我晚点再想」这种
 * 关闭路径而设——没有上限的话，永远不表态的用户会在每次登录时都看到弹窗。
 */
export const SOURCE_BACKFILL_MAX_DISMISSALS = 3;

/**
 * 弹窗可能出现的条件：当前工作区里，被 AI 指派人（agent 或 squad）完成
 * 的 issue 数达到该下限。
 *
 * 引导阶段根本不问 source——归因对用户是零回报的问题，所以要等 Multica
 * 已被证明交付了价值（agent 完成了真实工作）才舍得花这份人情。而且「你从
 * 哪听说我们」的回答率在「激活时刻」之后也比注册时高得多。三个完成 issue
 * 足以证明一个投入的新用户已经体验过核心工作流。
 *
 * 计数本身来自 `./queries.ts` 的 `agentCompletedIssueCountOptions`；
 * 弹窗把它与 `needsSourceBackfill` 组合使用。
 */
export const SOURCE_BACKFILL_MIN_AGENT_DONE_ISSUES = 3;

/**
 * 该不该向这个已完成引导的用户追问「你从哪听说 Multica」？
 *
 * 满足以下条件返回 true：
 *  - 已完成引导（`onboarded_at` 已设置），且
 *  - 尚未记录任何来源（空数组或缺失），且
 *  - 之前没有拒绝过 source 问题（无 skip 标记），且
 *  - 没有把弹窗关到超过永久关闭的次数。
 *
 * 这是用户级的半边闸门。工作区级的半边闸门——「这里的 agent 是否已完成至少
 * SOURCE_BACKFILL_MIN_AGENT_DONE_ISSUES 个 issue」——需要一次服务端查询，
 * 所以放在弹窗组件里（`source-backfill-modal.tsx`），它也靠本谓词决定那一次
 * 查询到底值不值得发。
 *
 * 纯函数——`dismissCount` 由外部传入，让 core 可以调用（不依赖
 * localStorage / StorageAdapter）。
 *
 * @param user - 当前用户（可为空）
 * @param dismissCount - 已关闭弹窗的次数
 * @returns 是否需要追问来源
 */
export function needsSourceBackfill(
  user: User | null | undefined,
  dismissCount: number,
): boolean {
  if (!user) return false;
  if (!user.onboarded_at) return false;
  if (dismissCount >= SOURCE_BACKFILL_MAX_DISMISSALS) return false;

  const q = user.onboarding_questionnaire as
    | Partial<QuestionnaireAnswers>
    | null
    | undefined;
  if (!q) return true;
  if (q.source_skipped === true) return false;
  // 多选之前的老行把 `source` 写成裸字符串。非空字符串与单元素数组一视
  // 同仁——用户确实答过。与 `OnboardingFlow.mergeQuestionnaire`（views）和
  // `stringOrSlice.UnmarshalJSON`（服务端）保持一致。
  const raw: unknown = q.source;
  if (Array.isArray(raw)) return raw.length === 0;
  if (typeof raw === "string") return raw.length === 0;
  return true;
}
