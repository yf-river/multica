"use client";

import type { ReactNode } from "react";
import { SidebarProvider, SidebarInset } from "@multica/ui/components/ui/sidebar";
import { ModalRegistry } from "../modals/registry";
import { AppSidebar } from "./app-sidebar";
import { DashboardGuard } from "./dashboard-guard";
import { NavigationProgress } from "./navigation-progress";
import { WorkspacePresencePrefetch } from "./workspace-presence-prefetch";
import { GlobalShortcuts } from "./global-shortcuts";

/**
 * 布局模块：仪表盘的整体布局外壳。
 * 组装侧边栏、内容区、全局快捷键、模态注册表等，是 Web/桌面端共用的骨架。
 */
interface DashboardLayoutProps {
  children: ReactNode;
  /** 渲染在 SidebarInset 内部（如 ChatWindow、ChatFab 这类绝对定位浮层） */
  extra?: ReactNode;
  /** 渲染在侧边栏头部，作为搜索触发器 */
  searchSlot?: ReactNode;
  /** 加载指示器 */
  loadingIndicator?: ReactNode;
}

/**
 * 仪表盘布局组件。
 * @param children 主内容区。
 * @param extra 叠加在主内容之上的浮层（如聊天窗口）。
 * @param searchSlot 侧边栏头部的搜索入口。
 * @param loadingIndicator 加载期间的占位指示器。
 */
export function DashboardLayout({
  children,
  extra,
  searchSlot,
  loadingIndicator,
}: DashboardLayoutProps) {
  return (
    <DashboardGuard
      loadingFallback={
        <div className="flex h-svh items-center justify-center">
          {loadingIndicator}
        </div>
      }
    >
      <SidebarProvider className="h-svh bg-app-shell">
        <GlobalShortcuts />
        <WorkspacePresencePrefetch />
        <AppSidebar searchSlot={searchSlot} />
        <SidebarInset className="relative overflow-hidden">
          <NavigationProgress />
          {children}
          <ModalRegistry />
          {extra}
        </SidebarInset>
      </SidebarProvider>
    </DashboardGuard>
  );
}
