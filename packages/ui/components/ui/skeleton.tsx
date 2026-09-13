import { cn } from "@multica/ui/lib/utils"

// 本文件是骨架屏占位（Skeleton）组件：内容还没加载出来时显示的灰色脉冲块，
// 告诉用户这里即将出现内容。

/**
 * Skeleton：加载占位块。呼吸闪烁 + 圆角灰底，调用方自行设定宽高/形状。
 */
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      className={cn("animate-pulse rounded-md bg-muted", className)}
      {...props}
    />
  )
}

export { Skeleton }
