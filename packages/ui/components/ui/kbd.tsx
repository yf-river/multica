import { cn } from "@multica/ui/lib/utils"

// 本文件是键盘按键（Kbd）组件：渲染 ⌘/Ctrl/K 这类快捷键按键的灰色小圆角块。

/**
 * Kbd：单个按键视觉块。`pointer-events-none` 让它不拦截点击。
 */
function Kbd({ className, ...props }: React.ComponentProps<"kbd">) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        // 不用给 tooltip 内的按键单独配色：上游 shadcn 会在浅色模式下把
        // tooltip 反成深色，但我们的 TooltipContent 保持 popover 的浅色面，
        // 所以常规的弱化按键色在这里依然可读。
        "pointer-events-none inline-flex h-5 w-fit min-w-5 items-center justify-center gap-1 rounded-sm bg-muted px-1 font-sans text-caption font-medium text-muted-foreground select-none [&_svg:not([class*='size-'])]:size-3",
        className
      )}
      {...props}
    />
  )
}

/** KbdGroup：把多个按键（如 ⌘ + K）并排组合 */
function KbdGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <kbd
      data-slot="kbd-group"
      className={cn("inline-flex items-center gap-1", className)}
      {...props}
    />
  )
}

export { Kbd, KbdGroup }
