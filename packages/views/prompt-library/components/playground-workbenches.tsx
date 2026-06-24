"use client";

import type { ReactNode } from "react";
import { Activity, Braces, ClipboardCheck, FileText, Loader2, Play, Save, TerminalSquare } from "lucide-react";
import { renderPromptTemplate } from "@multica/core/prompt-library";
import type {
  Agent,
  AgentRuntime,
  PromptEvaluationAsset,
  PromptEvaluationRun,
  PromptEvaluationRuntimeReadiness,
  PromptEvaluationStructuredCase,
  PromptLibraryItem,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { DEFAULT_AGENT_MODEL } from "./prompt-library-request-builders";

export function PromptPlaygroundWorkbench({
  selected,
  debugValuesText,
  onDebugValuesTextChange,
  debugResult,
  runningDebug,
  runs,
  assets,
  loading,
  onRunDebug,
}: {
  selected: PromptLibraryItem | null;
  debugValuesText: string;
  onDebugValuesTextChange: (value: string) => void;
  debugResult: ReturnType<typeof renderPromptTemplate>;
  runningDebug: boolean;
  runs: PromptEvaluationRun[];
  assets: PromptEvaluationAsset[];
  loading: boolean;
  onRunDebug: () => void;
}) {
  const variableNames = selected?.variables.map((item) => item.name).filter(Boolean) ?? [];
  const missingVariableSet = new Set(debugResult.missingVariables);
  const templateRuns = runs.filter((run) => run.run_kind === "模板渲染检查" && (!selected || run.prompt_id === selected.id));
  const debugAssets = assets.filter((asset) => {
    const payload = asset.payload ?? {};
    return (
      asset.asset_type === "优化运行" &&
      (!selected || asset.prompt_id === selected.id) &&
      (asset.description.includes("提示词调试场") || Object.prototype.hasOwnProperty.call(payload, "调试输出"))
    );
  });

  return (
    <section className="mx-auto grid max-w-7xl gap-4" data-testid="prompt-playground-panel">
      <div className="grid gap-3 2xl:grid-cols-[minmax(0,1.2fr)_minmax(320px,0.8fr)]">
        <section className="rounded-md border border-l-4 border-border/70 border-l-sky-500 bg-muted/10 p-4">
          <div className="flex flex-col gap-2 border-b pb-3 md:flex-row md:items-start md:justify-between">
            <div className="min-w-0">
              <h2 className="text-base font-semibold">模板渲染实验室</h2>
              <p className="mt-1 text-xs text-muted-foreground">
                这里只做本地模板渲染：选择已保存提示词，填入变量，确认最终提示词文本，并把结果记录为一次模板渲染检查。
              </p>
            </div>
            <Badge variant="secondary" className="w-fit shrink-0">本地渲染</Badge>
          </div>

          <div className="mt-4 grid gap-4">
            <div className="grid gap-2 md:grid-cols-4" data-testid="prompt-playground-purpose-map">
              {[
                ["输入", "提示词模板和变量样本"],
                ["执行", "浏览器本地渲染"],
                ["产出", "渲染文本和调试记录"],
                ["边界", "不创建任务、不消耗模型"],
              ].map(([label, detail]) => (
                <div key={label} className="min-w-0 rounded-md border bg-background px-3 py-2">
                  <div className="text-[11px] font-medium text-muted-foreground">{label}</div>
                  <div className="mt-1 truncate text-sm font-semibold">{detail}</div>
                </div>
              ))}
            </div>

            <div className="grid gap-2 rounded-md border border-dashed bg-background p-3 md:grid-cols-3" data-testid="prompt-playground-local-pipeline">
              {["选择已保存模板", "填写变量样本", "本地渲染记录"].map((label, index) => (
                <div key={label} className="min-w-0">
                  <div className="text-[11px] font-medium text-muted-foreground">步骤 {index + 1}</div>
                  <div className="mt-1 truncate text-sm font-semibold">{label}</div>
                </div>
              ))}
            </div>

            <div className="grid gap-3 rounded-md border bg-background p-3 md:grid-cols-[minmax(0,1fr)_240px]">
              <div className="min-w-0">
                <div className="text-xs font-medium text-muted-foreground">当前模板</div>
                <div className="mt-1 truncate text-sm font-semibold">{selected?.name ?? "请先从左侧选择提示词"}</div>
                <div className="mt-1 line-clamp-2 text-xs text-muted-foreground">{selected?.description || "提示词调试场不编辑模板，只验证变量填充后的最终文本。"}</div>
              </div>
              <div className="grid gap-1 text-xs text-muted-foreground">
                <div>类型：{selected?.prompt_type ?? "未选择"}</div>
                <div>版本：{selected ? `v${selected.version}` : "未选择"}</div>
                <div>变量：{variableNames.length > 0 ? variableNames.join("、") : "未声明"}</div>
              </div>
            </div>

            <div className="grid gap-3 xl:grid-cols-[minmax(0,0.85fr)_minmax(0,0.9fr)_minmax(0,1.25fr)]" data-testid="prompt-playground-template-lab">
              <section className="grid min-h-[260px] content-start gap-3 rounded-md border bg-background p-3" data-testid="prompt-playground-source-panel">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <FileText className="size-3.5 shrink-0 text-muted-foreground" />
                    <h3 className="truncate text-sm font-semibold">模板源</h3>
                  </div>
                  <Badge variant="outline" className="shrink-0 text-[11px]">只读</Badge>
                </div>
                <pre className="max-h-[260px] min-h-[180px] overflow-auto whitespace-pre-wrap rounded-md border bg-muted/20 p-3 font-mono text-xs leading-5" data-testid="prompt-playground-template-source">
                  {selected?.content || "从左侧选择提示词后查看模板源。"}
                </pre>
                <div className="grid gap-1 text-[11px] text-muted-foreground">
                  <div>模板类型：{selected?.prompt_type ?? "未选择"}</div>
                  <div>版本：{selected ? `v${selected.version}` : "未选择"}</div>
                  <div>变量声明：{variableNames.length}</div>
                </div>
              </section>

              <section className="grid min-h-[260px] content-start gap-3 rounded-md border bg-background p-3" data-testid="prompt-playground-variable-checklist">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <Braces className="size-3.5 shrink-0 text-muted-foreground" />
                    <h3 className="truncate text-sm font-semibold">变量样本</h3>
                  </div>
                  <Badge variant={debugResult.missingVariables.length > 0 ? "outline" : "secondary"} className="shrink-0 text-[11px]">
                    {debugResult.missingVariables.length > 0 ? `缺失 ${debugResult.missingVariables.length}` : "完整"}
                  </Badge>
                </div>
                <Textarea
                  value={debugValuesText}
                  aria-label="模板变量"
                  onChange={(event) => onDebugValuesTextChange(event.target.value)}
                  className="min-h-[150px] resize-y font-mono text-sm leading-6"
                  placeholder="任务标题=登录失败&#10;项目背景=账号系统"
                />
                <div className="grid gap-1.5">
                  {selected && selected.variables.length > 0 ? (
                    selected.variables.map((variable) => (
                      <div key={variable.name} className="flex min-w-0 items-center gap-2 rounded border bg-muted/10 px-2 py-1.5 text-xs">
                        <span className="min-w-0 flex-1 truncate font-medium">{variable.label || variable.name}</span>
                        <span className="shrink-0 text-muted-foreground">{variable.name}</span>
                        <Badge variant={missingVariableSet.has(variable.name) ? "outline" : "secondary"} className="shrink-0 text-[10px]">
                          {missingVariableSet.has(variable.name) ? "缺失" : "已填"}
                        </Badge>
                      </div>
                    ))
                  ) : (
                    <div className="rounded border border-dashed px-3 py-4 text-xs text-muted-foreground">该模板未声明变量。</div>
                  )}
                </div>
              </section>

              <section className="grid min-h-[260px] content-start gap-2 rounded-md border bg-background p-3">
                <div className="flex min-h-5 items-center gap-2">
                  <span className="text-xs font-medium text-muted-foreground">渲染文本</span>
                  {debugResult.missingVariables.length > 0 ? (
                    <Badge variant="outline" className="text-[11px]">
                      缺失 {debugResult.missingVariables.join("、")}
                    </Badge>
                  ) : (
                    <Badge variant="secondary" className="text-[11px]">
                      变量完整
                    </Badge>
                  )}
                </div>
                <pre className="min-h-[250px] overflow-auto whitespace-pre-wrap rounded-md border bg-background p-3 font-mono text-sm leading-6" data-testid="prompt-playground-rendered-output">
                  {debugResult.rendered || "选择提示词并填写变量后生成渲染结果。"}
                </pre>
              </section>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" onClick={onRunDebug} disabled={!selected || runningDebug}>
                {runningDebug ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                运行并记录
              </Button>
              {!selected && <span className="text-xs text-muted-foreground">先选择提示词后才能记录模板渲染。</span>}
              {selected && debugResult.missingVariables.length > 0 && (
                <span className="text-xs text-muted-foreground">缺失变量会记录为未通过模板检查。</span>
              )}
            </div>
          </div>
        </section>

        <section className="grid gap-3">
          <div className="rounded-md border border-l-4 border-border/70 border-l-sky-500 bg-muted/10 p-3" data-testid="prompt-playground-contract">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">调试边界</h3>
              <Badge variant="outline">不启动智能体</Badge>
            </div>
            <div className="mt-2 grid gap-1 text-xs text-muted-foreground">
              <div>输出只来自模板变量替换，不调用 Codex、Claude 或其它模型。</div>
              <div>适合检查变量缺失、中文口径、提示词结构和期望字段。</div>
              <div>需要真实任务、链路追踪、令牌用量和成本证据时，进入「智能体调试场」。</div>
            </div>
          </div>

          <div className="rounded-md border border-border/70 bg-muted/10 p-3" data-testid="prompt-playground-assets">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">调试记录资产</h3>
              <Badge variant="outline">{debugAssets.length}</Badge>
            </div>
            {loading ? (
              <div className="mt-3 h-16 rounded bg-muted/60" />
            ) : debugAssets.length === 0 ? (
              <div className="mt-3 rounded border border-dashed px-3 py-4 text-xs text-muted-foreground">暂无提示词调试记录。</div>
            ) : (
              <div className="mt-3 divide-y rounded-md border bg-background">
                {debugAssets.slice(0, 5).map((asset) => (
                  <div key={asset.id} className="px-3 py-2 text-xs">
                    <div className="truncate font-medium text-foreground">{asset.name}</div>
                    <div className="mt-1 truncate text-muted-foreground">{asset.status} · 用例 {asset.structured_case_count} · {asset.created_at}</div>
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="rounded-md border border-border/70 bg-muted/10 p-3" data-testid="prompt-playground-runs">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">最近模板渲染</h3>
              <Badge variant="outline">{templateRuns.length}</Badge>
            </div>
            {loading ? (
              <div className="mt-3 h-16 rounded bg-muted/60" />
            ) : templateRuns.length === 0 ? (
              <div className="mt-3 rounded border border-dashed px-3 py-4 text-xs text-muted-foreground">暂无模板渲染检查。</div>
            ) : (
              <div className="mt-3 divide-y rounded-md border bg-background">
                {templateRuns.slice(0, 5).map((run) => (
                  <div key={run.id} className="grid gap-1 px-3 py-2 text-xs">
                    <div className="flex min-w-0 items-center gap-2">
                      <Badge variant={run.status === "通过" ? "secondary" : "outline"} className="shrink-0">{run.status}</Badge>
                      <span className="min-w-0 flex-1 truncate font-medium text-foreground">{run.conclusion || run.trigger_source || run.id}</span>
                    </div>
                    <div className="truncate text-muted-foreground">
                      {run.total_cases} 个用例 · 通过率 {Math.round(run.pass_rate * 100)}% · 耗时 {formatDuration(run.total_duration_ms)}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </section>
      </div>
    </section>
  );
}

export function AgentPlaygroundWorkbench({
  selected,
  debugValuesText,
  onDebugValuesTextChange,
  debugResult,
  agentExpectedText,
  onAgentExpectedTextChange,
  runtimeReadiness,
  runtimeLoading,
  agents,
  runtimes,
  executionCatalogLoading,
  selectedExecutionAgentId,
  onSelectedExecutionAgentIdChange,
  saving,
  runningAgent,
  runs,
  assets,
  cases,
  loading,
  onSaveAgentDebugPackage,
  onRunAgentDebugPackage,
}: {
  selected: PromptLibraryItem | null;
  debugValuesText: string;
  onDebugValuesTextChange: (value: string) => void;
  debugResult: ReturnType<typeof renderPromptTemplate>;
  agentExpectedText: string;
  onAgentExpectedTextChange: (value: string) => void;
  runtimeReadiness: PromptEvaluationRuntimeReadiness;
  runtimeLoading: boolean;
  agents: Agent[];
  runtimes: AgentRuntime[];
  executionCatalogLoading: boolean;
  selectedExecutionAgentId: string;
  onSelectedExecutionAgentIdChange: (agentId: string) => void;
  saving: boolean;
  runningAgent: boolean;
  runs: PromptEvaluationRun[];
  assets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  loading: boolean;
  onSaveAgentDebugPackage: () => void;
  onRunAgentDebugPackage: () => void;
}) {
  const agentRuns = runs.filter((run) => isAgentEvaluationRun(run) || Boolean(run.task_id));
  const agentPackages = assets.filter((asset) => {
    const payload = asset.payload ?? {};
    return asset.asset_type === "实验" && (asset.name.includes("智能体调试包") || Object.prototype.hasOwnProperty.call(payload, "调试包"));
  });
  const promptCaseCount = selected ? cases.filter((item) => item.prompt_id === selected.id).length : 0;
  const selectedAgentRuns = selected ? agentRuns.filter((run) => run.prompt_id === selected.id) : agentRuns;
  const recentSelectedAgentRuns = [...selectedAgentRuns].sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 5);
  const latestSelectedAgentRun = recentSelectedAgentRuns[0] ?? null;
  const selectableAgents = agents.filter((agent) => !agent.archived_at);
  const selectedExecutionAgent = selectedExecutionAgentId === "__auto__" ? null : selectableAgents.find((agent) => agent.id === selectedExecutionAgentId) ?? null;
  const evaluationAgent = agents.find((agent) => agent.name.includes("训练评估") || agent.name.includes("训练与评估")) ?? null;
  const selectedRuntime = selectedExecutionAgent
    ? runtimes.find((runtime) => runtime.id === selectedExecutionAgent.runtime_id) ?? runtimeReadiness.runtime ?? runtimes.find((runtime) => runtime.status === "online") ?? runtimes[0] ?? null
    : runtimeReadiness.runtime ?? runtimes.find((runtime) => runtime.status === "online") ?? runtimes[0] ?? null;
  const onlineRuntimeCount = runtimes.filter((runtime) => runtime.status === "online").length;
  const executionAgentLabel = executionCatalogLoading
    ? "读取中"
    : selectedExecutionAgent
      ? formatAgentDisplayName(selectedExecutionAgent)
      : "自动选择训练评估智能体";
  const executionModel = selectedExecutionAgent?.model || evaluationAgent?.model || runtimeReadiness.model || DEFAULT_AGENT_MODEL;
  const explicitAgentReady = Boolean(selectedExecutionAgent && selectedRuntime?.status === "online");
  const autoAgentReady = runtimeReadiness.status === "就绪";
  const canCreateTask = Boolean(selected) && (selectedExecutionAgent ? explicitAgentReady : autoAgentReady) && !saving && !runningAgent;

  return (
    <section className="mx-auto grid max-w-7xl gap-4" data-testid="agent-playground-panel">
      <section className="rounded-md border border-l-4 border-border/70 border-l-emerald-500 bg-emerald-500/5 p-4" data-testid="agent-playground-run-console">
        <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
          <div className="min-w-0">
            <div className="flex min-w-0 items-center gap-2">
              <TerminalSquare className="size-4 shrink-0 text-muted-foreground" />
              <h2 className="truncate text-base font-semibold">真实任务发射台</h2>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              这里不做本地模板试算，而是把已保存提示词变成一次可观测的智能体任务，等待 runtime 领取并回写运行证据。
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={runtimeReadiness.status === "就绪" ? "secondary" : "outline"} className="w-fit shrink-0">
              {runtimeLoading ? "运行时检查中" : runtimeReadiness.label}
            </Badge>
            <Button size="sm" variant="secondary" onClick={onSaveAgentDebugPackage} disabled={!selected || saving}>
              {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
              保存为实验
            </Button>
            <Button size="sm" onClick={onRunAgentDebugPackage} disabled={!canCreateTask}>
              {runningAgent ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
              创建真实智能体任务
            </Button>
          </div>
        </div>

        <div className="mt-4 grid gap-2 md:grid-cols-4" data-testid="agent-playground-launch-brief">
          {[
            ["执行对象", executionAgentLabel],
            ["运行位置", selectedRuntime?.name ?? runtimeReadiness.label],
            ["模型策略", executionModel],
            ["入队链路", "写入真实任务队列"],
          ].map(([label, detail]) => (
            <div key={label} className="min-w-0 rounded-md border bg-muted/20 px-3 py-2">
              <div className="text-[11px] font-medium text-muted-foreground">{label}</div>
              <div className="mt-1 truncate text-sm font-semibold">{detail}</div>
            </div>
          ))}
        </div>

        <div className="mt-4 grid gap-3 rounded-md border bg-background p-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_240px]" data-testid="agent-playground-execution-topology">
          <div className="min-w-0" data-testid="agent-playground-agent-selector">
            <div className="text-[11px] font-medium text-muted-foreground">执行智能体</div>
            <Select value={selectedExecutionAgentId} onValueChange={(value) => onSelectedExecutionAgentIdChange(value ?? "__auto__")}>
              <SelectTrigger size="sm" className="mt-1 w-full">
                <SelectValue>{executionAgentLabel}</SelectValue>
              </SelectTrigger>
              <SelectContent align="start" className="max-h-72">
                <SelectItem value="__auto__">自动选择训练评估智能体</SelectItem>
                {selectableAgents.map((agent) => (
                  <SelectItem key={agent.id} value={agent.id}>
                    {formatAgentDisplayName(agent)} · {formatAgentStatus(agent.status)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <div className="mt-1 truncate text-xs text-muted-foreground">
              {selectedExecutionAgent
                ? `状态 ${formatAgentStatus(selectedExecutionAgent.status)} · 可见性 ${formatAgentVisibility(selectedExecutionAgent.visibility)}`
                : evaluationAgent
                  ? `自动选择训练评估智能体：自动模式会使用 ${formatAgentDisplayName(evaluationAgent)}，也可指定其它工作区智能体。`
                  : "自动选择训练评估智能体：真实运行前会确保训练评估执行智能体存在。"}
            </div>
          </div>
          <div className="min-w-0" data-testid="agent-playground-runtime-selector">
            <div className="text-[11px] font-medium text-muted-foreground">真实运行时</div>
            <div className="mt-1 truncate text-sm font-semibold">{selectedRuntime?.name ?? "暂无可用运行时"}</div>
            <div className="mt-1 truncate text-xs text-muted-foreground">
              在线 {onlineRuntimeCount} / 全部 {runtimes.length} · 供应方 {formatRuntimeProvider(selectedRuntime?.provider)}
            </div>
          </div>
          <div className="min-w-0 rounded-md border border-dashed px-3 py-2" data-testid="agent-playground-queue-contract">
            <div className="text-[11px] font-medium text-muted-foreground">入队链路</div>
            <div className="mt-1 truncate text-sm font-semibold">写入真实任务队列</div>
            <div className="mt-1 truncate text-xs text-muted-foreground">回写运行历史、任务消息和用量。</div>
          </div>
        </div>

        <div className="mt-4 grid gap-2 md:grid-cols-3" data-testid="agent-playground-task-pipeline">
          {[
            { label: "检查运行时", detail: runtimeLoading ? "检查中" : runtimeReadiness.status, icon: Activity },
            { label: "创建真实任务", detail: selected ? `模板 v${selected.version}` : "待选择提示词", icon: TerminalSquare },
            { label: "回写观测证据", detail: "任务、链路追踪、令牌用量、成本", icon: ClipboardCheck },
          ].map((item, index) => {
            const Icon = item.icon;
            return (
              <div key={item.label} className="min-w-0 rounded-md border bg-muted/20 px-3 py-3">
                <div className="flex items-center gap-2">
                  <Icon className="size-3.5 shrink-0 text-muted-foreground" />
                  <span className="text-[11px] font-medium text-muted-foreground">阶段 {index + 1}</span>
                </div>
                <div className="mt-2 truncate text-sm font-semibold">{item.label}</div>
                <div className="mt-1 truncate text-xs text-muted-foreground">{item.detail}</div>
              </div>
            );
          })}
        </div>

        <div className="mt-4 grid gap-2 md:grid-cols-4" data-testid="agent-playground-evidence-strip">
          {[
            ["运行时", runtimeLoading ? "检查中" : runtimeReadiness.status],
            ["结构化用例", String(promptCaseCount)],
            ["真实运行", String(selectedAgentRuns.length)],
            ["调试包", String(agentPackages.length)],
          ].map(([label, value]) => (
            <div key={label} className="min-w-0 rounded-md border bg-background px-3 py-2">
              <div className="text-[11px] font-medium text-muted-foreground">{label}</div>
              <div className="mt-1 truncate text-sm font-semibold">{value}</div>
            </div>
          ))}
        </div>
      </section>

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_340px]">
        <section className="grid gap-3 rounded-md border border-border/70 bg-muted/10 p-4" data-testid="agent-playground-task-payload">
          <div className="grid gap-3 border-b pb-3 md:grid-cols-[minmax(0,1fr)_220px]">
            <div className="min-w-0">
              <div className="text-xs font-medium text-muted-foreground">任务载荷来源</div>
              <div className="mt-1 truncate text-sm font-semibold">{selected?.name ?? "请先从右侧执行目标池选择提示词"}</div>
              <div className="mt-1 line-clamp-2 text-xs text-muted-foreground">{selected?.description || "智能体调试场只引用已保存模板，模板编辑请回到提示词库。"}</div>
            </div>
            <div className="grid gap-1 text-xs text-muted-foreground">
              <div>类型：{selected?.prompt_type ?? "未选择"}</div>
              <div>版本：{selected ? `v${selected.version}` : "未选择"}</div>
              <div>结构化用例：{promptCaseCount}</div>
            </div>
          </div>

          <div className="grid gap-3 2xl:grid-cols-[300px_minmax(0,1fr)]">
            <Field label="任务变量">
              <Textarea
                value={debugValuesText}
                onChange={(event) => onDebugValuesTextChange(event.target.value)}
                className="min-h-[180px] resize-y font-mono text-sm leading-6"
                placeholder="任务标题=登录失败&#10;项目背景=账号系统"
              />
            </Field>
            <div className="grid gap-1.5 text-sm" data-testid="agent-playground-context-preview">
              <div className="flex min-h-5 items-center gap-2">
                <span className="text-xs font-medium text-muted-foreground">将入队的任务正文</span>
                {debugResult.missingVariables.length > 0 ? (
                  <Badge variant="outline" className="text-[11px]">
                    缺失 {debugResult.missingVariables.join("、")}
                  </Badge>
                ) : (
                  <Badge variant="secondary" className="text-[11px]">
                    可入队
                  </Badge>
                )}
              </div>
              <pre className="min-h-[180px] overflow-auto whitespace-pre-wrap rounded-md border bg-background p-3 font-mono text-sm leading-6">
                {debugResult.rendered || "选择提示词并填写变量后生成任务上下文。"}
              </pre>
            </div>
          </div>

          <Field label="期望输出">
            <Textarea
              value={agentExpectedText}
              onChange={(event) => onAgentExpectedTextChange(event.target.value)}
              className="min-h-[120px] resize-y text-sm leading-6"
              placeholder="写下希望智能体最终交付的结构、证据和中文口径。"
            />
          </Field>

          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            {!selected && <span>先选择提示词后才能保存或执行。</span>}
            {selected && selectedExecutionAgent && !explicitAgentReady && <span>所选智能体绑定的运行时不在线，请先启动运行时。</span>}
            {selected && !selectedExecutionAgent && runtimeReadiness.status !== "就绪" && !runtimeLoading && <span>{runtimeReadiness.fix}</span>}
            {selected && (selectedExecutionAgent ? explicitAgentReady : runtimeReadiness.status === "就绪") && <span>点击创建后会写入真实任务队列，并在运行历史中回读任务标识、链路追踪和用量。</span>}
          </div>
        </section>

        <section className="grid content-start gap-3">
          <AgentExecutionConfigComparison
            current={{
              agent: executionAgentLabel || "未选择执行智能体",
              runtime: selectedRuntime?.name ?? runtimeReadiness.label,
              model: executionModel,
              prompt: selected?.name ?? "未选择提示词",
            }}
            latestRun={latestSelectedAgentRun}
            recentRuns={recentSelectedAgentRuns}
            agents={agents}
            runtimes={runtimes}
          />

          <div className="rounded-md border border-border/70 bg-muted/10 p-3" data-testid="agent-playground-runtime">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">真实执行准备度</h3>
              <Badge variant={runtimeReadiness.status === "就绪" ? "secondary" : "outline"}>{runtimeLoading ? "检查中" : runtimeReadiness.status}</Badge>
            </div>
            <div className="mt-2 grid gap-1 text-xs text-muted-foreground">
              <div>{runtimeLoading ? "正在检查当前工作区的运行时列表。" : runtimeReadiness.detail}</div>
              <div>目标模型：{runtimeReadiness.model || DEFAULT_AGENT_MODEL}</div>
              <div>运行时：{runtimeReadiness.runtime?.name ?? runtimeReadiness.runtime?.id ?? "未选择"}</div>
            </div>
          </div>

          <div className="rounded-md border border-l-4 border-border/70 border-l-emerald-500 bg-muted/10 p-3" data-testid="agent-playground-observability-contract">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">观测回写契约</h3>
              <Badge variant="outline">真实任务</Badge>
            </div>
            <div className="mt-2 grid gap-1 text-xs text-muted-foreground">
              <div>入队后生成任务标识，并绑定触发来源「智能体调试场」。</div>
              <div>运行时完成后同步消息、链路追踪、令牌用量、耗时和失败原因。</div>
              <div>运行历史和运行看板必须能回读同一条证据。</div>
            </div>
          </div>

          <div className="rounded-md border border-border/70 bg-muted/10 p-3" data-testid="agent-playground-packages">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">调试包资产</h3>
              <Badge variant="outline">{agentPackages.length}</Badge>
            </div>
            {loading ? (
              <div className="mt-3 h-16 rounded bg-muted/60" />
            ) : agentPackages.length === 0 ? (
              <div className="mt-3 rounded border border-dashed px-3 py-4 text-xs text-muted-foreground">暂无智能体调试包。</div>
            ) : (
              <div className="mt-3 divide-y rounded-md border bg-background">
                {agentPackages.slice(0, 5).map((asset) => (
                  <div key={asset.id} className="px-3 py-2 text-xs">
                    <div className="truncate font-medium text-foreground">{asset.name}</div>
                    <div className="mt-1 truncate text-muted-foreground">{asset.status} · 用例 {asset.structured_case_count} · {asset.created_at}</div>
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="rounded-md border border-border/70 bg-muted/10 p-3" data-testid="agent-playground-runs">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-sm font-semibold">最近智能体运行</h3>
              <Badge variant="outline">{agentRuns.length}</Badge>
            </div>
            {loading ? (
              <div className="mt-3 h-16 rounded bg-muted/60" />
            ) : agentRuns.length === 0 ? (
              <div className="mt-3 rounded border border-dashed px-3 py-4 text-xs text-muted-foreground">暂无真实智能体运行记录。</div>
            ) : (
              <div className="mt-3 divide-y rounded-md border bg-background">
                {agentRuns.slice(0, 5).map((run) => (
                  <div key={run.id} className="grid gap-1 px-3 py-2 text-xs">
                    <div className="flex min-w-0 items-center gap-2">
                      <Badge variant={run.status === "通过" ? "secondary" : "outline"} className="shrink-0">{run.status}</Badge>
                      <span className="min-w-0 flex-1 truncate font-medium text-foreground">{run.conclusion || run.trigger_source || run.id}</span>
                    </div>
                    <div className="truncate text-muted-foreground">
                      任务 {run.task_id ?? "未绑定"} · 模型 {run.model || "未记录"} · 耗时 {formatDuration(run.total_duration_ms)}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </section>
      </div>
    </section>
  );
}

function AgentExecutionConfigComparison({
  current,
  latestRun,
  recentRuns,
  agents,
  runtimes,
}: {
  current: {
    agent: string;
    runtime: string;
    model: string;
    prompt: string;
  };
  latestRun: PromptEvaluationRun | null;
  recentRuns: PromptEvaluationRun[];
  agents: Agent[];
  runtimes: AgentRuntime[];
}) {
  const latestAgent = latestRun?.agent_id ? agents.find((agent) => agent.id === latestRun.agent_id) ?? null : null;
  const latestRuntime = latestRun?.runtime_id ? runtimes.find((runtime) => runtime.id === latestRun.runtime_id) ?? null : null;
  const latestAgentLabel = latestAgent ? formatAgentDisplayName(latestAgent) ?? latestAgent.name : latestRun?.agent_id ? `智能体 ${latestRun.agent_id}` : "未绑定智能体";
  const latestRuntimeLabel = latestRuntime?.name ?? latestRun?.runtime_id ?? "未绑定运行时";
  const latestTokenTotal = (latestRun?.input_tokens ?? 0) + (latestRun?.output_tokens ?? 0);
  return (
    <div className="rounded-md border border-border/70 bg-background p-3" data-testid="agent-playground-config-comparison">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-semibold">执行配置对比</h3>
        <Badge variant={latestRun ? "secondary" : "outline"}>{latestRun ? "有历史运行" : "暂无历史"}</Badge>
      </div>
      <div className="mt-2 grid gap-2 text-xs">
        <div className="rounded border border-dashed px-2 py-2" data-testid="agent-playground-current-config">
          <div className="font-medium text-foreground">当前待执行配置</div>
          <div className="mt-1 grid gap-1 text-muted-foreground">
            <div className="truncate">提示词：{current.prompt}</div>
            <div className="truncate">执行智能体：{current.agent}</div>
            <div className="truncate">运行时：{current.runtime}</div>
            <div className="truncate">模型：{current.model}</div>
          </div>
        </div>
        {latestRun ? (
          <div className="rounded border px-2 py-2" data-testid="agent-playground-latest-run-config">
            <div className="font-medium text-foreground">最近同提示词真实运行</div>
            <div className="mt-1 grid gap-1 text-muted-foreground">
              <div className="truncate">状态：{latestRun.status} · {latestRun.run_kind}</div>
              <div className="truncate">执行智能体：{latestAgentLabel}</div>
              <div className="truncate">运行时：{latestRuntimeLabel}</div>
              <div className="truncate">模型：{latestRun.model || "未记录"}</div>
              <div className="truncate">任务：{latestRun.task_id ?? "未绑定"}</div>
              <div className="truncate">耗时：{formatDuration(latestRun.total_duration_ms)} · token {latestTokenTotal.toLocaleString()} · 预估成本 {formatMoney(latestRun.estimated_cost)}</div>
            </div>
          </div>
        ) : (
          <div className="rounded border border-dashed px-2 py-2 text-muted-foreground" data-testid="agent-playground-config-empty">
            暂无同提示词真实运行；创建真实智能体任务后会在这里对比执行智能体、运行时、模型、token 和成本。
          </div>
        )}
        <AgentRunComparisonTable runs={recentRuns} agents={agents} runtimes={runtimes} />
      </div>
    </div>
  );
}

function AgentRunComparisonTable({
  runs,
  agents,
  runtimes,
}: {
  runs: PromptEvaluationRun[];
  agents: Agent[];
  runtimes: AgentRuntime[];
}) {
  return (
    <div className="rounded border px-2 py-2" data-testid="agent-playground-run-comparison">
      <div className="flex items-center justify-between gap-2">
        <div className="font-medium text-foreground">最近运行横向对比</div>
        <Badge variant="outline" className="text-[10px]">最多 5 条</Badge>
      </div>
      {runs.length === 0 ? (
        <div className="mt-2 rounded border border-dashed px-2 py-3 text-muted-foreground" data-testid="agent-playground-run-comparison-empty">
          暂无可对比的真实运行。
        </div>
      ) : (
        <div className="mt-2 overflow-x-auto" data-testid="agent-playground-run-comparison-table">
          <table className="w-full min-w-[560px] text-left text-[11px]">
            <thead className="text-muted-foreground">
              <tr className="border-b">
                <th className="py-1 pr-2 font-medium">状态</th>
                <th className="py-1 pr-2 font-medium">执行智能体</th>
                <th className="py-1 pr-2 font-medium">运行时</th>
                <th className="py-1 pr-2 font-medium">模型</th>
                <th className="py-1 pr-2 font-medium">耗时</th>
                <th className="py-1 pr-2 font-medium">token</th>
                <th className="py-1 font-medium">成本</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((run) => {
                const agentLabel = run.agent_id
                  ? formatAgentDisplayName(agents.find((agent) => agent.id === run.agent_id) ?? null) ?? `智能体 ${run.agent_id.slice(0, 8)}`
                  : "未绑定";
                const runtimeLabel = run.runtime_id
                  ? runtimes.find((runtime) => runtime.id === run.runtime_id)?.name ?? `运行时 ${run.runtime_id.slice(0, 8)}`
                  : "未绑定";
                const tokenTotal = run.input_tokens + run.output_tokens;
                return (
                  <tr key={run.id} className="border-b last:border-b-0">
                    <td className="py-1.5 pr-2">
                      <Badge variant={run.status === "通过" ? "secondary" : "outline"} className="text-[10px]">{run.status}</Badge>
                    </td>
                    <td className="max-w-28 truncate py-1.5 pr-2 text-muted-foreground">{agentLabel}</td>
                    <td className="max-w-28 truncate py-1.5 pr-2 text-muted-foreground">{runtimeLabel}</td>
                    <td className="max-w-32 truncate py-1.5 pr-2 text-muted-foreground">{run.model || "未记录"}</td>
                    <td className="whitespace-nowrap py-1.5 pr-2 text-muted-foreground">{formatDuration(run.total_duration_ms)}</td>
                    <td className="whitespace-nowrap py-1.5 pr-2 text-muted-foreground">{tokenTotal.toLocaleString()}</td>
                    <td className="whitespace-nowrap py-1.5 text-muted-foreground">{formatMoney(run.estimated_cost)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1.5 text-sm">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

function isAgentEvaluationRun(run: PromptEvaluationRun): boolean {
  const kind = String(run.run_kind);
  return kind === "Agent执行" || kind === "智能体执行" || Boolean(run.task_id);
}

function formatDuration(value: unknown): string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "未记录";
  if (value < 1000) return `${Math.round(value)}ms`;
  return `${(value / 1000).toFixed(1)}s`;
}

function formatMoney(value: unknown): string {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) return "未记录";
  return `$${value.toFixed(4)}`;
}

function formatAgentStatus(status: Agent["status"]): string {
  const labels: Record<Agent["status"], string> = {
    idle: "空闲",
    working: "执行中",
    blocked: "阻塞",
    error: "异常",
    offline: "离线",
  };
  return labels[status] ?? status;
}

function formatAgentDisplayName(agent: Agent | null): string | null {
  if (!agent) return null;
  if (agent.name === "Multica 训练评估 Agent") return "Multica 训练评估智能体";
  return agent.name;
}

function formatAgentVisibility(visibility: Agent["visibility"]): string {
  const labels: Record<Agent["visibility"], string> = {
    workspace: "工作区可见",
    private: "私有",
  };
  return labels[visibility] ?? visibility;
}

function formatRuntimeProvider(provider: string | undefined): string {
  if (!provider) return "未选择";
  const labels: Record<string, string> = {
    codex: "Codex",
    claude: "Claude",
    codebuddy: "CodeBuddy",
  };
  return labels[provider.toLowerCase()] ?? provider;
}
