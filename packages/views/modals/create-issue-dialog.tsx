// 本文件职责：创建 issue 流程的外层「壳」，拥有唯一的 Dialog/DialogContent，负责在「手动模式」和「Agent 模式」之间切换内部面板。
"use client";

import { useLayoutEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { cn } from "@multica/ui/lib/utils";
import { Dialog, DialogContent } from "@multica/ui/components/ui/dialog";
import {
  useCreateModeStore,
  type CreateMode,
} from "@multica/core/issues/stores/create-mode-store";
import { AgentCreatePanel } from "./quick-create-issue";
import { ManualCreatePanel, manualDialogContentClass } from "./create-issue";
import { sourceContextPreviewOptions } from "@multica/core/issues/queries";
import { useIssueDraftStore } from "@multica/core/issues/stores/draft-store";
import { useWorkspaceId } from "@multica/core/hooks";

/**
 * 创建 issue 流程的外层「壳」。
 *
 * 它负责持有唯一的 `<Dialog>` 和 `<DialogContent>`：切换模式时只卸载/挂载内部面板，
 * Portal、遮罩层、弹窗本体都一直留在 DOM 里，因此 Base UI 不会重复播放打开动画，
 * 切换才会感觉是瞬时的。早先版本把 `<DialogContent>` 放在每个面板里，每次切换都会
 * 重放一遍关闭→打开的动画。
 *
 * `initialMode` 来自弹窗注册表（`quick-create-issue` → agent，`create-issue` → manual）。
 * 之后的切换只是本地 state，不再回写弹窗 store。
 *
 * 携带数据：面板切换模式时可以通过 `onSwitchMode` 把一份数据交给外壳，外壳会把它
 * 存成下一个面板的 `data`，这样预填效果和「重新打开」完全一致。
 *
 * 手动模式的 `isExpanded` 被提升到外壳：因为它驱动 `DialogContent` 的 className——
 * Popup 在这里，所以 className 也放在外壳；但切换这个状态的开关在手动面板内部。
 *
 * @param onClose     关闭弹窗的回调
 * @param initialMode 初始模式（agent 或 manual）
 * @param data        弹窗打开时传入的上下文，可携带预填字段或锚点评论等
 */
export function CreateIssueDialog({
  onClose,
  initialMode,
  data,
}: {
  onClose: () => void;
  initialMode: CreateMode;
  data?: Record<string, unknown> | null;
}) {
  const anchorCommentId = typeof data?.anchor_comment_id === "string"
    ? data.anchor_comment_id
    : null;
  if (anchorCommentId) {
    return (
      <SourceContextCreateIssueDialog
        onClose={onClose}
        initialMode={initialMode}
        data={data}
        anchorCommentId={anchorCommentId}
      />
    );
  }
  return <CreateIssueDialogBody onClose={onClose} initialMode={initialMode} data={data} />;
}

function SourceContextCreateIssueDialog({
  onClose,
  initialMode,
  data,
  anchorCommentId,
}: {
  onClose: () => void;
  initialMode: CreateMode;
  data?: Record<string, unknown> | null;
  anchorCommentId: string;
}) {
  const wsId = useWorkspaceId();
  const [draftReady, setDraftReady] = useState(false);
  const [sourceContextExpanded, setSourceContextExpanded] = useState(false);
  const sourceQuery = useQuery(sourceContextPreviewOptions(wsId, anchorCommentId));

  useLayoutEffect(() => {
    useIssueDraftStore.getState().beginIsolatedDraft();
    setDraftReady(true);
    return () => useIssueDraftStore.getState().endIsolatedDraft();
  }, []);

  if (!draftReady) return null;

  return (
    <CreateIssueDialogBody
      onClose={onClose}
      initialMode={initialMode}
      data={data}
      sourceContextExpanded={sourceContextExpanded}
      sourceContextData={{
        anchor_comment_id: anchorCommentId,
        source_context_preview: sourceQuery.isError || sourceQuery.isFetching
          ? undefined
          : sourceQuery.data,
        source_context_loading: sourceQuery.isLoading || sourceQuery.isFetching,
        source_context_failed: sourceQuery.isError,
        source_context_error: sourceQuery.error,
        source_context_refetch: sourceQuery.refetch,
        source_context_expanded: sourceContextExpanded,
        source_context_on_expanded_change: setSourceContextExpanded,
      }}
    />
  );
}

function CreateIssueDialogBody({
  onClose,
  initialMode,
  data,
  sourceContextData,
  sourceContextExpanded = false,
}: {
  onClose: () => void;
  initialMode: CreateMode;
  data?: Record<string, unknown> | null;
  sourceContextData?: Record<string, unknown>;
  sourceContextExpanded?: boolean;
}) {
  const setLastMode = useCreateModeStore((s) => s.setLastMode);
  const [mode, setMode] = useState<CreateMode>(initialMode);
  const [panelData, setPanelData] = useState(data ?? null);
  const [isExpanded, setIsExpanded] = useState(false);
  const effectiveData = sourceContextData
    ? { ...(panelData ?? {}), ...sourceContextData }
    : panelData;

  const switchTo = (next: CreateMode) => (carry?: Record<string, unknown> | null) => {
    setLastMode(next);
    setPanelData(carry ?? null);
    setMode(next);
  };

  const className =
    mode === "agent"
      ? cn(
          "p-0 gap-0 flex flex-col overflow-hidden",
          "!top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2",
          // 切换模式时的平滑尺寸过渡——手动模式用的是同一套缓动曲线。
          "!transition-all !duration-300 !ease-out",
          // 手机端留边。下面的宽度是 `!important`，能压过 DialogContent 自身的
          // 尺寸——但这也一并压过了它的 `max-w-[calc(100%-2rem)]` 安全边距，
          // 让卡片在 430px 视口下贴着屏幕两侧（MUL-6236）。在这里恢复边距，
          // 640px 以上再交给 `sm:` 的宽度接管。
          "!w-full !max-w-[calc(100vw-1.5rem)]",
          // 带来源上下文的创建需要「收起/展开」的数值化高度端点，这样预览过渡
          // 才能插值高度。普通快速创建没有可展开的预览，保持原来的内容驱动高度，
          // 仅对移动端浏览器工具栏做上限截断。
          isExpanded
            ? "!h-5/6 sm:!max-w-4xl"
            : sourceContextData
              ? sourceContextExpanded
                ? "!h-5/6 sm:!max-w-2xl"
                : "!h-96 sm:!max-w-xl"
              : "!max-h-[80dvh] sm:!max-w-xl",
        )
      : cn(
          manualDialogContentClass(isExpanded),
          sourceContextExpanded && "!h-5/6",
        );

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent
        finalFocus={false}
        showCloseButton={false}
        className={className}
      >
        {mode === "agent" ? (
          <AgentCreatePanel
            onClose={onClose}
            onSwitchMode={switchTo("manual")}
            data={effectiveData}
            isExpanded={isExpanded}
            setIsExpanded={setIsExpanded}
          />
        ) : (
          <ManualCreatePanel
            onClose={onClose}
            onSwitchMode={switchTo("agent")}
            data={effectiveData}
            isExpanded={isExpanded}
            setIsExpanded={setIsExpanded}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
