"use client";

// 第 1 步「关于你」问卷用的编辑风单选行卡片：细边框卡片 + 左侧圆圈单选标记，
// 以及选中后展开自由文本输入框的「其他」变体。
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/** 「其他」选项输入框允许的最大字符数（80）。 */
const OTHER_INPUT_MAX_LENGTH = 80;

/**
 * 第 1 步问卷里的编辑风单选行。
 *
 * 设计参考：onboarding(3) 的 `.opt`——默认是细边框，选中后变成「实心底边环 +
 * 圆圈标记实心化」。Enter/Space 可选中，整行都是点击热区。放在
 * StepQuestionnaire 的 `<fieldset role="radiogroup">` 里作为 ARIA radio 使用。
 *
 * @param selected 是否选中
 * @param onSelect 点击时选中该选项的回调
 * @param label 选项文案
 */
export function OptionCard({
  selected,
  onSelect,
  label,
}: {
  selected: boolean;
  onSelect: () => void;
  label: string;
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        "group flex w-full items-center gap-3.5 rounded-lg border bg-card px-4 py-2.5 text-left transition-all",
        selected
          ? "border-foreground shadow-[inset_0_0_0_1px_var(--color-foreground)]"
          : "hover:border-foreground/20 hover:bg-accent/30",
      )}
    >
      <RadioMark selected={selected} />
      <span className="text-body font-normal leading-tight text-foreground">
        {label}
      </span>
    </button>
  );
}

/**
 * 「其他」变体——选中后会在行下方展开一个 80 字符以内的文本输入框。
 * 首次展开自动聚焦，省一次点击。
 *
 * 用户改选其他兄弟选项时清空 `otherValue` 是父问卷的职责；本组件只保证
 * 用户在输入过程中焦点稳定、不会被重渲染打断。
 *
 * @param selected 是否选中
 * @param onSelect 点击选中的回调
 * @param otherValue 输入框当前内容
 * @param onOtherChange 输入内容变化回调
 * @param placeholder 输入框占位文案（同时用作无障碍标签）
 */
export function OtherOptionCard({
  selected,
  onSelect,
  otherValue,
  onOtherChange,
  placeholder,
}: {
  selected: boolean;
  onSelect: () => void;
  otherValue: string;
  onOtherChange: (value: string) => void;
  placeholder: string;
}) {
  const { t } = useT("onboarding");
  return (
    <div
      className={cn(
        "flex w-full flex-col rounded-lg border bg-card transition-all",
        selected
          ? "border-foreground shadow-[inset_0_0_0_1px_var(--color-foreground)]"
          : "hover:border-foreground/20",
      )}
    >
      <button
        type="button"
        role="radio"
        aria-checked={selected}
        onClick={onSelect}
        className="flex w-full items-center gap-3.5 px-4 py-2.5 text-left"
      >
        <RadioMark selected={selected} />
        <span className="text-body font-normal leading-tight text-foreground">
          {t(($) => $.option_card.other_label)}
        </span>
      </button>
      {selected && (
        <div className="px-4 pb-3 pl-[44px]">
          <Input
            autoFocus
            type="text"
            value={otherValue}
            onChange={(e) => onOtherChange(e.target.value)}
            placeholder={placeholder}
            maxLength={OTHER_INPUT_MAX_LENGTH}
            className="h-8 rounded-none border-x-0 border-t-0 border-b px-0 text-body shadow-none focus-visible:border-foreground focus-visible:ring-0"
            aria-label={placeholder}
          />
        </div>
      )}
    </div>
  );
}

/** 圆形单选标记：选中时中间的圆点填满（内外两圈实现）。 */
export function RadioMark({ selected }: { selected: boolean }) {
  return (
    <span
      aria-hidden
      className={cn(
        "relative inline-block h-4 w-4 shrink-0 rounded-full border-[1.5px] transition-colors",
        selected ? "border-foreground" : "border-border",
      )}
    >
      {selected && (
        <span className="absolute inset-[3px] rounded-full bg-foreground" />
      )}
    </span>
  );
}

/** 「其他」输入框的最大长度，随组件一并导出给需要保持一致上限的调用方。 */
export { OTHER_INPUT_MAX_LENGTH };
