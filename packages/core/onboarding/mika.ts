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

/** 当前成员尚无 Mika 或自己的 Mika 会话时，展示初始化入口。 */
export function memberNeedsMikaSetup(
  agents: Pick<Agent, "id" | "system_key">[],
  sessions: Pick<ChatSession, "agent_id" | "last_message">[],
): boolean {
  const mika = agents.find(isMikaAgent);
  if (!mika) return true;

  return !sessions.some((session) => session.agent_id === mika.id);
}
