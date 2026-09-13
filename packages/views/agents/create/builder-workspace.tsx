"use client";

// 本文件是“一条创建会话”的工作区：左侧聊天列、右侧实时草稿配置，可拖拽分栏。
// 页面以 key={sessionId} 挂载它，切会话即整树重挂载——这里的每份状态（草稿、已
// 应用消息标记、输入框）都属于一条会话，重挂载是唯一不会漏掉任何字段的重置。

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useDefaultLayout } from "react-resizable-panels";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable";
import {
  applyDraftRuntimeChange,
  decodeBuilderInput,
  encodeBuilderInput,
  mergeBuilderDraft,
  parseBuilderDraft,
  stripBuilderDraft,
} from "@multica/core/agents";
import {
  runtimeDisplayLabel,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import type { AgentBuilderSessionSummary } from "@multica/core/types";
import { AgentConfigurationPanel } from "./agent-configuration-panel";
import { BuilderConversation } from "./builder-conversation";
import { CreateAgentFooter } from "./create-agent-footer";
import { useBuilderDraftSync } from "./use-builder-draft-sync";
import { useBuilderSession } from "./use-builder-session";
import { useCreateAgentForm } from "./use-create-agent-form";
import { useCreateAgentSubmit } from "./use-create-agent-submit";
import { useT } from "../../i18n";

/**
 * 一条创建会话的工作区：左聊天、右实时配置。
 *
 * 页面以 `key={sessionId}` 挂载它，切换会话即拆掉重建。这是刻意的——这里的每份
 * 状态（草稿、哪条助手消息已被应用、输入框）都属于一条会话，而重挂载是唯一不会
 * 漏掉任何字段的“重置”。
 */
export function BuilderWorkspace({
  sessionId,
  squadId,
  session,
  sessionSettled,
  fallbackRuntimeId,
  onDiscarded,
  onRuntimeLabel,
}: {
  sessionId: string;
  squadId: string | null;
  /** 这条会话在列表里对应的行（到达后才有）。 */
  session: AgentBuilderSessionSummary | undefined;
  /** 列表查询已应答——无论数据还是错误。 */
  sessionSettled: boolean;
  /** 这条会话刚启动时的运行环境。会话要有一条消息才入列，所以第一轮对话时，
   *  这是“它跑在哪”的唯一真实答案。 */
  fallbackRuntimeId: string;
  /** 会话已不存在；去哪由页面决定。 */
  onDiscarded: () => void;
  /** 把这条会话落定的运行环境报给外壳的徽章。 */
  onRuntimeLabel: (label: string | null) => void;
}) {
  const { t } = useT("agents");
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_agent_builder_layout",
  });

  // 恢复中：会话已跑在某处，只有服务端知道在哪。它应答之前，表单不播种任何运行
  // 环境——回退到第一台可用者会让选择器停在“什么都不执行”的机器上（MUL-5163）。
  const runtimeSeed = useMemo(
    () => ({
      ready: sessionSettled,
      runtimeId: session?.runtime_id || fallbackRuntimeId,
    }),
    [fallbackRuntimeId, session?.runtime_id, sessionSettled],
  );
  const form = useCreateAgentForm({ runtimeSeed });
  const { draft, setDraft, selectedRuntime } = form;

  const draftSync = useBuilderDraftSync({
    sessionId,
    stored: session?.draft,
    storedSettled: sessionSettled,
    runtimeId: session?.runtime_id ?? "",
    draft,
    setDraft,
  });

  const builderModelsQuery = useQuery(
    runtimeModelsOptions(
      selectedRuntime?.status === "online" ? selectedRuntime.id : null,
    ),
  );
  // `null` 表示发现尚不可用（或失败）；`[]` 是权威目录但无可选模型。两种情况
  // 下 builder 都可以保留用户当前值，但不能凭空发明一个。
  const builderModelCatalog = useMemo(
    () =>
      builderModelsQuery.isSuccess
        ? builderModelsQuery.data.supported
          ? builderModelsQuery.data.models
          : []
        : null,
    [builderModelsQuery.data, builderModelsQuery.isSuccess],
  );
  const validBuilderModelIds = useMemo(
    () =>
      builderModelCatalog === null
        ? null
        : new Set(builderModelCatalog.map((model) => model.id)),
    [builderModelCatalog],
  );

  // 经 ref 读取，让交给会话 hook 的编码器保持最新，又不必让 hook 依赖每次目录查询。
  const encodeContext = useRef({
    draft,
    workspaceSkills: form.workspaceSkills,
    members: form.members,
    selectedRuntime,
    builderModelCatalog,
  });
  encodeContext.current = {
    draft,
    workspaceSkills: form.workspaceSkills,
    members: form.members,
    selectedRuntime,
    builderModelCatalog,
  };

  const builder = useBuilderSession({
    sessionId,
    encodeInput: (text) => {
      const context = encodeContext.current;
      return encodeBuilderInput(
        text,
        context.draft,
        context.workspaceSkills,
        context.members,
        context.selectedRuntime,
        context.builderModelCatalog,
      );
    },
  });

  const submit = useCreateAgentSubmit({
    draft,
    runtimeId: selectedRuntime?.id ?? null,
    squadId,
    template: "agent_builder",
    // 此处 Agent 已提交，builder 的收尾绝不能把成功创建变成可重试的创建错误。
    onCreated: () => builder.archiveAfterCreate(),
  });

  const skillIdSet = useMemo(
    () => new Set(form.workspaceSkills.map((skill) => skill.id)),
    [form.workspaceSkills],
  );
  const memberIdSet = useMemo(
    () => new Set(form.members.map((member) => member.user_id)),
    [form.members],
  );

  // 实时聊天更新可能原地改动缓存的 messages 数组。取最新一条结构化消息的
  // 标量身份/内容作 effect 输入——即使数组引用本身没变，草稿仍会被正确应用。
  const latestDraftMessage = [...builder.messages]
    .reverse()
    .find(
      (message) =>
        message.role === "assistant" && parseBuilderDraft(message.content),
    );
  const latestDraftMessageId = latestDraftMessage?.id;
  const latestDraftMessageContent = latestDraftMessage?.content;

  const { restored, markApplied } = draftSync;
  const appliedRef = useRef<string | null>(null);
  appliedRef.current = draftSync.appliedMessageId;

  useEffect(() => {
    // 门槛设在恢复上：已存配置落地前把回复合并进表单，会在下一拍被覆盖——而且
    // 那次合并还是拿空草稿算出来的。
    if (!restored) return;
    if (
      !latestDraftMessageId ||
      !latestDraftMessageContent ||
      latestDraftMessageId === appliedRef.current
    ) {
      return;
    }
    const payload = parseBuilderDraft(latestDraftMessageContent);
    if (!payload) return;
    markApplied(latestDraftMessageId);
    setDraft((current) =>
      mergeBuilderDraft(
        current,
        payload,
        skillIdSet,
        memberIdSet,
        validBuilderModelIds,
      ),
    );
  }, [
    latestDraftMessageContent,
    latestDraftMessageId,
    markApplied,
    memberIdSet,
    restored,
    setDraft,
    skillIdSet,
    validBuilderModelIds,
  ]);

  const displayMessages = useMemo(
    () =>
      builder.messages.map((message) => ({
        ...message,
        content:
          message.role === "user"
            ? decodeBuilderInput(message.content)
            : stripBuilderDraft(message.content),
      })),
    [builder.messages],
  );

  // 草稿的运行环境确知与 carrier 一致之前为 false。在那之前，任何选择都不得发到
  // 服务端：RuntimePicker 自己会通过 onSelect 播种空选择，而本面板的 onSelect 会去
  // 重绑实时会话——听从那个种子，等于把会话挪到“排序第一”的机器上：空闲时无声
  // 发生，不空闲时表现为“换运行环境前先打断当前回复”。
  const runtimeKnown = sessionSettled && draft.runtimeId.length > 0;

  useEffect(() => {
    onRuntimeLabel(selectedRuntime ? runtimeDisplayLabel(selectedRuntime) : null);
  }, [onRuntimeLabel, selectedRuntime]);

  const handleRuntimeSelect = async (runtimeId: string) => {
    if (!runtimeKnown) return;
    if (!runtimeId || runtimeId === draft.runtimeId) return;
    const bound = await builder.switchRuntime(runtimeId);
    if (!bound) return;
    // 模型 id 按运行环境区分，一并清掉按模型绑定的推理/档位覆盖——让新运行环境
    // 解析自己的默认，而不是保留一些它可能根本服务不了的值。
    setDraft((current) => applyDraftRuntimeChange(current, bound));
  };

  const canCreate =
    draft.name.trim().length > 0 &&
    form.draftReady &&
    !submit.creating &&
    !builder.pending;

  // 这里刻意不加 beforeunload 守卫：会话活在服务端、配置自动保存，刷新什么都不会
  // 丢。手动流程仍会警告，因为它的表单在服务端没有家。
  const [confirmingDiscard, setConfirmingDiscard] = useState(false);
  // 已不存在的会话没法展示、编辑或恢复；与其在一堆“无”上渲染可编辑外壳，
  // 不如离开。
  const { missing } = builder;
  useEffect(() => {
    if (missing) onDiscarded();
  }, [missing, onDiscarded]);

  const discard = async () => {
    if (!(await builder.destroy())) return;
    setConfirmingDiscard(false);
    onDiscarded();
  };

  return (
    <>
      {/* 面板组放在这里而不放在路由上，因此它的两个孩子就是这两个面板——与聊天页
          同构。一个子元素在一/两面板间交替的面板组（setup vs conversation）没法
          维持单一持久布局；经子组件 fragment 传入的面板也不是它能测量的子元素。 */}
      <ResizablePanelGroup
        orientation="horizontal"
        className="min-h-0 flex-1"
        defaultLayout={defaultLayout}
        onLayoutChanged={onLayoutChanged}
      >
        <ResizablePanel id="conversation" minSize="30%">
          <BuilderConversation
            sessionId={sessionId}
            messages={displayMessages}
            loading={builder.messagesLoading}
            pendingTask={builder.pendingTask}
            runtimeOnline={selectedRuntime?.status === "online"}
            onSend={builder.send}
            onStop={() => void builder.stop()}
            restoreDraftRequest={builder.restoreDraftRequest}
            onRestoreDraftApplied={builder.handleRestoreDraftApplied}
            error={builder.error}
          />
        </ResizablePanel>
        <ResizableHandle />
        <ResizablePanel
          id="config"
          defaultSize={420}
          minSize={340}
          groupResizeBehavior="preserve-pixel-size"
        >
          <div className="flex h-full min-h-0 flex-col border-l bg-muted/10">
            <div className="min-h-0 flex-1 overflow-y-auto">
              <div className="mx-auto max-w-2xl px-5 py-6">
                <div className="mb-6">
                  <h2 className="text-title-sm font-semibold tracking-tight">
                    {t(($) => $.creation_studio.live_draft)}
                  </h2>
                  <p className="mt-1 text-caption text-muted-foreground">
                    {t(($) => $.creation_studio.live_draft_hint)}
                  </p>
                </div>
                <AgentConfigurationPanel
                  compact
                  draft={draft}
                  onChange={setDraft}
                  runtimes={form.runtimes}
                  runtimesLoading={form.runtimesLoading}
                  members={form.members}
                  currentUserId={form.currentUserId}
                  nameError={submit.nameError}
                  onNameChange={(name) => {
                    submit.clearNameError();
                    setDraft((current) => ({ ...current, name }));
                  }}
                  onRuntimeSelect={(runtimeId) => {
                    void handleRuntimeSelect(runtimeId);
                  }}
                  runtimeSwitchPending={builder.pending}
                  // carrier 运行环境未知期间也锁定，选择器就不会给出一个它拒绝
                  // 执行的切换入口。
                  runtimeSwitchInFlight={
                    builder.switchingRuntime || !runtimeKnown
                  }
                />
              </div>
            </div>
            <CreateAgentFooter
              canCreate={canCreate}
              creating={submit.creating}
              squad={!!squadId}
              error={submit.formError}
              onCreate={() => void submit.create()}
              onDiscard={() => setConfirmingDiscard(true)}
              discarding={builder.closing}
            />
          </div>
        </ResizablePanel>
      </ResizablePanelGroup>

      <AlertDialog
        open={confirmingDiscard}
        onOpenChange={(open) => {
          if (!open && !builder.closing) setConfirmingDiscard(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.creation_studio.drafts.discard_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.creation_studio.drafts.discard_confirm)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={builder.closing}>
              {t(($) => $.creation_studio.drafts.discard_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                // 删除在途时保持弹窗打开；只有服务端真正接受后，`discard` 才会
                // 关闭它。
                event.preventDefault();
                void discard();
              }}
              disabled={builder.closing}
            >
              {t(($) => $.creation_studio.drafts.discard)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
