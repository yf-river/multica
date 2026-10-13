"use client";

// 本文件是检查器里的行内模型选择器（ModelDropdown 的轻量版）：同用 runtimeModelsOptions
// 数据源，但渲染在 PropertyPicker 内、适配单个 PropRow。去掉了“先选运行环境”的态，
// 因为检查器只在绑定运行环境后才渲染它。

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Cpu, Loader2, Plus } from "lucide-react";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { CHIP_CLASS } from "./chip";
import { useT } from "../../../i18n";

/**
 * 检查器里的行内模型选择器。`ModelDropdown`（创建 Agent 弹窗用的）的轻量表亲——
 * 数据同源（`runtimeModelsOptions`），但包在 PropertyPicker 里以放进单个 PropRow；
 * 去掉了“先选运行环境”状态，因为检查器只在运行环境绑定后才渲染它。
 *
 * 不支持“按 Agent 选模型”的运行时（supported=false）不渲染可点选择器，
 * 而是渲染一行斜体的“由运行时管理”。目前没有内置 provider 设此标记
 * （Antigravity 在 agy 1.0.6 起支持 `--model`），但分支保留给未来无模型的运行时。
 */
export function ModelPicker({
  runtimeId,
  runtimeOnline,
  value,
  canEdit = true,
  variant = "chip",
  showLabel = true,
  allowCustom = true,
  onChange,
}: {
  runtimeId: string | null;
  runtimeOnline: boolean;
  value: string | null;
  /** false 时渲染静态只读展示，不弹层。 */
  canEdit?: boolean;
  variant?: "chip" | "field";
  showLabel?: boolean;
  allowCustom?: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");

  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );
  const supported = modelsQuery.data?.supported ?? true;
  // 模型列表 memo 化，让下游每个 useMemo 拿到稳定引用；直接 `?? []` 会在每次
  // 渲染都新建数组，白白让过滤逻辑失效重算。
  const models = useMemo(
    () => modelsQuery.data?.models ?? [],
    [modelsQuery.data],
  );

  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    if (!s) return models;
    return models.filter(
      (m) =>
        m.id.toLowerCase().includes(s) || m.label.toLowerCase().includes(s),
    );
  }, [models, search]);

  const trimmedSearch = search.trim();
  const exactMatch = models.some(
    (m) => m.id === trimmedSearch || m.label === trimmedSearch,
  );
  const canCreate = allowCustom && trimmedSearch.length > 0 && !exactMatch;

  const triggerLabel = value === null ? t(($) => $.batch_runtime.choose_model) : value || t(($) => $.pickers.model_default);
  const triggerTitle = t(($) => $.pickers.model_tooltip, { value: triggerLabel });

  const select = async (id: string) => {
    setOpen(false);
    setSearch("");
    if (id !== value) await onChange(id);
  };

  if (!supported && !modelsQuery.isLoading) {
    if (variant === "field") {
      const control = (
        <div className="flex min-h-10 items-center gap-2 rounded-lg border border-dashed border-input bg-input/50 px-3 text-body text-muted-foreground">
          <Cpu className="h-4 w-4 shrink-0" aria-hidden="true" />
          <span className="truncate italic">
            {t(($) => $.pickers.model_managed_by_runtime)}
          </span>
        </div>
      );
      if (!showLabel) return control;
      return (
        <div className="flex min-w-0 flex-col">
          <Label>{t(($) => $.inspector.prop_model)}</Label>
          <div className="mt-1.5">{control}</div>
        </div>
      );
    }
    return (
      <span className="truncate italic text-muted-foreground">
        {t(($) => $.pickers.model_managed_by_runtime)}
      </span>
    );
  }

  if (!canEdit) {
    if (variant === "field") {
      const control = (
        <div className="flex min-h-10 items-center gap-2 rounded-lg border border-input bg-input/50 px-3 text-body text-muted-foreground">
          <Cpu className="h-4 w-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 truncate font-mono">{triggerLabel}</span>
        </div>
      );
      if (!showLabel) return control;
      return (
        <div className="flex min-w-0 flex-col">
          <Label>{t(($) => $.inspector.prop_model)}</Label>
          <div className="mt-1.5">{control}</div>
        </div>
      );
    }
    return (
      <span
        className="min-w-0 truncate px-1.5 py-0.5 font-mono text-micro text-muted-foreground"
        title={triggerTitle}
      >
        {triggerLabel}
      </span>
    );
  }

  const picker = (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width={
        variant === "field"
          ? "w-[var(--anchor-width)] min-w-[16rem] max-w-md"
          : "w-auto min-w-[16rem] max-w-md"
      }
      align="start"
      tooltip={triggerTitle}
      triggerRender={
        <button
          type="button"
          className={
            variant === "field"
              ? `${showLabel ? "mt-1.5 " : ""}flex min-h-10 w-full min-w-0 items-center gap-2 rounded-lg border border-input bg-transparent px-3 text-left text-body transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50`
              : CHIP_CLASS
          }
          aria-label={triggerTitle}
        />
      }
      trigger={
        <>
          {variant === "field" ? (
            <Cpu
              className="h-4 w-4 shrink-0 text-muted-foreground"
              aria-hidden="true"
            />
          ) : null}
          <span
            className={
              variant === "field"
                ? "min-w-0 flex-1 truncate font-mono"
                : "min-w-0 truncate font-mono text-micro"
            }
          >
            {triggerLabel}
          </span>
          {variant === "field" ? (
            <ChevronDown
              className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
                open ? "rotate-180" : ""
              }`}
              aria-hidden="true"
            />
          ) : null}
        </>
      }
      header={
        <div className="p-1.5">
          <Input
            autoFocus
            name="agent-model-search"
            autoComplete="off"
            aria-label={t(($) => $.pickers.model_search_placeholder)}
            placeholder={t(($) => $.pickers.model_search_placeholder)}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-7 text-caption"
          />
        </div>
      }
    >
      {modelsQuery.isLoading && (
        <div className="flex items-center gap-2 p-3 text-caption text-muted-foreground">
          <Loader2
            className="h-3 w-3 animate-spin motion-reduce:animate-none"
            aria-hidden="true"
          />
          {t(($) => $.pickers.model_discovering)}
        </div>
      )}

      {!modelsQuery.isLoading &&
        filtered.map((m) => (
          <PickerItem
            key={m.id}
            selected={m.id === value}
            onClick={() => void select(m.id)}
            // tooltip 总是携带规范模型 id——即使 chip 上显示的是更友好的 label——
            // 这样用户永远能看清真正下发到 Agent 的是哪个字符串。
            tooltip={m.label !== m.id ? `${m.label} · ${m.id}` : m.id}
          >
            {/* PickerItem 会把子内容包在 flex `<span>` 里。塞 `<div>` 会变成
                inline 里嵌 block（HTML5 非法），触发浏览器默认居中怪癖、把后代
                推离轴（模型 id 会漂到中间而不是在 label 下方左对齐）。用
                `<span block text-left>` 保证布局确定——与 thinking-picker.tsx
                里已修过的一致。 */}
            <span className="block min-w-0 flex-1 text-left">
              <span className="block truncate text-label font-medium">{m.label}</span>
              {m.label !== m.id && (
                <span className="mt-0.5 block truncate font-mono text-micro leading-snug text-muted-foreground">
                  {m.id}
                </span>
              )}
            </span>
          </PickerItem>
        ))}

      {!modelsQuery.isLoading && filtered.length === 0 && !canCreate && (
        <p className="px-3 py-3 text-center text-caption text-muted-foreground">
          {t(($) => $.pickers.model_empty)}
        </p>
      )}

      {canCreate && (
        <PickerItem
          selected={false}
          onClick={() => void select(trimmedSearch)}
          tooltip={t(($) => $.pickers.model_custom_tooltip, { value: trimmedSearch })}
        >
          <Plus className="h-3.5 w-3.5 shrink-0 text-primary" />
          <span className="truncate text-primary">
            {t(($) => $.pickers.model_custom_use, { value: trimmedSearch })}
          </span>
        </PickerItem>
      )}

      {value !== "" && (
        <button
          type="button"
          onClick={() => void select("")}
          className="mt-1 flex w-full items-center border-t px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
          title={t(($) => $.pickers.model_clear_title)}
        >
          {t(($) => $.pickers.model_clear)}
        </button>
      )}
    </PropertyPicker>
  );

  if (variant === "field") {
    if (!showLabel) return picker;
    return (
      <div className="flex min-w-0 flex-col">
        <Label>{t(($) => $.inspector.prop_model)}</Label>
        {picker}
      </div>
    );
  }

  return picker;
}
