"use client";

// 本文件是“一条 AI 创建会话”的页面，按自己的 session id 寻址。id 放在路径里，
// 因为会话比发起它的屏幕长寿：离开工作室并不会删除它（#6307），刷新、
// 前进/后退、重开标签页、直达链接都要落回同一条对话。

import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentBuilderSessionListOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useBackOrReplace, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { BuilderWorkspace } from "./builder-workspace";
import { AgentCreateChip, AgentCreateShell } from "./create-shell";
import { withSquadParam } from "./squad-param";

/**
 * 一条 AI 创建会话，按自己的 session id 寻址。
 *
 * id 放在路径里而非组件 state 或 query 参数，是因为这条会话比发起它的屏幕长寿：
 * 离开工作室不再删除它（#6307），所以刷新、前进/后退、重开的标签页与直达链接
 * 都必须能落回同一条对话。
 */
export function AiBuilderSessionPage({ sessionId }: { sessionId: string }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const backOrReplace = useBackOrReplace();
  const squadId = navigation.searchParams.get("squad");
  // 这条会话刚启动时所在的运行环境。第一轮对话读不回它——会话只有持有消息或已存
  // 配置才进列表——所以启动屏把它经 URL 传过来。此后的每次访问都没有它，届时
  // 列表已能给答案。
  const startedRuntimeId = navigation.searchParams.get("runtime") ?? "";

  const builderSessions = useQuery(agentBuilderSessionListOptions(wsId));
  const sessions = builderSessions.data ?? [];
  const sessionSettled = builderSessions.isSuccess || builderSessions.isError;
  const session = sessions.find((row) => row.session_id === sessionId);

  // 只有那条在开的会话知道自己最后落在哪台运行环境。
  const [runtimeLabel, setRuntimeLabel] = useState<string | null>(null);

  // 会话没了——在这里被丢弃、在别的标签页被删、或链接早已比它长寿。用 replace
  // 而非 push：这个地址已不可解析，不能让“返回”落在它上面，把它留在历史栈里。
  const leave = useCallback(
    () => navigation.replace(withSquadParam(paths.newAgentAi(), squadId)),
    [navigation, paths, squadId],
  );

  return (
    <AgentCreateShell
      title={
        squadId
          ? t(($) => $.creation_studio.squad_title)
          : t(($) => $.creation_studio.title)
      }
      step={t(($) => $.creation_studio.step_ai)}
      onBack={() => backOrReplace(paths.newAgent())}
      chips={
        <>
          <AgentCreateChip>
            {t(($) => $.creation_studio.modes.ai.title)}
          </AgentCreateChip>
          {runtimeLabel ? (
            <AgentCreateChip>{runtimeLabel}</AgentCreateChip>
          ) : null}
        </>
      }
    >
      <BuilderWorkspace
        // 切换会话会重挂载它之下的一切：草稿、已应用消息标记、输入框都属于同一条
        // 会话，而重挂载是唯一不会漏掉任何字段的“重置”。
        key={sessionId}
        sessionId={sessionId}
        squadId={squadId}
        session={session}
        sessionSettled={sessionSettled}
        fallbackRuntimeId={startedRuntimeId}
        onDiscarded={leave}
        onRuntimeLabel={setRuntimeLabel}
      />
    </AgentCreateShell>
  );
}
