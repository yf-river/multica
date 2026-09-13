"use client";

// 问题操作下拉菜单：一个三点按钮触发的问题操作下拉，内部复用的菜单项来自 issue-actions-menu-items。
import { useState, type ReactElement } from "react";
import type { Issue } from "@multica/core/types";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
} from "@multica/ui/components/ui/dropdown-menu";
import { useIssueActions } from "./use-issue-actions";
import {
  IssueActionsMenuItems,
  dropdownPrimitives,
} from "./issue-actions-menu-items";
import { AssigneePicker } from "../components/pickers";

interface IssueActionsDropdownProps {
  issue: Issue;
  /** 被 Base UI 通过 `render` 属性克隆用作触发器的单个 React 元素。 */
  trigger: ReactElement;
  align?: "start" | "end" | "center";
  /** 若设置，删除问题后离开当前页——回到用户来的地方；没有站内历史时则回退到该路径。 */
  onDeletedFallbackPath?: string;
}

/**
 * 问题操作下拉菜单：以三点按钮为触发器，渲染一组问题操作（改状态、优先级、指派人、删除等）。
 * @param props.issue - 操作的目标问题。
 * @param props.trigger - 下拉触发元素（三点按钮等）。
 * @param props.align - 菜单对齐方向，默认 `end`。
 * @param props.onDeletedFallbackPath - 删除后无历史可返回时的兜底跳转路径。
 */
export function IssueActionsDropdown({
  issue,
  trigger,
  align = "end",
  onDeletedFallbackPath,
}: IssueActionsDropdownProps) {
  const actions = useIssueActions(issue);
  const [assigneeOpen, setAssigneeOpen] = useState(false);

  // 外层的 `relative inline-flex` 是指派人选择器的锚定盒子：`triggerRender` 里那个
  // absolute、pointer-events-none 的 span 填满它，让弹层相对于下拉的三点按钮定位，
  // 而不必手动把 ref 穿过 Base UI 的锚点 API。
  return (
    <span className="relative inline-flex">
      <DropdownMenu>
        <DropdownMenuTrigger render={trigger} />
        <DropdownMenuContent align={align} className="w-auto">
          <IssueActionsMenuItems
            issue={issue}
            actions={actions}
            primitives={dropdownPrimitives}
            onOpenAssignee={() => setAssigneeOpen(true)}
            onDeletedFallbackPath={onDeletedFallbackPath}
          />
        </DropdownMenuContent>
      </DropdownMenu>
      {/* 只有用户真的打开时才挂载选择器。否则列表/看板里的每一行在挂载时都会订阅
          members/agents/squads/frequency 这些查询，成倍增加内存和渲染开销。 */}
      {assigneeOpen && (
        <AssigneePicker
          assigneeType={issue.assignee_type}
          assigneeId={issue.assignee_id}
          onUpdate={actions.updateField}
          open={assigneeOpen}
          onOpenChange={setAssigneeOpen}
          triggerRender={
            <span
              aria-hidden
              className="pointer-events-none absolute inset-0"
            />
          }
          trigger={<span />}
          align={align}
        />
      )}
    </span>
  );
}
