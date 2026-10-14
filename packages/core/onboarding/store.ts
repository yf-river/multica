// 本文件实现 onboarding 的持久化动作：保存问卷、完成引导、加入云端 waitlist。

import { api } from "../api";
import { useAuthStore } from "../auth";
import { setPersonProperties } from "../analytics";
import type { OnboardingCompletionPath, QuestionnaireAnswers } from "./types";

/**
 * 持久化问卷答案（一次一个或多个槽位——每个引导步骤只 PATCH 自己的槽位），
 * 并把刷新后的用户同步进 auth store。真源是 `user.onboarding_questionnaire`
 * （服务端 JSONB 列），这里不做客户端缓存。
 *
 * 刻意不持久化「按步骤断点续走」：每次进入引导都从 Welcome 开始。已答过的
 * 槽位在重进时预填；跳过的槽位当作全新（这次用户可以作答）。
 *
 * @param answers - 要保存的问卷答案（部分）
 */
export async function saveQuestionnaire(
  answers: Partial<QuestionnaireAnswers>,
): Promise<void> {
  const user = await api.patchOnboarding({ questionnaire: answers });
  useAuthStore.getState().setUser(user);
  // 角色与用途用于个性化。
  const useCaseList = answers.use_case ?? [];
  if (answers.role || useCaseList.length > 0) {
    setPersonProperties({
      ...(answers.role ? { role: answers.role } : {}),
      ...(useCaseList.length > 0 ? { use_case: useCaseList } : {}),
    });
  }
}

/**
 * 完成引导。POST /complete 原子地标记 `onboarded_at`（用 COALESCE 保护幂等），
 * 并恰好发出一次 `onboarding_completed` 分析事件。随后刷新 auth store，让
 * 每个闸门都看到更新后的用户——最重要的是工作区布局的硬闸门：它会把未完成
 * 引导的用户重定向回 /onboarding。
 *
 * 这是前端唯一会翻转 `onboarded_at` 的机制。连了 runtime 的引导会在调用本
 * 函数前创建 Mika 并排入隐藏的开场回合；显式「不连 runtime」的路径可能在
 * 导航后种一条 setup-guide issue。
 *
 * `completionPath` 是客户端对自己走了哪个 Step-3 出口的判断；服务端会按它
 * 给 `onboarding_completed` 做漏斗切分。不传路径的旧调用方会被记成 `unknown`。
 *
 * @param completionPath - 完成路径（可选）
 * @param workspaceId - 工作区 id（可选）
 */
export async function completeOnboarding(
  completionPath?: OnboardingCompletionPath,
  workspaceId?: string,
): Promise<void> {
  await api.markOnboardingComplete(
    completionPath || workspaceId
      ? { completion_path: completionPath, workspace_id: workspaceId }
      : undefined,
  );
  await useAuthStore.getState().refreshMe();
}

/**
 * 记录对云 runtime 的兴趣。纯副作用——不会完成引导；用户仍须选一条真实的
 * Step-3 路径（检测到 runtime 的 CLI）或点 Skip 才能继续。
 *
 * 返回的 user 对象不同步进 auth store：这里没有任何用户可见字段
 * （`onboarded_at`、`UserResponse` 里的任何东西）真的变化。
 *
 * @param email - 邮箱
 * @param reason - 感兴趣的原因
 */
export async function joinCloudWaitlist(
  email: string,
  reason: string,
): Promise<void> {
  await api.joinCloudWaitlist({ email, reason });
}
