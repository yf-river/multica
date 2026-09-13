// 项目上下文支持检测 hook：判断活动 agent 的 daemon 版本是否新到能把
// 会话的项目描述注入运行简报；由聊天页与悬浮窗口两条发送链路共用。
"use client";

import { useQuery } from "@tanstack/react-query";
import {
  runtimeListOptions,
  readRuntimeCliVersion,
  chatProjectContextSupported,
} from "@multica/core/runtimes";

/**
 * 活动 agent 的 daemon 是否新到能把聊天会话的项目描述注入运行简报。
 * `null` 表示"无从判断"（没有 agent、没绑定 runtime、或 runtime 行还没进缓存），
 * 此时绝不能警告：这是道软闸门，一条多余警告比一个被旧 daemon 丢弃的描述更糟——
 * 与 run-confirm 里的 handoff 提示闸门同一条策略。
 *
 * 聊天标签页控制器与悬浮 ChatWindow 两条发送链路共用这个 hook，
 * 这样判定规则不会在两个界面之间漂移。
 */
export function useChatProjectContextSupport(
  wsId: string,
  agent: { runtime_id?: string | null } | null | undefined,
  enabled = true,
): boolean | null {
  const { data: runtimes = [] } = useQuery({
    ...runtimeListOptions(wsId),
    enabled: enabled && !!wsId,
  });
  if (!agent?.runtime_id) return null;
  const runtime = runtimes.find((r) => r.id === agent.runtime_id);
  if (!runtime) return null;
  return chatProjectContextSupported(readRuntimeCliVersion(runtime.metadata));
}
