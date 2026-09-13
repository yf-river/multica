"use client";

// 本文件：FindBar —— 问题详情页的"页内查找"浮动条。
// 纯展示组件：所有搜索状态、高亮与滚动逻辑都在 `useInPageFind` 里。
// 仅在 `find.open` 时被渲染，因此输入框的 autoFocus 每次打开都会触发。
//
// `data-find-ignore` 让本浮动条自身文字不被纳入匹配遍历范围。

import type { KeyboardEvent } from "react";
import { ChevronDown, ChevronUp, Search, X } from "lucide-react";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import type { UseInPageFindResult } from "../hooks/use-in-page-find";

/**
 * 页内查找浮动条。props 见文件头说明；`find` 聚合了查询词、命中计数、
 * 当前命中的高亮位置与上一条/下一条/关闭等动作。
 */
export function FindBar({
  find,
  className,
}: {
  find: UseInPageFindResult;
  className?: string;
}) {
  const { t } = useT("issues");
  const {
    query,
    matchCount,
    activeIndex,
    setQuery,
    closeFind,
    goNext,
    goPrev,
    inputRef,
  } = find;

  const hasQuery = query.trim().length > 0;
  const countLabel = !hasQuery
    ? ""
    : matchCount === 0
      ? t(($) => $.detail.find.no_matches)
      : t(($) => $.detail.find.count, {
          current: activeIndex + 1,
          total: matchCount,
        });
  const noMatches = matchCount === 0;

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      if (e.shiftKey) goPrev();
      else goNext();
    } else if (e.key === "Escape") {
      e.preventDefault();
      closeFind();
    }
  };

  return (
    <div
      data-find-ignore
      role="search"
      className={cn(
        "flex items-center gap-1 rounded-lg border bg-popover/95 p-1 pl-2 shadow-md backdrop-blur supports-[backdrop-filter]:bg-popover/80",
        className,
      )}
    >
      <Search className="size-3.5 shrink-0 text-muted-foreground" />
      <Input
        ref={inputRef}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={handleKeyDown}
        autoFocus
        placeholder={t(($) => $.detail.find.placeholder)}
        aria-label={t(($) => $.detail.find.placeholder)}
        className="h-7 w-44 border-0 bg-transparent px-1 shadow-none focus-visible:ring-0"
      />
      <span className="min-w-[3.5rem] shrink-0 whitespace-nowrap text-right text-caption tabular-nums text-muted-foreground">
        {countLabel}
      </span>
      <div className="flex items-center">
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          disabled={noMatches}
          onClick={goPrev}
          aria-label={t(($) => $.detail.find.previous)}
          title={t(($) => $.detail.find.previous)}
        >
          <ChevronUp />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          disabled={noMatches}
          onClick={goNext}
          aria-label={t(($) => $.detail.find.next)}
          title={t(($) => $.detail.find.next)}
        >
          <ChevronDown />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          onClick={closeFind}
          aria-label={t(($) => $.detail.find.close)}
          title={t(($) => $.detail.find.close)}
        >
          <X />
        </Button>
      </div>
    </div>
  );
}
