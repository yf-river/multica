"use client";

// 本文件是“手动创建 Agent”页面：逐字段手填。`?duplicate=<id>` 从现有 Agent
// 播种同一张表单而不是从空开始——两者共用一个路由，因为播种之后它们就是同一屏、
// 同一条提交路径。草稿支持持久化（跨 Tab/刷新）与异步复制播种。

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { agentListOptions } from "@multica/core/workspace/queries";
import { useBackOrReplace, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { AgentConfigurationPanel } from "./agent-configuration-panel";
import { CreateAgentFooter } from "./create-agent-footer";
import { AgentCreateChip, AgentCreateShell } from "./create-shell";
import { useCreateAgentForm } from "./use-create-agent-form";
import { useCreateAgentSubmit } from "./use-create-agent-submit";
import { useDuplicateDraftSeed } from "./use-duplicate-draft-seed";
import {
  clearManualDraftForOwner,
  useManualDraftSync,
} from "./use-manual-draft-sync";

/**
 * 手动创建 Agent：每个字段手填。
 *
 * `?duplicate=<id>` 用现有 Agent 播种同一张表单而不是从空开始——两者共享这一个
 * 路由，因为播种后它们就是同一张屏、同一条提交路径。
 */
export function ManualCreateAgentPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const backOrReplace = useBackOrReplace();
  const duplicateId = navigation.searchParams.get("duplicate");
  const squadId = navigation.searchParams.get("squad");

  const form = useCreateAgentForm();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const duplicateAgent = duplicateId
    ? agents.find((agent) => agent.id === duplicateId) ?? null
    : null;

  // 复制时必须回退到别的运行环境（会丢掉源 Agent 的模型/推理/档位）时为 true。
  // 该提示解释那些空字段。
  const [duplicateRuntimeReset, setDuplicateRuntimeReset] = useState(false);
  // 复制草稿异步播种（它要等运行环境查询）。在那之前保存，会把“待播种替换的空
  // 表单”持久化；在那之前恢复，又会在下一拍被覆盖。
  const [duplicateSeeded, setDuplicateSeeded] = useState(false);

  useDuplicateDraftSeed({
    source: duplicateAgent,
    // `buildDuplicateDraft` 靠在这张列表里查找来决定“复制能否留在源运行环境上”。
    // 查询报错是可判定答案（运行环境无法确认，走兜底是对的）；pending 则不是。
    runtimesSettled: form.runtimesSettled,
    runtimes: form.runtimes,
    currentUserId: form.currentUserId,
    fallbackRuntimeId: form.usableRuntimes[0]?.id ?? "",
    nameSuffix: t(($) => $.create_dialog.duplicate_copy_suffix),
    onSeed: (duplicated, runtimeReset) => {
      // 只有“被迫回退”才提示；之后用户手动切运行环境是他自己的选择，无需解释。
      setDuplicateRuntimeReset(runtimeReset);
      form.setDraft(duplicated);
      setDuplicateSeeded(true);
    },
  });

  // #6287 报告的正是：桌面外壳只挂载当前 Tab，从别的 Tab 回来就是一次重挂载，
  // 之前输入的全没了。
  useManualDraftSync({
    duplicateId,
    draft: form.draft,
    setDraft: form.setDraft,
    ready: !duplicateId || duplicateSeeded,
  });

  const submit = useCreateAgentSubmit({
    draft: form.draft,
    runtimeId: form.selectedRuntime?.id ?? null,
    squadId,
    duplicateSource: duplicateAgent,
    // 提交即工作完成；若还留在存储里，会把已建成 Agent 的字段递给下一个打开
    // 这张表单的人。只清本流程的槽位——别人做到一半的复制仍是某人的未完成工作。
    onCreated: () => clearManualDraftForOwner(duplicateId),
  });

  const canCreate =
    form.draft.name.trim().length > 0 && form.draftReady && !submit.creating;

  return (
    <AgentCreateShell
      title={
        duplicateAgent
          ? t(($) => $.creation_studio.duplicate_title, {
              name: duplicateAgent.name,
            })
          : squadId
            ? t(($) => $.creation_studio.squad_title)
            : t(($) => $.creation_studio.title)
      }
      step={t(($) => $.creation_studio.step_configure)}
      // 复制操作从 Agent 列表页进来、不是从方式选择页，所以返回时回它来的地方，
      // 而不是再给一次“选方式”的机会。
      onBack={() =>
        backOrReplace(duplicateId ? paths.agents() : paths.newAgent())
      }
      chips={
        <>
          <AgentCreateChip>
            {t(($) => $.creation_studio.modes.blank.title)}
          </AgentCreateChip>
          {form.selectedRuntime && (
            <AgentCreateChip>
              {runtimeDisplayLabel(form.selectedRuntime)}
            </AgentCreateChip>
          )}
        </>
      }
    >
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-4xl px-5 py-8 sm:px-8">
          {duplicateAgent && (
            <div className="mb-5 rounded-lg border border-warning/30 bg-warning/5 px-4 py-3 text-body">
              {t(($) => $.creation_studio.duplicate_env_notice)}
            </div>
          )}
          {duplicateRuntimeReset && (
            <div className="mb-5 rounded-lg border border-warning/30 bg-warning/5 px-4 py-3 text-body">
              {t(($) => $.creation_studio.duplicate_runtime_reset_notice)}
            </div>
          )}
          <AgentConfigurationPanel
            draft={form.draft}
            onChange={form.setDraft}
            runtimes={form.runtimes}
            runtimesLoading={form.runtimesLoading}
            members={form.members}
            currentUserId={form.currentUserId}
            nameError={submit.nameError}
            onNameChange={(name) => {
              submit.clearNameError();
              form.setDraft((current) => ({ ...current, name }));
            }}
          />
        </div>
        <CreateAgentFooter
          canCreate={canCreate}
          creating={submit.creating}
          squad={!!squadId}
          error={submit.formError}
          onCreate={() => void submit.create()}
        />
      </div>
    </AgentCreateShell>
  );
}
