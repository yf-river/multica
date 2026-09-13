// 本文件是 Web 应用的「浏览器通知」桥。
//
// `handleInboxNew`（实时同步）是唯一决策点——它已按焦点与来源工作区的静音
// 偏好做了闸门——然后调用 `showWebNotification` 渲染横幅。
//
// 放在 `packages/core`（而不是 `packages/views`），因为调用方（`handleInboxNew`）
// 在 core，而 core 不能 import views（依赖方向是 views → core）。需要应用路由
// 的「点击路由」决策由宿主应用经 `registerSystemNotificationClickHandler` 注入
// ——core 保持无头。

"use client";

/** 一条系统通知的载荷。 */
export interface SystemNotificationPayload {
  /**
   * 来源工作区 slug。解析不到时为空串——点击随之变为空操作，而不是路由到
   * 错误的工作区（#3766）。
   */
  slug: string;
  /** 收件箱行 id——让点击可以把它标记已读。 */
  itemId: string;
  /** 收件箱页的 `?issue=<…>` 选择器（issue id，否则用条目 id）。 */
  issueKey: string;
  title: string;
  body: string;
}

type ClickHandler = (payload: SystemNotificationPayload) => void;

// 模块级单例——镜像桌面端 preload 在启动时注册一次行为的方式。Web 外壳注册
// 一个认得路由的处理器；未注册时（SSR、测试、挂载前）点击是静默空操作。
let clickHandler: ClickHandler | null = null;

/**
 * 注册点击通知后的路由方式（聚焦并跳到来源工作区的收件箱、聚焦到该条目）。
 * 由 Web 应用外壳调用一次；传 `null` 可注销。
 */
export function registerSystemNotificationClickHandler(
  handler: ClickHandler | null,
): void {
  clickHandler = handler;
}

// 从 `window`（而不是裸全局）读 Notification 构造器——既 SSR 安全，也能从
// core 的 Node 测试环境注入（那里默认没有 `window` / `Notification`）。
function getNotificationCtor(): typeof Notification | null {
  if (typeof window === "undefined") return null;
  const ctor = (window as { Notification?: typeof Notification }).Notification;
  return typeof ctor === "function" ? ctor : null;
}

/** 浏览器是否暴露 Notification API（SSR / 老引擎为 false）。 */
export function isWebNotificationSupported(): boolean {
  return getNotificationCtor() !== null;
}

export type WebNotificationPermission = NotificationPermission | "unsupported";

/** 当前权限；API 不可用时为 "unsupported"。 */
export function getWebNotificationPermission(): WebNotificationPermission {
  const ctor = getNotificationCtor();
  return ctor ? ctor.permission : "unsupported";
}

/**
 * 请求通知权限。解析为最终的权限，或 "unsupported"。
 * 只有 "default" 才会触发浏览器弹窗——已经决定过的权限（granted/denied）
 * 原样返回。
 */
export async function requestWebNotificationPermission(): Promise<WebNotificationPermission> {
  const ctor = getNotificationCtor();
  if (!ctor) return "unsupported";
  if (ctor.permission !== "default") return ctor.permission;
  try {
    return await ctor.requestPermission();
  } catch {
    // 老 Safari 用回调签名，可能拒绝 promise 形态。
    return ctor.permission;
  }
}

/**
 * 为一条新收件箱条目弹原生浏览器通知。除非「Notification API 受支持且权限
 * 为 granted」否则是空操作——「要不要弹」（焦点 + 静音闸门）由调用方
 * (`handleInboxNew`) 负责；这里只管渲染。点击横幅会聚焦标签页，并经已注册的
 * 点击处理器路由。
 */
export function showWebNotification(payload: SystemNotificationPayload): void {
  const ctor = getNotificationCtor();
  if (!ctor || ctor.permission !== "granted") return;
  let notification: Notification;
  try {
    notification = new ctor(payload.title, {
      body: payload.body,
      // 同一收件箱行的重复横幅折叠成一条（例如重连会重放 `inbox:new` 事件）。
      tag: payload.itemId,
    });
  } catch {
    // 有些引擎需要活动的 ServiceWorkerRegistration 才能构造 Notification
    // （特别是 Chrome on Android）。静默降级——应用内收件箱与未读角标仍会
    // 呈现新条目。
    return;
  }
  notification.onclick = () => {
    try {
      window.focus();
    } catch {
      // 尽力而为；有些浏览器禁止程序化聚焦。
    }
    notification.close();
    clickHandler?.(payload);
  };
}
