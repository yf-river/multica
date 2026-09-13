"use client";

// 本文件是“AI 对话式创建”的启动页：先选运行环境。它产出的会话是持久对象、住在
// 自己的地址上（ai-builder-session-page），因为会话是“离开再回来”而不是一口气
// 完成。进入会话用 replace 替换本页——运行环境选择在会话产生瞬间被消费，留在
// 历史栈只会让返回时再开一条新会话。

import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentBuilderSessionListOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import type { RuntimeDevice } from "@multica/core/types";
import { useBackOrReplace, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { BuilderSetupPanel } from "./builder-setup-panel";
import { AgentCreateChip, AgentCreateShell } from "./create-shell";
import { createPathWithParams } from "./squad-param";

/**
 * 开始一次对话式 Agent 创建：先选它跑在哪。
 *
 * 这个路由是“动作”——一屏一职。它产出的会话是持久对象、住在自己的地址
 *（ai-builder-session-page），因为会话是放下再回来、而不是一口气建完。两者曾共
 * 用一个路由，用 `?session=` 参数切换，结果一个路由渲染两个互不相关的屏幕，还把
 * 会话——那个有生命周期的东西——做成无生命周期页面的修饰符。
 *
 * 进入会话会 REPLACE 掉本入口。运行环境选择在会话存在的瞬间被消费——它冻结在
 * 隐藏的 carrier agent 上，无法从这里回头改——所以把它留在历史栈上，只会让返回
 * 时有机会再开一条会话。
 */
export function AiCreateAgentPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const backOrReplace = useBackOrReplace();
  const squadId = navigation.searchParams.get("squad");

  const builderSessions = useQuery(agentBuilderSessionListOptions(wsId));
  const sessions = builderSessions.data ?? [];

  // The chip has to say something before a runtime is settled on.
  const [runtimeLabel, setRuntimeLabel] = useState<string | null>(null);
  const handleRuntimeLabel = useCallback(
    (runtime: RuntimeDevice | null) =>
      setRuntimeLabel(runtime ? runtimeDisplayLabel(runtime) : null),
    [],
  );

  const open = useCallback(
    (sessionId: string, runtimeId: string | null) =>
      navigation.replace(
        createPathWithParams(paths.newAgentAiSession(sessionId), {
          squad: squadId,
          runtime: runtimeId,
        }),
      ),
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
      <BuilderSetupPanel
        sessions={sessions}
        // 恢复会话不携带运行环境种子：已入列的会话会报出自己的运行环境——
        // 运行环境切换之后，那才是真实答案。
        onResume={(sessionId) => open(sessionId, null)}
        onStarted={(sessionId, runtimeId) => open(sessionId, runtimeId)}
        onRuntimeLabel={handleRuntimeLabel}
      />
    </AgentCreateShell>
  );
}
