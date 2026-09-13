// 延迟弹层包装器：直到用户首次交互触发器时才挂载真正的 Popover/Menu 组件，避免密集列表里大量从未打开的弹层白白占用挂载开销。
"use client";

import { cloneElement, useState, type ReactElement, type ReactNode } from "react";

/**
 * 把弹层组件（Popover/Menu 根 + 触发器机制）的挂载推迟到用户首次交互触发器时。
 *
 * 密集界面（看板卡片、列表行）每个条目会挂好几个选择器，每个都带一棵 Base UI
 * 根/触发器树，还各自订阅查询——尽管几乎没人打开过。先渲染一个朴素的外观
 * 触发器、在交互时再换成真选择器，能把未触碰条目的每项挂载开销降到零
 * （MUL-4474 的后续：标签切换时重挂载主要就是卡在这些上，主线程冻结数秒）。
 *
 * 升级触发——刻意只用"结束一次手势"的事件：
 * - `click` 一步完成挂载真组件并打开。点击时打开与 Base UI 自己的触发器时机
 *   完全一致，且因为没有东西在手势中途替换元素（无 pointerenter/pointerdown
 *   预热），手势的每个事件都落在仍挂载的节点上——对真实指针和合成序列
 *   （测试、辅助技术）都是如此。进行中的点击被阻止并停止传播，外层链接无法
 *   执行原生导航，刚挂载弹层的"外部按下即关闭"也不会把这次点击当作外部点击
 *   而在同一瞬间关闭弹层。
 * - `Enter`/`Space` 对键盘做同样的事。
 *
 * 仅用于非受控用法：传 `open`/`onOpenChange`/`defaultOpen` 的调用方从一开始
 * 就需要真组件。
 * @param trigger 触发器内容，与宿主传给其弹层触发器的一致。
 * @param triggerRender 自定义触发器元素，对应宿主的 `triggerRender` prop。
 * @param triggerClassName 宿主默认触发器元素的类名，必须与真实（非延迟）
 *   触发器渲染的类字节级一致，才能让替换无感。
 * @param ariaHasPopup 真实触发器的 ARIA popup 类型。
 * @param children 升级后渲染真实组件的函数。
 */
export function DeferredPopup({
  trigger,
  triggerRender,
  triggerClassName,
  ariaHasPopup = "dialog",
  children,
}: {
  /** 触发器内容，与宿主传给其弹层触发器的一致。 */
  trigger?: ReactNode;
  /** 自定义触发器元素，对应宿主的 `triggerRender` prop。 */
  triggerRender?: ReactElement<Record<string, unknown>>;
  /**
   * 宿主默认触发器元素的类名，必须与真实（非延迟）触发器渲染的类字节级一致，
   * 才能让替换无感。
   */
  triggerClassName?: string;
  /** 真实触发器的 ARIA popup 类型（popover 用 "dialog"，下拉菜单用 "menu"），
   *  让冷触发器对辅助技术读起来一致。 */
  ariaHasPopup?: "dialog" | "menu";
  /** 升级后渲染真实组件的函数。 */
  children: (open: boolean, onOpenChange: (v: boolean) => void) => ReactNode;
}) {
  const [mounted, setMounted] = useState(false);
  const [open, setOpen] = useState(false);

  if (mounted) {
    return <>{children(open, setOpen)}</>;
  }

  const mountOpen = () => {
    setMounted(true);
    setOpen(true);
  };
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      e.stopPropagation();
      mountOpen();
    }
  };
  const handlers = {
    onClick: (e: React.MouseEvent) => {
      e.preventDefault();
      e.stopPropagation();
      mountOpen();
    },
    onKeyDown: handleKeyDown,
    "aria-haspopup": ariaHasPopup,
  };

  if (triggerRender) {
    // 镜像 Base UI 的 render-prop 语义：render 元素自带的 children 优先于
    // 组件级的触发器内容。
    if (triggerRender.props.children != null) {
      return cloneElement(triggerRender, handlers);
    }
    return cloneElement(triggerRender, handlers, trigger);
  }

  return (
    <button type="button" className={triggerClassName} {...handlers}>
      {trigger}
    </button>
  );
}
