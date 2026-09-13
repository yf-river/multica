"use client";

import { useCallback } from "react";
import { resolveClickIntent, type LinkClickIntent } from "./click-intent";
import { useNavigation } from "./context";

/**
 * navigation 模块：列表行的整行点击导航。
 *
 * 在列表组件顶部调用一次；它返回一个工厂，为每行的 href 构造要展开到
 * `<ListGridRow>`（一个普通 `<div>`，绝不是 `<a>`）上的 props。逐行调用工厂
 * 让它避开了在 `.map()` 里调用 `useNavigation` 的 hooks 规则陷阱。
 *
 * 整行点击即导航——行本身是点击目标，所以名称单元格保持纯文本（不嵌套
 * `<a>`，那会是冗余的第二个入口）。可交互单元格（复选框、kebab、内联编辑器）
 * 必须阻止冒泡，这样与它们的交互永远不会到达这些处理器——在这些元素上展开
 * `rowLinkInteractiveProps`，而不要只接 `onClick`，因为行还处理 `auxclick`。
 *
 * 经 `resolveClickIntent` 对齐 AppLink 的修饰键语义：普通左键点击 push；
 * cmd/ctrl 或中键点击打开浏览器标签页。由于行是 `<div>` 而不是 `<a>`，这里
 * 基于可分享 URL 打开浏览器标签。没有这个兜底，修饰键或中键点击会悄悄变成
 * 原地跳转。
 *
 * 调用方在行自己的 className 上加 `cursor-pointer`（不放进返回的 props，免得
 * 与行已有的 className 冲突）。
 */

/**
 * 供嵌套在 `useRowLink` 行内的可交互元素使用的 props。
 *
 * 同时阻止 `click` 与 `auxclick` 冒泡到行。只拦 `click` 是个陷阱：中键点击
 * 以 `auxclick` 冒泡，行处理器调用 `preventDefault()`，对嵌套的 `<a>` 而言
 * 这会取消锚元素原生的中键打开——于是行在后台标签打开，而不是链接自己的
 * 目标。这里不动元素自身的原生行为（不调 `preventDefault`），所以锚元素保持
 * 默认打开。
 */
export const rowLinkInteractiveProps = {
  onClick: (e: React.MouseEvent) => e.stopPropagation(),
  onAuxClick: (e: React.MouseEvent) => e.stopPropagation(),
};

/**
 * 返回按行 href 构造导航 props 的工厂：整行点击 push 或开新标签，悬停时
 * 预热路由。
 */
export function useRowLink() {
  const { push, prefetch, getShareableUrl } = useNavigation();

  return useCallback(
    (href: string) => {
      const open = (intent: LinkClickIntent) => {
        if (intent === "push") {
          push(href);
          return;
        }
        window.open(getShareableUrl(href), "_blank", "noopener,noreferrer");
      };
      return {
        onClick: (e: React.MouseEvent) => {
          // 子控件已处理过这次点击（控件会调 stopPropagation，不会走到这里；
          // defaultPrevented 兜住那些改调 preventDefault 的）。
          if (e.defaultPrevented || e.button !== 0) return;
          open(resolveClickIntent(e));
        },
        onAuxClick: (e: React.MouseEvent) => {
          if (e.defaultPrevented || e.button !== 1) return; // 中键点击
          e.preventDefault();
          open("background-tab");
        },
        onMouseEnter: () => prefetch?.(href),
      };
    },
    [push, prefetch, getShareableUrl],
  );
}
