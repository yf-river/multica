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

/** 个性化问卷。source 相关字段仅兼容已有数据，不用于收集或推广。 */
export interface QuestionnaireAnswers {
  source: string[];
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
