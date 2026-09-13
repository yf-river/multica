"use client";

// 本文件提供一个工作区技能的“多选列表”面板：顶部搜索框 + 可勾选的技能行。
// 同时被技能添加弹窗（SkillAddDialog）和创建表单的技能多选（SkillMultiSelect）复用，
// 行布局、搜索、空态/加载态、勾选指示都只在这里实现一份。

import { useState } from "react";
import { Search } from "lucide-react";
import { SkillIcon } from "../../skills/lib/skill-icon";
import type { SkillSummary } from "@multica/core/types";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

interface SkillPickerListProps {
  /** 要展示的技能列表。过滤（比如 SkillAddDialog 里剔除已挂上的技能）由调用方
   *  提前做好——本组件只负责渲染行。 */
  skills: readonly SkillSummary[];

  /** 当前被勾选中的技能 id。选中行显示打勾的 Checkbox 和浅色背景，点击即切换。 */
  selectedIds: ReadonlySet<string>;

  /** 每点一行触发一次；由调用方负责更新 `selectedIds`。 */
  onToggle: (skill: SkillSummary) => void;

  /** 是否在顶部显示搜索框，默认 true。 */
  searchable?: boolean;

  /** 技能查询是否在加载中，加载时显示转圈文案。 */
  loading?: boolean;

  /** 空列表/无匹配时的文案。调用方提供更贴场景的话术；缺省时回退到通用 i18n——
   *  弹窗和创建表单各自传自己风格的文案。 */
  emptyMessage?: string;
  noMatchMessage?: string;

  /** 外层容器 className。默认 `w-full`；调用方可传 `max-w-md` 之类限制宽度。 */
  className?: string;
}

/**
 * 工作区技能的多选列表（“有头无脑”的纯渲染组件）。SkillAddDialog（过滤掉已挂技能）
 * 和 SkillMultiSelect（创建表单里选技能）都用它——行布局、搜索框、空态/加载态、
 * shadcn Checkbox 指示都收在这一个面板里，以后改样式只改一处。
 *
 * 行内的名称 + 描述放在 `flex-1 min-w-0` 里做截断，避免长文本把 Checkbox 挤出视野。
 */
export function SkillPickerList({
  skills,
  selectedIds,
  onToggle,
  searchable = true,
  loading = false,
  emptyMessage,
  noMatchMessage,
  className,
}: SkillPickerListProps) {
  const { t } = useT("agents");
  const [query, setQuery] = useState("");

  const trimmedQuery = query.trim().toLowerCase();
  const filtered = trimmedQuery
    ? skills.filter((s) => {
        const name = s.name.toLowerCase();
        const description = s.description?.toLowerCase() ?? "";
        return name.includes(trimmedQuery) || description.includes(trimmedQuery);
      })
    : skills;

  const resolvedEmpty =
    emptyMessage ?? t(($) => $.create_dialog.skills_section.list_empty_default);
  const resolvedNoMatch =
    noMatchMessage ?? t(($) => $.create_dialog.skills_section.list_no_match);

  return (
    <div className={cn("w-full overflow-hidden rounded-lg border bg-card", className)}>
      {searchable && skills.length > 0 && (
        <div className="border-b p-2">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t(($) => $.create_dialog.skills_section.search_placeholder)}
              className="h-8 pl-7 text-caption"
            />
          </div>
        </div>
      )}

      <div className="max-h-64 space-y-0.5 overflow-y-auto p-1.5">
        {loading ? (
          <div className="py-6 text-center text-caption text-muted-foreground">
            {t(($) => $.create_dialog.skills_section.list_loading)}
          </div>
        ) : skills.length === 0 ? (
          <div className="py-6 text-center text-caption text-muted-foreground">{resolvedEmpty}</div>
        ) : filtered.length === 0 ? (
          <div className="py-6 text-center text-caption text-muted-foreground">{resolvedNoMatch}</div>
        ) : (
          filtered.map((skill) => {
            const isSelected = selectedIds.has(skill.id);
            return (
              <button
                key={skill.id}
                type="button"
                onClick={() => onToggle(skill)}
                aria-pressed={isSelected}
                className={cn(
                  "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left transition-colors",
                  isSelected ? "bg-accent" : "hover:bg-accent/50",
                )}
              >
                {/* 这里 Checkbox 只是“视觉指示器”——真正响应点击的是外面那层
                    <button>。所以让它不参与交互（tabIndex=-1、pointer-events-none），
                    只随行状态显示勾没勾。 */}
                <Checkbox
                  checked={isSelected}
                  tabIndex={-1}
                  className="pointer-events-none"
                />
                <SkillIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-body font-medium">{skill.name}</div>
                  {skill.description ? (
                    <div className="truncate text-caption text-muted-foreground">
                      {skill.description}
                    </div>
                  ) : null}
                </div>
              </button>
            );
          })
        )}
      </div>
    </div>
  );
}
