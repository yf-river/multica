// 本文件提供 Mika 的识别与「是否需要创建/引导 Mika」的判断。
//
// Mika 是 Multica 自带的伴生示例 agent，用固定的 system_key 标识。

import type { Agent, ChatSession } from "../types";

/**
 * 与服务端 `service.MikaSystemKey` 对应。Mika 靠这个 key 识别，绝不靠显示名
 * ——显示名由 owner 可编辑，靠名字判断的话，一次改名就会让工作区看起来像
 * 没有 Mika。
 */
export const MIKA_SYSTEM_KEY = "mika";

/** 判断某个 agent 是否为 Mika。 */
export function isMikaAgent(agent: Pick<Agent, "system_key">): boolean {
  return agent.system_key === MIKA_SYSTEM_KEY;
}

/**
 * 工作区是否还需要创建一个 Mika。
 *
 * 刻意判断「没有 Mika」而不是「没有任何 agent」：Runtimes 页的恢复入口用的
 * 是后者，于是先建任何一个普通 agent 就会藏起唯一能铸造 Mika 的入口——而
 * 通用 agent 接口造不出 Mika，因为既不吃 `kind` 也不吃 `system_key`。
 *
 * @param agents - 工作区里现有的 agent
 * @returns 是否需要创建 Mika
 */
export function workspaceNeedsMika(agents: Pick<Agent, "system_key">[]): boolean {
  return !agents.some(isMikaAgent);
}

/**
 * *当前这个成员*是否还需要「从 Mika 开始」的入口。
 *
 * 这和 `workspaceNeedsMika` 不是同一个问题；拿那个来给入口设闸是个陷阱：
 * 引导是三个服务端步骤——创建 agent、打开该成员的会话、排入开场回合——后
 * 两步可能在 agent 已落库后失败。agent 的 `agent:created` 广播随即失效 agent
 * 列表，于是卡片在第一步成功的瞬间就卸载了（连同它打开的对话框一起），并且
 * 刷新后也不再回来——因为 agent 是持久的、其余不是。成员因此得到一个启动
 * 不了的 Mika。
 *
 * 每个步骤都幂等，所以诚实的条件是「该成员是否真的已经有了一个被踢起来的
 * Mika 会话」——从这里重跑这套流程永远安全。
 *
 * @param agents - 工作区里的 agent
 * @param sessions - 当前成员拥有的会话
 * @returns 该成员是否还需要 Mika 引导入口
 */
export function memberNeedsMikaSetup(
  agents: Pick<Agent, "id" | "system_key">[],
  sessions: Pick<ChatSession, "agent_id" | "last_message">[],
): boolean {
  const mika = agents.find(isMikaAgent);
  if (!mika) return true;

  // 会话按成员隔离，所以这条就是调用者自己的会话。
  const session = sessions.find((s) => s.agent_id === mika.id);
  if (!session) return true;

  // 开场回合是以真实（隐藏）消息形式存储的，所以空会话意味着开场从未落地。
  return !session.last_message;
}
