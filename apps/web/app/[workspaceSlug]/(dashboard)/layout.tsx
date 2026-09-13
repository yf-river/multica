"use client";

/**
 * Dashboard（已登录工作区）布局：负责文档标题的实时同步，
 * 并组合侧边栏布局 + 搜索命令面板 + Web 通知桥 + 悬浮会话（FloatingChat）。
 */
import { Suspense } from "react";
import { DashboardLayout } from "@multica/views/layout";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { SearchCommand, SearchTrigger } from "@multica/views/search";
import { FloatingChat } from "@multica/views/chat/floating-chat";
import { WebNotificationBridge } from "@/components/web-notification-bridge";
import { WorkspaceDocumentTitle } from "@/platform/workspace-document-title";

/**
 * Dashboard 布局。
 *
 * @param props.children - 各工作区页面的内容。
 * @returns 侧边栏布局 + 附加弹层（搜索/通知/悬浮会话）。
 */
export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <>
      {/* app router 下 useSearchParams 需要 Suspense 边界。放在 DashboardLayout 外层，
          这样在守卫解析 workspace 期间标签页标题也能尽快就位。 */}
      <Suspense fallback={null}>
        <WorkspaceDocumentTitle />
      </Suspense>
      <DashboardLayout
        loadingIndicator={<MulticaIcon className="size-6" />}
        searchSlot={<SearchTrigger />}
        extra={
          <>
            <SearchCommand />
            <WebNotificationBridge />
            <FloatingChat />
          </>
        }
      >
        {children}
      </DashboardLayout>
    </>
  );
}
