// 应用是否真正处于前台（被用户看着）的 Hook。聊天回复到达时，若应用不在前台就
// 不能被自动标记为已读、未读角标也不能清掉，否则用户离开期间通知被静默吞掉。
// 聊天侧栏的角标与自动已读效果共用本信号，保证两处行为一致。

import { useSyncExternalStore } from "react";

// 监听三件事：页面可见性变化（切标签页/最小化）、窗口获得焦点、窗口失去焦点
// （被别的应用或窗口盖住）。任一事件发生都通知 React 重新读取快照。
function subscribe(onStoreChange: () => void): () => void {
  if (typeof document === "undefined") return () => {};
  document.addEventListener("visibilitychange", onStoreChange);
  window.addEventListener("focus", onStoreChange);
  window.addEventListener("blur", onStoreChange);
  return () => {
    document.removeEventListener("visibilitychange", onStoreChange);
    window.removeEventListener("focus", onStoreChange);
    window.removeEventListener("blur", onStoreChange);
  };
}

// 前台的定义：页面可见（不是后台标签页/最小化窗口）并且窗口持有操作系统焦点
// （没有被别的窗口挡在后面）。两者同时满足才算"用户真的在看着"。
function getSnapshot(): boolean {
  if (typeof document === "undefined") return true;
  return document.visibilityState === "visible" && document.hasFocus();
}

// 服务端渲染时不存在"处于前台"的窗口概念，默认返回 true，让首次客户端绘制与 SSR
// 快照一致；水合后 useSyncExternalStore 会立刻用真实值校正（一旦失去焦点/可见性
// 就翻成 false）。
function getServerSnapshot(): boolean {
  return true;
}

/**
 * 应用窗口当前是否真的在用户面前：文档可见（非后台标签页/最小化）且窗口持有
 * 系统焦点（未被其他窗口遮挡）。
 *
 * 调用方用它回答"用户现在真的在看这个界面吗"。应用不在前台时到达的聊天回复，
 * 不能被自动标记已读，还必须继续点亮未读角标——否则用户离开期间通知就被悄悄
 * 吃掉了（MUL-4485）。聊天侧栏角标和自动已读副作用共享这一信号：后台期间当前
 * 会话既计入角标又保留未读；回到前台时角标清除并触发已读。
 * @returns 是否处于前台。
 */
export function useAppForeground(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
