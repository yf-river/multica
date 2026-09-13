"use client"
// 本文件是悬停卡片（HoverCard）组件（基于 Base UI 的 PreviewCard）：
// 鼠标悬停/键盘聚焦在触发器上时，在附近浮出内容预览卡片（如用户资料卡）。

import { PreviewCard as PreviewCardPrimitive } from "@base-ui/react/preview-card"

import { cn } from "@multica/ui/lib/utils"

/** HoverCard：悬停卡片的根组件，管理开/关状态 */
function HoverCard({ ...props }: PreviewCardPrimitive.Root.Props) {
  return <PreviewCardPrimitive.Root data-slot="hover-card" {...props} />
}

/** HoverCardTrigger：触发卡片出现的元素 */
function HoverCardTrigger({ ...props }: PreviewCardPrimitive.Trigger.Props) {
  return (
    <PreviewCardPrimitive.Trigger data-slot="hover-card-trigger" {...props} />
  )
}

/**
 * HoverCardContent：浮出的预览卡片内容（渲染在 body 端传送门里）。
 *
 * 特别注意这里主动拦截了一组点击类事件（click/右键/中键/双击），不让它们
 * 冒泡到 React 组件树外层：Base UI 把弹层传送到 <body>，DOM 上虽然已经脱离
 * 触发器，但 React 的合成事件仍会沿组件树冒泡——不拦截的话，弹层里的点击
 * 会同时触发触发器任何祖先上的监听（比如可点击的列表行、外层 <a>）。
 *
 * 刻意不去拦截 pointerdown / mousedown：Base UI 的「点击外部关闭」监听的是
 * document 上的 pointerdown，并通过「是否点在 React 树内部」来判断要不要关。
 * 如果在弹层内拦掉 pointerdown，关闭逻辑会误以为点击发生在外部，导致要连点
 * 两次才能关掉（与 radix-ui/primitives#2782 相同的问题）。
 */
function HoverCardContent({
  className,
  side = "bottom",
  sideOffset = 4,
  align = "center",
  alignOffset = 4,
  onClick,
  onContextMenu,
  onAuxClick,
  onDoubleClick,
  ...props
}: PreviewCardPrimitive.Popup.Props &
  Pick<
    PreviewCardPrimitive.Positioner.Props,
    "align" | "alignOffset" | "side" | "sideOffset"
  >) {
  // Stop interaction events from bubbling out of the popup. Base UI portals
  // the popup to <body> so the DOM is detached, but React's synthetic event
  // system still bubbles through the React component tree — without this,
  // events on the popup would also fire on any ancestor of the trigger
  // (e.g. a clickable issue list row, a wrapping <a>).
  //
  // We stop the safe set: click / contextmenu / auxclick / dblclick.
  // We deliberately do NOT stop pointerdown / mousedown — Base UI's
  // outside-click dismiss listens to pointerdown on document and uses an
  // "inside React tree" check to decide whether to close. Stopping
  // pointerdown inside the popup would make the dismiss handler wrongly
  // think the click happened outside, requiring two clicks to close
  // (mirrors radix-ui/primitives#2782).
  // 组合 stop 包装器：阻止事件冒泡，同时仍执行调用方传进来的原回调（如果有）
  const stop = <E extends React.SyntheticEvent>(forwarded?: (e: E) => void) =>
    (e: E) => {
      e.stopPropagation()
      forwarded?.(e)
    }
  return (
    <PreviewCardPrimitive.Portal data-slot="hover-card-portal">
      <PreviewCardPrimitive.Positioner
        align={align}
        alignOffset={alignOffset}
        side={side}
        sideOffset={sideOffset}
        className="isolate z-50"
      >
        <PreviewCardPrimitive.Popup
          data-slot="hover-card-content"
          onClick={stop(onClick)}
          onContextMenu={stop(onContextMenu)}
          onAuxClick={stop(onAuxClick)}
          onDoubleClick={stop(onDoubleClick)}
          className={cn(
            "z-50 w-64 origin-(--transform-origin) rounded-lg bg-popover p-2.5 text-body text-popover-foreground shadow-md ring-1 ring-foreground/10 outline-hidden duration-100 data-[side=bottom]:slide-in-from-top-2 data-[side=inline-end]:slide-in-from-left-2 data-[side=inline-start]:slide-in-from-right-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95",
            className
          )}
          {...props}
        />
      </PreviewCardPrimitive.Positioner>
    </PreviewCardPrimitive.Portal>
  )
}

export { HoverCard, HoverCardTrigger, HoverCardContent }
