import { ThemeProvider as NextThemesProvider, useTheme } from "next-themes";

export { useTheme };
import { TooltipProvider } from "../ui/tooltip";

// 本文件是应用统一的主题 Provider：包一层 next-themes（按 class 切换明暗主题），
// 再配好全局 TooltipProvider 的延迟，让所有子组件默认共享同一套行为。

/**
 * ThemeProvider：明暗主题 + Tooltip 的全局配置入口。
 *
 * - 主题：以 `class` 属性方式切换，默认跟随系统，可被用户覆盖；切换时禁用过渡
 *   动画，避免切主题时整页闪烁。
 * - Tooltip：统一延迟 500ms 才弹出。
 *
 * 其余 props 透传给 next-themes 的 ThemeProvider。
 *
 * @param props 透传给 next-themes ThemeProvider 的属性（含 children）
 * @returns 包裹了主题与 Tooltip 配置的 Provider 树
 */
export function ThemeProvider({
  children,
  ...props
}: React.ComponentProps<typeof NextThemesProvider>) {
  return (
    <NextThemesProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      disableTransitionOnChange
      {...props}
    >
      <TooltipProvider delay={500}>
        {children}
      </TooltipProvider>
    </NextThemesProvider>
  );
}
