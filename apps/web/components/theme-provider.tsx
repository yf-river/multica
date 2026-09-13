"use client"

// 主题能力适配层：转发共享 ThemeProvider，并压制 React 19 对 next-themes 的误报告警。
export { ThemeProvider } from "@multica/ui/components/common/theme-provider"

// 屏蔽 React 19 对 next-themes 内联 <script> 的误报告警。
// 这段脚本工作正常，React 19 只是对组件里出现的任何 <script> 都会告警。
// 参见：https://github.com/pacocoursey/next-themes/issues/337
if (typeof window !== "undefined" && process.env.NODE_ENV === "development") {
  const orig = console.error;
  console.error = (...args: unknown[]) => {
    if (typeof args[0] === "string" && args[0].includes("Encountered a script tag"))
      return;
    orig.apply(console, args);
  };
}
