"use client";

import { useMemo, type ReactNode } from "react";
import { Activity, AlertTriangle, CheckCircle2, GitBranch, ListChecks, Timer } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { issueExecutionTreeOptions, issueListOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Issue, IssueTimelineNode, IssueExecutionTreeResponse } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { PageHeader } from "../../layout/page-header";
import { AppLink, useNavigation } from "../../navigation";

const STAGES = [
  { key: "pm", label: "PM", names: ["pm"] },
  { key: "01", label: "01 澄清", names: ["01", "01-clarify", "clarify"] },
  { key: "02", label: "02 设计", names: ["02", "02-design", "design"] },
  { key: "03", label: "03 拆分", names: ["03", "03-task-split", "split"] },
  { key: "04", label: "04 开发", names: ["04", "04-implement", "implement"] },
  { key: "05", label: "05 验证", names: ["05", "05-verify", "verify"] },
] as const;

const TARGET_CHILD_PROJECTS = ["gateway", "ida-deployment"] as const;
const ISSUE_REVIEW_DRAFT_DATASET_NAME = "Issue 复盘评测 Draft";

export function RunReviewsPage() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const selectedIssueId = navigation.searchParams.get("issue");
  const issuesQuery = useQuery(issueListOptions(wsId, { sort_by: "created_at", sort_direction: "desc" }));
  const issues = issuesQuery.data ?? [];
  const selectedIssue = useMemo(
    () => issues.find((issue) => issue.id === selectedIssueId) ?? issues[0] ?? null,
    [issues, selectedIssueId],
  );
  const treeQuery = useQuery({
    ...issueExecutionTreeOptions(selectedIssue?.id ?? ""),
    enabled: Boolean(selectedIssue?.id),
  });

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <div className="min-w-0">
          <h1 className="truncate text-sm font-semibold">运行复盘</h1>
          <p className="truncate text-xs text-muted-foreground">
            按 issue 回放 PM+01-05、子任务、token、耗时、轮次和证据。
          </p>
        </div>
      </PageHeader>

      <div className="grid min-h-0 flex-1 grid-cols-1 overflow-hidden lg:grid-cols-[360px_minmax(0,1fr)]">
        <aside className="min-h-0 border-b lg:border-r lg:border-b-0">
          <div className="flex h-10 items-center justify-between border-b px-3">
            <div className="text-xs font-medium text-muted-foreground">Issue 运行记录</div>
            <div className="text-xs text-muted-foreground">{issues.length} 条</div>
          </div>
          <div className="max-h-[38vh] min-h-0 overflow-y-auto lg:max-h-none lg:h-[calc(100vh-7.5rem)]">
            {issuesQuery.isLoading ? (
              <IssueListSkeleton />
            ) : issues.length === 0 ? (
              <div className="px-3 py-6 text-sm text-muted-foreground">暂无 issue。请先通过公开 UI/API 创建任务。</div>
            ) : (
              <div className="divide-y">
                {issues.map((issue) => (
                  <IssueRunRow
                    key={issue.id}
                    issue={issue}
                    active={issue.id === selectedIssue?.id}
                    href={`${paths.runReviews()}?issue=${encodeURIComponent(issue.id)}`}
                  />
                ))}
              </div>
            )}
          </div>
        </aside>

        <main className="min-h-0 overflow-y-auto">
          {selectedIssue ? (
            <RunReviewDetail
              issue={selectedIssue}
              tree={treeQuery.data}
              loading={treeQuery.isLoading}
              issueHref={paths.issueDetail(selectedIssue.id)}
              evalDraftHref={`${paths.trainingView("datasets")}?issue=${encodeURIComponent(selectedIssue.id)}&mode=draft`}
              optimizerHref={`${paths.trainingView("optimization-runs")}?issue=${encodeURIComponent(selectedIssue.id)}`}
            />
          ) : (
            <div className="px-6 py-10 text-sm text-muted-foreground">选择一条 issue 查看完整链路。</div>
          )}
        </main>
      </div>
    </div>
  );
}

function IssueRunRow({ issue, active, href }: { issue: Issue; active: boolean; href: string }) {
  const childTotal = issue.child_progress?.total ?? 0;
  const running = issue.agent_activity?.running_count ?? 0;
  const queued = issue.agent_activity?.queued_count ?? 0;
  return (
    <AppLink
      href={href}
      className={cn(
        "block px-3 py-2.5 text-left text-sm hover:bg-accent/60",
        active && "bg-accent text-accent-foreground",
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate font-medium">{issue.identifier ? `${issue.identifier} ` : ""}{issue.title}</div>
          <div className="mt-1 flex flex-wrap gap-1 text-[11px] text-muted-foreground">
            <span>{issue.project?.title ?? "未绑定项目"}</span>
            <span>状态 {statusLabel(issue.status)}</span>
            <span>子任务 {issue.child_progress?.done ?? 0}/{childTotal}</span>
          </div>
        </div>
        <span className={cn("shrink-0 rounded border px-1.5 py-0.5 text-[11px]", running > 0 ? "border-info/40 text-info" : "text-muted-foreground")}>
          {running > 0 ? `运行 ${running}` : queued > 0 ? `排队 ${queued}` : "复盘"}
        </span>
      </div>
    </AppLink>
  );
}

function RunReviewDetail({
  issue,
  tree,
  loading,
  issueHref,
  evalDraftHref,
  optimizerHref,
}: {
  issue: Issue;
  tree: IssueExecutionTreeResponse | undefined;
  loading: boolean;
  issueHref: string;
  evalDraftHref: string;
  optimizerHref: string;
}) {
  const summary = tree?.issue_summary;
  const timelineNodes = tree?.timeline_nodes ?? [];
  const stageRows = buildStageRows(timelineNodes);
  const childLanes = buildChildLanes(tree);
  const eventRows = timelineNodes.slice(0, 12);
  const queryClient = useQueryClient();
  const createDraftMut = useMutation({
    mutationFn: () => createIssueReviewDraftCase(issue, tree, stageRows, childLanes),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ["prompt-library"] });
      toast.success(`评测 Draft 已生成：${created.case_name || created.id}`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "生成评测 Draft 失败");
    },
  });
  const createdDraft = createDraftMut.data;
  const createdDraftHref = createdDraft
    ? `${evalDraftHref}&case=${encodeURIComponent(createdDraft.id)}&status=draft`
    : evalDraftHref;

  return (
    <div className="space-y-4 px-4 py-4">
      <section className="rounded-md border bg-card">
        <div className="flex flex-col gap-3 border-b px-4 py-3 lg:flex-row lg:items-start lg:justify-between">
          <div className="min-w-0">
            <div className="text-xs text-muted-foreground">{issue.identifier}</div>
            <h2 className="mt-0.5 truncate text-base font-semibold">{issue.title}</h2>
            <div className="mt-1 flex flex-wrap gap-2 text-xs text-muted-foreground">
              <span>项目：{issue.project?.title ?? "未绑定"}</span>
              <span>状态：{statusLabel(issue.status)}</span>
              <span>验收：{summary?.acceptance_status ?? "待运行"}</span>
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            <AppLink className="rounded border px-2.5 py-1.5 text-xs hover:bg-accent" href={issueHref}>返回 issue</AppLink>
            <button
              type="button"
              className="rounded border border-info/40 bg-info/10 px-2.5 py-1.5 text-xs text-info hover:bg-info/15 disabled:cursor-not-allowed disabled:opacity-60"
              onClick={() => createDraftMut.mutate()}
              disabled={createDraftMut.isPending || !tree}
              data-testid="run-review-create-eval-draft"
            >
              {createDraftMut.isPending ? "生成中..." : "生成评测 Draft"}
            </button>
            <AppLink className="rounded border px-2.5 py-1.5 text-xs hover:bg-accent" href={optimizerHref}>优化 Skill</AppLink>
          </div>
        </div>

        {createdDraft && (
          <div className="border-b bg-info/5 px-4 py-2 text-xs text-muted-foreground" data-testid="run-review-created-eval-draft">
            已生成 draft case {createdDraft.id}。请进入训练与评估确认输入、期望行为和验证方式，再批准为 active。
            <AppLink className="ml-2 text-info underline-offset-2 hover:underline" href={createdDraftHref}>
              查看 Draft
            </AppLink>
          </div>
        )}

        <div className="grid gap-0 divide-y text-sm md:grid-cols-4 md:divide-x md:divide-y-0">
          <Metric label="总耗时" value={formatDuration(summary?.total_duration_ms ?? 0)} icon={<Timer className="size-3.5" />} />
          <Metric label="Token" value={formatNumber((summary?.total_input_tokens ?? 0) + (summary?.total_output_tokens ?? 0))} icon={<Activity className="size-3.5" />} />
          <Metric label="轮次" value={formatNumber(summary?.agent_turn_count ?? 0)} icon={<ListChecks className="size-3.5" />} />
          <Metric label="证据节点" value={formatNumber(summary?.node_count ?? 0)} icon={<CheckCircle2 className="size-3.5" />} />
        </div>
      </section>

      {loading ? <DetailSkeleton /> : null}

      <section className="rounded-md border bg-card">
        <SectionTitle title="横向时序图" subtitle="按真实开始/结束时间绘制 PM+01-05 与跨项目子任务泳道；缺时间或缺节点会明确标记。" />
        <TimelineLaneChart stageRows={stageRows} childLanes={childLanes} timelineNodes={timelineNodes} />
      </section>

      <section className="rounded-md border bg-card">
        <SectionTitle title="子任务泳道" subtitle="父 issue 只展示 gateway / ida-deployment 子 issue 引用和等待状态。" />
        <div className="space-y-2 px-4 pb-4">
          {childLanes.map((lane) => (
            <div key={lane.key} className={cn("flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm", lane.issue ? "bg-background" : "border-dashed bg-muted/20")}>
              <div className="min-w-0">
                <div className="font-medium">{lane.label}</div>
                <div className="truncate text-xs text-muted-foreground">{lane.issue ? `${lane.issue.identifier} ${lane.issue.title}` : "尚未创建或未在执行树中关联"}</div>
              </div>
              <span className="shrink-0 rounded border px-2 py-1 text-xs text-muted-foreground">{lane.issue ? statusLabel(lane.issue.status) : "缺失"}</span>
            </div>
          ))}
        </div>
      </section>

      <section className="rounded-md border bg-card">
        <SectionTitle title="节点表" subtitle="字段固定：节点、Agent、状态、耗时、Token、轮次、证据。" />
        <div className="hidden md:block">
          <table className="w-full table-fixed text-sm">
            <thead className="border-y bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="w-[18%] px-3 py-2 font-medium">节点</th>
                <th className="w-[18%] px-3 py-2 font-medium">Agent</th>
                <th className="w-[14%] px-3 py-2 font-medium">状态</th>
                <th className="w-[14%] px-3 py-2 font-medium">耗时</th>
                <th className="w-[14%] px-3 py-2 font-medium">Token</th>
                <th className="w-[10%] px-3 py-2 font-medium">轮次</th>
                <th className="w-[12%] px-3 py-2 font-medium">证据</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {stageRows.map((stage) => (
                <tr key={stage.key}>
                  <td className="truncate px-3 py-2">{stage.label}</td>
                  <td className="truncate px-3 py-2 text-muted-foreground">{stage.node?.agent_name ?? stage.key}</td>
                  <td className="truncate px-3 py-2">{stage.node ? statusLabel(stage.node.status) : "缺失"}</td>
                  <td className="truncate px-3 py-2">{formatDuration(stage.node?.duration_ms ?? 0)}</td>
                  <td className="truncate px-3 py-2">{formatNumber((stage.node?.input_tokens ?? 0) + (stage.node?.output_tokens ?? 0))}</td>
                  <td className="truncate px-3 py-2">{formatNumber(stage.node?.agent_turn_count ?? 0)}</td>
                  <td className="truncate px-3 py-2">{stage.node?.evidence_refs?.length ? `${stage.node.evidence_refs.length} 条` : "待补齐"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="divide-y md:hidden">
          {stageRows.map((stage) => (
            <div key={stage.key} className="px-4 py-3 text-sm">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0 truncate font-medium">{stage.label}</div>
                <span className="shrink-0 rounded border px-2 py-0.5 text-xs text-muted-foreground">
                  {stage.node ? statusLabel(stage.node.status) : "缺失"}
                </span>
              </div>
              <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-xs text-muted-foreground">
                <NodeFact label="Agent" value={stage.node?.agent_name ?? stage.key} />
                <NodeFact label="耗时" value={formatDuration(stage.node?.duration_ms ?? 0)} />
                <NodeFact label="Token" value={formatNumber((stage.node?.input_tokens ?? 0) + (stage.node?.output_tokens ?? 0))} />
                <NodeFact label="轮次" value={formatNumber(stage.node?.agent_turn_count ?? 0)} />
                <NodeFact label="证据" value={stage.node?.evidence_refs?.length ? `${stage.node.evidence_refs.length} 条` : "待补齐"} />
              </div>
            </div>
          ))}
        </div>
      </section>

      <section className="rounded-md border bg-card">
        <SectionTitle title="事件流" subtitle="按执行树 timeline 展示最近事件。" />
        <div className="divide-y">
          {eventRows.length > 0 ? eventRows.map((node) => (
            <div key={node.node_id} className="flex gap-3 px-4 py-3 text-sm">
              <GitBranch className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
              <div className="min-w-0">
                <div className="truncate font-medium">{node.summary || node.node_type}</div>
                <div className="mt-0.5 text-xs text-muted-foreground">
                  {node.agent_name || node.node_type} · {statusLabel(node.status)} · {formatDuration(node.duration_ms)}
                  {node.usage_unavailable_trace ? " · usage unavailable" : ""}
                </div>
              </div>
            </div>
          )) : (
            <div className="flex gap-2 px-4 py-6 text-sm text-muted-foreground">
              <AlertTriangle className="size-4" />
              暂无事件。真实任务开始后会回写 trace、用量和证据。
            </div>
          )}
        </div>
      </section>
    </div>
  );
}

function SectionTitle({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <div className="px-4 py-3">
      <div className="text-sm font-semibold">{title}</div>
      <div className="mt-0.5 text-xs text-muted-foreground">{subtitle}</div>
    </div>
  );
}

function Metric({ label, value, icon }: { label: string; value: string; icon: ReactNode }) {
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="rounded-md border bg-background p-2 text-muted-foreground">{icon}</div>
      <div>
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className="text-sm font-semibold">{value}</div>
      </div>
    </div>
  );
}

function NodeFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <span className="text-muted-foreground/80">{label}：</span>
      <span className="break-words">{value}</span>
    </div>
  );
}

type StageRow = ReturnType<typeof buildStageRows>[number];
type ChildLane = ReturnType<typeof buildChildLanes>[number];

interface TimelineBarRow {
  key: string;
  label: string;
  kind: "stage" | "child";
  status: string;
  startMs: number | null;
  endMs: number | null;
  durationMs: number;
  tokenTotal: number;
  turns: number;
  missing: boolean;
}

function TimelineLaneChart({
  stageRows,
  childLanes,
  timelineNodes,
}: {
  stageRows: StageRow[];
  childLanes: ChildLane[];
  timelineNodes: IssueTimelineNode[];
}) {
  const rows = buildTimelineBarRows(stageRows, childLanes, timelineNodes);
  const timedRows = rows.filter((row) => row.startMs !== null && row.endMs !== null);
  const min = timedRows.length > 0 ? Math.min(...timedRows.map((row) => row.startMs as number)) : 0;
  const max = timedRows.length > 0 ? Math.max(...timedRows.map((row) => row.endMs as number)) : min + 1;
  const span = Math.max(max - min, 1);
  const ticks = timedRows.length > 0
    ? [min, min + span / 2, max].map((value) => formatTimeTick(value))
    : ["开始", "中点", "结束"];

  return (
    <div className="px-4 pb-4" data-testid="run-review-horizontal-timeline">
      <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-3 text-[11px] text-muted-foreground">
        <div />
        <div className="grid grid-cols-3">
          {ticks.map((tick, index) => (
            <div key={`${tick}-${index}`} className={cn(index === 1 && "text-center", index === 2 && "text-right")}>
              {tick}
            </div>
          ))}
        </div>
      </div>
      <div className="mt-2 space-y-1.5">
        {rows.map((row) => (
          <div key={row.key} className="grid grid-cols-[6.5rem_minmax(0,1fr)] items-center gap-x-3">
            <div className="min-w-0">
              <div className="truncate text-xs font-medium">{row.label}</div>
              <div className="truncate text-[11px] text-muted-foreground">{statusLabel(row.status)}</div>
            </div>
            <div className="relative h-9 overflow-hidden rounded-md border bg-muted/20">
              <div className="absolute inset-y-0 left-1/3 w-px bg-border/70" />
              <div className="absolute inset-y-0 left-2/3 w-px bg-border/70" />
              {row.missing || row.startMs === null || row.endMs === null ? (
                <div className="flex h-full items-center px-2 text-[11px] text-muted-foreground">
                  {row.missing ? "缺节点" : "缺时间"}
                </div>
              ) : (
                <div
                  className={cn(
                    "absolute top-1 bottom-1 min-w-[2rem] rounded px-2 text-[11px] leading-7 text-white shadow-sm",
                    row.kind === "child" ? "bg-sky-600" : "bg-emerald-600",
                  )}
                  data-testid={`run-review-timeline-bar-${row.key}`}
                  style={{
                    left: `${Math.max(0, ((row.startMs - min) / span) * 100)}%`,
                    width: `${Math.max(6, ((row.endMs - row.startMs) / span) * 100)}%`,
                  }}
                  title={`${row.label} · ${formatDuration(row.durationMs)} · Token ${formatNumber(row.tokenTotal)} · 轮次 ${formatNumber(row.turns)}`}
                >
                  <span className="block truncate">
                    {formatDuration(row.durationMs)} · {formatNumber(row.tokenTotal)} token
                  </span>
                </div>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function buildTimelineBarRows(
  stageRows: StageRow[],
  childLanes: ChildLane[],
  timelineNodes: IssueTimelineNode[],
): TimelineBarRow[] {
  const stageBars = stageRows.map((stage) => {
    const timing = timelineTiming(stage.node);
    return {
      key: stage.key,
      label: stage.label,
      kind: "stage" as const,
      status: stage.node?.status ?? "missing",
      ...timing,
      durationMs: stage.node?.duration_ms ?? timing.durationMs,
      tokenTotal: (stage.node?.input_tokens ?? 0) + (stage.node?.output_tokens ?? 0),
      turns: stage.node?.agent_turn_count ?? 0,
      missing: !stage.node,
    };
  });
  const childBars = childLanes.map((lane) => {
    const node = timelineNodes.find((item) => item.node_type === "child_issue_ref" && item.child_issue_id === lane.issue?.id);
    const timing = timelineTiming(node);
    return {
      key: lane.key,
      label: lane.label,
      kind: "child" as const,
      status: lane.issue?.status ?? "missing",
      ...timing,
      durationMs: node?.duration_ms ?? timing.durationMs,
      tokenTotal: (node?.input_tokens ?? 0) + (node?.output_tokens ?? 0),
      turns: node?.agent_turn_count ?? 0,
      missing: !lane.issue,
    };
  });
  return [...stageBars, ...childBars];
}

function timelineTiming(node: IssueTimelineNode | undefined) {
  if (!node) return { startMs: null, endMs: null, durationMs: 0 };
  const start = parseTimeMs(node.started_at);
  const completed = parseTimeMs(node.completed_at);
  const duration = Math.max(node.duration_ms ?? 0, 0);
  if (start === null && completed === null) return { startMs: null, endMs: null, durationMs: duration };
  const startMs = start ?? Math.max((completed as number) - Math.max(duration, 60_000), 0);
  const endMs = Math.max(completed ?? startMs + Math.max(duration, 60_000), startMs + Math.max(duration, 60_000));
  return { startMs, endMs, durationMs: Math.max(duration, endMs - startMs) };
}

function parseTimeMs(value: string | undefined) {
  if (!value) return null;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function formatTimeTick(value: number) {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

function buildStageRows(nodes: IssueTimelineNode[]) {
  return STAGES.map((stage) => ({
    ...stage,
    node: nodes.filter((node) => {
      const haystack = `${node.agent_name ?? ""} ${node.summary ?? ""} ${node.node_id ?? ""}`.toLowerCase();
      return stage.names.some((name) => haystack.includes(name));
    }).sort(compareStageNodeCandidates)[0],
  }));
}

function compareStageNodeCandidates(left: IssueTimelineNode, right: IssueTimelineNode) {
  return stageNodeScore(right) - stageNodeScore(left);
}

function stageNodeScore(node: IssueTimelineNode) {
  let score = 0;
  if (node.node_type === "agent_task") score += 1000;
  if (node.status === "completed" || node.status === "已完成") score += 200;
  if (node.status === "cancelled" || node.status === "已取消") score -= 200;
  if ((node.input_tokens ?? 0) + (node.output_tokens ?? 0) > 0) score += 100;
  if (node.started_at && node.completed_at) score += 50;
  if ((node.agent_turn_count ?? 0) > 0) score += 25;
  return score;
}

function buildChildLanes(tree: IssueExecutionTreeResponse | undefined) {
  return TARGET_CHILD_PROJECTS.map((key) => {
    const child = tree?.root?.children?.find((node) => {
      const text = `${node.issue.project?.title ?? ""} ${node.issue.title} ${node.issue.identifier}`.toLowerCase();
      return text.includes(key);
    });
    return { key, label: key === "gateway" ? "gateway 子任务" : "ida-deployment 子任务", issue: child?.issue };
  });
}

async function createIssueReviewDraftCase(
  issue: Issue,
  tree: IssueExecutionTreeResponse | undefined,
  stageRows: ReturnType<typeof buildStageRows>,
  childLanes: ReturnType<typeof buildChildLanes>,
) {
  if (!tree) throw new Error("执行树尚未加载，不能生成评测 Draft");
  const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集", status: "启用" });
  let asset = assets.items.find((item) => item.name === ISSUE_REVIEW_DRAFT_DATASET_NAME);
  if (!asset) {
    asset = await api.createPromptEvaluationAsset({
      name: ISSUE_REVIEW_DRAFT_DATASET_NAME,
      description: "从运行复盘生成的 eval case draft，需人工确认后进入 active 评测集。",
      asset_type: "数据集",
      status: "启用",
      payload: {
        schema_version: 1,
        schema: "multica.training_evaluation.payload.v1",
        语义版本: "multica.training_evaluation.v1",
        cases: [],
        payload_contract: {
          source: "run-review",
          review_flow: "draft -> approved -> active",
        },
      },
    });
  }
  const stageFacts = stageRows.map((stage) => ({
    stage: stage.label,
    status: stage.node ? stage.node.status : "missing",
    agent: stage.node?.agent_name ?? stage.key,
    duration_ms: stage.node?.duration_ms ?? 0,
    token_total: (stage.node?.input_tokens ?? 0) + (stage.node?.output_tokens ?? 0),
    turns: stage.node?.agent_turn_count ?? 0,
    evidence_refs: stage.node?.evidence_refs ?? [],
  }));
  const childFacts = childLanes.map((lane) => ({
    lane: lane.key,
    issue_id: lane.issue?.id ?? null,
    identifier: lane.issue?.identifier ?? null,
    title: lane.issue?.title ?? null,
    status: lane.issue?.status ?? "missing",
  }));
  const caseName = `${issue.identifier ? `${issue.identifier} ` : ""}${issue.title}`.trim() || `issue ${issue.id}`;
  return api.createPromptEvaluationCase({
    asset_id: asset.id,
    prompt_id: asset.prompt_id,
    case_name: `Draft: ${caseName}`,
    variables: {
      issue_id: issue.id,
      issue_identifier: issue.identifier,
      issue_title: issue.title,
      project: issue.project?.title ?? "",
      current_status: issue.status,
      source: "run-review",
    },
    expected_contains: ["PM", "01", "02", "03", "04", "05", "gateway", "ida-deployment", "evidence"],
    input: {
      source: "run-review",
      issue: {
        id: issue.id,
        identifier: issue.identifier,
        title: issue.title,
        project: issue.project?.title ?? null,
        status: issue.status,
      },
      run_review: {
        issue_summary: tree.issue_summary ?? null,
        stage_facts: stageFacts,
        child_lanes: childFacts,
        timeline_node_count: tree.timeline_nodes?.length ?? 0,
      },
    },
    expected: {
      expected_behavior: "能复现该 issue 的 PM+01-05 执行链路，识别跨项目子任务，并保留可追溯证据。",
      validation: "检查 DAG/子任务、阶段节点、token/耗时/轮次、gateway/ida-deployment child lane 和 evidence refs。",
      approval_required: true,
      review_flow: "draft -> approved -> active",
    },
    tags: ["issue-review", "draft", `issue:${issue.id}`, issue.project?.title ?? "unknown-project"],
    status: "draft",
  });
}

function IssueListSkeleton() {
  return (
    <div className="space-y-2 p-3">
      {Array.from({ length: 5 }).map((_, index) => (
        <Skeleton key={index} className="h-16 w-full rounded-md" />
      ))}
    </div>
  );
}

function DetailSkeleton() {
  return <Skeleton className="h-24 w-full rounded-md" />;
}

function formatDuration(ms: number) {
  if (!ms || ms <= 0) return "0m";
  const totalSeconds = Math.round(ms / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes <= 0) return `${seconds}s`;
  return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`;
}

function formatNumber(value: number) {
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 0 }).format(value || 0);
}

function statusLabel(status: string) {
  const labels: Record<string, string> = {
    backlog: "待规划",
    todo: "待办",
    in_progress: "进行中",
    in_review: "验收中",
    done: "已完成",
    completed: "已完成",
    failed: "失败",
    blocked: "阻塞",
    cancelled: "已取消",
    queued: "排队",
    running: "运行中",
  };
  return labels[status] ?? status;
}
