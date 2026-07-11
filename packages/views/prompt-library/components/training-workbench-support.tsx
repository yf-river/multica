"use client";

import type { ReactNode } from "react";
import type {
  PromptEvaluationAsset,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationRun,
  PromptEvaluationStructuredCase,
  PromptEvaluationAssetType,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { useT } from "../../i18n/use-t";
import type { CaseSummary } from "./case-model";
import type { RunStatusFilter } from "./run-model";
import {
  summarizeSkillScenarioTarget,
  summarizeWritingModelBenchmark,
  type TrainingWorkbenchTab,
} from "@multica/core/training";

type WorkbenchTab = TrainingWorkbenchTab;

export function emptyTrainingRouteText(activeTab: WorkbenchTab) {
  switch (activeTab) {
    case "用例库":
      return "暂无用例库，先新建用例库或从 trace 导入用例";
    case "测试套件":
      return "暂无测试套件，先把稳定用例组织成可回归的套件";
    default:
      return "暂无评估资产";
  }
}

type TrainingRouteIntro = {
  route: string;
  title: string;
  subtitle: string;
  facts: Array<[string, string]>;
  evidence: string;
};

export function trainingRouteIntro(
  activeTab: WorkbenchTab,
  context: {
    visibleAssets: PromptEvaluationAsset[];
    cases: PromptEvaluationStructuredCase[];
    runs: PromptEvaluationRun[];
    candidates: PromptEvaluationOptimizationCandidate[];
    runStatusFilter: RunStatusFilter;
  },
): TrainingRouteIntro {
  const enabledAssets = context.visibleAssets.filter((asset) => asset.status === "启用").length;
  switch (activeTab) {
    case "用例库": {
      const datasetRows = context.visibleAssets.reduce((sum, asset) => sum + asset.dataset_row_count, 0);
      const traceCases = context.cases.filter((item) => item.source === "trace").length;
      return {
        route: "datasets",
        title: "用例库",
        subtitle: "把真实 trace 和手工样例沉淀成可复跑的评测用例，用于后续测试套件和实验。",
        facts: [
          ["用例库", String(context.visibleAssets.length)],
          ["启用", String(enabledAssets)],
          ["用例", formatNumber(datasetRows)],
          ["trace 样本", formatNumber(traceCases)],
        ],
        evidence: "公开 API 创建/回读用例库，页面可从真实 trace 导入并维护结构化用例。",
      };
    }
    case "测试套件": {
      const suiteCases = context.visibleAssets.reduce((sum, asset) => sum + asset.test_suite_case_count, 0);
      return {
        route: "test-suites",
        title: "测试套件回归",
        subtitle: "把一组稳定用例固定为回归套件，用来反复验证提示词、智能体和小队 SOP 是否退化。",
        facts: [
          ["测试套件", String(context.visibleAssets.length)],
          ["启用", String(enabledAssets)],
          ["套件用例", formatNumber(suiteCases)],
          ["结构化用例", formatNumber(context.cases.length)],
        ],
        evidence: "页面可创建套件资产、维护手工用例，并通过评测记录回读每次套件执行结果。",
      };
    }
    case "评测记录": {
      const reviewRuns = context.runs.filter((run) => run.status === "需人工复核").length;
      return {
        route: "runs",
        title: context.runStatusFilter === "需人工复核" ? "人工复核队列" : "评测记录与证据",
        subtitle: "按运行记录回看任务、模型、耗时、评估结论和服务端证据快照，支持同步、取消和人工复核。",
        facts: [
          ["当前筛选", context.runStatusFilter === "全部" ? "全部运行" : context.runStatusFilter],
          ["运行记录", formatNumber(context.runs.length)],
          ["人工复核", formatNumber(reviewRuns)],
          ["带任务记录", formatNumber(context.runs.filter((run) => Boolean(run.task_id)).length)],
        ],
        evidence: "每条运行可展开 task/message/trace/usage 证据，并可归档服务端证据快照。",
      };
    }
    default:
      return {
        route: "assets",
        title: activeTab,
        subtitle: "评估资产页面。",
        facts: [["资产", String(context.visibleAssets.length)]],
        evidence: "通过公开 API 创建和回读。",
      };
  }
}

export function TrainingFocusedIssueCallout({
  activeTab,
  issueId,
  taskCount,
}: {
  activeTab: WorkbenchTab;
  issueId: string;
  taskCount: number;
}) {
  const { t } = useT("prompt-library");
  const actionLabel = activeTab === "用例库"
    ? t(($) => $.workbench.focused_issue.dataset_action)
    : t(($) => $.workbench.focused_issue.context_action);
  return (
    <section className="rounded-md border border-info/30 bg-info/5 px-3 py-2 text-xs" data-testid="training-focused-issue-callout">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <div className="font-medium text-foreground">
            {t(($) => $.workbench.focused_issue.title, { id: issueId })}
          </div>
          <div className="mt-0.5 text-muted-foreground">
            {taskCount > 0
              ? t(($) => $.workbench.focused_issue.task_count, { count: taskCount })
              : t(($) => $.workbench.focused_issue.loading)}
            {" "}{actionLabel}
          </div>
        </div>
        <a className="shrink-0 rounded border bg-background px-2 py-1 text-[11px] hover:bg-accent" href={`../issues/${encodeURIComponent(issueId)}`}>
          {t(($) => $.workbench.focused_issue.back)}
        </a>
      </div>
    </section>
  );
}

export function TrainingRouteIntroCard({
  route,
  title,
  subtitle,
  facts,
  evidence,
  action,
}: TrainingRouteIntro & { action?: ReactNode }) {
  const { t } = useT("prompt-library");
  return (
    <section className="rounded-md border border-border/70 bg-muted/15 px-4 py-3" data-testid={`training-route-intro-${route}`}>
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="text-xs font-medium text-muted-foreground">{t(($) => $.workbench.module_label)}</div>
          <h3 className="mt-1 text-base font-semibold">{title}</h3>
          <p className="mt-1 max-w-3xl text-xs leading-5 text-muted-foreground">{subtitle}</p>
          <p className="mt-2 max-w-3xl text-xs leading-5 text-muted-foreground">{evidence}</p>
        </div>
        {action ? <div className="shrink-0">{action}</div> : null}
      </div>
      <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        {facts.map(([label, value]) => (
          <div key={label} className="min-w-0 rounded-md border bg-background px-3 py-2" data-testid={`training-route-intro-fact-${route}-${label}`}>
            <div className="truncate text-[11px] text-muted-foreground">{label}</div>
            <div className="mt-1 truncate text-sm font-semibold">{value}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

export function TrainingRouteWorkspaceBand({
  activeTab,
  route,
  visibleAssets,
  cases,
  runs,
  candidates,
  runStatusFilter,
}: {
  activeTab: WorkbenchTab;
  route: string;
  visibleAssets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  runs: PromptEvaluationRun[];
  candidates: PromptEvaluationOptimizationCandidate[];
  runStatusFilter: RunStatusFilter;
}) {
  const config = trainingRouteOperatingModel(activeTab, {
    visibleAssets,
    cases,
    runs,
    candidates,
    runStatusFilter,
  });
  if (!config) return null;
  return (
    <section className={`grid gap-3 rounded-md border px-4 py-3 ${config.className}`} data-testid={`training-route-operating-model-${route}`}>
      <div className="flex flex-col gap-2 md:flex-row md:items-start md:justify-between">
        <div className="min-w-0">
          <div className="text-xs font-medium text-muted-foreground">{config.kicker}</div>
          <h3 className="mt-1 text-sm font-semibold">{config.title}</h3>
          <p className="mt-1 max-w-3xl text-xs leading-5 text-muted-foreground">{config.description}</p>
        </div>
        <Badge variant="outline" className="w-fit shrink-0">{config.badge}</Badge>
      </div>
      <div className="grid gap-2 md:grid-cols-3">
        {config.steps.map((step, index) => (
          <div key={step.label} className="min-w-0 rounded-md border bg-background px-3 py-2" data-testid={`training-route-operating-step-${route}-${index + 1}`}>
            <div className="text-[11px] font-medium text-muted-foreground">{step.label}</div>
            <div className="mt-1 truncate text-sm font-semibold">{step.title}</div>
            <div className="mt-1 line-clamp-2 text-xs leading-5 text-muted-foreground">{step.detail}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

type TrainingRouteOperatingModel = {
  kicker: string;
  title: string;
  description: string;
  badge: string;
  className: string;
  steps: Array<{ label: string; title: string; detail: string }>;
};

function trainingRouteOperatingModel(
  activeTab: WorkbenchTab,
  context: {
    visibleAssets: PromptEvaluationAsset[];
    cases: PromptEvaluationStructuredCase[];
    runs: PromptEvaluationRun[];
    candidates: PromptEvaluationOptimizationCandidate[];
    runStatusFilter: RunStatusFilter;
  },
): TrainingRouteOperatingModel | null {
  switch (activeTab) {
    case "用例库": {
      const datasetRows = context.visibleAssets.reduce((sum, asset) => sum + asset.dataset_row_count, 0);
      const traceCases = context.cases.filter((item) => item.source === "trace").length;
      return {
        kicker: "用例库工作台",
        title: "用例入库、锁定版本、下游复用",
        description: "用例库页面关注评测输入本身：从 trace 或手工样例形成结构化用例，锁定可追溯版本，再供测试套件和实验引用。",
        badge: "用例事实",
        className: "border-sky-500/30 bg-sky-500/5",
        steps: [
          { label: "入口", title: "trace 导入或手工用例", detail: `${formatNumber(traceCases)} 条 trace 用例，用例库 ${formatNumber(context.visibleAssets.length)} 个` },
          { label: "版本", title: "锁定用例库版本", detail: `${formatNumber(datasetRows)} 条用例可形成版本指纹，避免实验偷偷读最新数据` },
          { label: "复用", title: "供测试套件和实验绑定", detail: "下游资产通过用例库版本证明输入一致，便于回归和对比" },
        ],
      };
    }
    case "测试套件": {
      const suiteCases = context.visibleAssets.reduce((sum, asset) => sum + asset.test_suite_case_count, 0);
      return {
        kicker: "测试套件工作台",
        title: "固定试卷、断言回归、失败定位",
        description: "测试套件页面关注稳定回归：把多条用例和断言组织成一张试卷，反复验证提示词、智能体或小队流程是否退化。",
        badge: "回归试卷",
        className: "border-violet-500/30 bg-violet-500/5",
        steps: [
          { label: "组织", title: "用例组成套件", detail: `${formatNumber(suiteCases)} 条套件用例，${formatNumber(context.cases.length)} 条结构化用例` },
          { label: "执行", title: "反复运行同一试卷", detail: "评测记录会记录通过率、失败原因、耗时和 token" },
          { label: "定位", title: "断言级复盘", detail: "失败后可跳到运行证据、生成候选或进入人工复核" },
        ],
      };
    }
    case "评测记录": {
      const taskRuns = context.runs.filter((run) => Boolean(run.task_id)).length;
      const reviewRuns = context.runs.filter((run) => run.status === "需人工复核").length;
      return {
        kicker: "评测记录工作台",
        title: "运行检索、证据展开、人工复核",
        description: "评测记录页面关注证据检索：按状态筛选运行，展开 task、message、trace、usage、span 和服务端快照。",
        badge: context.runStatusFilter === "全部" ? "证据检索" : context.runStatusFilter,
        className: "border-emerald-500/30 bg-emerald-500/5",
        steps: [
          { label: "筛选", title: "按运行状态定位", detail: `当前筛选：${context.runStatusFilter === "全部" ? "全部运行" : context.runStatusFilter}` },
          { label: "证据", title: "任务和 Trace 展开", detail: `${formatNumber(taskRuns)} 条运行绑定任务，可展开消息、工具调用和用量` },
          { label: "复核", title: "人工复核队列", detail: `${formatNumber(reviewRuns)} 条运行等待人工判断，可通过或驳回` },
        ],
      };
    }
    default:
      return null;
  }
}

export function tabToAssetType(tab: WorkbenchTab): PromptEvaluationAssetType | null {
  if (tab === "用例库") return "数据集";
  if (tab === "测试套件") return "测试套件";
  return null;
}

export function assetTypeLabel(assetType: PromptEvaluationAssetType): string {
  return assetType === "数据集" ? "用例库" : assetType;
}

export function canManageStructuredCases(asset: PromptEvaluationAsset): boolean {
  return asset.asset_type === "数据集" || asset.asset_type === "测试套件";
}


export function summarizeAssetPayload(asset: PromptEvaluationAsset, caseSummary?: CaseSummary): string {
  const payload = asset.payload ?? {};
  const cases = Array.isArray(payload.cases) ? payload.cases.length : Array.isArray(payload["数据集"]) ? payload["数据集"].length : 0;
  const skillTarget = summarizeSkillScenarioTarget(asset);
  if (skillTarget) return `Skill 场景评测 · ${skillTarget}`;
  const writingBenchmark = summarizeWritingModelBenchmark(asset);
  if (writingBenchmark) return `多模型写作评测 · ${writingBenchmark}`;
  if (caseSummary && caseSummary.total > 0) {
    const sourceParts = [];
    if (caseSummary.manual > 0) sourceParts.push(`手工 ${caseSummary.manual}`);
    if (caseSummary.trace > 0) sourceParts.push(`trace导入 ${caseSummary.trace}`);
    if (caseSummary.payload > 0) sourceParts.push(`资产载荷 ${caseSummary.payload}`);
    return `结构化用例 ${caseSummary.total} 个${sourceParts.length > 0 ? `（${sourceParts.join("，")}；运行优先使用）` : ""}`;
  }
  if (payload["最近Agent运行"]) return "包含真实智能体运行";
  if (payload["运行结果"]) return "包含运行结果";
  return cases > 0 ? `${cases} 个用例` : "未记录用例";
}

export function summarizeAgentRun(asset: PromptEvaluationAsset): string | null {
  const payload = asset.payload ?? {};
  const run = payload["最近Agent运行"];
  if (!run || typeof run !== "object" || Array.isArray(run)) return null;
  const record = run as Record<string, unknown>;
  const status = stringFromRecord(record, "状态") || "未知状态";
  const taskId = stringFromRecord(record, "trace/任务标识") || stringFromRecord(record, "trace/task id");
  const agent = stringFromRecord(record, "执行Agent");
  const model = stringFromRecord(record, "模型");
  return `智能体任务：${status}${taskId ? ` · 任务标识 ${taskId}` : ""}${agent ? ` · ${agent}` : ""}${model ? ` · ${model}` : ""}`;
}

export function summarizeDatasetVersion(asset: PromptEvaluationAsset): string | null {
  const payload = asset.payload ?? {};
  const version = payload["最近数据集版本"];
  if (!version || typeof version !== "object" || Array.isArray(version)) return null;
  const record = version as Record<string, unknown>;
  const versionNumber = stringFromRecord(record, "version");
  const rowCount = stringFromRecord(record, "row_count");
  const fingerprint = stringFromRecord(record, "row_fingerprint");
  const createdAt = stringFromRecord(record, "created_at");
  if (!versionNumber && !rowCount && !fingerprint) return null;
  const parts = [`用例库版本 v${versionNumber || "?"}`];
  if (rowCount) parts.push(`${rowCount} 行`);
  if (fingerprint) parts.push(`指纹 ${fingerprint.slice(0, 12)}`);
  if (createdAt) parts.push(createdAt);
  return parts.join(" · ");
}

export function summarizeLinkedDatasetVersions(asset: PromptEvaluationAsset): string | null {
  const payload = asset.payload ?? {};
  const raw = payload["linked_dataset_versions"] ?? payload["数据集版本"] ?? payload["关联数据集版本"];
  if (!Array.isArray(raw) || raw.length === 0) return null;
  const parts = raw
    .map((item) => {
      if (!item || typeof item !== "object" || Array.isArray(item)) return "";
      const record = item as Record<string, unknown>;
      const datasetName = stringFromRecord(record, "dataset_name") || stringFromRecord(record, "数据集名称") || stringFromRecord(record, "name") || stringFromRecord(record, "名称");
      const version = stringFromRecord(record, "version") || stringFromRecord(record, "版本");
      const fingerprint = stringFromRecord(record, "row_fingerprint") || stringFromRecord(record, "行指纹");
      const versionId = stringFromRecord(record, "dataset_version_id") || stringFromRecord(record, "数据集版本ID");
      const label = datasetName || "用例库";
      const versionLabel = version ? `v${version}` : versionId ? `版本 ${versionId.slice(0, 8)}` : "未声明版本";
      return `${label} ${versionLabel}${fingerprint ? ` · 指纹 ${fingerprint.slice(0, 10)}` : ""}`;
    })
    .filter(Boolean);
  return parts.length > 0 ? `绑定用例库版本：${parts.join("；")}` : null;
}

function stringFromRecord(record: Record<string, unknown>, key: string): string {
  const value = record[key];
  if (typeof value === "string") return value;
  if (typeof value === "number" && Number.isFinite(value)) return String(value);
  return "";
}

function formatNumber(value: unknown): string {
  return typeof value === "number" && Number.isFinite(value) ? value.toLocaleString("zh-CN") : "0";
}

export function downloadTextFile(content: string, filename: string, mimeType: string) {
  const blob = new Blob([content], { type: mimeType });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}
