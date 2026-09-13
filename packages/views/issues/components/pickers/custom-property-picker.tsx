"use client";

// 本文件：CustomPropertyPicker —— 问题"自定义属性值"的编辑/展示组件族。
//
// CustomPropertyValueEditor    —— 带乐观更新的编辑入口（issue detail 用）；
// CustomPropertyValueInput     —— 无变更副作用（不写 store/请求）的编辑器本体，
//                                 创建问题等草稿流程直接复用，issue detail 在
//                                 外层再包上乐观 mutation；
// CustomPropertyValueDisplay   —— 属性值的只读展示（行触发器 / 后续卡片）。
//
// 编辑器形态跟随属性定义类型：
//   select        → PropertyPicker（每选项一行）
//   multi_select  → PropertyPicker + 点击切换行（保持打开）
//   date          → 日历弹层（镜像 DueDatePicker）
//   checkbox      → 是/否选择器
//   actor         → 成员选择器（提交即关闭）
//   multi_actor   → 成员选择器 + 切换行（保持打开）
//   text/number/url → 弹层内输入框，Enter 提交
//
// 已归档的定义强制只读（弹层只提供"清空"——服务端拒绝给已归档属性写新值，
// 但始终允许 unset）；比当前构建更新的未知类型降级为只读视图。

import { useEffect, useState } from "react";
import { CalendarDays, Check, ExternalLink } from "lucide-react";
import { toast } from "sonner";
import type { Issue, IssueProperty, IssuePropertyValue } from "@multica/core/types";
import { hasUnknownActorRef } from "@multica/core/types";
import {
  useSetIssueProperty,
  useUnsetIssueProperty,
} from "@multica/core/properties";
import {
  toDateOnly,
  dateOnlyToLocalDate,
  formatDateOnly,
} from "@multica/core/issues/date";
import { Calendar } from "@multica/ui/components/ui/calendar";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useLocale, useT } from "../../../i18n";
import { PropertyPicker, PickerItem } from "./property-picker";
import { ActorPropertyPicker, ActorPropertyDisplay } from "./actor-property-picker";

const EDITABLE_PROPERTY_TYPES = [
  "select",
  "multi_select",
  "date",
  "checkbox",
  "text",
  "number",
  "url",
  "actor",
  "multi_actor",
];

/**
 * 编辑器是否必须降级为只读（"清空"仍然提供——过期的值始终可被清理）。
 * 三种原因：
 *
 *   1. 定义已被归档。
 *   2. 定义的类型比当前构建更新。
 *   3. 单个 `actor` 值引用了本构建无法解析的种类。否则它会渲染成空值，
 *      用户以为字段未设置，就会覆盖一个从未被展示过的值。`multi_actor`
 *      豁免：它的开关往返保留未知条目而非整体替换（MUL-6286 review）。
 */
export function isCustomPropertyReadOnly(
  property: IssueProperty,
  value: IssuePropertyValue | undefined,
): boolean {
  if (property.archived) return true;
  if (!EDITABLE_PROPERTY_TYPES.includes(property.type)) return true;
  if (property.type === "actor" && hasUnknownActorRef(value)) return true;
  return false;
}

/**
 * 单个问题、单个自定义属性的值编辑器。编辑器形态跟随定义类型（见文件头）。
 * 内部走乐观 mutation；已归档定义渲染只读（弹层只提供"清空"——
 * 服务端拒绝新值但允许 unset）。来自更新服务端的未知类型降级为只读视图。
 */
export function CustomPropertyValueEditor({
  issue,
  property,
  defaultOpen = false,
  open,
  onOpenChange,
}: {
  issue: Issue;
  property: IssueProperty;
  defaultOpen?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const setProperty = useSetIssueProperty();
  const unsetProperty = useUnsetIssueProperty();
  const value = issue.properties[property.id];
  const onError = (error: unknown) =>
    toast.error(error instanceof Error ? error.message : String(error));

  return (
    <CustomPropertyValueInput
      property={property}
      value={value}
      defaultOpen={defaultOpen}
      open={open}
      onOpenChange={onOpenChange}
      onChange={(next) => {
        if (next === undefined) {
          unsetProperty.mutate(
            { issueId: issue.id, propertyId: property.id },
            { onError },
          );
          return;
        }
        setProperty.mutate(
          { issueId: issue.id, propertyId: property.id, value: next },
          { onError },
        );
      }}
    />
  );
}

/**
 * 无变更副作用的自定义属性编辑器。创建流程在问题还只是草稿时使用它；
 * issue detail 在它外层用正常的乐观 mutation 包装。
 */
export function CustomPropertyValueInput({
  property,
  value,
  onChange,
  defaultOpen = false,
  open: controlledOpen,
  onOpenChange: controlledOnOpenChange,
  trigger,
  triggerRender,
}: {
  property: IssueProperty;
  value: IssuePropertyValue | undefined;
  onChange: (value: IssuePropertyValue | undefined) => void;
  defaultOpen?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
}) {
  const { t } = useT("issues");
  const [internalOpen, setInternalOpen] = useState(defaultOpen);
  const open = controlledOpen ?? internalOpen;
  const setOpen = controlledOnOpenChange ?? setInternalOpen;
  const hasValue = value !== undefined;

  const commit = (next: IssuePropertyValue) => onChange(next);
  const clear = () => onChange(undefined);
  const valueTrigger = trigger ?? (
    <CustomPropertyValueDisplay property={property} value={value} />
  );

  const emptyLabel = (
    <span className="text-muted-foreground">
      {t(($) => $.pickers.custom_property.empty)}
    </span>
  );

  // 空值作为第一行，而不是页脚按钮——这正是其他所有选择器放"无值"的位置，
  // 且作为真实的一行，属性未设置时可以承载对勾标记。
  const emptyRow = (
    <PickerItem
      emptyValue
      selected={!hasValue}
      onClick={() => {
        clear();
        setOpen(false);
      }}
    >
      <span className="text-muted-foreground">{t(($) => $.pickers.custom_property.none)}</span>
    </PickerItem>
  );

  const readOnly = isCustomPropertyReadOnly(property, value);

  if (readOnly) {
    return (
      <PropertyPicker
        open={open}
        onOpenChange={setOpen}
        align="start"
        trigger={valueTrigger}
        triggerRender={triggerRender}
      >
        {emptyRow}
        <p className="px-2 py-1.5 text-caption text-muted-foreground">
          {property.archived
            ? t(($) => $.pickers.custom_property.archived_hint)
            : t(($) => $.pickers.custom_property.unknown_value_hint)}
        </p>
      </PropertyPicker>
    );
  }

  switch (property.type) {
    case "select": {
      const options = property.config.options ?? [];
      return (
        <PropertyPicker
          open={open}
          onOpenChange={setOpen}
          align="start"
          searchable={options.length > 7}
          trigger={valueTrigger}
          triggerRender={triggerRender}
        >
          {emptyRow}
          {options.map((option) => (
            <PickerItem
              key={option.id}
              selected={value === option.id}
              onClick={() => {
                commit(option.id);
                setOpen(false);
              }}
            >
              <span className="size-2.5 shrink-0 rounded-full" style={{ backgroundColor: option.color }} />
              <span className="truncate">{option.name}</span>
            </PickerItem>
          ))}
        </PropertyPicker>
      );
    }
    case "multi_select": {
      const options = property.config.options ?? [];
      const selected = Array.isArray(value) ? value : [];
      const toggle = (optionId: string) => {
        const next = selected.includes(optionId)
          ? selected.filter((id) => id !== optionId)
          : [...selected, optionId];
        if (next.length === 0) clear();
        else commit(next);
      };
      return (
        <PropertyPicker
          open={open}
          onOpenChange={setOpen}
          align="start"
          searchable={options.length > 7}
          trigger={valueTrigger}
          triggerRender={triggerRender}
        >
          {emptyRow}
          {options.map((option) => (
            <PickerItem
              key={option.id}
              selected={selected.includes(option.id)}
              onClick={() => toggle(option.id)}
            >
              <span className="size-2.5 shrink-0 rounded-full" style={{ backgroundColor: option.color }} />
              <span className="truncate">{option.name}</span>
            </PickerItem>
          ))}
        </PropertyPicker>
      );
    }
    case "actor":
    case "multi_actor":
      return (
        <ActorPropertyPicker
          property={property}
          value={value}
          onChange={onChange}
          open={open}
          onOpenChange={setOpen}
          trigger={valueTrigger}
          triggerRender={triggerRender}
          emptyRow={emptyRow}
        />
      );
    case "date": {
      const date = typeof value === "string" ? dateOnlyToLocalDate(value) : undefined;
      return (
        <Popover open={open} onOpenChange={setOpen}>
          <PopoverTrigger
            className={triggerRender ? undefined : "flex items-center gap-1.5 cursor-pointer rounded px-1 -mx-1 hover:bg-accent/30 transition-colors overflow-hidden"}
            render={triggerRender}
          >
            {valueTrigger}
          </PopoverTrigger>
          <PopoverContent className="w-auto p-0" align="start">
            {/* 日历上方的空值行——与 DateOnlyPicker 同一位置。 */}
            <button
              type="button"
              onClick={() => {
                clear();
                setOpen(false);
              }}
              className="flex w-full items-center gap-3 border-b px-3 py-2 text-left text-body transition-colors hover:bg-accent"
            >
              <span className="flex min-w-0 flex-1 items-center gap-2 text-muted-foreground">
                {t(($) => $.pickers.custom_property.none)}
              </span>
              <Check className={`h-3.5 w-3.5 shrink-0 text-muted-foreground ${date ? "invisible" : ""}`} />
            </button>
            <Calendar
              mode="single"
              selected={date}
              onSelect={(d: Date | undefined) => {
                if (d) commit(toDateOnly(d));
                else clear();
                setOpen(false);
              }}
            />
          </PopoverContent>
        </Popover>
      );
    }
    case "checkbox":
      return (
        <PropertyPicker
          open={open}
          onOpenChange={setOpen}
          align="start"
          trigger={valueTrigger}
          triggerRender={triggerRender}
        >
          {emptyRow}
          <PickerItem
            selected={value === true}
            onClick={() => {
              commit(true);
              setOpen(false);
            }}
          >
            {t(($) => $.pickers.custom_property.true_label)}
          </PickerItem>
          <PickerItem
            selected={value === false}
            onClick={() => {
              commit(false);
              setOpen(false);
            }}
          >
            {t(($) => $.pickers.custom_property.false_label)}
          </PickerItem>
        </PropertyPicker>
      );
    default:
      return (
        <TextishPropertyEditor
          property={property}
          value={value}
          open={open}
          onOpenChange={setOpen}
          onCommit={commit}
          onClear={clear}
          emptyLabel={emptyLabel}
          trigger={valueTrigger}
          triggerRender={triggerRender}
        />
      );
  }
}

/** text / number / url 共用的"弹层内输入框"编辑器。 */
function TextishPropertyEditor({
  property,
  value,
  open,
  onOpenChange,
  onCommit,
  onClear,
  emptyLabel,
  trigger,
  triggerRender,
}: {
  property: IssueProperty;
  value: IssuePropertyValue | undefined;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onCommit: (next: IssuePropertyValue) => void;
  onClear: () => void;
  emptyLabel: React.ReactNode;
  trigger?: React.ReactNode;
  triggerRender?: React.ReactElement<Record<string, unknown>>;
}) {
  const { t } = useT("issues");
  const [draft, setDraft] = useState("");

  useEffect(() => {
    if (open) setDraft(value === undefined ? "" : String(value));
  }, [open, value]);

  const placeholder =
    property.type === "url"
      ? t(($) => $.pickers.custom_property.url_placeholder)
      : property.type === "number"
        ? t(($) => $.pickers.custom_property.number_placeholder)
        : t(($) => $.pickers.custom_property.value_placeholder);

  const submit = () => {
    const trimmed = draft.trim();
    if (!trimmed) {
      if (value !== undefined) onClear();
      onOpenChange(false);
      return;
    }
    if (property.type === "number") {
      const parsed = Number(trimmed);
      if (Number.isNaN(parsed)) return;
      onCommit(parsed);
    } else {
      onCommit(trimmed);
    }
    onOpenChange(false);
  };

  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger
        className={triggerRender ? undefined : "flex items-center gap-1.5 cursor-pointer rounded px-1 -mx-1 hover:bg-accent/30 transition-colors overflow-hidden"}
        render={triggerRender}
      >
        {trigger ?? (value === undefined ? (
          emptyLabel
        ) : (
          <CustomPropertyValueDisplay property={property} value={value} />
        ))}
      </PopoverTrigger>
      <PopoverContent className="w-64 p-2" align="start">
        <form
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
          className="flex items-center gap-2"
        >
          <Input
            autoFocus
            type={property.type === "number" ? "number" : "text"}
            step={property.type === "number" ? "any" : undefined}
            inputMode={property.type === "number" ? "decimal" : undefined}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder={placeholder}
            className="h-8"
          />
          {property.type === "url" && typeof value === "string" && (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.pickers.custom_property.open_link)}
              onClick={() => window.open(value, "_blank", "noopener,noreferrer")}
            >
              <ExternalLink className="size-3.5" />
            </Button>
          )}
        </form>
      </PopoverContent>
    </Popover>
  );
}

/**
 * 自定义属性值的只读视图，各处行触发器共用（先覆盖侧栏行，之后卡片/过滤器）。
 * 选项 id 解析为带名字和颜色的 chip；未知 id（定义里已被删除的选项）
 * 静默丢弃，绝不渲染原始 UUID。
 */
export function CustomPropertyValueDisplay({
  property,
  value,
}: {
  property: IssueProperty;
  value: IssuePropertyValue | undefined;
}) {
  const { t } = useT("issues");
  const locale = useLocale();
  if (value === undefined) {
    return (
      <span className="text-muted-foreground">
        {t(($) => $.pickers.custom_property.empty)}
      </span>
    );
  }
  const options = property.config.options ?? [];
  switch (property.type) {
    case "select": {
      const option = options.find((o) => o.id === value);
      if (!option) {
        return (
          <span className="text-muted-foreground">
            {t(($) => $.pickers.custom_property.empty)}
          </span>
        );
      }
      return (
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="size-2.5 shrink-0 rounded-full" style={{ backgroundColor: option.color }} />
          <span className="truncate">{option.name}</span>
        </span>
      );
    }
    case "multi_select": {
      const ids = Array.isArray(value) ? value : [];
      const selected = options.filter((o) => ids.includes(o.id));
      if (selected.length === 0) {
        return (
          <span className="text-muted-foreground">
            {t(($) => $.pickers.custom_property.empty)}
          </span>
        );
      }
      return (
        <span className="flex min-w-0 flex-wrap items-center gap-1">
          {selected.map((option) => (
            <span
              key={option.id}
              className="inline-flex max-w-32 items-center gap-1 rounded-full border border-surface-border px-1.5 py-px text-micro"
            >
              <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: option.color }} />
              <span className="truncate">{option.name}</span>
            </span>
          ))}
        </span>
      );
    }
    case "actor":
    case "multi_actor":
      return (
        <ActorPropertyDisplay
          value={value}
          emptyLabel={
            <span className="text-muted-foreground">
              {t(($) => $.pickers.custom_property.empty)}
            </span>
          }
        />
      );
    case "date":
      return (
        <span className="flex items-center gap-1.5">
          <CalendarDays className="h-3.5 w-3.5 text-muted-foreground" />
          {typeof value === "string"
            ? formatDateOnly(value, { month: "short", day: "numeric" }, locale)
            : String(value)}
        </span>
      );
    case "checkbox":
      return (
        <span>
          {value === true
            ? t(($) => $.pickers.custom_property.true_label)
            : t(($) => $.pickers.custom_property.false_label)}
        </span>
      );
    case "url":
      return (
        <span className="flex min-w-0 items-center gap-1.5">
          <ExternalLink className="h-3 w-3 shrink-0 text-muted-foreground" />
          <span className="truncate">{String(value)}</span>
        </span>
      );
    default:
      return <span className="truncate tabular-nums">{String(value)}</span>;
  }
}
