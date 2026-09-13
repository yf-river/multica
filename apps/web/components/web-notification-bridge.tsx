"use client";

/**
 * Web 通知点击桥：把系统通知的点击路由到来源工作区的收件箱（详见下方组件注释）。
 */
import { useEffect, useRef } from "react";
import {
  registerSystemNotificationClickHandler,
  type SystemNotificationPayload,
} from "@multica/core/platform";
import { paths } from "@multica/core/paths";
import { useNavigation } from "@multica/views/navigation";

/**
 * 把浏览器通知（Notification）的点击事件接到「来源 workspace 的收件箱」并定位到被点的那条。
 * 组件把 Notification API 的 onclick 回调注册进 core 单例。
 *
 * 跳转用的是通知发出时的 slug——即来源 workspace——而不是当前活跃的 workspace，
 * 因此即使用户之后切换了工作区，点击也总能打开正确的收件箱（#3766）。
 * slug 为空（来源未解析出）则忽略。把行标记为已读由 InboxPage 的选中项 effect
 * 处理，它覆盖了 `?issue=` URL 参数这条路径。
 */
export function WebNotificationBridge() {
  const { push } = useNavigation();
  // 导航适配器的身份会随当前路由变化；用 ref 让已注册的点击处理器保持稳定，
  // 同时始终调用最新的 push。
  const pushRef = useRef(push);
  useEffect(() => {
    pushRef.current = push;
  }, [push]);

  useEffect(() => {
    registerSystemNotificationClickHandler(
      ({ slug, issueKey }: SystemNotificationPayload) => {
        if (!slug) return;
        const inboxPath = `${paths.workspace(slug).inbox()}?issue=${encodeURIComponent(issueKey)}`;
        pushRef.current(inboxPath);
      },
    );
    return () => registerSystemNotificationClickHandler(null);
  }, []);

  return null;
}
