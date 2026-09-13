"use client";

// 本文件：PriorityPicker —— 优先级选择器（下拉菜单）。
// 无受控状态的调用方若自带触发器内容（看板卡片、列表行），会拿到一枚
// "延迟挂载"的仿冒触发器——浮层机制首次交互才挂载（原因见 DeferredPopup）。
// 选项列表按 PRIORITY_DISPLAY_ORDER 排序，其中"无优先级"排在最前。

import { useState } from "react";
import type { IssuePriority, UpdateIssueRequest } from "@multica/core/types";
import { PRIORITY_DISPLAY_ORDER, PRIORITY_CONFIG } from "@multica/core/issues/config";
import { PriorityIcon } from "../priority-icon";
import { DeferredPopup } from "../../../common/deferred-popup";
import { PropertyPicker, PickerItem, PICKER_TRIGGER_CLASS } from "./property-picker";
import { useT } from "../../../i18n";

interface PriorityPickerProps {
  /**
   * 当前选中的优先级，用于勾选对应行。`null` 表示"没有一个单一当前值"
   *（例如横跨多个优先级的批量选择）——此时不勾任何行。单问题调用方
   * 总是传具体优先级。
   */
  priority: IssuePriority | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
  /** 首次挂载即展开。供"渐进式展示"的侧栏用——新加的字段立刻进入编辑态。 */
  defaultOpen?: boolean;
}

/**
 * 自带触发器内容、且不受控的调用方（看板卡片、列表行）会得到一枚延迟
 * 挂载的仿冒触发器；浮层机制在首次交互时才挂载。原因见 `DeferredPopup`。
 */
export function PriorityPicker(props: PriorityPickerProps) {
  const hasDeferredTriggerContent =
    props.trigger !== undefined || props.triggerRender?.props.children != null;
  const canDefer =
    props.open === undefined &&
    props.onOpenChange === undefined &&
    !props.defaultOpen &&
    hasDeferredTriggerContent;
  if (!canDefer) {
    return <PriorityPickerImpl {...props} />;
  }
  return (
    <DeferredPopup
      trigger={props.trigger}
      triggerRender={props.triggerRender}
      triggerClassName={PICKER_TRIGGER_CLASS}
    >
      {(open, onOpenChange) => (
        <PriorityPickerImpl {...props} open={open} onOpenChange={onOpenChange} />
      )}
    </DeferredPopup>
  );
}

function PriorityPickerImpl({
  priority,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align,
  defaultOpen = false,
}: PriorityPickerProps) {
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const { t } = useT("issues");

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-44"
      align={align}
      triggerRender={triggerRender}
      trigger={
        customTrigger ??
        (priority != null ? (
          <>
            <PriorityIcon priority={priority} className="shrink-0" />
            <span className="truncate">{t(($) => $.priority[priority])}</span>
          </>
        ) : null)
      }
    >
      {/* "无优先级"排最前——见 PRIORITY_DISPLAY_ORDER。 */}
      {PRIORITY_DISPLAY_ORDER.map((p) => {
        const c = PRIORITY_CONFIG[p];
        return (
          <PickerItem
            key={p}
            selected={p === priority}
            onClick={() => {
              onUpdate({ priority: p });
              setOpen(false);
            }}
          >
            <span className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-caption font-medium ${c.badgeBg} ${c.badgeText}`}>
              <PriorityIcon priority={p} className="h-3 w-3" inheritColor />
              {t(($) => $.priority[p])}
            </span>
          </PickerItem>
        );
      })}
    </PropertyPicker>
  );
}
