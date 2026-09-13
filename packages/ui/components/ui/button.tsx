"use client"
// 本文件是全局统一的按钮组件（基于 Base UI 的 ButtonPrimitive）：
// 提供 default/outline/brand/brandSubtle/secondary/ghost/destructive/link 等视觉变体
// 与 default/xs/sm/lg/icon 系列尺寸，产品里所有可点击按钮都走这里。

import { Button as ButtonPrimitive } from "@base-ui/react/button"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@multica/ui/lib/utils"

/** 按钮的样式变体与尺寸定义（class-variance-authority），同时可导出给需要复用同一套样式的调用方 */
const buttonVariants = cva(
  "group/button inline-flex shrink-0 items-center justify-center rounded-lg border border-transparent bg-clip-padding text-body font-medium whitespace-nowrap transition-[color,background-color,border-color,box-shadow,transform] outline-none select-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 active:not-aria-[haspopup]:translate-y-px disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-3 aria-invalid:ring-destructive/20 dark:aria-invalid:border-destructive/50 dark:aria-invalid:ring-destructive/40 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground [a]:hover:bg-primary/80",
        outline:
          "border-border bg-background hover:bg-muted hover:text-foreground aria-expanded:bg-muted aria-expanded:text-foreground dark:border-input dark:bg-input/30 dark:hover:bg-input/50",
        // 品牌色实心态，用于「当前处于开启状态」的控件（被激活的筛选、被选中的
        // 开关）。这里刻意自洽，不能靠 outline 之上再传 brand 的 className——
        // outline 自带 dark:bg-input/30、hover:bg-muted、aria-expanded:bg-muted，
        // 这些会在级联中胜出（dark: 的靠特异性胜出，因为 dark 编译成
        // `&:is(.dark *)`），把色块重新涂成中性色。当年暗色模式下品牌色莫名
        // 消失就是这个问题——见 MUL-4884。
        //
        // brand 自己不需要 dark: 变体：--brand token 已经按主题翻转，
        // 所以同一组规则在明暗两种主题下都正确。
        // aria-expanded 特意钉在与 hover 相同的值上：展开 popover 时视觉上
        // 表现为「悬停加深」而不是「换了个颜色」。
        brand:
          "border-brand bg-brand text-brand-foreground hover:bg-brand/90 hover:text-brand-foreground active:bg-brand/85 aria-expanded:bg-brand/90 aria-expanded:text-brand-foreground",
        // 品牌浅色态，用于「这里有活动」——有存在感，但不抢占高饱和的实心态。
        // 明暗主题各自取不同的透明度档：同样的 alpha 在白色和近黑背景上观感
        // 不同，所以暗色模式下会再热一档。
        brandSubtle:
          "border-brand/28 bg-brand/7 text-foreground hover:bg-brand/12 hover:text-foreground active:bg-brand/16 aria-expanded:bg-brand/12 aria-expanded:text-foreground dark:border-brand/45 dark:bg-brand/12 dark:hover:bg-brand/18 dark:active:bg-brand/24 dark:aria-expanded:bg-brand/18",
        secondary:
          "bg-secondary text-secondary-foreground hover:bg-secondary/80 aria-expanded:bg-secondary aria-expanded:text-secondary-foreground",
        ghost:
          "hover:bg-muted hover:text-foreground aria-expanded:bg-muted aria-expanded:text-foreground dark:hover:bg-muted/50",
        destructive:
          "bg-destructive/10 text-destructive hover:bg-destructive/20 focus-visible:border-destructive/40 focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:hover:bg-destructive/30 dark:focus-visible:ring-destructive/40",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default:
          "h-8 gap-1.5 px-2.5 has-data-[icon=inline-end]:pr-2 has-data-[icon=inline-start]:pl-2",
        xs: "h-6 gap-1 rounded-[min(var(--radius-md),10px)] px-2 text-caption in-data-[slot=button-group]:rounded-lg has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*='size-'])]:size-3",
        sm: "h-7 gap-1 rounded-[min(var(--radius-md),12px)] px-2.5 text-label in-data-[slot=button-group]:rounded-lg has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 [&_svg:not([class*='size-'])]:size-3.5",
        lg: "h-9 gap-1.5 px-2.5 has-data-[icon=inline-end]:pr-3 has-data-[icon=inline-start]:pl-3",
        icon: "size-8",
        "icon-xs":
          "size-6 rounded-[min(var(--radius-md),10px)] in-data-[slot=button-group]:rounded-lg [&_svg:not([class*='size-'])]:size-3",
        "icon-sm":
          "size-7 rounded-[min(var(--radius-md),12px)] in-data-[slot=button-group]:rounded-lg",
        "icon-lg": "size-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

/**
 * Button：项目统一按钮。基于 Base UI 的 Button 原语，套用 buttonVariants 里
 * 的样式变体，并把全部原生属性透传给底层按钮。
 *
 * @param props 含 Base UI Button 原生属性 + variant/size（className 参与变体合并）
 * @returns 按钮元素
 */
function Button({
  className,
  variant = "default",
  size = "default",
  ...props
}: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) {
  return (
    <ButtonPrimitive
      data-slot="button"
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { Button, buttonVariants }
