// 本文件实现「引导 Mika」：创建或复用工作区的 Mika，并启动它的开场会话。

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

/**
 * 创建或复用工作区的 Mika，并启动它的开场会话。
 *
 * 服务端每一小步都幂等，所以重试、重复提交或两个客户端抢同一个工作区都会
 * 收敛到同一个 agent、同一个会话、同一个开场回合——agent 与会话在各自的
 * 咨询锁下创建，开场回合在会话锁下写入。Mika 的任何配置都不在这里决定；
 * 配置归服务端所有，这正是它再也不会像「客户端自拼 payload」那样偏离产品
 * 定义的原因。
 *
 * @param input - 引导输入
 * @returns agent、会话与开场回合的结果
 */
export async function bootstrapMika(
  input: BootstrapMikaInput,
): Promise<
  BootstrapMikaResult & {
    kickoff: Awaited<ReturnType<typeof api.startMikaOnboarding>>;
  }
> {
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
    throw new Error("Mika onboarding session was not returned");
  }

  const kickoff = await api.startMikaOnboarding(
    chatSession.id,
    input.workspaceSlug,
  );

  return { agent, chatSession, kickoff };
}

/** React Query 版本的引导 Mika：成功后失效相关工作区/会话/消息缓存。 */
export function useBootstrapMika(workspaceId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: BootstrapMikaInput) => bootstrapMika(input),
    onSuccess: ({ chatSession }) => {
      // 不再种任何「待处理任务」：开场由服务端写入，本函数 resolve 时已经
      // 持久化，聊天视图没有需要在途等待的东西。失效消息列表是为了让它浮现
      // ——而且因为它是真实持久化状态，刷新后仍在，不像它取代的待处理任务
      // 标记那样会丢。
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
