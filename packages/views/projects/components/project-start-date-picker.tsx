"use client";

// 项目开始日期选择胶囊：共享 DateOnlyPicker 的薄封装，创建弹窗与详情侧栏共用。
import { CalendarClock } from "lucide-react";
import type { UpdateProjectRequest } from "@multica/core/types";
import { DateOnlyPicker } from "../../common/date-only-picker";
import { useT } from "../../i18n";

/**
 * 项目开始日期胶囊。共享 DateOnlyPicker 的薄封装——只负责提供字段名（通过
 * onChange）、图标和 projects 的文案；日历 / 清除 / 格式化行为全在基座里，
 * 免得跟任务的选择器各自漂移。同一个组件既服务「创建项目弹窗」（把返回的值
 * 写进本地草稿状态），也服务项目侧栏。
 *
 * @param startDate 当前开始日期（YYYY-MM-DD 或 null）
 * @param onUpdate 变更回调（携带局部更新对象）
 * @param triggerRender 自定义触发器元素
 * @param align 弹层对齐方向
 * @param open 受控打开状态
 * @param onOpenChange 打开状态变化回调
 */
export function ProjectStartDatePicker({
  startDate,
  onUpdate,
  triggerRender,
  align = "start",
  open,
  onOpenChange,
}: {
  startDate: string | null;
  onUpdate: (updates: Partial<UpdateProjectRequest>) => void;
  /** 自定义触发器元素（例如创建弹窗里的胶囊按钮）。 */
  triggerRender?: React.ReactElement<Record<string, unknown>>;
  align?: "start" | "center" | "end";
  /** 受控打开状态——让 ⋯ 溢出菜单能展开并打开这个胶囊。 */
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
}) {
  const { t } = useT("projects");
  return (
    <DateOnlyPicker
      value={startDate}
      onChange={(v) => onUpdate({ start_date: v })}
      icon={<CalendarClock className="h-3.5 w-3.5 text-muted-foreground" />}
      placeholder={t(($) => $.detail.prop_start_date)}
      emptyLabel={t(($) => $.detail.no_start_date)}
      triggerRender={triggerRender}
      align={align}
      open={open}
      onOpenChange={onOpenChange}
    />
  );
}
