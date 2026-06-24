"use client";

import { CheckCircle2, PlayCircle, Search, Target } from "lucide-react";
import type {
  PromptEvaluationRun,
  PromptEvaluationRuntimeReadiness,
  PromptEvaluationStructuredCase,
  PromptLibraryItem,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Input } from "@multica/ui/components/ui/input";

type PromptSelectorProps = {
  query: string;
  onQueryChange: (value: string) => void;
  loading: boolean;
  items: PromptLibraryItem[];
  selectedId: string | null;
  onSelect: (id: string) => void;
};

export function PromptPlaygroundPromptList({
  query,
  onQueryChange,
  loading,
  items,
  selectedId,
  onSelect,
}: PromptSelectorProps) {
  return (
    <aside className="flex min-h-0 flex-col border-b bg-sky-500/5 md:border-b-0 md:border-r" data-testid="prompt-playground-prompt-list">
      <div className="space-y-3 border-b p-3">
        <div className="rounded-md border border-sky-500/30 bg-background px-3 py-2" data-testid="prompt-playground-selector-summary">
          <div className="text-xs font-semibold">本地模板目录</div>
          <div className="mt-1 text-xs text-muted-foreground">只挑选已保存版本，进入右侧质检工作单做变量覆盖和渲染检查。</div>
        </div>
        <PromptSearchInput value={query} onChange={onQueryChange} placeholder="搜索要渲染的提示词" />
        <div className="rounded-md border border-dashed border-sky-500/30 bg-background px-3 py-2 text-xs text-muted-foreground" data-testid="prompt-playground-template-boundary">
          模板选择只显示版本和变量，不读取运行时、不创建真实任务。
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {loading ? (
          <PromptListSkeleton />
        ) : items.length === 0 ? (
          <div className="p-6 text-sm text-muted-foreground">暂无可调试提示词</div>
        ) : (
          <div className="divide-y">
            {items.map((item) => (
              <button
                key={item.id}
                type="button"
                onClick={() => onSelect(item.id)}
                data-testid="prompt-playground-template-card"
                className={`flex w-full flex-col gap-2 border-l-4 px-3 py-3 text-left transition-colors hover:bg-sky-500/10 ${
                  selectedId === item.id ? "border-l-sky-500 bg-sky-500/10" : "border-l-transparent"
                }`}
              >
                <div className="flex min-w-0 items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-sm font-medium">{item.name}</span>
                  <Badge variant={item.status === "启用" ? "secondary" : "outline"} className="shrink-0">
                    {item.status}
                  </Badge>
                </div>
                <div className="grid gap-1 text-xs text-muted-foreground" data-testid="prompt-playground-template-brief">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="shrink-0">{item.prompt_type}</span>
                    <span className="truncate">版本 v{item.version}</span>
                    <span className="truncate">模板变量 {item.variables.length}</span>
                  </div>
                  <div className="truncate">质检对象：模板源、变量样本、最终渲染文本</div>
                </div>
              </button>
            ))}
          </div>
        )}
      </div>
    </aside>
  );
}

export function AgentPlaygroundPromptList({
  query,
  onQueryChange,
  loading,
  items,
  selectedId,
  onSelect,
  cases,
  runs,
  runtimeReadiness,
  runtimeLoading,
}: PromptSelectorProps & {
  cases: PromptEvaluationStructuredCase[];
  runs: PromptEvaluationRun[];
  runtimeReadiness: PromptEvaluationRuntimeReadiness;
  runtimeLoading: boolean;
}) {
  return (
    <aside className="flex min-h-0 flex-col border-t bg-emerald-500/5 xl:border-l xl:border-t-0" data-testid="agent-playground-prompt-list">
      <div className="space-y-3 border-b bg-background/80 p-3">
        <div className="rounded-md border border-emerald-500/30 bg-emerald-500/10 px-3 py-2" data-testid="agent-playground-selector-summary">
          <div className="flex items-center justify-between gap-2">
            <span className="inline-flex items-center gap-1.5 text-xs font-semibold">
              <Target className="size-3.5 text-emerald-600" />
              执行目标池
            </span>
            <Badge variant={runtimeReadiness.status === "就绪" ? "secondary" : "outline"} className="shrink-0">
              {runtimeLoading ? "检查中" : runtimeReadiness.status}
            </Badge>
          </div>
          <div className="mt-1 text-xs text-muted-foreground">从右侧选择要执行的提示词模板，左侧发射台会创建可观测任务并回写消息、链路追踪、令牌用量和耗时。</div>
        </div>
        <PromptSearchInput value={query} onChange={onQueryChange} placeholder="搜索执行目标" />
        <div className="rounded-md border border-dashed border-emerald-500/30 bg-background px-3 py-2 text-xs text-muted-foreground" data-testid="agent-playground-execution-boundary">
          这里只选执行目标；任务变量、期望输出、运行时准备度和观测证据在左侧发射台完成。
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        {loading ? (
          <PromptListSkeleton />
        ) : items.length === 0 ? (
          <div className="p-6 text-sm text-muted-foreground">暂无可执行提示词</div>
        ) : (
          <div className="grid gap-2" data-testid="agent-playground-target-queue">
            {items.map((item, index) => {
              const caseCount = cases.filter((entry) => entry.prompt_id === item.id).length;
              const runCount = runs.filter((run) => run.prompt_id === item.id && (run.run_kind === "Agent执行" || Boolean(run.task_id))).length;
              const selected = selectedId === item.id;
              const readyLabel = runtimeLoading ? "准备度检查中" : runtimeReadiness.status === "就绪" ? "可发射" : "待修复运行时";
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => onSelect(item.id)}
                  data-testid="agent-playground-target-queue-item"
                  className={`grid w-full grid-cols-[2.25rem_minmax(0,1fr)] gap-2 rounded-md border px-2.5 py-2.5 text-left transition-colors hover:border-emerald-500/50 hover:bg-emerald-500/10 ${
                    selected ? "border-emerald-500 bg-emerald-500/10 shadow-sm" : "border-border bg-background"
                  }`}
                >
                  <span className={`flex size-9 items-center justify-center rounded-md border text-xs font-semibold ${
                    selected ? "border-emerald-500 bg-emerald-600 text-white" : "bg-muted/40 text-muted-foreground"
                  }`}>
                    {String(index + 1).padStart(2, "0")}
                  </span>
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-start gap-2">
                      <span className="min-w-0 flex-1 truncate text-sm font-semibold">{item.name}</span>
                      <Badge variant={item.status === "启用" ? "secondary" : "outline"} className="shrink-0">
                        {item.status}
                      </Badge>
                    </div>
                    <div className="mt-1 grid gap-1 text-xs text-muted-foreground">
                      <div className="flex min-w-0 items-center gap-1.5">
                        <PlayCircle className="size-3.5 shrink-0 text-emerald-600" />
                        <span className="truncate">入队目标 · {item.prompt_type}</span>
                      </div>
                      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="inline-flex items-center gap-1">
                          <CheckCircle2 className="size-3.5 text-muted-foreground" />
                          执行用例 {caseCount}
                        </span>
                        <span>真实运行 {runCount}</span>
                        <span>{readyLabel}</span>
                      </div>
                    </div>
                  </div>
                </button>
              );
            })}
          </div>
        )}
      </div>
    </aside>
  );
}

function PromptSearchInput({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
}) {
  return (
    <div className="relative">
      <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
      <Input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        className="h-8 pl-8 text-sm"
      />
    </div>
  );
}

function PromptListSkeleton() {
  return (
    <div className="space-y-2 p-3">
      {Array.from({ length: 5 }).map((_, index) => (
        <div key={index} className="h-16 rounded-md bg-muted/60" />
      ))}
    </div>
  );
}
