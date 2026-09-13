"use client";

// 项目选择器：下拉搜索当前工作区的项目（支持拼音匹配），用于把某个对象（如
// 任务）关联到一个项目，第一行永远是「无项目」用于清除。
import { useState } from "react";
import { FolderKanban } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { projectListOptions } from "@multica/core/projects/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import type { UpdateIssueRequest } from "@multica/core/types";
import { ProjectIcon } from "./project-icon";
import {
  PropertyPicker,
  PickerItem,
  PickerEmpty,
  PICKER_TRIGGER_CLASS,
} from "../../issues/components/pickers/property-picker";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";

/**
 * 项目选择器：从工作区项目列表里选一个项目并写回。
 *
 * @param projectId 当前关联的项目 id（null 表示未关联）
 * @param onUpdate 变更回调（携带局部更新对象，如 `{ project_id: p.id }`）
 * @param triggerRender 自定义触发器（可选）；缺省渲染一个占位触发器按钮
 * @param align 弹层对齐方向
 * @param defaultOpen 首次挂载是否直接展开下拉（「渐进展开」的侧栏用它让新加的
 *   字段立刻进入编辑态）
 * @param open 受控打开状态
 * @param onOpenChange 打开状态变化回调
 * @param disabled 只读锁：为 true 时触发器禁用、移出 Tab 顺序、菜单永远打不开，
 *   因此任何项目变更都不会发生——无论指针还是键盘。清除入口在菜单里，锁菜单
 *   就等于锁清除。需要在某个瞬态窗口（如聊天发送中）冻结选择的调用方传它
 */
export function ProjectPicker({
  projectId,
  onUpdate,
  triggerRender,
  align = "start",
  defaultOpen = false,
  open: controlledOpen,
  onOpenChange,
  disabled = false,
}: {
  projectId: string | null;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  triggerRender?: React.ReactElement;
  align?: "start" | "center" | "end";
  /** 是否在首次挂载时展开下拉。用于「渐进展开」的侧栏，让新加的字段立刻进入编辑态。 */
  defaultOpen?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** 只读锁。为 true 时触发器禁用、移出 Tab 顺序、菜单永远打不开，因此不会有任何
   *  项目上下文变更发生——无论指针还是键盘。清除入口在菜单里，所以锁菜单就等于
   *  锁清除。需要在瞬态窗口（如聊天发送进行中）冻结选择的调用方传这个。 */
  disabled?: boolean;
}) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const current = projects.find((p) => p.id === projectId);
  const [filter, setFilter] = useState("");
  // 归一成「恒为布尔」的受控 open，与其它选择器（状态/优先级/负责人/标签）一致。
  // Base UI 会锁存受控的 `open={true}`——之后再传 `undefined` 不会把它关掉——
  // 所以像创建任务弹窗那样传 `open={cond ? true : undefined}` 的调用方，
  // 选中项目后弹窗会被卡住打不开。
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  // 禁用的选择器永远不能处于打开状态，也没有任何交互能重新打开它。
  const open = disabled ? false : controlledOpen ?? internalOpen;
  const setOpen = disabled ? () => {} : onOpenChange ?? setInternalOpen;

  // 客户端过滤：子串匹配 + 拼音匹配，让中文项目名也能用拉丁字母输入找到
  // （如 "sjtmh" → 「数据透明化」）。
  const query = filter.trim().toLowerCase();
  const filtered = projects.filter(
    (p) => p.title.toLowerCase().includes(query) || matchesPinyin(p.title, query),
  );

  // 自带触发器的调用方（创建胶囊、聊天胶囊、autopilot 卡片、表格单元格）
  // 完全接管触发器。
  const resolvedTriggerRender = triggerRender ?? (
    <button type="button" disabled={disabled} className={PICKER_TRIGGER_CLASS} />
  );

  return (
    <div className="inline-flex min-w-0">
      <PropertyPicker
        open={open}
        onOpenChange={setOpen}
        width="w-52"
        align={align}
        searchable
        searchPlaceholder={t(($) => $.picker.search_placeholder)}
        onSearchChange={setFilter}
        triggerRender={resolvedTriggerRender}
        trigger={
          current ? (
            <>
              <ProjectIcon project={current} size="sm" />
              <span className="truncate">{current.title}</span>
            </>
          ) : (
            <>
              <FolderKanban className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <span className="truncate">{t(($) => $.picker.no_project)}</span>
            </>
          )
        }
      >
        {/* 「无项目」——无论是否在搜索都永远排第一行；也是胶囊不再带内联 × 后
            唯一的清除入口。和负责人选择器里的「未指派」行互相呼应。 */}
        <PickerItem
          emptyValue
          selected={!projectId}
          onClick={() => {
            onUpdate({ project_id: null });
            setOpen(false);
          }}
        >
          <FolderKanban className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-muted-foreground">{t(($) => $.picker.no_project)}</span>
        </PickerItem>

        {filtered.map((p) => (
          <PickerItem
            key={p.id}
            selected={p.id === projectId}
            onClick={() => {
              onUpdate({ project_id: p.id });
              setOpen(false);
            }}
          >
            <ProjectIcon project={p} size="sm" />
            <span className="truncate">{p.title}</span>
          </PickerItem>
        ))}

        {projects.length === 0 && (
          <div className="px-2 py-1.5 text-caption text-muted-foreground">{t(($) => $.picker.empty)}</div>
        )}
        {projects.length > 0 && filtered.length === 0 && query && <PickerEmpty />}
      </PropertyPicker>
    </div>
  );
}
