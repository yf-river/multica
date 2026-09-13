"use client"
// 本文件是「复合输入框」（InputGroup）组件族：把输入框和它周围的前缀/后缀
// 文字、图标、按钮、下拉选择、时间输入等合成一个整体带边框的控件，
// 常用来做搜索框、带单位的输入、成组操作按钮等。

import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@multica/ui/lib/utils"
import { Button } from "@multica/ui/components/ui/button"
import { Input } from "@multica/ui/components/ui/input"
import { SelectTrigger } from "@multica/ui/components/ui/select"
import { Textarea } from "@multica/ui/components/ui/textarea"
import { TimeInput } from "@multica/ui/components/ui/time-input"

/**
 * InputGroup：复合输入框的容器。整组共享一个边框，组内控件不带自己的边。
 * 校验错误时整组边框变危险色；聚焦时整组亮聚焦环。
 */
function InputGroup({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="input-group"
      role="group"
      className={cn(
        "group/input-group relative flex h-8 w-full min-w-0 items-center rounded-lg border border-input transition-colors outline-none in-data-[slot=combobox-content]:focus-within:border-inherit in-data-[slot=combobox-content]:focus-within:ring-0 has-disabled:bg-input/50 has-disabled:opacity-50 has-[[data-slot=input-group-control]:focus-visible]:border-ring has-[[data-slot=input-group-control]:focus-visible]:ring-3 has-[[data-slot=input-group-control]:focus-visible]:ring-ring/50 has-[[data-slot][aria-invalid=true]]:border-destructive has-[[data-slot][aria-invalid=true]]:ring-3 has-[[data-slot][aria-invalid=true]]:ring-destructive/20 has-[>[data-align=block-end]]:h-auto has-[>[data-align=block-end]]:flex-col has-[>[data-align=block-start]]:h-auto has-[>[data-align=block-start]]:flex-col has-[>textarea]:h-auto dark:bg-input/30 dark:has-disabled:bg-input/80 dark:has-[[data-slot][aria-invalid=true]]:ring-destructive/40 has-[>[data-align=block-end]]:[&>input]:pt-3 has-[>[data-align=block-start]]:[&>input]:pb-3 has-[>[data-align=inline-end]]:[&>input]:pr-1.5 has-[>[data-align=inline-start]]:[&>input]:pl-1.5",
        className
      )}
      {...props}
    />
  )
}

/** 附加件（addon）的位置变体：inline-* 左右两侧 / block-* 上下两行 */
const inputGroupAddonVariants = cva(
  "flex h-auto cursor-text items-center justify-center gap-2 py-1.5 text-body font-medium text-muted-foreground select-none group-data-[disabled=true]/input-group:opacity-50 [&>kbd]:rounded-[calc(var(--radius)-5px)] [&>svg:not([class*='size-'])]:size-4",
  {
    variants: {
      align: {
        "inline-start":
          "order-first pl-2 has-[>button]:ml-[-0.3rem] has-[>kbd]:ml-[-0.15rem]",
        "inline-end":
          "order-last pr-2 has-[>button]:mr-[-0.3rem] has-[>kbd]:mr-[-0.15rem]",
        "block-start":
          "order-first w-full justify-start px-2.5 pt-2 group-has-[>input]/input-group:pt-2 [.border-b]:pb-2",
        "block-end":
          "order-last w-full justify-start px-2.5 pb-2 group-has-[>input]/input-group:pb-2 [.border-t]:pt-2",
      },
    },
    defaultVariants: {
      align: "inline-start",
    },
  }
)
/**
 * InputGroupAddon：贴在输入框左/右/上/下的附加区（前缀图标、单位、提示等）。
 * 点击附加区等价于点输入框：会自动把焦点还给组内的 input（点其中的按钮除外）。
 */
function InputGroupAddon({
  className,
  align = "inline-start",
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof inputGroupAddonVariants>) {
  return (
    <div
      role="group"
      data-slot="input-group-addon"
      data-align={align}
      className={cn(inputGroupAddonVariants({ align }), className)}
      onMouseDown={(e) => {
        if ((e.target as HTMLElement).closest("button")) {
          return
        }
        // 附加件算输入框的一部分：点它不能让输入框失焦——否则 blur 先发生，
        // 等下面的 click 再把焦点移回来时，输入框的 blur 处理器（提交、关闭等）
        // 早就已经跑过了。
        e.preventDefault()
      }}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest("button")) {
          return
        }
        e.currentTarget.parentElement?.querySelector("input")?.focus()
      }}
      {...props}
    />
  )
}

/** 组内按钮的尺寸变体，做成比独立按钮更贴合的紧凑尺寸 */
const inputGroupButtonVariants = cva(
  "flex items-center gap-2 text-body shadow-none",
  {
    variants: {
      size: {
        xs: "h-6 gap-1 rounded-[calc(var(--radius)-3px)] px-1.5 [&>svg:not([class*='size-'])]:size-3.5",
        sm: "",
        "icon-xs":
          "size-6 rounded-[calc(var(--radius)-3px)] p-0 has-[>svg]:p-0",
        "icon-sm": "size-8 p-0 has-[>svg]:p-0",
      },
    },
    defaultVariants: {
      size: "xs",
    },
  }
)

/** InputGroupButton：组内的小按钮（如密码可见性切换、清空等） */
function InputGroupButton({
  className,
  type = "button",
  variant = "ghost",
  size = "xs",
  ...props
}: Omit<React.ComponentProps<typeof Button>, "size" | "type"> &
  VariantProps<typeof inputGroupButtonVariants> & {
    type?: "button" | "submit" | "reset"
  }) {
  return (
    <Button
      type={type}
      data-size={size}
      variant={variant}
      className={cn(inputGroupButtonVariants({ size }), className)}
      {...props}
    />
  )
}

/** InputGroupText：组内的一段静态文字/图标（如货币符号） */
function InputGroupText({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "flex items-center gap-2 text-body text-muted-foreground [&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    />
  )
}

/** InputGroupInput：组内真正的文本框——去掉自身边框，融入组的整体框 */
function InputGroupInput({
  className,
  ...props
}: React.ComponentProps<"input">) {
  return (
    <Input
      data-slot="input-group-control"
      className={cn(
        "flex-1 rounded-none border-0 bg-transparent shadow-none ring-0 focus-visible:ring-0 disabled:bg-transparent aria-invalid:ring-0 dark:bg-transparent dark:disabled:bg-transparent",
        className
      )}
      {...props}
    />
  )
}

/**
 * InputGroupSelectTrigger：组内的下拉选择触发器——它复用了组的边框而不是自带。
 *
 * 组的边框靠 `[data-slot=input-group-control]:focus-visible` 点亮；如果触发器
 * 保留自己的 slot，那么下拉选项真正被聚焦时组框反而不亮。所以让触发器顶替
 * control slot：边框、聚焦环、底色都归组所有；它只保留自己内容决定的宽度，
 * 让下拉控件的落点恰好在其最长选项处。
 */
function InputGroupSelectTrigger({
  className,
  ...props
}: React.ComponentProps<typeof SelectTrigger>) {
  return (
    <SelectTrigger
      data-slot="input-group-control"
      className={cn(
        "h-full rounded-none border-0 bg-transparent shadow-none focus-visible:border-transparent focus-visible:ring-0 dark:bg-transparent dark:hover:bg-transparent",
        className
      )}
      {...props}
    />
  )
}

/**
 * InputGroupTimeInput：组内的时间输入控件——同样复用组的边框。
 * control slot 打在「时/分片段」上而不是整体字段上：组靠当前获得焦点的
 * control 来点亮边框，而这里真正被聚焦的是小时或分钟片段，不会是它们的容器。
 */
function InputGroupTimeInput({
  className,
  ...props
}: React.ComponentProps<typeof TimeInput>) {
  return (
    <TimeInput
      segmentSlot="input-group-control"
      className={cn(
        "h-full min-w-0 flex-1 justify-center rounded-none border-0 bg-transparent focus-within:border-transparent focus-within:ring-0 dark:bg-transparent",
        className
      )}
      {...props}
    />
  )
}

/** InputGroupTextarea：组内的多行文本域——去掉自身边框融入组框 */
function InputGroupTextarea({
  className,
  ...props
}: React.ComponentProps<"textarea">) {
  return (
    <Textarea
      data-slot="input-group-control"
      className={cn(
        "flex-1 resize-none rounded-none border-0 bg-transparent py-2 shadow-none ring-0 focus-visible:ring-0 disabled:bg-transparent aria-invalid:ring-0 dark:bg-transparent dark:disabled:bg-transparent",
        className
      )}
      {...props}
    />
  )
}

export {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupText,
  InputGroupInput,
  InputGroupSelectTrigger,
  InputGroupTextarea,
  InputGroupTimeInput,
}
