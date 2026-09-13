"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Cpu, Loader2, Plus, Check, Info } from "lucide-react";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import type { RuntimeModel } from "@multica/core/types";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

// 本文件是 Agent 的“模型选择下拉框”（可搜索、可手动输入自定义值）。模型目录从
// 当前选中的运行环境拉取——daemon 会借心跳机会按需枚举模型。若某个运行环境不支持
// “按 Agent 指定模型”（supported=false），下拉框就渲染成禁用并附解释，而不是默默
// 接受一个后端会忽略的值。目前是 qwenpaw 和 mcode 两种（集合以
// agent.ModelSelectionSupported 为唯一口径）；Antigravity 在 agy 1.0.6 支持
// `--model` 后已从中移除。
//
// supported=false 与“模型发现失败”是两种不同的状态，不能混为一谈：发现失败会让
// resolveRuntimeModels 抛错，此时 modelsQuery.isError 渲染“发现失败”提示，并保留
// 下方可手输的自定义入口——这正是运行环境一个模型都枚举不出来时（MUL-6606）
// 用户需要的兜底。

/**
 * 模型选择下拉框。模型来自所给运行环境的目录；找不到模型时给出可手输的自定义入口。
 * @param runtimeId 选中的运行环境 id；为空则不做发现。
 * @param runtimeOnline 运行环境是否在线（离线时不请求发现）。
 * @param value 当前模型 id（空串 = 不覆盖，用运行时默认）。
 * @param onChange 选中新模型时回调（空串 = 清除覆盖）。
 * @param disabled 只读态。
 */
export function ModelDropdown({
  runtimeId,
  runtimeOnline,
  value,
  onChange,
  disabled,
}: {
  runtimeId: string | null;
  runtimeOnline: boolean;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");

  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );

  const supported = modelsQuery.data?.supported ?? true;
  // 给模型列表一个稳定引用——直接写 `?? []` 会在每次渲染都新建数组，
  // 让下游所有 useMemo 跟着失效重算。
  const models = useMemo(
    () => modelsQuery.data?.models ?? [],
    [modelsQuery.data],
  );
  const grouped = useMemo(() => groupByProvider(models), [models]);
  // resolveRuntimeModels 会原样抛 daemon 返回的错误文本，所以这里拿到的是
  // 运行环境自己的话（外加 daemon 附加的提示）。只有 isError 为 true 时才读它。
  const discoveryError =
    modelsQuery.error instanceof Error
      ? modelsQuery.error.message.trim() || null
      : null;

  // 当所选运行环境报告“不支持按 Agent 选模型”时，清掉以前存过的值——
  // 免得残留一份永远不会生效的“幽灵配置”。
  useEffect(() => {
    if (!supported && value !== "") {
      onChange("");
    }
  }, [supported, value, onChange]);

  const filtered = useMemo(() => {
    if (!search.trim()) return grouped;
    const needle = search.toLowerCase();
    const out: Record<string, RuntimeModel[]> = {};
    for (const [provider, list] of Object.entries(grouped)) {
      const matches = list.filter(
        (m) =>
          m.id.toLowerCase().includes(needle) ||
          m.label.toLowerCase().includes(needle),
      );
      if (matches.length > 0) out[provider] = matches;
    }
    return out;
  }, [grouped, search]);

  const trimmedSearch = search.trim();
  const exactMatch = models.some(
    (m) => m.id === trimmedSearch || m.label === trimmedSearch,
  );
  const canCreate = trimmedSearch.length > 0 && !exactMatch;

  const select = (id: string) => {
    onChange(id);
    setOpen(false);
    setSearch("");
  };

  const triggerLabel =
    value ||
    (disabled
      ? t(($) => $.model_dropdown.select_runtime_first)
      : runtimeOnline
        ? t(($) => $.model_dropdown.default_provider)
        : t(($) => $.model_dropdown.runtime_offline_manual));

  if (!supported && !modelsQuery.isLoading) {
    return (
      <div className="flex flex-col min-w-0">
        <div className="flex h-6 items-center">
          <Label className="text-caption text-muted-foreground">{t(($) => $.model_dropdown.label)}</Label>
        </div>
        <div className="mt-1.5 flex items-start gap-2 rounded-lg border border-dashed border-border bg-muted/30 px-3 py-2.5 text-body text-muted-foreground">
          <Info className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0">
            <div>{t(($) => $.model_dropdown.managed_by_runtime_title)}</div>
            <div className="mt-0.5 text-caption">
              {t(($) => $.model_dropdown.managed_by_runtime_hint)}
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col min-w-0">
      <div className="flex h-6 items-center justify-between">
        <Label className="text-caption text-muted-foreground">{t(($) => $.model_dropdown.label)}</Label>
        {modelsQuery.isError && (
          <span
            className="text-caption text-muted-foreground"
            title={discoveryError ?? undefined}
          >
            {t(($) => $.model_dropdown.discovery_failed)}
          </span>
        )}
      </div>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          disabled={disabled}
          className="flex w-full min-w-0 items-center gap-3 rounded-lg border border-border bg-background px-3 py-2.5 mt-1.5 text-left text-body transition-colors hover:bg-muted disabled:pointer-events-none disabled:opacity-50"
        >
          <Cpu className="h-4 w-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            {/* 这层 flex 是为了跟 RuntimePicker 触发器的 DOM 保持一致——两个下拉常
                并排出现，若不套 flex，行内/块级盒子高度算法会留下约 1px 的对不齐。 */}
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">{triggerLabel}</span>
            </div>
            {value && (
              <div className="truncate text-caption text-muted-foreground">
                {modelLabel(models, value)}
              </div>
            )}
          </div>
          <ChevronDown
            className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${open ? "rotate-180" : ""}`}
          />
        </PopoverTrigger>
        <PopoverContent
          align="start"
          className="w-[var(--anchor-width)] p-0 overflow-hidden"
        >
          <div className="border-b border-border p-2">
            <Input
              autoFocus
              placeholder={t(($) => $.pickers.model_search_placeholder)}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="h-8"
            />
          </div>
          <div className="max-h-72 overflow-y-auto p-1">
            {modelsQuery.isLoading && (
              <div className="flex items-center gap-2 px-3 py-6 text-body text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                {t(($) => $.pickers.model_discovering)}
              </div>
            )}

            {!modelsQuery.isLoading &&
              Object.entries(filtered).map(([provider, list]) => (
                <div key={provider} className="mb-1">
                  {provider && (
                    <div className="px-2 pt-1.5 pb-0.5 text-caption font-medium uppercase tracking-wide text-muted-foreground">
                      {provider}
                    </div>
                  )}
                  {list.map((m) => (
                    <button
                      type="button"
                      key={m.id}
                      onClick={() => select(m.id)}
                      className={`flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-body transition-colors ${
                        m.id === value ? "bg-accent" : "hover:bg-accent/50"
                      }`}
                    >
                      <div className="min-w-0 flex-1">
                        <div className="truncate font-medium">{m.label}</div>
                        {m.label !== m.id && (
                          <div className="truncate text-caption text-muted-foreground">
                            {m.id}
                          </div>
                        )}
                      </div>
                      {m.id === value && (
                        <Check className="h-4 w-4 shrink-0 text-primary" />
                      )}
                    </button>
                  ))}
                </div>
              ))}

            {/* 发现失败的原因在这里说明（标签行的注释文字区放不下一整句话）。
                运行环境自己的原话才是有可操作性的部分——比如 hermes 会直接告诉你该跑
                哪条命令——所以原样渲染、允许选中。旁边配上手动输入提示：因为光有原因
                没有出路，只是更漂亮的死胡同。 */}
            {!modelsQuery.isLoading && modelsQuery.isError && (
              <div className="px-3 py-4 text-body text-muted-foreground">
                <div className="flex items-start gap-2">
                  <Info className="mt-0.5 h-4 w-4 shrink-0" />
                  <div className="min-w-0">
                    <div className="font-medium text-foreground">
                      {t(($) => $.pickers.model_discovery_failed_title)}
                    </div>
                    {discoveryError && (
                      <div className="mt-1 whitespace-pre-wrap break-words text-caption select-text">
                        {discoveryError}
                      </div>
                    )}
                    <div className="mt-1.5 text-caption">
                      {t(($) => $.pickers.model_discovery_failed_hint)}
                    </div>
                  </div>
                </div>
              </div>
            )}

            {!modelsQuery.isLoading &&
              !modelsQuery.isError &&
              Object.keys(filtered).length === 0 &&
              !canCreate && (
                <div className="px-3 py-6 text-center text-body text-muted-foreground">
                  {t(($) => $.pickers.model_empty_with_dot)}
                </div>
              )}

            {canCreate && (
              <button
                type="button"
                onClick={() => select(trimmedSearch)}
                className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-body text-primary transition-colors hover:bg-accent/50"
              >
                <Plus className="h-4 w-4 shrink-0" />
                <span className="truncate">
                  {t(($) => $.pickers.model_custom_use, { value: trimmedSearch })}
                </span>
              </button>
            )}

            {value && (
              <button
                type="button"
                onClick={() => select("")}
                className="mt-1 flex w-full items-center gap-2 border-t border-border px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
              >
                {t(($) => $.model_dropdown.clear_full)}
              </button>
            )}
          </div>
        </PopoverContent>
      </Popover>
    </div>
  );
}

/** 把模型列表按 provider 分组（provider 为空的分到 “” 组，列表顶端无标题处）。 */
function groupByProvider(models: RuntimeModel[]): Record<string, RuntimeModel[]> {
  const out: Record<string, RuntimeModel[]> = {};
  for (const m of models) {
    const key = m.provider ?? "";
    if (!out[key]) out[key] = [];
    out[key].push(m);
  }
  return out;
}

/** 触发器副标题：根据选中 id 反查目录，给出它的归属 provider；查不到就是手动输入的 “custom”。 */
function modelLabel(models: RuntimeModel[], id: string): string {
  const found = models.find((m) => m.id === id);
  if (!found) return "custom";
  return found.provider ? found.provider : "model";
}
