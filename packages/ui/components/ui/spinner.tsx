import { cn } from "@multica/ui/lib/utils"
import { Loader2Icon } from "lucide-react"

// 本文件是通用加载圈（Spinner）组件：旋转的加载图标，带 status 语义标签。

/**
 * Spinner：旋转加载圈。默认 4 倍字号大小，可用 className 覆盖尺寸/颜色。
 */
function Spinner({ className, ...props }: React.ComponentProps<"svg">) {
  return (
    <Loader2Icon role="status" aria-label="Loading" className={cn("size-4 animate-spin", className)} {...props} />
  )
}

export { Spinner }
