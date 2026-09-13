"use client";

/**
 * suggestion 弹层的通用渲染器：把 Tiptap Suggestion 插件的回调接到一个
 * React 组件上，用 Floating UI 把它定位在光标（虚拟元素）附近，并处理好
 * 生命周期（打开/更新/键盘/外部点击关闭）。
 *
 * mention 与斜杠命令两个弹层共用同一套定位与关闭逻辑，只差列表组件本身。
 */

import type { ComponentType } from "react";
import { autoUpdate, computePosition, flip, offset, shift, size } from "@floating-ui/dom";
import { ReactRenderer } from "@tiptap/react";
import { exitSuggestion, type SuggestionKeyDownProps, type SuggestionProps } from "@tiptap/suggestion";
import type { PluginKey } from "@tiptap/pm/state";

/** createSuggestionPopupRender 的配置：插件的 key、要渲染的组件，以及把
 *  SuggestionProps 翻译成组件 props 的函数。 */
interface SuggestionPopupRenderOptions<
  TItem,
  TSelected = TItem,
  TRef = unknown,
  TComponentProps extends object = object,
> {
  pluginKey: PluginKey;
  component: ComponentType<TComponentProps>;
  getProps: (props: SuggestionProps<TItem, TSelected>) => TComponentProps;
  onKeyDown?: (
    ref: TRef | null | undefined,
    props: SuggestionKeyDownProps,
  ) => boolean;
}

/**
 * 生成一份可传给 Suggestion 插件的渲染回调对象（onStart/onUpdate/onKeyDown/
 * onExit）。列表内容的 key 处理由 getProps 接入的组件自行负责。
 */
export function createSuggestionPopupRender<
  TItem,
  TSelected = TItem,
  TRef = unknown,
  TComponentProps extends object = object,
>({
  pluginKey,
  component,
  getProps,
  onKeyDown,
}: SuggestionPopupRenderOptions<TItem, TSelected, TRef, TComponentProps>) {
  return () => {
    let renderer: ReactRenderer<TRef> | null = null;
    let popup: HTMLDivElement | null = null;
    let removeOutsideListeners: (() => void) | null = null;
    let removeAutoUpdate: (() => void) | null = null;

    const cleanup = () => {
      removeOutsideListeners?.();
      removeOutsideListeners = null;
      removeAutoUpdate?.();
      removeAutoUpdate = null;
      renderer?.destroy();
      renderer = null;
      popup?.remove();
      popup = null;
    };

    const requestExit = (props: SuggestionProps<TItem, TSelected>) => {
      exitSuggestion(props.editor.view, pluginKey);
    };

    const isInsideSuggestionSurface = (
      target: EventTarget | null,
      props: SuggestionProps<TItem, TSelected>,
    ) => {
      if (!(target instanceof Node)) return false;
      return props.editor.view.dom.contains(target) || !!popup?.contains(target);
    };

    const installOutsideListeners = (props: SuggestionProps<TItem, TSelected>) => {
      removeOutsideListeners?.();
      const doc = props.editor.view.dom.ownerDocument;
      const win = doc.defaultView ?? window;

      const onPointerDown = (event: PointerEvent) => {
        if (isInsideSuggestionSurface(event.target, props)) return;
        requestExit(props);
      };

      const onFocusIn = (event: FocusEvent) => {
        if (isInsideSuggestionSurface(event.target, props)) return;
        requestExit(props);
      };

      const onWindowBlur = () => {
        requestExit(props);
      };

      doc.addEventListener("pointerdown", onPointerDown, true);
      doc.addEventListener("focusin", onFocusIn, true);
      win.addEventListener("blur", onWindowBlur);

      removeOutsideListeners = () => {
        doc.removeEventListener("pointerdown", onPointerDown, true);
        doc.removeEventListener("focusin", onFocusIn, true);
        win.removeEventListener("blur", onWindowBlur);
      };
    };

    const updatePosition = (
      el: HTMLDivElement,
      clientRect: (() => DOMRect | null) | null | undefined,
    ) => {
      if (!clientRect) return;
      const virtualEl = {
        getBoundingClientRect: () => clientRect() ?? new DOMRect(),
      };
      computePosition(virtualEl, el, {
        // 默认向上打开。主要宿主是底部锚定的 composer（聊天输入框、issue
        // 评论/回复），其空间充足侧在光标上方；优先那边，`size` 会钳到较大的
        // 预算而不产生可见压缩，`flip` 只在光标贴近视口顶部这种罕见情况下
        // 才把它翻下去。若反过来默认 `bottom-start`，只要下方还剩一丁点
        // 空间弹层就赖在下面——哪怕上方明明宽裕得多——观感就是「多半向下
        // 开然后被挤扁」。正文型编辑器（issue/project 描述、agent 指令）也
        // 挂这个弹层，那里光标上下通常都有空间，所以无论默认取哪边 `flip`
        // 都能保证它在屏幕内。
        placement: "top-start",
        strategy: "fixed",
        middleware: [
          offset(6),
          flip({ padding: 8 }),
          shift({ padding: 8 }),
          size({
            padding: 8,
            apply({ availableHeight }) {
              // 把「视口可用高度」作为 CSS 变量发出去，让列表组件（真正的
              // 滚动容器）自己把 max-height 钳到
              // `min(designMax, availableHeight)`。在这个包裹层上写 maxHeight
              // 是无效的——包裹层不裁剪、内部列表在滚——那样固定高度的列表
              // 会溢出视口边缘。这里不给下限：空间紧张时，一个短些的可滚动
              // 列表也胜过被裁出屏幕的列表。
              el.style.setProperty(
                "--suggestion-available-height",
                // 防负预算（光标恰好滚出边界）：负的 max-height 是非法 CSS，
                // 会让钳制整个失效、列表直接溢出。
                `${Math.max(0, Math.round(availableHeight))}px`,
              );
            },
          }),
        ],
      }).then(({ x, y, placement }) => {
        if (popup !== el) return;
        el.style.left = `${x}px`;
        el.style.top = `${y}px`;
        el.dataset.side = placement.startsWith("top") ? "top" : "bottom";
      });
    };

    const trackPosition = (
      el: HTMLDivElement,
      clientRect: (() => DOMRect | null) | null | undefined,
    ) => {
      removeAutoUpdate?.();
      removeAutoUpdate = null;
      if (!clientRect) return;
      const virtualEl = {
        getBoundingClientRect: () => clientRect() ?? new DOMRect(),
      };
      removeAutoUpdate = autoUpdate(virtualEl, el, () => updatePosition(el, clientRect), {
        ancestorResize: true,
        ancestorScroll: true,
        elementResize: true,
        layoutShift: true,
      });
    };

    return {
      onStart: (props: SuggestionProps<TItem, TSelected>) => {
        renderer = new ReactRenderer(component, {
          props: getProps(props),
          editor: props.editor,
        });

        const doc = props.editor.view.dom.ownerDocument;
        popup = doc.createElement("div");
        popup.style.position = "fixed";
        popup.style.zIndex = "50";
        popup.appendChild(renderer.element);
        doc.body.appendChild(popup);

        installOutsideListeners(props);
        trackPosition(popup, props.clientRect);
        updatePosition(popup, props.clientRect);
      },

      onUpdate: (props: SuggestionProps<TItem, TSelected>) => {
        renderer?.updateProps(getProps(props));
        if (popup) {
          trackPosition(popup, props.clientRect);
          updatePosition(popup, props.clientRect);
        }
      },

      onKeyDown: (props: SuggestionKeyDownProps) => {
        // `Esc` 是 suggestion 插件也会匹配的旧别名。
        if (props.event.key === "Escape" || props.event.key === "Esc") {
          // 弹层开着时按 Escape 必须只关弹层。返回 true 会让 ProseMirror 调
          // preventDefault()，但 ProseMirror 从不阻止冒泡，而 Base UI 的关闭
          // 层在 `document` 上以冒泡阶段监听 Escape 且不看 defaultPrevented。
          // 不 stopPropagation 的话，这次关闭弹层的按键会连带关闭宿主 Dialog、
          // 丢掉草稿（MUL-5429）。本 handler 是唯一知道「弹层开着」的层，
          // 在这里拦住一次性修好所有宿主（建 issue 弹窗、聊天输入、评论
          // composer）。
          props.event.preventDefault();
          props.event.stopPropagation();
          cleanup();
          return true;
        }
        return onKeyDown?.(renderer?.ref, props) ?? false;
      },

      onExit: () => {
        cleanup();
      },
    };
  };
}
