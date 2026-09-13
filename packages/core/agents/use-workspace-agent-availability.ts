"use client";

// 判断"当前用户在这个工作区是否有可聊的 Agent"的三态 hook。
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "../hooks";
import { useAuthStore } from "../auth";
import { agentListOptions, memberListOptions } from "../workspace/queries";
import { canAssignAgentToIssue } from "../permissions";

/**
 * 三态"当前用户在本工作区是否有可对话的 Agent"。
 *
 * 为什么是三态而不是布尔：Agent 列表的答案在服务器上，在列表查询解析之前答案
 * 真的是"未知"。调用方必须区分"加载中"和"确认无"——把它们塌缩成布尔会让界面
 * 在挂载后的头几百毫秒闪烁禁用/空态，哪怕工作区其实有 Agent。
 *
 *   "loading"   —— Agent 或成员列表仍在飞行中（UI 保持中性）
 *   "none"      —— 两个查询都已解析，用户一个可指派的 Agent 都没有
 *   "available" —— 至少有一个 Agent 通过归档 + 可见性过滤
 */
export type WorkspaceAgentAvailability = "loading" | "none" | "available";

/**
 * 镜像 AssigneePicker 与聊天 Agent 下拉用的"逐 Agent 可见性/归档"过滤，
 * 让三处选择器对"这个 Agent 可不可达"永不一致。
 *
 * 成员也要查：`canAssignAgentToIssue` 要读调用者角色来决定 `private` Agent 的
 * 可见性——没有成员数据，一份刚加载完的 Agent 列表也可能给出错误答案。
 * @param enabled 是否允许发起查询（默认 true）。
 * @returns 三态可用性。
 */
export function useWorkspaceAgentAvailability(
  enabled = true,
): WorkspaceAgentAvailability {
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id);
  const { data: agents, isFetched: agentsFetched } = useQuery({
    ...agentListOptions(wsId),
    enabled,
  });
  const { data: members, isFetched: membersFetched } = useQuery({
    ...memberListOptions(wsId),
    enabled,
  });

  // 任一查询还没完成都算 loading——避免把"未知"误报成"无"。
  if (!agentsFetched || !membersFetched) return "loading";

  const rawRole = members?.find((m) => m.user_id === userId)?.role;
  const role =
    rawRole === "owner" || rawRole === "admin" || rawRole === "member"
      ? rawRole
      : null;

  const hasVisibleAgent = (agents ?? []).some(
    (a) =>
      !a.archived_at &&
      canAssignAgentToIssue(a, { userId: userId ?? null, role }).allowed,
  );

  return hasVisibleAgent ? "available" : "none";
}
