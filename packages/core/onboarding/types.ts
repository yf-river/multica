// 本文件定义 onboarding（新用户引导）的类型：步骤、完成路径与问卷答案结构。

/** 引导流程里会持久化的步骤。 */
export type OnboardingStep =
  | "welcome"
  | "about_you"
  | "workspace"
  | "runtime";

/**
 * 用户离开引导流程的「出口路径」。随 POST /api/me/onboarding/complete 发送，
 * 也会镜像到 PostHog 的 `onboarding_completed` 事件。必须与
 * `server/internal/analytics/events.go` 里的 `OnboardingPath*` 常量保持同步。
 */
export type OnboardingCompletionPath =
  | "full"
  | "runtime_skipped"
  | "cloud_waitlist"
  | "skip_existing"
  | "invite_accept";

/** 「你是怎么知道 Multica 的」的选项。 */
export type Source =
  | "friends_colleagues"
  | "search"
  | "social_x"
  | "social_linkedin"
  | "social_youtube"
  | "social_github"
  | "social_other"
  | "blog_newsletter"
  | "ai_assistant"
  | "from_work"
  | "event_conference"
  | "dont_remember"
  | "other";

/** 「你主要做什么」的角色选项。 */
export type Role =
  | "engineer"
  | "product"
  | "designer"
  | "founder"
  | "marketing"
  | "writer"
  | "research"
  | "ops"
  | "student"
  | "other";

/** 「你想拿 Multica 做什么」的用途选项。 */
export type UseCase =
  | "ship_code"
  | "manage_team"
  | "personal_tasks"
  | "plan_research"
  | "write_publish"
  | "automate_ops"
  | "evaluate"
  | "other";

/**
 * 问卷答案结构。`use_case` 允许多选（用户可能同时为好几件事雇佣 Multica）；
 * `source` 和 `role` 是单选——`source` 只记录主要获客渠道，便于做干净的自述
 * 归因统计（数组形态是为兼容 v2 多选行保留的；客户端现在总是提交单元素数组），
 * `role` 保持单选是因为下游个性化需要「一个」主要身份。
 *
 * `role` / `use_case` 在流程里的 About-you 步骤收集；`source` 不再在引导时
 * 询问——它改为在用户看过 agent 完成工作后，通过工作区内的 source-backfill
 * 弹窗收集（见 `needs-backfill.ts`）。三者仍放在同一个结构里，因为共享同一个
 * JSONB 列和 PATCH 接口。
 *
 * `*_skipped: true` 用来区分「显式跳过/拒绝」与「用户从没走到那个槽位」。
 * 对个性化而言两种都是「未知」；skip 标记是为分析服务的，也让未来再提示时
 * 不再打扰已拒绝过的用户。
 *
 * 向后兼容：本应用旧版本把 `source` 和 `use_case` 写成单个字符串。
 * `onboarding-flow.tsx` 里的 `mergeQuestionnaire` 在读取时把这些行升级成
 * 单元素数组；服务端的 `questionnaireAnswers.UnmarshalJSON` 同样处理。
 * `version` 保持在 2——JSONB 列无 schema，机械地升版本只会在分析里多一个
 * 值，存储不变；我们保持同一个漏斗队列。
 */
export interface QuestionnaireAnswers {
  source: Source[];
  source_other: string | null;
  source_skipped: boolean;
  role: Role | null;
  role_other: string | null;
  role_skipped: boolean;
  use_case: UseCase[];
  use_case_other: string | null;
  use_case_skipped: boolean;
  version: 2;
}
