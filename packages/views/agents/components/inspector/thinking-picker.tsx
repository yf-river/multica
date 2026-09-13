"use client";

// 本文件是 Agent 的“推理/努力档位”选择器：选项直接来自运行时逐模型目录的
// supported_levels，弹层选择后写入 Agent 的 thinking_level。chunk 态用于检查器行内，
// field 态用于 General 设置表单。

import { useState } from "react";
import { Brain, ChevronDown } from "lucide-react";
import type { RuntimeModelThinkingLevel } from "@multica/core/types";
import { Label } from "@multica/ui/components/ui/label";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { CHIP_CLASS } from "./chip";
import { useT } from "../../../i18n";

/**
 * Agent 的推理/努力档位选择器（MUL-2339）。只有当当前模型的 `supported_levels`
 * 非空时才渲染——目前是 Claude、Codex、OpenCode；其余 provider 什么都不显示。
 * 目录由 daemon 发现，value/label 对与各 CLI 自己的文案（`Low`、`Extra high`…）
 * 逐字一致，绝不在 provider 之间做归一化。
 *
 * 空串是“不覆盖”的哨兵：后端整体省略 effort 参数，由上游 CLI 自己的配置 /
 * 内置默认决定模型实际跑在什么档位。这个状态渲染成“跟随 CLI 配置”，而不是
 * 单独挑一个档位当出厂默认——因为运行期的真实默认属于用户本地的 CLI 安装，
 * 不属于 Multica 的目录。
 */
export function ThinkingPicker({
  value,
  levels,
  canEdit = true,
  variant = "chip",
  showLabel = true,
  onChange,
}: {
  /** 已持久化的 thinking_level —— "" 表示“跟随本地 CLI 配置”。 */
  value: string;
  /** 当前 (运行环境, 模型) 支持的档位。行可见时通常非空；但 ThinkingPropRow 的
   *  “清孤儿值”路径会用空列表 + 已存值挂载本选择器，好让用户看到并清掉悬空值。 */
  levels: RuntimeModelThinkingLevel[];
  /** false 时渲染静态只读展示，不弹层。 */
  canEdit?: boolean;
  variant?: "chip" | "field";
  showLabel?: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);

  const selected = value ? levels.find((l) => l.value === value) : undefined;
  // 值“非空但在列表里找不到”（换模型丢掉了该选项，或 CLI 升级后目录缩水）：
  // 直接展示原始 token，让用户看清实际存的是什么并清掉它——而不是把它悄悄标成
  // “默认”，可后端仍会发送这个过期值。
  const triggerLabel = selected
    ? selected.label
    : value || t(($) => $.pickers.thinking_default);
  const triggerTitle = t(($) => $.pickers.thinking_tooltip, {
    value: triggerLabel,
  });

  const select = async (next: string) => {
    setOpen(false);
    if (next !== value) await onChange(next);
  };

  if (!canEdit) {
    if (variant === "field") {
      const control = (
        <div className="flex min-h-10 items-center gap-2 rounded-lg border border-input bg-input/50 px-3 text-body text-muted-foreground">
          <Brain className="h-4 w-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 truncate">{triggerLabel}</span>
        </div>
      );
      if (!showLabel) return control;
      return (
        <div className="flex min-w-0 flex-col">
          <Label>{t(($) => $.inspector.prop_thinking)}</Label>
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
          ? "w-[var(--anchor-width)] min-w-[14rem] max-w-md"
          : "w-auto min-w-[14rem] max-w-md"
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
            <Brain
              className="h-4 w-4 shrink-0 text-muted-foreground"
              aria-hidden="true"
            />
          ) : null}
          <span
            className={
              variant === "field"
                ? "min-w-0 flex-1 truncate"
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
    >
      {levels.map((l) => (
        <PickerItem
          key={l.value}
          selected={l.value === value}
          onClick={() => void select(l.value)}
        >
          {/* PickerItem 会把子内容包在一个 flex `<span>` 里。若在这里塞 `<div>`，
              就成了 inline 里嵌 block（HTML5 非法），会触发浏览器怪异行为、
              把后代元素的 x 偏移搞乱。所以用显式 `block` + `text-left` 的
              `<span>`，让每一行的布局都确定——无论该行有没有 `default` 徽章兄弟。 */}
          {/* 这里刻意不打“模型出厂默认”徽章：当选择器处于“跟随 CLI 配置”
              （value === ""）时，Multica 会省略 `--effort`，由本地 CLI 配置决定——
              模型出厂默认与实际生效的档位无关，所以给某个选项标“默认”反而误导。 */}
          <span className="block min-w-0 flex-1 text-left">
            <span className="truncate text-label font-medium">{l.label}</span>
            {l.description && (
              <span className="mt-0.5 block text-micro leading-snug text-muted-foreground">
                {l.description}
              </span>
            )}
          </span>
        </PickerItem>
      ))}

      {value && (
        <button
          type="button"
          onClick={() => void select("")}
          className="mt-1 flex w-full items-center border-t px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
          title={t(($) => $.pickers.thinking_clear_title)}
        >
          {t(($) => $.pickers.thinking_clear)}
        </button>
      )}
    </PropertyPicker>
  );

  if (variant === "field") {
    if (!showLabel) return picker;
    return (
      <div className="flex min-w-0 flex-col">
        <Label>{t(($) => $.inspector.prop_thinking)}</Label>
        {picker}
      </div>
    );
  }

  return picker;
}
