"use client";

// 状态名称解析 hook：给定状态 KEY，返回界面应当显示的标签。
// 负责把内置状态的英文种子名翻译成当前语言，同时让自定义状态沿用工作区自己起的名字。
import { useCallback } from "react";
import { isIssueStatusCategory } from "@multica/core/issue-statuses";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import type { IssueStatusCategory } from "@multica/core/types";
import { useT } from "../../i18n";

/**
 * 把状态 KEY 解析成界面要显示的标签（MUL-6243）。
 *
 * 状态目录为每个状态（含 7 个内置状态）都带一个 `name`，但内置状态的这些名字是
 * 服务端用英文种下的。直接渲染会把非英文工作区里的「进行中」显示成 "In Progress"，
 * 所以内置状态一律走 i18n 翻译；只有自定义状态——名字由工作区自己起、不可翻译——
 * 才用状态目录里的 `name`。
 *
 * 遇到目录里完全不认识的状态 KEY（比如几秒前在别处刚创建、或请求还在路上）时，
 * 回退到显示原始 KEY，而不是渲染成空白。
 */
export function useStatusLabel(wsId: string) {
  const { t } = useT("issues");
  const { entryOf } = useIssueStatuses(wsId);

  return useCallback(
    (statusKey: string): string => {
      if (isIssueStatusCategory(statusKey)) {
        return t(($) => $.status[statusKey as IssueStatusCategory]);
      }
      return entryOf(statusKey)?.name ?? statusKey;
    },
    [t, entryOf],
  );
}
