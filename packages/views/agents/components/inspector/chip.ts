// 本文件导出检查器里几个下拉（运行环境 / 模型 / 可见性 / 并发数）共用的“触发器”样式串。

/**
 * 检查器下拉触发器共用样式。
 *
 * 关键取舍：
 * - `rounded-md`（6px）：圆角够柔和，像按钮而不是标签页。
 * - `hover:bg-accent`：单层悬停背景就承担了“这是按钮”的全部暗示——曾试过在其上
 *   再叠悬停边框，但“边框 + 背景”两层悬停让 chip 轮廓变花哨，却没有多传达任何信息。
 * - `min-w-0`：保证内部 `truncate` 的子元素不会溢出检查器 320px 宽的窄列。
 *
 * 刻意不给默认边框：静止时 chip 应安静地躺在行里；鼠标一进来背景翻转，
 * “可点击”的暗示自然浮现。
 */
export const CHIP_CLASS =
  "group flex min-w-0 cursor-pointer items-center gap-1.5 rounded-md px-1.5 py-0.5 text-caption transition-colors hover:bg-accent";
