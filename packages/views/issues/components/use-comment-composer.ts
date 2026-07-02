"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@multica/core/api";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import {
  useCommentDraftStore,
  type CommentDraftKey,
} from "@multica/core/issues/stores";
import type { Attachment } from "@multica/core/types";
import { contentReferencesAttachment } from "@multica/core/types";
import {
  type ContentEditorRef,
  useFileDropZone,
} from "../../editor";
import { useCommentTriggerPreview } from "../hooks/use-comment-trigger-preview";

interface UseCommentComposerOptions {
  issueId: string;
  parentId?: string;
  draftKey?: CommentDraftKey;
  onSubmit: (
    content: string,
    attachmentIds?: string[],
    suppressAgentIds?: string[],
  ) => Promise<boolean>;
}

export function useCommentComposer({
  issueId,
  parentId,
  draftKey,
  onSubmit,
}: UseCommentComposerOptions) {
  const editorRef = useRef<ContentEditorRef>(null);
  const initialDraft = draftKey
    ? useCommentDraftStore.getState().getDraft(draftKey)
    : undefined;
  const [content, setContent] = useState(initialDraft ?? "");
  const [isEmpty, setIsEmpty] = useState(() => !initialDraft?.trim());
  const [submitting, setSubmitting] = useState(false);
  const [suppressedAgentIds, setSuppressedAgentIds] = useState<Set<string>>(
    () => new Set(),
  );
  const [pendingAttachments, setPendingAttachments] = useState<Attachment[]>([]);
  const triggerPreview = useCommentTriggerPreview({
    issueId,
    parentId,
    content,
  });
  const setDraft = useCommentDraftStore((s) => s.setDraft);
  const clearDraft = useCommentDraftStore((s) => s.clearDraft);
  const { uploadWithToast } = useFileUpload(api);
  const { isDragOver, dropZoneProps } = useFileDropZone({
    onDrop: (files) => files.forEach((file) => editorRef.current?.uploadFile(file)),
  });

  useEffect(() => {
    if (!draftKey) return;
    const flush = () => {
      const markdown = editorRef.current?.getMarkdown();
      if (markdown && markdown.trim().length > 0) {
        setDraft(draftKey, markdown);
      }
    };
    const onVisibilityChange = () => {
      if (document.visibilityState === "hidden") flush();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("pagehide", flush);
    return () => {
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener("pagehide", flush);
    };
  }, [draftKey, setDraft]);

  const handleUpload = useCallback(
    async (file: File) => {
      const result = await uploadWithToast(file, { issueId });
      if (result) {
        setPendingAttachments((prev) => [...prev, result]);
      }
      return result;
    },
    [uploadWithToast, issueId],
  );

  useEffect(() => {
    setSuppressedAgentIds(new Set());
  }, [issueId, parentId]);

  useEffect(() => {
    const visible = new Set(triggerPreview.agents.map((agent) => agent.id));
    setSuppressedAgentIds((prev) => {
      const next = new Set([...prev].filter((id) => visible.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [triggerPreview.agents]);

  const toggleSuppressedAgent = useCallback((agentId: string) => {
    setSuppressedAgentIds((prev) => {
      const next = new Set(prev);
      if (next.has(agentId)) next.delete(agentId);
      else next.add(agentId);
      return next;
    });
  }, []);

  const handleEditorUpdate = useCallback(
    (markdown: string) => {
      setContent(markdown);
      setIsEmpty(!markdown.trim());
      if (!draftKey) return;
      if (markdown.trim().length > 0) setDraft(draftKey, markdown);
      else clearDraft(draftKey);
    },
    [clearDraft, draftKey, setDraft],
  );

  const handleSubmit = useCallback(async () => {
    const markdown = editorRef.current
      ?.getMarkdown()
      ?.replace(/(\n\s*)+$/, "")
      .trim();
    if (!markdown || submitting) return;

    const activeIds = pendingAttachments
      .filter((attachment) => contentReferencesAttachment(markdown, attachment))
      .map((attachment) => attachment.id);
    const suppressAgentIds = triggerPreview.agents
      .filter((agent) => suppressedAgentIds.has(agent.id))
      .map((agent) => agent.id);

    setSubmitting(true);
    try {
      const ok = await onSubmit(
        markdown,
        activeIds.length > 0 ? activeIds : undefined,
        suppressAgentIds.length > 0 ? suppressAgentIds : undefined,
      );
      if (ok) {
        editorRef.current?.clearContent();
        setContent("");
        setIsEmpty(true);
        setSuppressedAgentIds(new Set());
        setPendingAttachments([]);
        if (draftKey) clearDraft(draftKey);
      }
    } finally {
      setSubmitting(false);
    }
  }, [
    clearDraft,
    draftKey,
    onSubmit,
    pendingAttachments,
    submitting,
    suppressedAgentIds,
    triggerPreview.agents,
  ]);

  return {
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
  };
}
