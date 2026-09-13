"use client";

// @提及里可点击跳转的项目引用：把纯展示的 ProjectChip 包成指向项目详情页的链接，
// 与 IssueMentionCard 互为对应。
import { AppLink } from "../../navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { ProjectChip } from "./project-chip";

interface ProjectMentionCardProps {
  projectId: string;
  /** 项目解析不出来时的兜底文案（如它的标题）。 */
  fallbackLabel?: string;
}

/**
 * 可导航的项目引用——把 ProjectChip 包在指向项目详情页的 AppLink 里。是
 * IssueMentionCard 的对应物，并刻意长成同款：`ProjectChip` 只负责展示，
 * 所以「提及要不要做成链接」由外层决定，而这个决定应该只集中在一处。
 *
 * 之前它哪都不在——包链接的代码内联在只读渲染器里——代价就是只有项目提及踩过
 * 的坑：`.project-mention` 从没拿到那条「取消通用链接外观」的 CSS 规则，链接悬停
 * 卡片也一直不知道要跳过它。做成具名组件后，下一条同类规则就有了明确的去处。
 *
 * 渲染成真正的 `<a>` 链接：提及必须能用 Tab 聚焦、能用 Enter 激活，还要暴露可
 * 复制 / 可新标签页打开的 URL。`<span onClick>` 长得一样、鼠标也好使，这正是那种
 * 回归容易被漏掉的原因（见 project-mention-a11y.test.tsx）。
 *
 * AppLink 已经管了普通点击、修饰键点击和桌面端新标签页适配，这里不用重造。
 * 和任务不同，项目没有「新标签页打开」偏好要遵守——那个设置只作用于任务链接。
 *
 * @param projectId 目标项目 id
 * @param fallbackLabel 解析失败时的兜底文案
 */
export function ProjectMentionCard({
  projectId,
  fallbackLabel,
}: ProjectMentionCardProps) {
  const p = useWorkspacePaths();
  return (
    <AppLink
      href={p.projectDetail(projectId)}
      className="project-mention inline-flex"
    >
      <ProjectChip
        projectId={projectId}
        fallbackLabel={fallbackLabel}
        className="cursor-pointer hover:bg-accent transition-colors"
      />
    </AppLink>
  );
}
