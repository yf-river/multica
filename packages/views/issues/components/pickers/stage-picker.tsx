"use client";

// 本文件：StagePicker —— 问题阶段（stage）选择器。
// 阶段是可用的数字 1..top，选项列表随"当前值、兄弟最大值"动态扩展，
// 保证新建/编辑兄弟问题时已用到的更高阶段仍可选、且总是能再加一档。

import { useState } from "react";
import { Milestone } from "lucide-react";
import type { UpdateIssueRequest } from "@multica/core/types";
import { PropertyPicker, PickerItem } from "./property-picker";
import { useT } from "../../../i18n";

/**
 * 父问题各子问题中被分配的最高阶段（都没有则 0）。告诉 {@link StagePicker}
 * 选项列表该扩展到多远——好让创建/编辑兄弟问题时，已被占用的更高阶段仍然可选。
 */
export function maxSiblingStage(children: readonly { stage: number | null }[]): number {
  return children.reduce((m, c) => (c.stage != null && c.stage > m ? c.stage : m), 0);
}

/**
 * 选择器提供的阶段选项（Stage 1..top）。`top` 始终覆盖当前阶段、
 * 最高兄弟阶段（`maxStage`）以及再往上一档（好新增阶段）——并设下限，
 * 保证 Stage 1–3 始终可选。
 */
export function stageOptions(stage: number | null, maxStage = 0): number[] {
  const top = Math.max(stage ?? 0, maxStage, 2) + 1;
  return Array.from({ length: top }, (_, i) => i + 1);
}

/**
 * 阶段选择器。选项列表见文件头。
 */
export function StagePicker({
  stage,
  onUpdate,
  maxStage = 0,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align,
  defaultOpen = false,
}: {
  stage: number | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  /** 兄弟中的最高阶段——让选择器能多提供一档。 */
  maxStage?: number;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
  /** 首次挂载即打开（渐进披露侧栏用）。 */
  defaultOpen?: boolean;
}) {
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const { t } = useT("issues");

  const options = stageOptions(stage, maxStage);

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-44"
      align={align}
      triggerRender={triggerRender}
      trigger={
        customTrigger ?? (
          <>
            <Milestone className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <span className="truncate">
              {stage == null
                ? t(($) => $.stage.none)
                : t(($) => $.stage.value, { n: stage })}
            </span>
          </>
        )
      }
    >
      {/* "无阶段" —— 永远第一行，与其他选择器一致。保留各值行的图标列，
          让标签对齐。 */}
      <PickerItem
        emptyValue
        selected={stage == null}
        onClick={() => {
          onUpdate({ stage: null });
          setOpen(false);
        }}
      >
        <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground">
          <Milestone className="h-3 w-3 shrink-0" />
          <span className="truncate">{t(($) => $.stage.none)}</span>
        </span>
      </PickerItem>
      {options.map((s) => (
        <PickerItem
          key={s}
          selected={s === stage}
          onClick={() => {
            onUpdate({ stage: s });
            setOpen(false);
          }}
        >
          <span className="inline-flex items-center gap-1.5 text-caption">
            <Milestone className="h-3 w-3 shrink-0 text-muted-foreground" />
            {t(($) => $.stage.value, { n: s })}
          </span>
        </PickerItem>
      ))}
    </PropertyPicker>
  );
}
