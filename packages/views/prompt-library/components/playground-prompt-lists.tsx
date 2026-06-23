"use client";

import { Search } from "lucide-react";
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
    <aside className="flex min-h-0 flex-col border-b bg-muted/10 md:border-b-0 md:border-r" data-testid="prompt-playground-prompt-list">
      <div className="space-y-3 border-b p-3">
        <div className="rounded-md border bg-background px-3 py-2" data-testid="prompt-playground-selector-summary">
          <div className="text-xs font-semibold">本地模板实验室</div>
          <div className="mt-1 text-xs text-muted-foreground">只验证变量填充和最终提示词文本，不创建任务。</div>
        </div>
        <PromptSearchInput value={query} onChange={onQueryChange} placeholder="搜索要渲染的提示词" />
        <div className="rounded-md border bg-muted/20 px-3 py-2 text-xs text-muted-foreground" data-testid="prompt-playground-template-boundary">
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
                className={`flex w-full flex-col gap-2 px-3 py-3 text-left transition-colors hover:bg-muted/60 ${
                  selectedId === item.id ? "bg-muted" : ""
                }`}
              >
                <div className="flex min-w-0 items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-sm font-medium">{item.name}</span>
                  <Badge variant={item.status === "启用" ? "secondary" : "outline"} className="shrink-0">
                    {item.status}
                  </Badge>
                </div>
                <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                  <span className="shrink-0">{item.prompt_type}</span>
                  <span className="truncate">版本 v{item.version}</span>
                  <span className="truncate">模板变量 {item.variables.length}</span>
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
    <aside className="flex min-h-0 flex-col border-b md:border-b-0 md:border-r" data-testid="agent-playground-prompt-list">
      <div className="space-y-3 border-b p-3">
        <div className="rounded-md border bg-muted/20 px-3 py-2" data-testid="agent-playground-selector-summary">
          <div className="flex items-center justify-between gap-2">
            <span className="text-xs font-semibold">真实任务入口</span>
            <Badge variant={runtimeReadiness.status === "就绪" ? "secondary" : "outline"} className="shrink-0">
              {runtimeLoading ? "检查中" : runtimeReadiness.status}
            </Badge>
          </div>
          <div className="mt-1 text-xs text-muted-foreground">选择模板后创建可观测任务，回写消息、链路追踪、令牌用量和耗时。</div>
        </div>
        <PromptSearchInput value={query} onChange={onQueryChange} placeholder="搜索要执行的提示词" />
        <div className="rounded-md border bg-muted/20 px-3 py-2 text-xs text-muted-foreground" data-testid="agent-playground-execution-boundary">
          执行上下文会检查运行时、结构化用例和最近真实运行；模板内容仍由「提示词库」维护。
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {loading ? (
          <PromptListSkeleton />
        ) : items.length === 0 ? (
          <div className="p-6 text-sm text-muted-foreground">暂无可执行提示词</div>
        ) : (
          <div className="divide-y">
            {items.map((item) => {
              const caseCount = cases.filter((entry) => entry.prompt_id === item.id).length;
              const runCount = runs.filter((run) => run.prompt_id === item.id && (run.run_kind === "Agent执行" || Boolean(run.task_id))).length;
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => onSelect(item.id)}
                  className={`flex w-full flex-col gap-2 px-3 py-3 text-left transition-colors hover:bg-muted/60 ${
                    selectedId === item.id ? "bg-muted" : ""
                  }`}
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">{item.name}</span>
                    <Badge variant={item.status === "启用" ? "secondary" : "outline"} className="shrink-0">
                      {item.status}
                    </Badge>
                  </div>
                  <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                    <span className="shrink-0">{item.prompt_type}</span>
                    <span className="truncate">执行用例 {caseCount}</span>
                    <span className="truncate">真实运行 {runCount}</span>
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
