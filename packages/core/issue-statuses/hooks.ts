// 本文件提供 React 侧读取「工作区状态目录」的 Hook：拉取并解析、记住失败
// 状态、把重试能力一并放进目录对象。

import { useQuery } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { buildIssueStatusCatalog, issueStatusListOptions, type IssueStatusCatalog } from "./queries";

/**
 * 工作区的状态目录，解析后并 memoize 返回（MUL-6243）。
 *
 * 显式接收 `wsId` 而不是从 context 里读，遵循本仓库的状态规则——目录是按
 * 工作区分隔的，而切换工作区不会重挂应用，模块级快照会过期。
 *
 * @param wsId - 工作区 id
 * @returns 已解析、可按需重试的状态目录
 */
export function useIssueStatuses(wsId: string): IssueStatusCatalog {
  const { data, isPending, isError, refetch } = useQuery({
    ...issueStatusListOptions(wsId),
    enabled: Boolean(wsId),
  });
  // `refetch` 跟着目录一起走，而不只是那个失败标志：被失败的目录卡住的界面
  // 总得给用户一个出口，而从视图层回头去摸 React Query 会重复 query key。
  // (MUL-6243)
  const retry = useCallback(() => {
    void refetch();
  }, [refetch]);
  // pending/error 要一路带过去，不能丢掉：按自定义状态 key 路由的界面，没有
  // 它们就无法区分「目录还没到」和「目录失败了」，而两者需要不同的 UI——
  // 一个 spinner 和一个重试按钮。(MUL-6243)
  return useMemo(
    () => buildIssueStatusCatalog(data, { isPending, isError, retry }),
    [data, isPending, isError, retry],
  );
}
