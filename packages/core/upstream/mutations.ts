// 上游同步的写入操作。采纳只是一次标记：不改代码、不建任务。
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { upstreamKeys } from "./queries";

export function useMarkUpstreamItems() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (body: { ids: string[]; status: "adopted" | "ignored" | "open"; project_id?: string }) =>
      api.markUpstreamItems(body),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: upstreamKeys.all(wsId) });
    },
  });
}

export function useCreateUpstreamAbsorptionTask(projectId="multica") {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (itemId: string) => api.createUpstreamAbsorptionTask(itemId,projectId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: upstreamKeys.all(wsId) });
    },
  });
}

export function useUpdateUpstreamConfig() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (body: {
      project_id?: string;
      bootstrap_base_sha?: string;
      upstream_url: string;
      upstream_branch: string;
      local_ref: string;
      batch_size: number;
      schedule_hour: number;
      enabled?: boolean;
      runtime_id?: string;
      model?: string;
      thinking_level?: string;
      instructions?: string;
    }) => api.updateUpstreamConfig(body),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: upstreamKeys.all(wsId) });
    },
  });
}

