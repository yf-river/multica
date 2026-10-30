"use client";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useLocale } from "../../i18n";
import { Button } from "@multica/ui/components/ui/button";
export function DeploymentsTab({
  projectId,
  canWrite,
}: {
  projectId: string;
  canWrite: boolean;
}) {
  const zh = useLocale().startsWith("zh");
  const qc = useQueryClient();
  const key = ["upstream-deployments", projectId];
  const query = useQuery({
    queryKey: key,
    queryFn: () => api.getUpstreamDeployments(projectId),
    refetchInterval: 5000,
  });
  const action = useMutation({
    mutationFn: (value: {
      action: "prepare" | "deploy" | "recover";
      version?: string;
    }) => api.prepareUpstreamDeployment(projectId, value.action, value.version),
    onSettled: () => qc.invalidateQueries({ queryKey: key }),
  });
  const busy =
    query.data?.jobs.some(
      (j) => j.status === "pending" || j.status === "running",
    ) === true;
  const labels: Record<string, string> = zh
    ? {
        pending: "等待执行",
        running: "执行中",
        succeeded: "已完成",
        pending_acceptance: "待业务验收",
        failed: "失败",
        interrupted: "已中断",
      }
    : {};
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-body font-medium">
          {zh ? "部署管理" : "Deployments"}
        </h2>
        <Button
          disabled={
            !canWrite || !query.data?.available || busy || action.isPending
          }
          onClick={() => action.mutate({ action: "prepare" })}
        >
          {zh ? "准备候选并验证" : "Prepare and validate candidate"}
        </Button>
      </div>
      {query.data?.available === false ? (
        <p className="text-body text-muted-foreground">
          {zh
            ? "当前环境尚未配置独立部署目标。"
            : "No isolated deployment target configured in this environment."}
        </p>
      ) : null}
      {query.error || action.error ? (
        <p role="alert" className="text-body text-destructive">
          {(query.error ?? action.error)?.message}
        </p>
      ) : null}
      {query.data?.jobs.map((job) => {
        const version =
          typeof job.result.version === "string"
            ? job.result.version
            : job.version;
        const verified = job.result.status === "verified";
        return (
          <article key={job.id} className="rounded-lg border p-4 text-body">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <strong>{({prepare: zh ? "准备候选" : "Prepare candidate", deploy: zh ? "部署" : "Deploy", recover: zh ? "恢复版本" : "Restore version"} as Record<string, string>)[job.action] ?? job.action} · {version || "—"}</strong>
              <span>{labels[job.status] ?? job.status}</span>
              {job.action === "deploy" &&
              ["pending_acceptance", "interrupted", "failed"].includes(
                job.status,
              ) ? (
                <Button
                  variant="outline"
                  disabled={!canWrite || busy || action.isPending}
                  onClick={() => action.mutate({ action: "recover" })}
                >
                  {zh ? "恢复上一版本" : "Restore previous version"}
                </Button>
              ) : null}
              {job.action === "prepare" && verified ? (
                <Button
                  disabled={!canWrite || busy || action.isPending}
                  onClick={() => {
                    if (
                      window.confirm(
                        zh
                          ? `部署 OpenViking ${version}？`
                          : `Deploy OpenViking ${version}?`,
                      )
                    )
                      action.mutate({ action: "deploy", version });
                  }}
                >
                  {zh ? "部署" : "Deploy"}
                </Button>
              ) : null}
            </div>
            <p className="mt-2 text-caption text-muted-foreground">
              {new Date(job.updated_at).toLocaleString()}
            </p>
            {job.error ? (
              <p className="mt-2 break-words text-destructive">{job.error}</p>
            ) : null}
            <details className="mt-2">
              <summary>
                {zh ? "验证与部署结果" : "Validation and deployment result"}
              </summary>
              <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words text-caption">
                {JSON.stringify(job.result, null, 2)}
              </pre>
            </details>
          </article>
        );
      })}
    </div>
  );
}
