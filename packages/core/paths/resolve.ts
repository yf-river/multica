// 本文件决定「认证之后跳去哪里」以及「用户是否已完成引导」。

import type { Workspace } from "../types";
import { useAuthStore } from "../auth";
import { paths } from "./paths";

/**
 * 解析认证后的默认落地路径。优先级（已完成引导优先）：
 *   !hasOnboarded               → /onboarding
 *   hasOnboarded + workspace[0] → /<first.slug>/issues
 *   hasOnboarded + no workspace → /workspaces/new
 *
 * V3 不变量：`onboarded_at != null` 是「可否访问 /<slug>/*」的唯一真源。工作区
 * layout 与其路由守卫都以此设闸——把未引导用户直接送去 /issues，只会被 layout
 * 的闸门弹回 /onboarding，白费一次导航往返。所以先查 onboarded。
 *
 * 在 v3 里「有工作区但 !onboarded」物理上很少见（用户只有在第 2 步与第 3 步之间
 * 关掉应用才会落到这种状态——问卷与 runtime 选择都在工作区创建之后、完成引导
 * 之前运行）。OnboardingFlow 的第 2 步已经认得已有工作区并提供「继续 {name}」，
 * 所以恢复是无缝的。
 *
 * 需要邀请感知路由的调用方（callback / login）会在调用本解析器之前自己处理
 * 「未引导但有待处理邀请」的分支——本解析器只处理「已查过邀请之后」的目的地。
 *
 * @param workspaces - 当前用户的工作区列表
 * @param hasOnboarded - 是否已完成引导
 * @returns 认证后应跳转的路径
 */
export function resolvePostAuthDestination(
  workspaces: Workspace[],
  hasOnboarded: boolean,
): string {
  if (!hasOnboarded) {
    return paths.onboarding();
  }
  const first = workspaces[0];
  if (first) {
    return paths.workspace(first.slug).issues();
  }
  return paths.newWorkspace();
}

/**
 * 是否已完成引导。唯一真源：`users.onboarded_at`，随每次认证响应的 user 对象
 * 一起到达。
 */
export function useHasOnboarded(): boolean {
  return useAuthStore((s) => s.user?.onboarded_at != null);
}
