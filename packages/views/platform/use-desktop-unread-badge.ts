import { useEffect } from "react";
import { useInboxUnreadCount } from "@multica/core/inbox/queries";

/** 桌面端原生 API 中与角标相关的最小形状。 */
type BadgeCapableAPI = {
  setUnreadBadge?: (count: number) => void;
};

/** 读取注入到 window 上的桌面端原生 API；web 上没有返回 undefined。 */
function getDesktopAPI(): BadgeCapableAPI | undefined {
  if (typeof window === "undefined") return undefined;
  return (window as unknown as { desktopAPI?: BadgeCapableAPI }).desktopAPI;
}

/**
 * platform 模块：把收件箱未读数镜像到系统 Dock/任务栏角标上。
 * web 端（没有 `desktopAPI`）与登录页（没有工作区 ⇒ 计数默认为 0，正好清掉
 * 上一会话遗留的过期角标）都是空操作。
 */
export function useDesktopUnreadBadge(wsId: string | null | undefined): void {
  const count = useInboxUnreadCount(wsId, { whileHidden: true });
  useEffect(() => {
    getDesktopAPI()?.setUnreadBadge?.(count);
  }, [count]);
}
