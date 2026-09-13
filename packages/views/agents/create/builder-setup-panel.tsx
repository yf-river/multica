"use client";

// 本文件是“AI 对话式创建”流程里、会话产生前的一步：选定运行环境再开聊。
// 运行环境无法推迟决定——它会被冻结到隐藏的 carrier agent 上，并成为新 Agent 的
// 默认运行环境。本面板只持这两个字段的一次性表单，真正的草稿属于会话、随会话创建。

import { useEffect } from "react";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  AgentBuilderSessionSummary,
  RuntimeDevice,
} from "@multica/core/types";
import { BuilderSetup } from "./builder-conversation";
import { UnfinishedDraftsBanner } from "./unfinished-drafts";
import { useBuilderSession } from "./use-builder-session";
import { useCreateAgentForm } from "./use-create-agent-form";

/**
 * 会话存在前的“选运行环境”一步。
 *
 * 运行环境不能拖后——创建会话时就冻结到隐藏的 carrier agent 上，并成为新 Agent
 * 的默认运行环境。本面板为一个一次性表单而设，恰好管这两个字段；真正的草稿
 * 属于会话、并随会话一起创建。
 */
export function BuilderSetupPanel({
  sessions,
  onResume,
  onStarted,
  onRuntimeLabel,
}: {
  /** 未完成的会话。已有存货时，本屏就不是死胡同：选择器是从这里回到它们的唯一入口。 */
  sessions: AgentBuilderSessionSummary[];
  onResume: (sessionId: string) => void;
  /** 把新会话 id 与运行环境交回，让路由能打开它。 */
  onStarted: (sessionId: string, runtimeId: string) => void;
  onRuntimeLabel: (runtime: RuntimeDevice | null) => void;
}) {
  const paths = useWorkspacePaths();
  const form = useCreateAgentForm();
  const { draft, setDraft, selectedRuntime } = form;

  const builder = useBuilderSession({
    sessionId: "",
    // 本屏不发任何东西；编码器只有会话存在后才用得上。
    encodeInput: (text) => text,
  });

  useEffect(() => {
    onRuntimeLabel(selectedRuntime);
  }, [onRuntimeLabel, selectedRuntime]);

  const startConversation = async () => {
    if (selectedRuntime?.status !== "online") return;
    const startedId = await builder.start(selectedRuntime.id, draft.model);
    if (startedId) onStarted(startedId, selectedRuntime.id);
  };

  return (
    <BuilderSetup
      banner={
        <UnfinishedDraftsBanner sessions={sessions} onResume={onResume} />
      }
      draft={draft}
      onChange={setDraft}
      runtimes={form.runtimes}
      runtimesLoading={form.runtimesLoading}
      members={form.members}
      currentUserId={form.currentUserId}
      selectedRuntime={selectedRuntime}
      starting={builder.starting}
      error={builder.error}
      onStart={() => void startConversation()}
      connectRuntimeHref={paths.runtimes()}
    />
  );
}
