"use client"
// 本文件是可折叠（Collapsible）组件（基于 Base UI 的 Collapsible）：
// 展开/收起一块内容，常用于「更多详情」等场景。

import { Collapsible as CollapsiblePrimitive } from "@base-ui/react/collapsible"

/** Collapsible：折叠区的根组件，管理展开状态 */
function Collapsible({ ...props }: CollapsiblePrimitive.Root.Props) {
  return <CollapsiblePrimitive.Root data-slot="collapsible" {...props} />
}

/** CollapsibleTrigger：点击后切换展开/收起的触发区（通常是个按钮） */
function CollapsibleTrigger({ ...props }: CollapsiblePrimitive.Trigger.Props) {
  return (
    <CollapsiblePrimitive.Trigger data-slot="collapsible-trigger" {...props} />
  )
}

/** CollapsibleContent：被折叠的面板内容，收起时自动隐藏 */
function CollapsibleContent({ ...props }: CollapsiblePrimitive.Panel.Props) {
  return (
    <CollapsiblePrimitive.Panel data-slot="collapsible-content" {...props} />
  )
}

export { Collapsible, CollapsibleTrigger, CollapsibleContent }
