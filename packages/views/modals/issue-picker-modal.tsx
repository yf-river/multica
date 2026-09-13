// 本文件职责：通用「搜索并选择一个 issue」的弹窗——命令式搜索框（输入防抖 + 可中断请求），
// 结果列表排除指定 id 后供点击选择；被「设父/设子」等弹窗复用。
"use client";

import { issueStatusCategory } from "@multica/core/issues";
import { useState, useEffect, useCallback, useRef } from "react";
import type { Issue } from "@multica/core/types";
import { api } from "@multica/core/api";
import {
  Command,
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
} from "@multica/ui/components/ui/command";
import { StatusIcon } from "../issues/components/status-icon";
import { useT } from "../i18n";

/** IssuePickerModal 的入参约定。 */
interface IssuePickerModalProps {
  /** 是否打开弹窗 */
  open: boolean;
  /** 弹窗开关变化的回调（点击遮罩/按 Esc 时为 false） */
  onOpenChange: (open: boolean) => void;
  /** 弹窗标题 */
  title: string;
  /** 弹窗副标题说明 */
  description: string;
  /** 结果中要排除的 issue id（如当前 issue 自身、已有的父/子 issue） */
  excludeIds: string[];
  /** 用户选中某条 issue 时的回调 */
  onSelect: (issue: Issue) => void;
}

/**
 * 通用「搜索并选择 issue」弹窗。
 *
 * 基于 Command 的搜索选择器：输入关键字（300ms 防抖）远程搜索最多 20 条 issue，
 * 结果中剔除 `excludeIds` 后再展示，点击即回调 `onSelect` 并关闭。关闭时重置内部
 * 查询状态，保证下次打开是干净的初始态。
 */
export function IssuePickerModal({
  open,
  onOpenChange,
  title,
  description,
  excludeIds,
  onSelect,
}: IssuePickerModalProps) {
  const { t } = useT("modals");
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<Issue[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const abortRef = useRef<AbortController>(undefined);

  // 弹窗关闭时把搜索状态打回初始态，避免下次打开还残留上一次的关键字与结果。
  useEffect(() => {
    if (!open) {
      setQuery("");
      setResults([]);
      setIsLoading(false);
    }
  }, [open]);

  const search = useCallback(
    (q: string) => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
      if (abortRef.current) abortRef.current.abort();

      if (!q.trim()) {
        setResults([]);
        setIsLoading(false);
        return;
      }

      setIsLoading(true);
      debounceRef.current = setTimeout(async () => {
        const controller = new AbortController();
        abortRef.current = controller;
        try {
          const res = await api.searchIssues({
            q: q.trim(),
            limit: 20,
            include_closed: true,
            signal: controller.signal,
          });
          if (!controller.signal.aborted) {
            setResults(res.issues.filter((i) => !excludeIds.includes(i.id)));
            setIsLoading(false);
          }
        } catch {
          if (!controller.signal.aborted) {
            setIsLoading(false);
          }
        }
      }, 300);
    },
    [excludeIds],
  );

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={description}
    >
      <Command shouldFilter={false}>
        <CommandInput
          placeholder={t(($) => $.issue_picker.search_placeholder)}
          value={query}
          onValueChange={(v) => {
            setQuery(v);
            search(v);
          }}
        />
        <CommandList>
          {isLoading && (
            <div className="py-6 text-center text-body text-muted-foreground">
              {t(($) => $.issue_picker.searching)}
            </div>
          )}
          {!isLoading && query.trim() && results.length === 0 && (
            <CommandEmpty>{t(($) => $.issue_picker.no_results)}</CommandEmpty>
          )}
          {!isLoading && !query.trim() && (
            <div className="py-6 text-center text-body text-muted-foreground">
              {t(($) => $.issue_picker.prompt_to_search)}
            </div>
          )}
          {results.length > 0 && (
            <CommandGroup>
              {results.map((issue) => (
                <CommandItem
                  key={issue.id}
                  value={issue.id}
                  onSelect={() => {
                    onSelect(issue);
                    onOpenChange(false);
                  }}
                >
                  <StatusIcon
                    status={issue.status}
                    category={issueStatusCategory(issue) ?? undefined}
                    className="h-3.5 w-3.5 shrink-0"
                  />
                  <span className="text-muted-foreground shrink-0">{issue.identifier}</span>
                  <span className="truncate">{issue.title}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          )}
        </CommandList>
      </Command>
    </CommandDialog>
  );
}
