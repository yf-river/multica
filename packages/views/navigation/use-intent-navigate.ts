"use client";

import { useCallback } from "react";
import type { LinkClickIntent } from "./click-intent";
import { useNavigation } from "./context";

/**
 * navigation 模块：AppLink / useRowLink 的命令式孪生版本，给那些通过回调
 * 收到鼠标事件、而不是自己持有锚元素的界面使用（DataTable 行、命令面板
 * 条目）：对导航适配器执行一次已解析的点击意图。
 *
 * 标签页意图基于可分享 URL 使用 `window.open`。JavaScript 无法强制浏览器开
 * 后台标签，所以两种标签意图走同一个浏览器 API。
 */
export function useIntentNavigate() {
  const navigation = useNavigation();

  return useCallback(
    (href: string, intent: LinkClickIntent) => {
      const { push, getShareableUrl } = navigation;
      if (intent === "push") {
        push(href);
        return;
      }
      window.open(getShareableUrl(href), "_blank", "noopener,noreferrer");
    },
    [navigation],
  );
}
