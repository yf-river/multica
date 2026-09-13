"use client";

/** 智能体详情页路由：从 URL 参数解出 agentId 后渲染共享详情视图。 */
import { use } from "react";
import { AgentDetailPage } from "@multica/views/agents/agent-detail-page";

/**
 * 智能体详情路由。
 *
 * @param props.params - Next.js 注入的动态参数（含智能体 id，Promise 形式）。
 * @returns 智能体详情页。
 */
export default function AgentDetailRoute({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <AgentDetailPage agentId={id} />;
}
