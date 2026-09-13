"use client";

// 本文件：SourceContextContent —— 来源上下文的统一内容展示层。
// 负责渲染源问题快照的标题/描述 + 捕获的评论线程时间线，并把"相对快照的
// 变化"就地标注出来。预览与持久化查看等各类展示面共用它，保证层级与间距
// 视觉一致，不因调用方不同而漂移。

import type { ReactNode } from "react";
import type {
  SourceContextCommentSnapshot,
  SourceContextIssueSnapshot,
} from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { ReadonlyContent } from "../../editor";
import { SourceContextCommentList } from "./source-context-comment-list";

/**
 * 源问题快照 + 捕获评论线程的共享展示（职责见文件头）。
 *
 * "源问题是否已变化、现在是否可用"等新鲜度/状态文案由调用方负责，
 * 本组件只负责内容层级与间距——这样"预览"与"持久化的快照"两种展示面
 * 不会在视觉上彼此漂移。`issueTitleSuffix` 可挂在标题后追加标识
 * （如"现在已是 MUL-xxx"）；差异标注相关 props 原样透传给评论列表。
 */
export function SourceContextContent({
  sourceIssue,
  comments,
  anchorCommentId,
  issueTitleSuffix,
  getAuthorLabel,
  changedCommentIds,
  addedComments,
  removedCommentIds,
  className,
}: {
  sourceIssue: SourceContextIssueSnapshot;
  comments: SourceContextCommentSnapshot[];
  anchorCommentId: string;
  issueTitleSuffix?: ReactNode;
  getAuthorLabel?: (comment: SourceContextCommentSnapshot) => string;
  changedCommentIds?: string[];
  addedComments?: SourceContextCommentSnapshot[];
  removedCommentIds?: string[];
  className?: string;
}) {
  return (
    <div
      data-slot="source-context-content"
      className={cn("space-y-8", className)}
    >
      <section className="space-y-4">
        <h3 className="break-words text-body font-semibold text-pretty">
          {sourceIssue.identifier} · {sourceIssue.title}
          {issueTitleSuffix}
        </h3>
        {sourceIssue.description && (
          <ReadonlyContent content={sourceIssue.description} />
        )}
      </section>
      <SourceContextCommentList
        comments={comments}
        anchorCommentId={anchorCommentId}
        getAuthorLabel={getAuthorLabel}
        changedCommentIds={changedCommentIds}
        addedComments={addedComments}
        removedCommentIds={removedCommentIds}
      />
    </div>
  );
}
