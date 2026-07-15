"use client"

import { Progress as ProgressPrimitive } from "@base-ui/react/progress"

import { cn } from "@multica/ui/lib/utils"

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
      <ProgressPrimitive.Track
        className="relative flex h-1 w-full items-center overflow-x-hidden rounded-full bg-muted"
        data-slot="progress-track"
      >
        <ProgressPrimitive.Indicator
          data-slot="progress-indicator"
          className="h-full bg-primary transition-all"
        />
      </ProgressPrimitive.Track>
    </ProgressPrimitive.Root>
  )
}

export { Progress }
