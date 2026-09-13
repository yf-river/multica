"use client"
// 本文件是全局 toast 通知容器（sonner 的 Toaster 定制版）：
// 统一定制了成功/信息/警告/错误/加载五种图标与明暗主题下的配色变量。

import { useTheme } from "next-themes"
import { Toaster as Sonner, type ToasterProps } from "sonner"
import { CircleCheckIcon, InfoIcon, TriangleAlertIcon, OctagonXIcon, Loader2Icon } from "lucide-react"

/**
 * Toaster：toast 消息容器，需在应用根部挂载一次。
 * 各状态图标统一换成 lucide 并配上主题色。
 */
const Toaster = ({ ...props }: ToasterProps) => {
  // 用 `resolvedTheme`（解析后的具体值 "light"/"dark"）而不是 `theme`
  // （可能是 "system"）。若把 "system" 直接透传，sonner 会自己去读
  // `prefers-color-scheme`，那可能与 next-themes 挂在 `html` 上的 .dark
  // 类不一致——结果就是在暗色界面上弹出一个浅色 toast。
  const { resolvedTheme = "system" } = useTheme()

  return (
    <Sonner
      theme={resolvedTheme as ToasterProps["theme"]}
      className="toaster group"
      icons={{
        success: (
          <CircleCheckIcon className="size-4 text-success" />
        ),
        info: (
          <InfoIcon className="size-4 text-info" />
        ),
        warning: (
          <TriangleAlertIcon className="size-4 text-warning" />
        ),
        error: (
          <OctagonXIcon className="size-4 text-destructive" />
        ),
        loading: (
          <Loader2Icon className="size-4 animate-spin text-brand" />
        ),
      }}
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border)",
          "--border-radius": "var(--radius)",
        } as React.CSSProperties
      }
      toastOptions={{
        classNames: {
          toast: "cn-toast",
        },
      }}
      {...props}
    />
  )
}

export { Toaster }
