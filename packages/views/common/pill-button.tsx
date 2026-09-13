// 药丸形按钮：PillButton 与可清除的 ClearablePillButton 共用同一套药丸外观。
"use client";

import { X as XIcon } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";

// 共享的药丸外观。`PillButton` 把外观放在按钮自身；可清除变体则放在承载触发器
// 和清除按钮的外壳上，让两者读起来是同一个对象。
//
// 硬性宽度上限：药丸承载用户生成的文本（项目标题、负责人名、标签名），而创建
// 工具栏是 flex-wrap 行——不加限制的药丸会一直拉伸到独占整行，把兄弟元素都挤
// 到下一行。max-w-56（14rem）与聊天输入框的项目药丸一致。`min-w-0` +
// `overflow-hidden` 让内部的 `truncate` span 真正能收缩（flex 子项只有在 overflow
// 隐藏后才会放弃 `min-width: auto`）；前导图标是 `shrink-0`，所以文本先让位。
const PILL_CHROME =
  "inline-flex items-center rounded-full border text-caption min-w-0 max-w-56 overflow-hidden transition-colors";

/**
 * 药丸形按钮，用于可点击的筛选/上下文标签。
 * @param children 按钮内容。
 * @param className 附加样式。
 * @param props 其余原生按钮属性。
 */
export function PillButton({
  children,
  className,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      type="button"
      className={cn(
        PILL_CHROME,
        "gap-1.5 px-2.5 py-1",
        "hover:bg-accent/60 cursor-pointer",
        "data-popup-open:bg-accent data-popup-open:text-accent-foreground",
        "disabled:cursor-not-allowed disabled:hover:bg-transparent",
        className,
      )}
      {...props}
    >
      {children}
    </button>
  );
}

/**
 * 带尾部快速清除 "×" 的药丸触发器，用于值可一键丢弃的药丸（项目上下文、父级
 * 链接等）。外观与 `PillButton` 一致，所以同一行可以混用两种。
 *
 * 两个兄弟 `<button>` 放在一个普通外壳里——绝不出现按钮嵌套按钮，也绝不使用
 * 被替换掉的那种覆盖式 "×"：那个版本是触发器的绝对定位兄弟，每个调用方都得
 * 为它预留右侧内边距，而五个里有三个没预留，结果把 "×" 画到了值上面
 * （MUL-5666）。
 *
 * 无论是否设置 `onClear`，根元素都保持 `<span>`。清除动作的存在与否对应着
 * "字段是否有值"，它会在关闭弹层的同一次提交里翻转——那时若切换根元素类型，
 * 会在关闭中途把弹层自己的触发器重挂载。
 *
 * 转发来的 props（包括 Base UI `render` 合并进来的 `ref`、`onClick` 和
 * `data-popup-open`）落在内部触发器按钮上，这样弹层锚定到触发器，而 "×"
 * 保持在它的命中区域之外。
 * @param children 药丸内容。
 * @param className 附加样式。
 * @param onClear 清除回调；省略则渲染普通药丸（"×" 只在有值可丢弃时出现）。
 * @param clearLabel "×" 的可访问名称，设置 `onClear` 时必填（按钮无文本）。
 * @param disabled 是否禁用。
 * @param props 其余原生按钮属性。
 */
export function ClearablePillButton({
  children,
  className,
  onClear,
  clearLabel,
  disabled,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  /** 省略则渲染普通药丸——"×" 只在有值可丢弃时出现。 */
  onClear?: (() => void) | undefined;
  /** "×" 的可访问名称（如"清除项目"）。设置 `onClear` 时必填：按钮没有文本。 */
  clearLabel?: string;
}) {
  // 触发器禁用时也禁用清除：两者都是对同一字段的写入，一个只读却仍能被清空的
  // 药丸并不是只读的。
  const clearable = !!onClear && !disabled;
  return (
    <span
      className={cn(
        PILL_CHROME,
        !disabled && "hover:bg-accent/60",
        // 打开态高亮在触发器上（Base UI 在那里打属性），但它必须涂满整个药丸。
        "has-[[data-popup-open]]:bg-accent has-[[data-popup-open]]:text-accent-foreground",
        className,
      )}
    >
      <button
        type="button"
        disabled={disabled}
        className={cn(
          "flex min-w-0 items-center gap-1.5 overflow-hidden py-1 pl-2.5 cursor-pointer",
          clearable ? "pr-1" : "pr-2.5",
          "disabled:cursor-not-allowed",
        )}
        {...props}
      >
        {children}
      </button>
      {clearable && (
        <button
          type="button"
          aria-label={clearLabel}
          title={clearLabel}
          onClick={(e) => {
            e.stopPropagation();
            onClear?.();
          }}
          className="flex shrink-0 items-center py-1 pl-0.5 pr-2 text-muted-foreground hover:text-foreground cursor-pointer"
        >
          <XIcon className="size-3" />
        </button>
      )}
    </span>
  );
}
