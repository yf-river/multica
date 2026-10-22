// 上游同步页（"/upstream"）：顶部状态条 + 两个横向页签。
// 页面只读展示服务端台账；所有 git 操作都在 daemon 侧 Agent 里完成。
"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { GitMerge, Settings } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { upstreamSnapshotOptions } from "@multica/core/upstream/queries";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";
import { PendingTab } from "./pending-tab";
import { RunsTab } from "./runs-tab";
import { UpstreamSettings } from "./settings-panel";
import { Button } from "@multica/ui/components/ui/button";

function shortSha(sha: string): string {
  return sha ? sha.slice(0, 8) : "";
}

export function UpstreamPage() {
  const { t } = useT("upstream");
  const wsId = useWorkspaceId();
  const [tab, setTab] = useState("pending");
  const [showSettings, setShowSettings] = useState(false);
  const { data, isLoading, error } = useQuery(upstreamSnapshotOptions(wsId));

  const config = data?.config;
  const head = config?.head_sha ?? "";
  const running = !!data?.active_run_id;

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {error ? <p role="alert" className="px-4 py-2 text-body text-danger">{error.message}</p>:null}
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-surface-border px-4 py-4 sm:px-6">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <GitMerge aria-hidden="true" className="h-4 w-4 text-muted-foreground" />
            <h1 className="text-title font-semibold">{t(($) => $.page.title)}</h1>
          </div>
          <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.page.tagline)}</p>
        </div>
        <div className="flex shrink-0 items-start gap-3">
        <Button size="sm" variant="ghost" aria-expanded={showSettings} onClick={() => setShowSettings((v) => !v)}>
          <Settings aria-hidden="true" className="mr-1 h-3.5 w-3.5" />
          {t(($) => $.page.setup.title)}
        </Button>
        <div className="text-right text-caption text-muted-foreground">
          {isLoading ? (
            <Skeleton className="h-4 w-32" />
          ) : (
            <>
              <div>{head ? t(($) => $.page.status.head, { sha: shortSha(head) }) : t(($) => $.page.status.never)}</div>
              {running ? (
                <div className="text-brand">{t(($) => $.page.status.running)}</div>
              ) : null}
              {!config?.agent_id ? (
                <div className="text-warning">{t(($) => $.page.status.no_agent)}</div>
              ) : null}
              {config?.error ? <div className="text-danger">{t(($) => $.page.status.error)}</div> : null}
            </>
          )}
        </div>
        </div>
      </header>

      <Tabs value={tab} onValueChange={setTab} className="flex min-h-0 flex-1 flex-col">
        <div className="border-b border-surface-border px-4 sm:px-6">
          <TabsList variant="line" className="w-full justify-start">
            <TabsTrigger value="pending">
              {t(($) => $.page.tabs.pending)}
              {data?.pending ? <span className="ml-1.5 text-faint-foreground">{data.pending}</span> : null}
            </TabsTrigger>
            <TabsTrigger value="runs">{t(($) => $.page.tabs.runs)}</TabsTrigger>
          </TabsList>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
          {showSettings ? (
            <div className="mb-4">
              <UpstreamSettings snapshot={data} />
            </div>
          ) : null}
          <TabsContent value="pending" className="mt-0">
            <PendingTab wsId={wsId} />
          </TabsContent>
          <TabsContent value="runs" className="mt-0">
            <RunsTab wsId={wsId} />
          </TabsContent>
        </div>
      </Tabs>
    </div>
  );
}
