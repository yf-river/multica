"use client";

// 项目状态 / 优先级 / 相对日期的 i18n 标签 Hook：所有要显示项目状态与优先级
// 文案的地方都走这里取翻译。
import type { ProjectStatus, ProjectPriority } from "@multica/core/types";
import { useT } from "../../i18n";

// 下面这些 Hook 返回「感知 i18n」的项目状态 / 优先级标签映射。它们取代了
// PROJECT_STATUS_CONFIG / PROJECT_PRIORITY_CONFIG 上静态的 `.label` 字段；
// 连搜索——最后一个还读 `PROJECT_STATUS_CONFIG.label` 的调用方——现在也改走
// 这里，因此整个仓库不再有地方读这两个 `.label`。要给项目状态命名，请用这些
// Hook；core 里的 `.label` 字段是历史遗留，不是第二个真源。与 inbox 的
// `useTypeLabels` 互为镜像。

/** 返回各项目状态对应的本地化文案。 */
export function useProjectStatusLabels(): Record<ProjectStatus, string> {
  const { t } = useT("projects");
  return {
    planned: t(($) => $.status.planned),
    in_progress: t(($) => $.status.in_progress),
    paused: t(($) => $.status.paused),
    completed: t(($) => $.status.completed),
    cancelled: t(($) => $.status.cancelled),
  };
}

/** 返回各项目优先级对应的本地化文案。 */
export function useProjectPriorityLabels(): Record<ProjectPriority, string> {
  const { t } = useT("projects");
  return {
    urgent: t(($) => $.priority.urgent),
    high: t(($) => $.priority.high),
    medium: t(($) => $.priority.medium),
    low: t(($) => $.priority.low),
    none: t(($) => $.priority.none),
  };
}

// 「1d ago」/「3mo ago」/「Today」——走 i18next 的相对日期小工具。返回一个函数，
// 让调用方保持原来的 `formatRelativeDate(iso)` 用法。
/** 返回「把 ISO 日期格式化成相对时间文案」的函数（如今天 / 1 天前 / 3 个月前）。 */
export function useFormatRelativeDate(): (date: string) => string {
  const { t } = useT("projects");
  return (date: string) => {
    const diff = Date.now() - new Date(date).getTime();
    const days = Math.floor(diff / (1000 * 60 * 60 * 24));
    if (days < 1) return t(($) => $.relative_date.today);
    if (days === 1) return t(($) => $.relative_date.one_day_ago);
    if (days < 30) return t(($) => $.relative_date.days_ago, { count: days });
    const months = Math.floor(days / 30);
    return t(($) => $.relative_date.months_ago, { count: months });
  };
}
