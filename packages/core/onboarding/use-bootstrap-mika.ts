// 创建或复用工作区的 Mika 和当前成员的普通会话。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { chatKeys } from "../chat/queries";
import type { ChatSession, MikaBootstrapResponse } from "../types";
import { workspaceKeys } from "../workspace/queries";

/** 引导 Mika 的输入参数。 */
export interface BootstrapMikaInput {
  /**
   * 正在配置的工作区 slug。下面每次调用都显式带它，而不是依赖全局的
   * 「当前工作区」单例：这套流程作用于应用还没导航过去的工作区，而且桌面端
   * 的标签系统也会写那个单例——流程中途它可能已被回收，Mika 会被建到
   * 之前激活的工作区里。
   */
  workspaceSlug: string;
  runtimeId: string;
  /** Mika 用的模型。留空回退到 runtime 自己的默认模型。 */
  model?: string;
  /** 开场会话的（本地化）标题。 */
  title: string;
}

/** 引导 Mika 的核心结果。 */
export interface BootstrapMikaResult {
  agent: MikaBootstrapResponse;
  chatSession: ChatSession;
}

/** 创建或复用 Mika 及当前成员的普通会话。 */
export async function bootstrapMika(input: BootstrapMikaInput): Promise<BootstrapMikaResult> {
  const agent = await api.createMikaAgent(
    {
      runtime_id: input.runtimeId,
      model: input.model,
      session_title: input.title,
    },
    input.workspaceSlug,
  );

  // 服务端把 agent 和会话一起解析出来。旧实现是先列出该成员的会话、看到标题
  // 不匹配就新建一个——先查后插且没有任何东西做串行化，于是两个标签页会各开
  // 各的引导会话。标题只是展示文案，绝不能定义会话身份。
  const chatSession = agent.onboarding_session;
  if (!chatSession) {
    throw new Error("Mika session was not returned");
  }

  return { agent, chatSession };
}

/** React Query 版本的引导 Mika：成功后失效相关工作区/会话/消息缓存。 */
export function useBootstrapMika(workspaceId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: BootstrapMikaInput) => bootstrapMika(input),
    onSuccess: ({ chatSession }) => {
      return Promise.all([
        queryClient.invalidateQueries({
          queryKey: workspaceKeys.agents(workspaceId),
        }),
        queryClient.invalidateQueries({
          queryKey: chatKeys.sessions(workspaceId),
        }),
        queryClient.invalidateQueries({
          queryKey: chatKeys.messages(chatSession.id),
        }),
        queryClient.invalidateQueries({
          queryKey: chatKeys.messagesPage(chatSession.id),
        }),
      ]);
    },
  });
}
