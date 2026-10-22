// 上游同步设置：只做三件事——选运行时、定每批条数、看内置提示词。
// Agent 实体的创建与提示词维护都在服务端完成，这里不出现"新建智能体"。
"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { useUpdateUpstreamConfig } from "@multica/core/upstream/mutations";
import type { UpstreamSnapshot } from "@multica/core/upstream/queries";
import { Button } from "@multica/ui/components/ui/button";
import { useWorkspaceId } from "@multica/core/hooks";
import { useT } from "../../i18n";

const RUNNABLE_PROVIDERS = new Set(["codex", "codebuddy"]);

export function UpstreamSettings({ snapshot }: { snapshot?: UpstreamSnapshot }) {
  const { t } = useT("upstream");
  const wsId = useWorkspaceId();
  const runtimes = useQuery(runtimeListOptions(wsId));
  const save = useUpdateUpstreamConfig();

  const [runtimeId, setRuntimeId] = useState("");
  const [enabled,setEnabled]=useState(true);
 const [batchSize, setBatchSize] = useState(200);
  const [initialized,setInitialized]=useState(false);
 const [savedOnce, setSavedOnce] = useState(false);

  // 快照是异步到的：第一次拿到时把服务端值灌进本地表单，
  // 之后用户的输入不再被覆盖。
  useEffect(() => {
    if (!snapshot || initialized) return;
    setInitialized(true);
    setEnabled(snapshot.config.sync_state!=="paused");
    setRuntimeId((prev) => prev || snapshot.config.runtime_id || "");
    setBatchSize((prev) => (prev === 200 ? snapshot.config.batch_size || 200 : prev));
  }, [snapshot,initialized]);

  const usable = (runtimes.data ?? []).filter(
    (r) => RUNNABLE_PROVIDERS.has(r.provider) && (r.status === "online" || r.id === runtimeId),
  );

  const submit = () => {
    const cfg = snapshot?.config;
    save.mutate(
      {
        upstream_url: cfg?.upstream_url || "https://github.com/multica-ai/multica.git",
        upstream_branch: cfg?.upstream_branch || "main",
        local_ref: cfg?.local_ref || "origin/personal-studio-upstream",
        enabled,
        batch_size: batchSize,
        runtime_id: runtimeId || undefined,
      },
      { onSuccess: () => setSavedOnce(true) },
    );
  };

  return (
    <div className="space-y-4 rounded-lg border border-surface-border bg-muted/20 px-4 py-4">
      <div>
        <label htmlFor="upstream-runtime" className="text-body font-medium">{t(($) => $.page.setup.runtime)}</label>
        <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.page.setup.runtime_hint)}</p>
        {usable.length === 0 ? (
          <p className="mt-2 text-caption text-warning">{t(($) => $.page.setup.runtime_empty)}</p>
        ) : (
          <select
            id="upstream-runtime"
            name="upstream-runtime"
            autoComplete="off"
            className="mt-2 h-9 w-full max-w-sm rounded-md border border-input bg-background px-2 text-body"
            value={runtimeId}
            onChange={(event) => {
              setRuntimeId(event.target.value);
              setSavedOnce(false);
            }}
          >
            <option value="">{t(($) => $.page.setup.no_agent)}</option>
            {usable.map((runtime) => (
              <option key={runtime.id} value={runtime.id}>
                {runtimeDisplayLabel(runtime)}
              </option>
            ))}
          </select>
        )}
      </div>

      <div>
        <label htmlFor="upstream-batch" className="text-body font-medium">{t(($) => $.page.setup.batch_size)}</label>
        <input
          id="upstream-batch"
          name="upstream-batch"
          autoComplete="off"
          type="number"
          min={1}
          max={500}
          className="mt-2 h-9 w-32 rounded-md border border-input bg-background px-2 text-body"
          value={batchSize}
          onChange={(event) => {
            setBatchSize(Number(event.target.value) || 200);
            setSavedOnce(false);
          }}
        />
      </div>

      <label className="flex items-center gap-2 text-body"><input type="checkbox" name="upstream-enabled" checked={enabled} onChange={e=>{setEnabled(e.target.checked);setSavedOnce(false)}} />{t(($)=>$.page.setup.enabled)}</label>
 <p className="text-caption text-muted-foreground">{t(($)=>$.page.setup.schedule)}</p>
 <details className="rounded-md border border-surface-border bg-background px-3 py-2">
        <summary className="cursor-pointer text-caption text-muted-foreground">
          {t(($) => $.page.setup.instructions)}
        </summary>
        <p className="mt-1 text-caption text-faint-foreground">{t(($) => $.page.setup.instructions_hint)}</p>
        <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words text-caption text-muted-foreground">
          {snapshot?.instructions ?? ""}
        </pre>
      </details>

      <div className="flex items-center gap-2">
        <Button size="sm" disabled={save.isPending || !runtimeId || !Number.isInteger(batchSize) || batchSize<1 || batchSize>500} onClick={submit}>
          {t(($) => $.page.setup.save)}
        </Button>
        {save.isError ? <p role="alert" className="text-caption text-danger">{save.error.message}</p>:null}
        {savedOnce ? <span aria-live="polite" className="text-caption text-muted-foreground">{t(($) => $.page.setup.saved)}</span> : null}
      </div>
    </div>
  );
}
