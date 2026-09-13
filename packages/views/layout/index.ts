/**
 * 布局模块的公共出口。
 * 集中导出侧边栏、页头、进度条、守卫等布局相关组件与 Hook。
 */
export { AppSidebar } from "./app-sidebar";
export {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "./collection-page";
export { ROUTE_ICON_COMPONENTS, routeIconForPath } from "./route-icon-components";
export {
  useTabPresentation,
  ResourceLeadingVisual,
} from "./tab-presentation";
export type { TabPresentationResult } from "./tab-presentation";
export { DashboardGuard } from "./dashboard-guard";
export { DashboardLayout } from "./dashboard-layout";
export { useDashboardGuard } from "./use-dashboard-guard";
export { WorkspaceLoader } from "./workspace-loader";
export { WorkspacePresencePrefetch } from "./workspace-presence-prefetch";
export { GlobalShortcuts } from "./global-shortcuts";
export { NavigationProgress } from "./navigation-progress";
