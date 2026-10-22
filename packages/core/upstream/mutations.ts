// 上游同步的写入操作。采纳只是一次标记：不改代码、不建任务。
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { upstreamKeys } from "./queries";

export function useMarkUpstreamItems() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (body: { ids: string[]; status: "adopted" | "ignored" | "open" }) =>
      api.markUpstreamItems(body),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: upstreamKeys.all(wsId) });
    },
  });
}
