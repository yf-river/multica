"use client";

// 本文件提供创建 Agent 表单里的“技能多选”控件：默认折叠成一行按钮，展开后变成
// 一个可勾选/取消的技能列表。视觉层复用 SkillPickerList，保证它和技能弹窗两处长得一致。

import { useState } from "react";
import { ChevronDown, Plus, X } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { SkillSummary } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { skillListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { SkillPickerList } from "./skill-picker-list";

interface SkillMultiSelectProps {
  /** 当前选中的技能 id 集合（受控），来自表单状态。 */
  selectedIds: ReadonlySet<string>;
  /** 每次勾选/取消后，把全新的选中集合交回给调用方。 */
  onChange: (next: Set<string>) => void;
}

/**
 * 创建 Agent 表单用的技能多选包装。默认折叠；展开后渲染成一个“勾选即增减”的
 * SkillPickerList（点击就在本地选中集合里加/减）。
 *
 * 通过 SkillPickerList 与技能弹窗共用同一套“搜索 + 行渲染 + 状态提示”的逻辑——
 * 搜索、行样式、选中提示都只有一份实现，任何一处的调整两边同时生效。
 */
export function SkillMultiSelect({
  selectedIds,
  onChange,
}: SkillMultiSelectProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { data: workspaceSkills = [], isLoading } = useQuery(skillListOptions(wsId));
  const [expanded, setExpanded] = useState(selectedIds.size > 0);

  const label = t(($) => $.create_dialog.skills_section.label);

  const toggle = (skill: SkillSummary) => {
    const next = new Set(selectedIds);
    if (next.has(skill.id)) next.delete(skill.id);
    else next.add(skill.id);
    onChange(next);
  };

  if (!expanded) {
    return (
      <div>
        <div className="text-caption font-medium uppercase tracking-wider text-muted-foreground">
          {label}
        </div>
        <button
          type="button"
          onClick={() => setExpanded(true)}
          className="mt-1.5 flex w-full items-center gap-2.5 rounded-lg border bg-card px-3 py-3 text-left transition-colors hover:border-primary/40 hover:bg-accent/40"
        >
          <Plus className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1 truncate text-body text-muted-foreground">
            {selectedIds.size > 0
              ? t(($) => $.create_dialog.skills_section.selected, {
                  count: selectedIds.size,
                })
              : t(($) => $.create_dialog.skills_section.placeholder)}
          </div>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-faint-foreground" />
        </button>
      </div>
    );
  }

  return (
    <div>
      <div className="flex items-center justify-between">
        <div className="text-caption font-medium uppercase tracking-wider text-muted-foreground">
          {label}
          {selectedIds.size > 0 ? (
            <span className="ml-2 text-muted-foreground">({selectedIds.size})</span>
          ) : null}
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => setExpanded(false)}
          className="h-6 gap-1 px-2 text-caption"
        >
          <X className="h-3 w-3" />
          {t(($) => $.create_dialog.skills_section.collapse)}
        </Button>
      </div>

      <div className="mt-1.5">
        <SkillPickerList
          skills={workspaceSkills}
          selectedIds={selectedIds}
          onToggle={toggle}
          loading={isLoading}
          emptyMessage={t(($) => $.create_dialog.skills_section.list_empty_multi)}
        />
      </div>
    </div>
  );
}
