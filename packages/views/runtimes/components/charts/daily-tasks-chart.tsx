// ============================================================================
// 每日任务数三段堆叠图（runtime 用量页「任务」概览）
//
// 角色定位：把每天「完成 / 取消 / 失败」三种终态的任务计数画成一根纵向堆叠
// 柱。三段叠在同一根柱上，用户不必另开一张图就能看出失败率的天天走势。
//
// 数据流：DailyTasksData（每天一条：日期/标签 + 三种终态计数）→ 三段 Bar 按
// stackId="tasks" 堆叠，配色由 tasksChartConfig 决定。
// ============================================================================
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
} from "recharts";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { useLocale, useT } from "../../../i18n";

// 三段堆叠——完成在底部（chart-1，品牌主色），取消在中间（chart-3，弱化：
// 手动中止是一种「结局」而不是「错误」），失败在最顶（chart-5，突出强调）。
// 这样用户无需单独的图就能看到失败率逐日走势。
const tasksChartConfig = {
  completed: { label: "Completed", color: "var(--chart-1)" },
  cancelled: { label: "Cancelled", color: "var(--chart-3)" },
  failed: { label: "Failed", color: "var(--chart-5)" },
} satisfies ChartConfig;

// 一天的任务数据：日期/展示标签 + 完成/失败/取消三种终态任务数。
export interface DailyTasksData {
  date: string;
  label: string;
  completed: number;
  failed: number;
  cancelled: number;
}

/**
 * 每日任务数堆叠柱状图（完成/取消/失败）。
 * 悬浮提示底部额外汇总三段的当日总数。
 */
export function DailyTasksChart({ data }: { data: DailyTasksData[] }) {
  const { t } = useT("runtimes");
  const locale = useLocale();
  return (
    <ChartContainer config={tasksChartConfig} className="aspect-[3/1] w-full">
      <BarChart data={data} margin={{ left: 0, right: 0, top: 4, bottom: 0 }}>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="label"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          interval="preserveStartEnd"
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          allowDecimals={false}
          width="auto"
        />
        <ChartTooltip
          content={
            <ChartTooltipContent
              formatter={(value, name) => `${value} ${name}`}
              footer={(payload) => {
                const total = payload.reduce(
                  (sum, item) =>
                    sum +
                    (typeof item.value === "number" ? item.value : 0),
                  0,
                );
                return (
                  <div className="flex items-center justify-between gap-2 font-medium">
                    <span>{t(($) => $.charts.tooltip_total)}</span>
                    <span className="font-mono tabular-nums">
                      {total.toLocaleString(locale)}
                    </span>
                  </div>
                );
              }}
            />
          }
        />
        <Bar
          dataKey="completed"
          stackId="tasks"
          fill="var(--color-completed)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="cancelled"
          stackId="tasks"
          fill="var(--color-cancelled)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="failed"
          stackId="tasks"
          fill="var(--color-failed)"
          radius={[3, 3, 0, 0]}
        />
      </BarChart>
    </ChartContainer>
  );
}
