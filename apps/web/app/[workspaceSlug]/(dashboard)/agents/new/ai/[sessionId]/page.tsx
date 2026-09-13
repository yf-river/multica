"use client";

/** AI 对话式创建智能体的会话页路由：解出 sessionId 渲染对应搭建会话。 */
import { use } from "react";
import { AiBuilderSessionPage } from "@multica/views/agents/create/ai-session";

/**
 * AI 创建会话路由。
 *
 * @param props.params - 动态参数，含会话 id（Promise 形式）。
 * @returns 对应会话的 AI 搭建页。
 */
export default function NewAgentAiSessionRoute({
  params,
}: {
  params: Promise<{ sessionId: string }>;
}) {
  const { sessionId } = use(params);
  return <AiBuilderSessionPage sessionId={sessionId} />;
}
