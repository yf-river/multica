"use client";

// 评论触发预览 hook：根据评论内容里 @ 的智能体/小队，预判发送后会触发谁、以及谁会被阻止。
import { useEffect, useMemo, useRef, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues/queries";
import { parseMentions } from "@multica/core/issues/comment-trigger-outcomes";
import type { CommentReviewReply, CommentTriggerPreviewAgent, CommentTriggerOutcome } from "@multica/core/types";

const COMMENT_TRIGGER_PREVIEW_DEBOUNCE_MS = 300;
const NOTE_COMMAND_RE = /^\/note(?:$|\s)/i;

/** useCommentTriggerPreview 返回的预览结果。 */
export interface UseCommentTriggerPreviewResult {
  /** 发送后会被触发的智能体列表。 */
  agents: CommentTriggerPreviewAgent[];
  // 照原样发出去却「不会」被触发的显式 @agent / @squad 提及（MUL-4525 §2），
  // 让输入框能在发送前发出警告。
  blocked: CommentTriggerOutcome[];
  // 回复审查员时这条草稿会通知谁（GONG-63）。普通评论/线程为 null。
  reviewReply: CommentReviewReply | null;
  // A structured @all mention is a member broadcast, but never starts agents
  // by itself. This is static mention semantics, not a delivery guarantee:
  // edits do not notify, and a new comment may have no eligible recipients.
  hasAllMembersMention: boolean;
}

/**
 * 判断当前草稿是否是 /note 命令（这类评论不会触发智能体）。
 * @param content - 评论草稿内容。
 * @returns 是否为 /note 命令。
 */
export function isNoteCommentDraft(content: string): boolean {
  return NOTE_COMMAND_RE.test(content.replace(/^[ \t\r\n]+/, ""));
}

/**
 * 生成评论触发预览的签名：只由被 @ 的智能体/小队集合决定，用于缓存和去重。
 * 空内容或 /note 命令返回 "empty"。
 * @param content - 评论草稿内容。
 * @returns 用于查询 key 的签名字符串。
 */
export function commentTriggerPreviewSignature(content: string): string {
  if (!content.trim() || isNoteCommentDraft(content)) return "empty";

  const seen = new Set<string>();
  const tokens: string[] = [];
  for (const { type, id } of parseMentions(content)) {
    if (type === "issue") continue;
    const token = `${type}:${id}`;
    if (seen.has(token)) continue;
    seen.add(token);
    tokens.push(token);
  }

  return `nonempty|${tokens.join(",")}`;
}

function queryKeyMatchesPreviewContext(
  queryKey: readonly unknown[] | undefined,
  issueId: string,
  parentId: string,
  editingCommentId: string,
) {
  if (!queryKey) return false;
  const prefix = issueKeys.commentTriggerPreview(issueId);
  return (
    prefix.every((part, index) => queryKey[index] === part) &&
    queryKey[prefix.length] === parentId &&
    queryKey[prefix.length + 1] === editingCommentId
  );
}

function useDebouncedSignature(signature: string) {
  const [debouncedSignature, setDebouncedSignature] = useState("empty");

  useEffect(() => {
    if (signature === "empty") {
      setDebouncedSignature("empty");
      return;
    }

    const timer = window.setTimeout(() => {
      setDebouncedSignature(signature);
    }, COMMENT_TRIGGER_PREVIEW_DEBOUNCE_MS);

    return () => window.clearTimeout(timer);
  }, [signature]);

  return debouncedSignature;
}

/**
 * 根据评论草稿内容实时预览会触发的智能体和会被阻止的提及。
 * @param options.issueId - 问题 id。
 * @param options.parentId - 父评论 id（回复场景）。
 * @param options.editingCommentId - 正在编辑的评论 id。
 * @param options.content - 评论草稿内容。
 * @returns 触发预览结果（agents + blocked）。
 */
export function useCommentTriggerPreview({
  issueId,
  parentId,
  editingCommentId,
  content,
}: {
  issueId: string;
  parentId?: string;
  editingCommentId?: string;
  content: string;
}): UseCommentTriggerPreviewResult {
  const signature = useMemo(() => commentTriggerPreviewSignature(content), [content]);
  const hasAllMembersMention = useMemo(
    () => parseMentions(content).some(({ type, id }) => type === "all" && id === "all"),
    [content],
  );
  const debouncedSignature = useDebouncedSignature(signature);
  const contentRef = useRef(content);
  const parentKey = parentId ?? "";
  const editingKey = editingCommentId ?? "";

  useEffect(() => {
    contentRef.current = content;
  }, [content]);

  const previewQuery = useQuery({
    queryKey: [...issueKeys.commentTriggerPreview(issueId), parentKey, editingKey, debouncedSignature],
    queryFn: () => api.previewCommentTriggers(issueId, contentRef.current, parentId, editingCommentId),
    enabled: signature !== "empty" && debouncedSignature !== "empty",
    retry: false,
    // 答案取决于实时的队列状态（待处理任务去重），而不仅是提及集合，所以缓存结果
    // 在其签名再次出现时也必须重新校验——这里如果设成 Infinity，曾经把一个「没人触发」
    // 的过期快照钉死，而那快照是在智能体还在排队时拍的。
    staleTime: 0,
    // 只在同一个输入框上下文重新请求时，才保留上一次的智能体列表。
    // 跨问题/父评论/编辑上下文时绝不能显示来自另一个输入框的过期 chip。
    placeholderData: (previousData, previousQuery) =>
      queryKeyMatchesPreviewContext(previousQuery?.queryKey, issueId, parentKey, editingKey)
        ? keepPreviousData(previousData)
        : undefined,
  });

  // 加载中和错误都故意表现为「没有智能体」：预览只是增强功能，
  // 输入框对空列表什么都不渲染。
  if (signature === "empty" || debouncedSignature === "empty") {
    return { agents: [], blocked: [], reviewReply: null, hasAllMembersMention };
  }

  return {
    agents: previewQuery.data?.agents ?? [],
    blocked: previewQuery.data?.blocked ?? [],
    reviewReply: previewQuery.data?.review_reply ?? null,
    hasAllMembersMention,
  };
}
