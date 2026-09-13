"use client";

// 问卷选项卡片：带图标/Logo/Emoji 的选项胶囊（单选或多选），以及选中后内嵌
// 自由文本输入框的「其他」变体。由「关于你」引导步骤与工作区源回填问卷共用。
import type { ReactNode } from "react";
import { Check } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";

/** 「其他」选项输入框允许的最大字符数（80），供本组件及调用方共用。 */
const OTHER_INPUT_MAX_LENGTH = 80;

/**
 * 问卷卡片网格里一个选项的数据描述。`slug` 是真正持久化到后端的枚举值；
 * `icon` 是要显示在前面的 React 节点（lucide 图标、品牌 SVG 或 Emoji 块）；
 * `label` 是由调用方预先解析好的本地化文案。`isOther` 让这张卡片切换成
 * 「自由输入」那一行。
 */
export interface QuestionOption {
  slug: string;
  icon: ReactNode;
  label: string;
  isOther?: boolean;
}

/**
 * 一个问卷选项，渲染成可自动换行的胶囊按钮。
 *
 * 这些选项以前是四列网格里的通栏带边框卡片。在引导流程的窄列里网格没有容身之处，
 * 而把 18 个选项堆成整行又会让一屏内容变成一次长滚动。胶囊就是这种「选项很多」
 * 题型自己的答案：它们会自动换行来适配列宽。
 *
 * `mode` 控制无障碍角色：单选问题用 `"radio"`（如 role、source），多选用
 * `"checkbox"`（如 use case）。视觉上两者完全一致——高亮的描边本身已表达
 * 「已选中」；多选胶囊的区别只是点击时不会取消其他胶囊，这是父组件的职责。
 *
 * @param icon 选项前面的图标
 * @param label 选项文案
 * @param selected 是否选中
 * @param onSelect 点击时的回调
 * @param mode 单选 radio / 多选 checkbox（仅影响无障碍语义）
 */
export function IconOptionCard({
  icon,
  label,
  selected,
  onSelect,
  mode = "radio",
}: {
  icon: ReactNode;
  label: string;
  selected: boolean;
  onSelect: () => void;
  mode?: "radio" | "checkbox";
}) {
  return (
    <Button
      type="button"
      variant="outline"
      size="lg"
      role={mode}
      aria-checked={selected}
      onClick={onSelect}
      className={cn(selected && "border-primary/30 bg-primary/5")}
    >
      <span
        aria-hidden
        className={cn(
          "flex shrink-0 items-center text-muted-foreground [&_svg]:size-4",
          selected && "text-primary",
        )}
      >
        {icon}
      </span>
      <span>{label}</span>
      {selected ? <Check aria-hidden className="size-4 shrink-0" /> : null}
    </Button>
  );
}

/**
 * 「其他」变体——还是同一个胶囊，但选中后它的文案位置变成输入框：输入框无边框、
 * 沿用胶囊本身的字体，所以这行和旁边的选项一样重，而不是突然跳成一条通栏输入域。
 * 打开时自动聚焦；按 Enter 触发父组件的 `onConfirm`。
 *
 * @param icon 选项前面的图标
 * @param label 未选中时显示的文案
 * @param selected 是否选中
 * @param onSelect 点击选中的回调（仅在未选中时触发）
 * @param otherValue 输入框当前内容
 * @param onOtherChange 输入内容变化回调
 * @param onConfirm 按 Enter 确认时的回调
 * @param placeholder 输入框占位文案（同时用作无障碍标签）
 * @param mode 单选 radio / 多选 checkbox（仅影响无障碍语义）
 */
export function IconOtherOptionCard({
  icon,
  label,
  selected,
  onSelect,
  otherValue,
  onOtherChange,
  onConfirm,
  placeholder,
  mode = "radio",
}: {
  icon: ReactNode;
  label: string;
  selected: boolean;
  onSelect: () => void;
  otherValue: string;
  onOtherChange: (value: string) => void;
  onConfirm: () => void;
  placeholder: string;
  mode?: "radio" | "checkbox";
}) {
  return (
    <Button
      type="button"
      variant="outline"
      size="lg"
      role={mode}
      aria-checked={selected}
      onClick={() => {
        if (!selected) onSelect();
      }}
      className={cn(selected && "border-primary/30 bg-primary/5")}
    >
      <span
        aria-hidden
        className={cn(
          "flex shrink-0 items-center text-muted-foreground [&_svg]:size-4",
          selected && "text-primary",
        )}
      >
        {icon}
      </span>
      {selected ? (
        <input
          autoFocus
          type="text"
          value={otherValue}
          onChange={(e) => onOtherChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && otherValue.trim()) {
              e.preventDefault();
              onConfirm();
            }
          }}
          placeholder={placeholder}
          maxLength={OTHER_INPUT_MAX_LENGTH}
          aria-label={placeholder}
          className="w-32 min-w-0 border-0 bg-transparent p-0 text-inherit placeholder:text-muted-foreground focus:outline-none"
        />
      ) : (
        <span>{label}</span>
      )}
    </Button>
  );
}

/** 「其他」输入框的最大长度，随组件一并导出给需要保持一致上限的调用方。 */
export { OTHER_INPUT_MAX_LENGTH };
