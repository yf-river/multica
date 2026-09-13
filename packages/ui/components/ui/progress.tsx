"use client"
// 本文件是进度条（Progress）组件族（基于 Base UI 的 Progress）：
// 展示任务完成进度，可带标题（Label）与数值（Value），进度条 + 可选子内容。

import { Progress as ProgressPrimitive } from "@base-ui/react/progress"

import { cn } from "@multica/ui/lib/utils"

/**
 * Progress：进度条根组件。children 会渲染在进度条上方（如标签行），
 * 轨道与指示条由内部自动拼装。
 */
function Progress({
  className,
  children,
  value,
  ...props
}: ProgressPrimitive.Root.Props) {
  return (
    <ProgressPrimitive.Root
      value={value}
      data-slot="progress"
      className={cn("flex flex-wrap gap-3", className)}
      {...props}
    >
      {children}
      <ProgressTrack>
        <ProgressIndicator />
      </ProgressTrack>
    </ProgressPrimitive.Root>
  )
}

/** ProgressTrack：进度条底槽（灰色背景轨道） */
function ProgressTrack({ className, ...props }: ProgressPrimitive.Track.Props) {
  return (
    <ProgressPrimitive.Track
      className={cn(
        "relative flex h-1 w-full items-center overflow-x-hidden rounded-full bg-muted",
        className
      )}
      data-slot="progress-track"
      {...props}
    />
  )
}

/** ProgressIndicator：填充进度的彩色指示条，宽度由 Base UI 按 value 计算 */
function ProgressIndicator({
  className,
  ...props
}: ProgressPrimitive.Indicator.Props) {
  return (
    <ProgressPrimitive.Indicator
      data-slot="progress-indicator"
      className={cn("h-full bg-primary transition-all", className)}
      {...props}
    />
  )
}

/** ProgressLabel：进度条标题（如「正在上传附件」） */
function ProgressLabel({ className, ...props }: ProgressPrimitive.Label.Props) {
  return (
    <ProgressPrimitive.Label
      className={cn("text-body font-medium", className)}
      data-slot="progress-label"
      {...props}
    />
  )
}

/** ProgressValue：当前进度数值文本（右对齐，等宽数字） */
function ProgressValue({ className, ...props }: ProgressPrimitive.Value.Props) {
  return (
    <ProgressPrimitive.Value
      className={cn(
        "ml-auto text-body text-muted-foreground tabular-nums",
        className
      )}
      data-slot="progress-value"
      {...props}
    />
  )
}

export {
  Progress,
  ProgressTrack,
  ProgressIndicator,
  ProgressLabel,
  ProgressValue,
}
