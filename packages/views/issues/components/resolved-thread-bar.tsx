// 本文件：ResolvedThreadBar / CommentsFoldBar —— 已解决评论线程的两条折叠条。
//
// ResolvedThreadBar：整条线程折叠——ROOT 评论被解决（"解决线程"）。
//   整条线程（根 + 每条回复）收进这一条。
// CommentsFoldBar：中间折叠——某条回复是解决点（"带评论解决线程"）。
//   根与解决回复保持可见，其余回复收进这条位于两者之间的条。
//
// 两者共用同一套"作者标签"逻辑（去重 + 至多命名 MAX_NAMED_AUTHORS 位）。

import { CheckCircle2, ChevronRight } from "lucide-react";
import { useActorName } from "@multica/core/workspace/hooks";
import { Card } from "@multica/ui/components/ui/card";
import type { TimelineEntry } from "@multica/core/types";
import { isDeletedComment } from "@multica/core/issues/comment-deletion";
import { useT } from "../../i18n";

interface ResolvedThreadBarProps {
  /** 已解决的根评论。 */
  entry: TimelineEntry;
  /**
   * 线程根下全部嵌套回复的平铺列表。由 `issue-detail.tsx` 的 `timelineView`
   * 在与 CommentCard 同一条遍历里预算好，保证计数 + 作者列表与展开视图
   * 渲染的一致（只看直接子级会少算嵌套回复）。
   */
  replies: TimelineEntry[];
  onExpand: () => void;
}

const MAX_NAMED_AUTHORS = 2;

// `entries` 里按首次出现顺序去重后的作者们，压成一段标签
//（"Alice"、"Alice, Bob"、"Alice, Bob 与另外 2 人"）。两条条共用。
function useAuthorsLabel(entries: TimelineEntry[]): string {
  const { t } = useT("issues");
  const { getActorName } = useActorName();

  const seen = new Set<string>();
  const authors: Array<{ type: string; id: string }> = [];
  for (const e of entries) {
    if (isDeletedComment(e)) continue;
    const key = `${e.actor_type}:${e.actor_id}`;
    if (seen.has(key)) continue;
    seen.add(key);
    authors.push({ type: e.actor_type, id: e.actor_id });
  }

  if (authors.length <= MAX_NAMED_AUTHORS) {
    return authors.map((a) => getActorName(a.type, a.id)).join(", ");
  }
  const named = authors.slice(0, MAX_NAMED_AUTHORS).map((a) => getActorName(a.type, a.id)).join(", ");
  return t(($) => $.comment.resolve.bar_authors_more, {
    names: named,
    count: authors.length - MAX_NAMED_AUTHORS,
  });
}

/**
 * 整条线程折叠条（职责见文件头）。
 */
export function ResolvedThreadBar({ entry, replies, onExpand }: ResolvedThreadBarProps) {
  const { t } = useT("issues");
  const authorsLabel = useAuthorsLabel([entry, ...replies]);
  // Deleted replies render nothing when the thread is expanded, so the folded
  // count must not promise them either.
  const count = 1 + replies.filter((reply) => !isDeletedComment(reply)).length;

  return (
    <Card className="!py-0 !gap-0 overflow-hidden">
      <button
        type="button"
        onClick={onExpand}
        className="flex w-full items-center justify-between px-4 py-3 text-left transition-colors cursor-pointer hover:bg-muted/50"
      >
        <span className="flex min-w-0 items-center gap-2.5 text-body text-muted-foreground">
          <CheckCircle2 className="h-4 w-4 shrink-0" />
          <span className="truncate">
            {t(($) => $.comment.resolve.bar, { count, authors: authorsLabel })}
          </span>
        </span>
        <ChevronRight className="h-3.5 w-3.5 rotate-90 shrink-0 text-muted-foreground" />
      </button>
    </Card>
  );
}

interface CommentsFoldBarProps {
  /** 收在条后的非解决回复。 */
  replies: TimelineEntry[];
  onExpand: () => void;
}

/**
 * 中间折叠条（职责见文件头）。
 */
export function CommentsFoldBar({ replies, onExpand }: CommentsFoldBarProps) {
  const { t } = useT("issues");
  const authorsLabel = useAuthorsLabel(replies);

  return (
    <button
      type="button"
      onClick={onExpand}
      className="flex w-full items-center justify-between rounded-md bg-muted/45 px-3 py-2.5 text-left transition-colors cursor-pointer hover:bg-muted"
    >
      <span className="flex min-w-0 items-center gap-2.5 text-body text-muted-foreground">
        <ChevronRight className="h-3.5 w-3.5 rotate-90 shrink-0" />
        <span className="truncate">
          {t(($) => $.comment.resolve.fold, { count: replies.length, authors: authorsLabel })}
        </span>
      </span>
    </button>
  );
}
