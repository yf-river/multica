import {
  Inbox,
  MessageSquare,
  CircleUser,
  ListTodo,
  FolderKanban,
  Zap,
  Bot,
  Users,
  BarChart3,
  Monitor,
  Server,
  BookOpenText,
  Settings,
  Heart,
  File,
  FileText,
  FileImage,
  FileCode,
  FileArchive,
  FileAudio,
  FileVideo,
  FileQuestion,
  type LucideIcon,
} from "lucide-react";
import { resolveRouteIconName, type RouteIconName } from "@multica/core/paths";

/**
 * 布局模块：路由图标名到图标组件的注册表。
 * 是 `@multica/core/paths` 定义的路由图标契约的渲染半边。
 *
 * 每个 {@link RouteIconName} 都必须有对应条目——`Record` 类型让缺失的 key
 * 直接变成编译错误。
 */
export const ROUTE_ICON_COMPONENTS: Record<RouteIconName, LucideIcon> = {
  Inbox,
  MessageSquare,
  CircleUser,
  ListTodo,
  FolderKanban,
  Zap,
  Bot,
  Users,
  BarChart3,
  Monitor,
  Server,
  BookOpenText,
  Settings,
  Heart,
  File,
  FileText,
  FileImage,
  FileCode,
  FileArchive,
  FileAudio,
  FileVideo,
  FileQuestion,
};

/**
 * 根据工作区作用域路径或资源 URL 解析出图标组件。
 *
 * 这是导航界面唯一应该使用的入口：它接收它们已有的路径而不是图标名，
 * 所以调用方无需持有、持久化或强转图标名。`resolveRouteIconName` 总返回
 * 一个有效名字，注册表也是完整的，因此结果永远不会是 undefined——未知
 * 路由会退回默认图标，而不是什么都不渲染。
 *
 * 导航界面用它来取页面级图标，所以一条路由在任何地方都只有同一个兜底图标。
 * @param path 工作区作用域路径或资源 URL。
 */
export function routeIconForPath(path: string): LucideIcon {
  return ROUTE_ICON_COMPONENTS[resolveRouteIconName(path)];
}
