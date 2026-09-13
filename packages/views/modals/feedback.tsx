// 本文件职责：「发送反馈」弹窗——带富文本编辑器（支持拖拽/选择图片上传）与快捷键提交，
// 把反馈内容连同当前页面地址一起发给服务端。
"use client";

import { useRef, useState } from "react";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import {
  ContentEditor,
  type ContentEditorRef,
  useFileDropZone,
  FileDropOverlay,
  useUploadGate,
  useEditorUpload,
} from "../editor";
import {
  useCreateFeedback,
  useFeedbackDraftStore,
  FEEDBACK_KINDS,
  type FeedbackKind,
} from "@multica/core/feedback";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useT } from "../i18n";
import { useShortcut } from "@multica/core/shortcuts";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import { currentPath, useOptionalNavigation } from "../navigation";

const MAX_MESSAGE_LEN = 10000;

const FEEDBACK_KIND_SET = new Set<FeedbackKind>(FEEDBACK_KINDS);

/**
 * 合并「草稿里已有的反馈正文」和「本次入口带进来的初始消息」。
 *
 * 规则：任何一方为空就直接用另一方；草稿已包含该初始消息则保持草稿不动（避免重复）；
 * 否则用分隔线把初始消息接到草稿后面。既保留用户草稿，又不丢掉入口附带的上下文。
 */
function composeFeedbackInitialMessage(draftMessage: string, incomingInitialMessage: string) {
  const draft = draftMessage.trim();
  const incoming = incomingInitialMessage.trim();
  if (!incoming) return draftMessage;
  if (!draft) return incomingInitialMessage;
  if (draft.includes(incoming)) return draftMessage;
  return `${draftMessage}

---

${incomingInitialMessage}`;
}

/**
 * 「发送反馈」弹窗。
 *
 * 富文本编辑反馈内容（含拖拽/上传图片），可附带来源入口（`data.kind` 区分反馈类型，
 * `initialMessage`/`data.initialMessage` 预填正文），提交时自动带上当前可分享页面地址。
 * 支持 ⌘/Ctrl+Enter 快捷键发送，成功后清空草稿并关闭。
 *
 * @param onClose        关闭弹窗的回调
 * @param data           弹窗上下文，可携带 `kind`（反馈类型）与 `initialMessage`（初始正文）
 * @param initialMessage 直接传入的初始正文，优先级高于 `data.initialMessage`
 */
export function FeedbackModal({
  onClose,
  data,
  initialMessage,
}: {
  onClose: () => void;
  data?: Record<string, unknown> | null;
  initialMessage?: string;
}) {
  const sendShortcut = useShortcut("send");
  const { t } = useT("modals");
  const { t: tEditor } = useT("editor");
  const workspace = useCurrentWorkspace();
  const navigation = useOptionalNavigation();
  const draft = useFeedbackDraftStore((s) => s.draft);
  const setDraft = useFeedbackDraftStore((s) => s.setDraft);
  const clearDraft = useFeedbackDraftStore((s) => s.clearDraft);

  const editorRef = useRef<ContentEditorRef>(null);
  const incomingInitialMessage =
    initialMessage ?? (typeof data?.initialMessage === "string" ? data.initialMessage : "");
  const kind = typeof data?.kind === "string" && FEEDBACK_KIND_SET.has(data.kind as FeedbackKind)
    ? (data.kind as FeedbackKind)
    : undefined;
  const seededMessage = composeFeedbackInitialMessage(draft.message, incomingInitialMessage);
  const [message, setMessage] = useState(seededMessage);
  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((f) => editorRef.current?.uploadFile(f)),
  });
  const { uploadWithToast } = useEditorUpload();
  // 上传中途提交会被上闸拦住，但按钮本身仍可点击——若只靠按钮禁用，点击后唯一的
  // 反馈是那枚事后才冒出来的 toast，体验不明确。
  const uploadGate = useUploadGate(editorRef);
  const mutation = useCreateFeedback();

  const canSubmit =
    message.trim().length > 0 &&
    message.length <= MAX_MESSAGE_LEN &&
    !mutation.isPending &&
    !uploadGate.uploading;

  const handleSubmit = async () => {
    // 按钮能依赖带防抖的 `message` state，但快捷键不行：⌘+Enter 可能赶在
    // ContentEditor 150ms 的 onUpdate 触发之前到达，所以下面用编辑器 ref
    // 作为提交时刻的内容来源。
    if (mutation.isPending) return;
    // 保留这个 toast：快捷键可能在按钮被禁用且不可见时触发，如果静默忽略，
    // 用户会以为 ⌘+Enter 失灵了。
    if (uploadGate.isBlocked()) {
      toast.info(t(($) => $.feedback.toast_uploading));
      return;
    }
    // 提交时直接从编辑器 ref 读内容——`message` state 因 `debounceMs` 会比敲击
    // 落后 150ms，紧跟输入立即按 ⌘+Enter 会提交到旧内容。
    const latest = editorRef.current?.getMarkdown()?.trim() ?? "";
    if (!latest) return;
    if (latest.length > MAX_MESSAGE_LEN) {
      toast.error(t(($) => $.feedback.toast_too_long));
      return;
    }
    try {
      const browserUrl =
        typeof window !== "undefined" &&
        (window.location.protocol === "http:" ||
          window.location.protocol === "https:")
          ? window.location.href
          : undefined;
      const currentUrl = navigation
        ? navigation.getShareableUrl(currentPath(navigation))
        : browserUrl;
      await mutation.mutateAsync({
        message: latest,
        url: currentUrl,
        workspace_id: workspace?.id,
        kind,
      });
      clearDraft();
      toast.success(t(($) => $.feedback.toast_sent));
      onClose();
    } catch (err) {
      const msg =
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.feedback.toast_failed);
      toast.error(msg);
    }
  };

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-2xl !h-[28rem] p-0 gap-0 flex flex-col overflow-hidden">
        <DialogHeader className="px-5 pt-4 pb-2 shrink-0">
          <DialogTitle>{t(($) => $.feedback.title)}</DialogTitle>
          <p className="mt-1 text-caption text-muted-foreground">
            {t(($) => $.feedback.github_hint_prefix)}
            <a
              href="https://github.com/multica-ai/multica/issues"
              target="_blank"
              rel="noopener noreferrer"
              className="text-brand underline decoration-brand/40 underline-offset-2 hover:decoration-brand"
            >
              {t(($) => $.feedback.github_hint_link)}
            </a>
          </p>
        </DialogHeader>

        <div className="flex-1 min-h-0 px-5 pb-3">
          <div
            {...dropZoneProps}
            className="relative h-full overflow-y-auto rounded-lg border-1 border-border transition-colors focus-within:border-brand"
          >
            <ContentEditor
              ref={editorRef}
              defaultValue={seededMessage}
              placeholder={t(($) => $.feedback.placeholder)}
              onUpdate={(md) => { setMessage(md); setDraft({ message: md }); }}
              onUploadFile={(file) => uploadWithToast(file)}
              onUploadingChange={uploadGate.onUploadingChange}
              onSubmit={handleSubmit}
              debounceMs={150}
              showBubbleMenu={false}
              className="px-3 py-2"
            />
            {isDragOver && <FileDropOverlay />}
          </div>
        </div>

        <div className="flex items-center justify-between px-4 py-3 border-t shrink-0">
          <FileUploadButton
            size="sm"
            multiple
            onSelect={(file) => editorRef.current?.uploadFile(file)}
          />
          <Button
            size="sm"
            onClick={handleSubmit}
            disabled={!canSubmit}
            aria-disabled={uploadGate.uploading || undefined}
            aria-busy={uploadGate.uploading || undefined}
          >
            {mutation.isPending
              ? t(($) => $.feedback.sending)
              : uploadGate.uploading
                ? tEditor(($) => $.upload.in_progress)
                : t(($) => $.feedback.send)}
            {sendShortcut ? (
              <ShortcutKeycaps
                shortcut={sendShortcut}
                decorative
                className="ml-1"
                keyClassName="border-background/30 bg-background/15 text-primary-foreground shadow-none"
              />
            ) : null}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
