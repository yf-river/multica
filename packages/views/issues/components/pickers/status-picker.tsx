"use client";

// 本文件：StatusPicker —— 问题状态选择器。
// 选项来自工作区状态目录（`useStatusOptions`），归档状态不参与——归档
// 让一个状态退役、不再被新分配，但已用它的问题保持不动。目录落地前回退到
// 7 个内置状态，保证冷启动渲染出的就是以前的老样子，而不是空弹层（MUL-6243）。
// 选项超过阈值自动开启搜索。

import { useMemo, useState } from "react";
import type { IssueStatus, UpdateIssueRequest } from "@multica/core/types";
import { STATUS_CONFIG } from "@multica/core/issues/config";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { StatusIcon } from "../status-icon";
import { PropertyPicker, PickerItem } from "./property-picker";
import { useT } from "../../../i18n";
import { useStatusLabel } from "../../utils/status-label";
import { useStatusOptions } from "../../utils/status-options";

/** 选项超过这个数量时，平铺列表不再可一眼扫完（自动开搜索）。 */
const SEARCH_THRESHOLD = 9;

/**
 * 状态选择器。
 */
export function StatusPicker({
  status,
  onUpdate,
  trigger: customTrigger,
  triggerRender,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  align,
}: {
  /**
   * 当前选中的状态，用来勾选匹配行。`null` 表示"没有单一当前值"
   * （例如跨多个状态的批量选择）——此时没有行被勾选。单问题调用方总是传
   * 具体状态。
   */
  status: IssueStatus | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  align?: "start" | "center" | "end";
}) {
  const [internalOpen, setInternalOpen] = useState(false);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const [query, setQuery] = useState("");
  const { t } = useT("issues");
  // StatusPicker 的每个调用点都在工作区外壳内（issue detail、表格、看板批量
  // 工具条、创建问题弹窗），所以这里的 provider 一定有。
  const wsId = useWorkspaceId();
  const { categoryOf, colorOf } = useIssueStatuses(wsId);
  const labelOf = useStatusLabel(wsId);

  /**
   * 可提供的状态，平铺成一张列表，按规范类别顺序排列。
   *
   * 归档状态被排除：归档让一个状态从未来分配中退役，但不动已经在用它的问题。
   * 目录落地前回退到 7 个内置状态，让冷渲染提供的选项与以往完全一致，
   * 而不是一个空弹层。（MUL-6243）
   */
  const allOptions = useStatusOptions(wsId);

  const options = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return allOptions;
    return allOptions.filter((o) => o.label.toLowerCase().includes(q));
  }, [allOptions, query]);

  const searchable = allOptions.length > SEARCH_THRESHOLD;

  return (
    <PropertyPicker
      open={open}
      onOpenChange={(v) => {
        if (!v) setQuery("");
        setOpen(v);
      }}
      width="w-52"
      align={align}
      triggerRender={triggerRender}
      searchable={searchable}
      searchPlaceholder={t(($) => $.filters.search_status)}
      onSearchChange={setQuery}
      trigger={
        customTrigger ??
        (status != null ? (
          <>
            <StatusIcon
              status={status}
              category={categoryOf(status)}
              color={colorOf(status)}
              className="h-3.5 w-3.5 shrink-0"
            />
            <span className="truncate">{labelOf(status)}</span>
          </>
        ) : null)
      }
    >
      {options.map((option) => (
        <PickerItem
          key={option.key}
          selected={option.key === status}
          hoverClassName={STATUS_CONFIG[option.category].hoverBg}
          onClick={() => {
            onUpdate({ status: option.key });
            setOpen(false);
            setQuery("");
          }}
        >
          <StatusIcon
            status={option.key}
            category={option.category}
            color={option.color}
            className="h-3.5 w-3.5"
          />
          <span className="truncate">{option.label}</span>
        </PickerItem>
      ))}
    </PropertyPicker>
  );
}
