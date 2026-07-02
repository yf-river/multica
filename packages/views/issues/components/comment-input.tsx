"use client";

import { cn } from "@multica/ui/lib/utils";
import { ContentEditor, FileDropOverlay } from "../../editor";
import { FileUploadButton } from "@multica/ui/components/common/file-upload-button";
import { SubmitButton } from "@multica/ui/components/common/submit-button";
import { enterKey, formatShortcut, modKey } from "@multica/core/platform";
import { useT } from "../../i18n";
import { CommentTriggerChips } from "./comment-trigger-chips";
import { useCommentComposer } from "./use-comment-composer";

interface CommentInputProps {
  issueId: string;
  /** Resolves true on success, false on failure. The composer keeps the text
   *  (editor locked + button spinning) until this settles, then clears only on
   *  success — a failed send must not silently discard the user's draft. */
  onSubmit: (content: string, attachmentIds?: string[], suppressAgentIds?: string[]) => Promise<boolean>;
}

function CommentInput({ issueId, onSubmit }: CommentInputProps) {
  const { t } = useT("issues");
  const draftKey = `new:${issueId}` as const;
  const {
    dropZoneProps,
    editorRef,
    handleEditorUpdate,
    handleSubmit,
    handleUpload,
    initialDraft,
    isDragOver,
    isEmpty,
    pendingAttachments,
    submitting,
    suppressedAgentIds,
    toggleSuppressedAgent,
    triggerPreview,
  } = useCommentComposer({ issueId, draftKey, onSubmit });

  return (
    <div
      {...dropZoneProps}
      className="relative flex flex-col rounded-lg bg-card pb-8 ring-1 ring-border"
    >
      {/* Lock the editor while the send is in flight. ContentEditor can't
          toggle Tiptap's `editable` post-mount (see its docstring), so the
          documented way to make it non-interactive is a pointer-events-none +
          dimmed wrapper. */}
      <div
        className={cn(
          "flex-1 min-h-0 overflow-y-auto px-3 py-2",
          submitting && "pointer-events-none opacity-60",
        )}
        aria-busy={submitting || undefined}
      >
        <ContentEditor
          ref={editorRef}
          defaultValue={initialDraft}
          placeholder={t(($) => $.comment.leave_comment_placeholder)}
          onUpdate={handleEditorUpdate}
          onSubmit={handleSubmit}
          onUploadFile={handleUpload}
          debounceMs={100}
          currentIssueId={issueId}
          attachments={pendingAttachments}
          enableSlashCommands
          slashCommandMode="command"
        />
      </div>
      <div className="absolute bottom-1 left-2 right-28 min-w-0">
        <CommentTriggerChips
          agents={triggerPreview.agents}
          suppressedAgentIds={suppressedAgentIds}
          onToggle={toggleSuppressedAgent}
        />
      </div>
      <div className="absolute bottom-1 right-1.5 flex items-center gap-1">
        <FileUploadButton
          size="sm"
          multiple
          onSelect={(file) => editorRef.current?.uploadFile(file)}
        />
        <SubmitButton
          onClick={handleSubmit}
          disabled={isEmpty}
          loading={submitting}
          tooltip={`${t(($) => $.comment.send_tooltip)} · ${formatShortcut(modKey, enterKey)}`}
        />
      </div>
      {isDragOver && <FileDropOverlay />}
    </div>
  );
}

export { CommentInput };
