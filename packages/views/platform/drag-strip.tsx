import type { CSSProperties } from "react";

/**
 * platform 模块：48px 高的透明条，声明 `-webkit-app-region: drag`，让 macOS
 * 用户能抓住窗口顶边拖动——就在原生红绿灯的下方（并为其让出空间）。
 *
 * 把它作为任何全窗口、非仪表盘视图（onboarding、新建工作区、邀请、无权限等）
 * 的第一个 flex 子元素。本条自身没有背景，父级背景透过它铺满，页面读起来是
 * 「真正的全出血」，而顶部 48px 仍可拖动。
 *
 * 跨平台：`-webkit-app-region` 是 Chromium 专属 CSS 扩展；普通浏览器会静默
 * 忽略它，元素退化成普通 48px 顶部留白。这让它可以安全地留在共享的
 * `packages/views/` 里而无需按平台分叉。
 *
 * 必须是 flex 子元素，而不是绝对定位浮层：本代码库经验表明
 * `-webkit-app-region` 的命中测试与 z-index 层级叠加并不可靠
 * （见 CLAUDE.md「Drag region」说明）。
 */
export function DragStrip() {
  return (
    <div
      aria-hidden
      className="h-12 shrink-0"
      style={{ WebkitAppRegion: "drag" } as CSSProperties}
    />
  );
}
