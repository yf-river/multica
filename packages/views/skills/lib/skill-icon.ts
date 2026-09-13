// 本文件职责：定义「skill 实体」在 UI 各处统一使用的图标——从路由图标表里派生，
// 而不是各页面各自重新声明。
import type { LucideIcon } from "lucide-react";
import { WORKSPACE_PAGES } from "@multica/core/paths";
import { ROUTE_ICON_COMPONENTS } from "../../layout/route-icon-components";

/**
 * 只要把 skill 当实体渲染就会用到的图标：列表头部、空状态、详情页身份区、
 * agent 的 skill 行、skill 选择器等。
 *
 * 从 skill 路由图标派生而不是另起炉灶，是为了让侧边栏、桌面端 Tab 栏和所有页内
 * 表面不会再各自漂移——此前同一个东西显示过四种不同图标（BookOpenText、
 * BookOpen、FileText、Sparkles）。
 *
 * 注意这是 skill 这个「实体」的图标；skill 内部的文件不算 skill，它们在
 * `file-tree.tsx` 里用各自的文件类型图标。
 */
export const SkillIcon: LucideIcon =
  ROUTE_ICON_COMPONENTS[WORKSPACE_PAGES.skills.icon];
