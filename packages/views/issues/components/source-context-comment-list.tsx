"use client";

// 本文件：SourceContextCommentList —— 来源上下文里的评论线程时间线列表。
// 把捕获快照中的评论（含相对快照后来新增的评论）按创建时间升序合并渲染，
// 并对相对快照发生变化的评论（新增/修改/删除）就地标注差异。

import type { SourceContextCommentSnapshot } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { ReadonlyContent } from "../../editor";
import { useT } from "../../i18n";

/**
 * 评论线程时间线列表（职责见文件头）。
 *
 * `comments` 是捕获时刻的评论；`addedComments` 是相对快照后来新增的评论
 * （若与捕获快照重叠会被去重）。`changedCommentIds` / `removedCommentIds` /
 * `addedComments` 用于给差异评论加底色并朗读差异类型。锚点评论
 * （`anchorCommentId`）会带一枚"来源评论"徽章；作者名默认取自快照，
 * 需要展示"捕获后改名/归档"等状态时由 `getAuthorLabel` 覆写。
 */
export function SourceContextCommentList({
  comments,
  anchorCommentId,
  getAuthorLabel,
  changedCommentIds,
  addedComments,
  removedCommentIds,
  className,
}: {
  comments: SourceContextCommentSnapshot[];
  anchorCommentId: string;
  getAuthorLabel?: (comment: SourceContextCommentSnapshot) => string;
  changedCommentIds?: string[];
  addedComments?: SourceContextCommentSnapshot[];
  removedCommentIds?: string[];
  className?: string;
}) {
  const { t } = useT("issues");
  const changedComments = new Set(changedCommentIds ?? []);
  const removedComments = new Set(removedCommentIds ?? []);
  const capturedCommentIds = new Set(comments.map((comment) => comment.id));
  const addedOnly = (addedComments ?? []).filter((comment) => !capturedCommentIds.has(comment.id));
  const addedCommentIds = new Set(addedOnly.map((comment) => comment.id));
  const displayComments = [...comments, ...addedOnly].sort((left, right) => {
    if (left.created_at !== right.created_at) return left.created_at < right.created_at ? -1 : 1;
    if (left.id === right.id) return 0;
    return left.id < right.id ? -1 : 1;
  });

  return (
    <ol
      className={cn("space-y-3", className)}
      aria-label={t(($) => $.source_context.thread_history)}
    >
      {displayComments.map((comment) => {
        const changeKind = removedComments.has(comment.id)
          ? "deleted"
          : addedCommentIds.has(comment.id)
            ? "added"
            : changedComments.has(comment.id)
              ? "changed"
              : null;
        const changeLabel = changeKind === "added"
          ? t(($) => $.source_context.comment_change_added)
          : changeKind === "changed"
            ? t(($) => $.source_context.comment_change_changed)
            : changeKind === "deleted"
              ? t(($) => $.source_context.comment_change_deleted)
              : null;
        return (
          <li
            key={comment.id}
            className="border-l-2 pl-3 transition-colors duration-150 hover:border-l-foreground/50"
          >
            <div
              className={cn(
                changeKind && "-my-1 -mr-2 rounded-md px-2 py-1.5",
                changeKind === "added" && "bg-success/5",
                changeKind === "changed" && "bg-amber-500/5",
                changeKind === "deleted" && "bg-destructive/5",
              )}
              data-source-context-change-kind={changeKind ?? undefined}
            >
              <div className="flex min-w-0 items-center gap-2 text-caption font-medium text-muted-foreground">
                <span className="truncate">
                  {addedCommentIds.has(comment.id)
                    ? comment.author.name
                    : getAuthorLabel?.(comment) ?? comment.author.name}
                </span>
                {comment.id === anchorCommentId && (
                  <span className="shrink-0 rounded bg-info/10 px-1.5 py-0.5 text-info">
                    {t(($) => $.source_context.source_comment)}
                  </span>
                )}
                {changeLabel && <span className="sr-only">{changeLabel}</span>}
              </div>
              <div className="mt-1.5 break-words">
                {comment.deleted === true ? (
                  <p className="italic text-muted-foreground">{t(($) => $.comment.deleted_placeholder)}</p>
                ) : (
                  <ReadonlyContent content={comment.content} />
                )}
              </div>
            </div>
          </li>
        );
      })}
    </ol>
  );
}
