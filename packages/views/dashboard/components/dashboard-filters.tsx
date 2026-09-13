"use client";

// 仪表盘页面级筛选控件：时间范围筛选器和项目筛选器。
import { CalendarDays, ChevronDown, FolderKanban } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { ProjectIcon } from "../../projects/components/project-icon";
import { useT } from "../../i18n";
import { ALL_PROJECTS, TIME_RANGES, type TimeRange } from "./dashboard-shared";

/** 项目筛选器下拉里用到的项目精简信息。 */
type DashboardProject = { id: string; title: string; icon: string | null };

/**
 * 页面级时间范围筛选器。
 *
 * 用「显示当前值 + 单选菜单」的一个按钮，代替五个常驻展开的分段。那五个分段曾是
 * 头部最宽、信息量最低的东西：它们编码的值其实在每个 KPI 标签里已经重复出现了
 * （"Cost · 30D"）。折叠起来每改一次多一次点击，却把头部空间还了回来。
 *
 * 没有「清除」项：时间范围是页面上每个查询的必填参数，根本没有空值可以回退。
 *
 * @param days 当前选中的时间范围
 * @param onChange 时间范围变化时的回调
 */
export function TimeRangeFilter({
  days,
  onChange,
}: {
  days: TimeRange;
  onChange: (days: TimeRange) => void;
}) {
  const { t } = useT("usage");
  const current = TIME_RANGES.find((r) => r.days === days) ?? TIME_RANGES[2];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="outline"
            size="sm"
            aria-label={t(($) => $.filter.period_label)}
            className="gap-1 px-2.5"
          >
            <CalendarDays className="size-3.5 text-muted-foreground" />
            <span className="tabular-nums">{current.label}</span>
            <ChevronDown className="size-3 text-muted-foreground" />
          </Button>
        }
      />
      <DropdownMenuContent align="end" className="w-auto min-w-32">
        <DropdownMenuRadioGroup
          value={String(days)}
          onValueChange={(value) => onChange(Number(value) as TimeRange)}
        >
          {TIME_RANGES.map((range) => (
            <DropdownMenuRadioItem
              key={range.days}
              value={String(range.days)}
              className="tabular-nums"
            >
              {range.label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * 页面级项目筛选器。
 *
 * 做成单层下拉，刻意不包一层通用的「Filter」菜单：只有一个维度时，外层菜单
 * 只是把可筛选项藏到多一次悬停之后。触发器像 `TimeRangeFilter` 一样直接显示当前值：
 * 全程中性描边，一旦选中就展示该项目的图标和名字——被命名的值本身就是「已选中」的信号，
 * 不需要实心层级。以后要是再加第二个维度（agent / model / runtime），再把它折回
 * `IssueDisplayControls` 那种组合菜单里。
 *
 * @param projects 当前工作区的项目列表
 * @param projectValue 当前选中的项目 id（可为 ALL_PROJECTS）
 * @param onProjectChange 项目变化时的回调
 */
export function ProjectFilter({
  projects,
  projectValue,
  onProjectChange,
}: {
  projects: DashboardProject[];
  projectValue: string;
  onProjectChange: (value: string) => void;
}) {
  const { t } = useT("usage");
  const allLabel = t(($) => $.filter.all_projects);
  // 一个已无法解析的项目 id（项目被删了，或切换工作区后残留的旧 id）应视为「未筛选」——
  // 页面在推导查询真正用的 `projectId` 时也是这么处理的，这样这个 chip 就不会声称
  // 数据其实没有被它收窄过的那个筛选。
  const selected = projects.find((p) => p.id === projectValue);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="outline"
            size="sm"
            aria-label={t(($) => $.filter.project_label)}
            className={selected ? "gap-1 px-2.5" : "gap-1 px-2.5 text-muted-foreground"}
          >
            {selected ? (
              <ProjectIcon project={selected} size="sm" />
            ) : (
              <FolderKanban className="size-3.5" />
            )}
            <span className="max-w-40 truncate">
              {selected ? selected.title : allLabel}
            </span>
            <ChevronDown className="size-3 text-muted-foreground" />
          </Button>
        }
      />
      <DropdownMenuContent align="end" className="max-h-72 w-auto min-w-52">
        <DropdownMenuRadioGroup
          value={projectValue}
          onValueChange={(value) => onProjectChange(value ?? ALL_PROJECTS)}
        >
          <DropdownMenuRadioItem value={ALL_PROJECTS}>
            <FolderKanban className="size-3.5 shrink-0 text-muted-foreground" />
            <span className="truncate">{allLabel}</span>
          </DropdownMenuRadioItem>
          {projects.map((project) => (
            <DropdownMenuRadioItem key={project.id} value={project.id}>
              <ProjectIcon project={project} size="sm" />
              <span className="truncate">{project.title}</span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
