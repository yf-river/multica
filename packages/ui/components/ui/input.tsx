import * as React from "react"
import { Input as InputPrimitive } from "@base-ui/react/input"

import { cn } from "@multica/ui/lib/utils"

// 本文件是全局统一的文本输入框（Input）组件：单行文本框的标准样式，
// 也内置了 file 型输入（选文件按钮）的排版。

/**
 * Input：单行文本输入框，产品内统一外观与状态色（错误、禁用、聚焦）。
 * 原生 input 属性全部透传。
 */
function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <InputPrimitive
      type={type}
      data-slot="input"
      className={cn(
        "h-8 w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1 text-title-sm transition-colors outline-none file:inline-flex file:h-6 file:border-0 file:bg-transparent file:text-body file:font-medium file:text-foreground placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 md:text-body dark:bg-input/30 dark:disabled:bg-input/80 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40",
        className
      )}
      {...props}
    />
  )
}

export { Input }
