"use client";

// 本文件：LabelPicker —— 问题标签多选选择器。
// 触发器上以行内 chips 展示当前已选标签，弹出层可切换工作区内任意标签。
//
// 两种模式：
//   - 挂载模式（传 `issueId`）：attach/detach 走乐观请求，UI 先于服务端确认更新。
//   - 草稿模式（省略 `issueId`）：选择集由调用方经 `selectedIds` /
//     `onSelectedIdsChange` 持有，在调用方自行挂载前不落库。创建问题弹窗用。
//
// 搜索无匹配时支持行内创建：输入新名字按 Enter（或点"创建 X"行）即用哈希
// 派生的颜色创建标签并一步选中。两种模式下创建的标签都是真实的工作区标签，
// 只有"挂载"这一步不同。
//
// 底部"管理标签"项跳到工作区 Labels 设置页——问题/Agent/技能标签目录
// 的唯一管理入口。

import { useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Tag, Plus, Settings2 } from "lucide-react";
import { toast } from "sonner";
import type { Label } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  labelListOptions,
  issueLabelsOptions,
  useAttachLabel,
  useDetachLabel,
  useCreateLabel,
} from "@multica/core/labels";
import { LabelChip } from "../../../labels/label-chip";
import { useNavigation } from "../../../navigation";
import {
  PropertyPicker,
  PickerItem,
  PickerEmpty,
} from "./property-picker";
import { useT } from "../../../i18n";

interface LabelPickerProps {
  /**
   * 要编辑标签的问题。**草稿模式**（创建问题弹窗这类问题还不存在时）请省略：
   * 改传 `selectedIds` + `onSelectedIdsChange`，等问题创建后再把标签挂上去。
   */
  issueId?: string;
  /** 草稿模式的选择集。设置了 `issueId` 时被忽略。 */
  selectedIds?: string[];
  /** 草稿模式的变更处理器。设置了 `issueId` 时被忽略。 */
  onSelectedIdsChange?: (ids: string[]) => void;
  /** 可选受控 open 状态（测试 / cmd+k 集成用）。 */
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  align?: "start" | "center" | "end";
  /** 首次挂载即打开选择器。渐进披露侧栏用，让新加字段立刻进入编辑态。 */
  defaultOpen?: boolean;
  /** 自定义触发器元素（如创建工具条的 `PillButton`）。设置后已选标签 chips
   *  在它内部渲染、且不带各自的 × 按钮——移除 <button> 不能嵌在触发器
   *  <button> 内，所以移除通过在打开的弹层里关掉该标签完成。 */
  triggerRender?: React.ReactElement;
}

/**
 * 从选择器内联创建标签时使用的调色板。按 hash(name) 轮转，
 * 同名标签永远得到同色；之后仍可从"管理"弹窗改颜色。
 */
const INLINE_COLORS = [
  "#ef4444", "#f97316", "#eab308", "#22c55e", "#14b8a6",
  "#3b82f6", "#6366f1", "#a855f7", "#ec4899", "#64748b",
] as const;

function pickInlineColor(name: string): string {
  let hash = 0;
  for (let i = 0; i < name.length; i++) {
    hash = (hash * 31 + name.charCodeAt(i)) >>> 0;
  }
  return INLINE_COLORS[hash % INLINE_COLORS.length] ?? INLINE_COLORS[0]!;
}

/**
 * 标签多选选择器。触发器用行内 chips 展示已选标签，可在弹出层切换任意标签。
 *
 * 两种模式：
 * - **挂载模式**（传 `issueId`）：attach/detach 走乐观请求——UI 在服务端
 *   确认前就更新。
 * - **草稿模式**（省略 `issueId`）：选择集由调用方经 `selectedIds` /
 *   `onSelectedIdsChange` 持有，在调用方自行挂载前不持久化。创建问题弹窗用。
 *
 * 搜索词无匹配时提供行内创建：输入新名字按 Enter（或点"创建 X"行）会用
 * 哈希派生的颜色创建标签并一步选中。两种模式下创建的标签都是真实的工作区
 * 标签，只是挂载这一步不同。
 *
 * 底部"管理标签"项打开工作区 Labels 设置页——问题/Agent/技能标签目录的
 * 唯一管理界面。
 */
export function LabelPicker({
  issueId,
  selectedIds = [],
  onSelectedIdsChange,
  open: controlledOpen,
  onOpenChange,
  align = "start",
  defaultOpen = false,
  triggerRender,
}: LabelPickerProps) {
  const { t } = useT("issues");
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  const open = controlledOpen ?? internalOpen;
  const setOpen = onOpenChange ?? setInternalOpen;
  const [filter, setFilter] = useState("");
  const navigation = useNavigation();
  const paths = useWorkspacePaths();

  // 同步锁：防止快速连续 Enter / 点击导致的重复提交。React state
  // （create.isPending、filter）要到下一次渲染才可见，所以同一 tick 内
  // 的两个事件都可能通过 canCreate 守卫、各发一次 create.mutate——
  // 第二个会撞 409 并弹出一个用户没造成错误却出现的红色 toast。
  // 用 ref 干净地关上这段窗口期。
  const creatingRef = useRef(false);

  // 问题还不存在时即草稿模式：选择集放在调用方手里，而不是打 attach/detach
  // 接口。
  const isDraft = issueId === undefined;

  const wsId = useWorkspaceId();
  const { data: allLabels = [] } = useQuery(labelListOptions(wsId));
  // `issueLabelsOptions` 对空 id 会自我禁用，所以草稿路径永远不会发出
  // 按问题读标签的请求。
  const { data: attachedLabels = [] } = useQuery(issueLabelsOptions(wsId, issueId ?? ""));

  // Hooks 必须无条件调用；草稿模式下空 id 永不会被用到，因为 toggle/create
  // 都走 onSelectedIdsChange。
  const attach = useAttachLabel(issueId ?? "");
  const detach = useDetachLabel(issueId ?? "");
  const create = useCreateLabel();

  // 已选集同时驱动触发器 chips 与列表勾选。草稿模式按工作区列表解析 id
  // （丢弃期间已被删除的标签）并保留用户的选择顺序。
  const selectedLabels = useMemo<Label[]>(() => {
    if (!isDraft) return attachedLabels;
    return selectedIds
      .map((id) => allLabels.find((l) => l.id === id))
      .filter((l): l is Label => Boolean(l));
  }, [isDraft, attachedLabels, selectedIds, allLabels]);

  const selectedIdSet = useMemo(
    () => new Set(selectedLabels.map((l) => l.id)),
    [selectedLabels],
  );

  const query = filter.trim();
  const queryLower = query.toLowerCase();
  const filtered = allLabels.filter((l) => l.name.toLowerCase().includes(queryLower));
  const exactMatch = allLabels.some((l) => l.name.toLowerCase() === queryLower);
  const canCreate = query.length > 0 && !exactMatch && !create.isPending;

  const removeLabel = (labelId: string) => {
    if (isDraft) {
      onSelectedIdsChange?.(selectedIds.filter((id) => id !== labelId));
    } else {
      detach.mutate(labelId);
    }
  };

  const toggle = (labelId: string) => {
    if (isDraft) {
      onSelectedIdsChange?.(
        selectedIdSet.has(labelId)
          ? selectedIds.filter((id) => id !== labelId)
          : [...selectedIds, labelId],
      );
    } else if (selectedIdSet.has(labelId)) {
      detach.mutate(labelId);
    } else {
      attach.mutate(labelId);
    }
  };

  const createAndAttach = () => {
    if (!canCreate || creatingRef.current) return;
    creatingRef.current = true;
    const name = query;
    create.mutate(
      { name, color: pickInlineColor(name) },
      {
        onSuccess: (label) => {
          if (isDraft) {
            onSelectedIdsChange?.([...selectedIds, label.id]);
          } else {
            attach.mutate(label.id);
          }
          setFilter("");
        },
        onError: (err: unknown) => {
          toast.error(err instanceof Error ? err.message : t(($) => $.pickers.label.create_failed));
        },
        onSettled: () => {
          creatingRef.current = false;
        },
      },
    );
  };

  const openManage = () => {
    setOpen(false);
    navigation.push(`${paths.settings()}?tab=labels`);
  };

  const hasLabels = selectedLabels.length > 0;

  // 在自定义触发器（PillButton）里，触发器本身是按钮，chips 不能再自带
  // 移除按钮。否则退回到 issue-detail 侧栏用的 chip 包裹 div。
  const resolvedTriggerRender =
    triggerRender ??
    (hasLabels ? (
      <div className="flex flex-wrap items-center gap-1 cursor-pointer rounded px-1 -mx-1 hover:bg-accent/30 transition-colors" />
    ) : undefined);

  return (
    <div className="flex flex-col gap-1.5">
      <PropertyPicker
        open={open}
        onOpenChange={(v: boolean) => {
          setOpen(v);
          if (!v) setFilter("");
        }}
        width="w-80"
        align={align}
        searchable
        searchPlaceholder={t(($) => $.pickers.label.search_placeholder)}
        onSearchChange={setFilter}
        triggerRender={resolvedTriggerRender}
        trigger={
          hasLabels ? (
            <>
              {selectedLabels.map((l) => (
                <LabelChip
                  key={l.id}
                  label={l}
                  onRemove={triggerRender ? undefined : () => removeLabel(l.id)}
                />
              ))}
            </>
          ) : (
            <>
              <Tag className="h-3.5 w-3.5 text-muted-foreground" />
              <span className="text-muted-foreground">{t(($) => $.pickers.label.trigger_label)}</span>
            </>
          )
        }
        footer={
          // 渲染在方向键可导航的 listbox 之外，键盘导航不会把
          // "管理标签…"当成又一个标签选项。
          <button
            type="button"
            onClick={openManage}
            className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body text-muted-foreground hover:bg-accent transition-colors"
          >
            <Settings2 className="h-3.5 w-3.5" />
            <span>{t(($) => $.pickers.label.manage_action)}</span>
          </button>
        }
      >
        {filtered.map((label) => {
          const selected = selectedIdSet.has(label.id);
          return (
            <PickerItem
              key={label.id}
              selected={selected}
              onClick={() => toggle(label.id)}
            >
              <span
                className="inline-block h-3 w-3 shrink-0 rounded-full"
                style={{ backgroundColor: label.color }}
                aria-hidden
              />
              <span className="truncate">{label.name}</span>
            </PickerItem>
          );
        })}
        {filtered.length === 0 && !canCreate && <PickerEmpty />}
        {canCreate && (
          <PickerItem selected={false} onClick={createAndAttach}>
            <Plus className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <span className="truncate">
              {t(($) => $.pickers.label.create_action)} <span className="font-medium">&ldquo;{query}&rdquo;</span>
            </span>
            <span
              className="ml-auto inline-block h-3 w-3 shrink-0 rounded-full"
              style={{ backgroundColor: pickInlineColor(query) }}
              aria-hidden
            />
          </PickerItem>
        )}
      </PropertyPicker>
    </div>
  );
}
