"use client"
// 本文件是分隔线（Separator）组件：一条水平或垂直的细线，用来划分内容区块。

import { Separator as SeparatorPrimitive } from "@base-ui/react/separator"

import { cn } from "@multica/ui/lib/utils"

/**
 * Separator：分隔线。
 * @param props.orientation 方向：'horizontal' 横线（默认）/ 'vertical' 竖线
 */
function Separator({
  className,
  orientation = "horizontal",
  ...props
}: SeparatorPrimitive.Props) {
  return (
    <SeparatorPrimitive
      data-slot="separator"
      orientation={orientation}
      className={cn(
        "shrink-0 bg-border data-horizontal:h-px data-horizontal:w-full data-vertical:w-px data-vertical:self-stretch",
        className
      )}
      {...props}
    />
  )
}

export { Separator }
