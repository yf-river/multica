"use client"
// 本文件是表单标签（Label）组件：字段名文本，点击它可把焦点交给关联控件，
// 并会在关联控件禁用时自动变灰。

import * as React from "react"

import { cn } from "@multica/ui/lib/utils"

/**
 * Label：字段标签。普通 label 原生行为（for/点击聚焦）都保留；
 * 通过 group-data-[disabled] / peer-disabled 跟随所属字段的禁用态变灰。
 */
function Label({ className, ...props }: React.ComponentProps<"label">) {
  return (
    <label
      data-slot="label"
      className={cn(
        "flex items-center gap-2 text-body leading-none font-medium select-none group-data-[disabled=true]:pointer-events-none group-data-[disabled=true]:opacity-50 peer-disabled:cursor-not-allowed peer-disabled:opacity-50",
        className
      )}
      {...props}
    />
  )
}

export { Label }
