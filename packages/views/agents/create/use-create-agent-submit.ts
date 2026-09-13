"use client";

// 本文件是“手动创建/复制 Agent”的提交 hook：把草稿建成 Agent，成功即离开创建
// 流程（跳 Agent 详情或 squad 详情）。刻意不做乐观更新——成功后要导航走，Agent
// 必须先真实存在；squad 加入失败降级为警告，绝不因此让整个创建回滚。

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { buildCreateAgentRequest, type AgentDraft } from "@multica/core/agents";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Agent } from "@multica/core/types";
import {
  cacheAgentResponse,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

/** 创建失败的错误分类：409 名字冲突专挂名字字段，其余挂表单级错误文案。 */
interface AgentCreateErrors {
  nameError: string | null;
  formError: string | null;
}

export function classifyAgentCreateError(
  error: unknown,
  fallbackMessage: string,
  conflictMessage: string,
): AgentCreateErrors {
  const message =
    error instanceof Error && error.message ? error.message : fallbackMessage;
  return error instanceof ApiError && error.status === 409
    ? { nameError: conflictMessage, formError: null }
    : { nameError: null, formError: message };
}

/**
 * 提交草稿并离开创建流程。
 *
 * 刻意不做乐观更新：成功后要导航离开，所以 Agent 必须在目的地渲染前真实存在。
 * squad 加入失败只报警告而不是让创建失败——那一刻 Agent 已提交，重试只会造出
 * 重复的 Agent。
 */
export function useCreateAgentSubmit(options: {
  draft: AgentDraft;
  runtimeId: string | null;
  squadId: string | null;
  /** 用于 `agent_created` 分析事件的创建来源标注。 */
  template?: string;
  duplicateSource?: Agent | null;
  /** Agent 提交后、导航前执行。 */
  onCreated?: (agent: Agent) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const qc = useQueryClient();

  const [creating, setCreating] = useState(false);
  const [nameError, setNameError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);

  const { draft, runtimeId, squadId, template, duplicateSource, onCreated } =
    options;

  const create = async () => {
    if (!runtimeId || creating) return;
    setCreating(true);
    setNameError(null);
    setFormError(null);
    try {
      const agent = await api.createAgent(
        buildCreateAgentRequest({
          draft,
          runtimeId,
          template,
          duplicateSource,
        }),
      );
      if (!agent.id) throw new Error(t(($) => $.creation_studio.create_failed));

      if (squadId) {
        try {
          await api.addSquadMember(squadId, {
            member_type: "agent",
            member_id: agent.id,
          });
          await Promise.all([
            qc.invalidateQueries({
              queryKey: [...workspaceKeys.squads(wsId), squadId, "members"],
            }),
            qc.invalidateQueries({
              queryKey: [...workspaceKeys.squads(wsId), squadId],
            }),
          ]);
        } catch (error) {
          toast.warning(
            t(($) => $.create_dialog.squad_join_failed_toast, {
              name: agent.name || draft.name.trim(),
              error: error instanceof Error ? error.message : "unknown error",
            }),
          );
        }
      }

      await onCreated?.(agent);
      cacheAgentResponse(qc, wsId, agent);
      // 创建响应已足够权威，可直接打开详情。其它列表投影的修正放到后台跑，
      // 别让导航等第二次网络请求。
      void qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      toast.success(
        t(($) => $.creation_studio.created, {
          name: agent.name || draft.name.trim(),
        }),
      );
      navigation.push(
        squadId ? paths.squadDetail(squadId) : paths.agentDetail(agent.id),
      );
    } catch (error) {
      const nextErrors = classifyAgentCreateError(
        error,
        t(($) => $.creation_studio.create_failed),
        t(($) => $.creation_studio.name_conflict),
      );
      setNameError(nextErrors.nameError);
      setFormError(nextErrors.formError);
      setCreating(false);
    }
  };

  return {
    create,
    creating,
    nameError,
    formError,
    clearNameError: () => setNameError(null),
  };
}
