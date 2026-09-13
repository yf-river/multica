// ============================================================================
// 每周任务数三段堆叠图（runtime 用量页「周视图」任务）
//
// 角色定位：DailyTasksChart 的周粒度版本——同样的完成/取消/失败堆叠柱，
// 每根柱聚合一个自然周。进行中的周半透明渲染，与 WeeklyCostChart /
// WeeklyTokensChart 一致，让未满的一周处处都读作从属的「进行中」。
// ============================================================================
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Cell,
} from "recharts";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { useLocale, useT } from "../../../i18n";

// DailyTasksChart 的周粒度版——同样的完成/取消/失败三段堆叠，每根柱聚合
// 一个自然周（周一至周日）。进行中的周半透明（与周费用/周 token 图一致）。
const weeklyTasksChartConfig = {
  completed: { label: "Completed", color: "var(--chart-1)" },
  cancelled: { label: "Cancelled", color: "var(--chart-3)" },
  failed: { label: "Failed", color: "var(--chart-5)" },
} satisfies ChartConfig;

// 一周的任务数据：周起止/标签/范围 + partial/daysCovered + 三种终态计数。
export interface WeeklyTasksData {
  weekStart: string;
  weekEnd: string;
  label: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
  completed: number;
  failed: number;
  cancelled: number;
}

/**
 * 每周任务堆叠柱状图（完成/取消/失败）。进行中的周半透明显示。
 */
export function WeeklyTasksChart({ data }: { data: WeeklyTasksData[] }) {
  const { t } = useT("usage");
  const { t: tRuntimes } = useT("runtimes");
  const locale = useLocale();
  return (
    <ChartContainer
      config={weeklyTasksChartConfig}
      className="aspect-[3/1] w-full"
    >
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
              labelKey="rangeLabel"
              labelFormatter={(_label, payload) => {
                const row = payload[0]?.payload as WeeklyTasksData | undefined;
                if (!row) return "";
                return row.partial
                  ? t(($) => $.weekly.partial_label, {
                      range: row.rangeLabel,
                      covered: row.daysCovered,
                    })
                  : row.rangeLabel;
              }}
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
                    <span>{tRuntimes(($) => $.charts.tooltip_total)}</span>
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
        >
          {data.map((d) => (
            <Cell key={`${d.weekStart}-c`} fillOpacity={d.partial ? 0.5 : 1} />
          ))}
        </Bar>
        <Bar
          dataKey="cancelled"
          stackId="tasks"
          fill="var(--color-cancelled)"
          radius={[0, 0, 0, 0]}
        >
          {data.map((d) => (
            <Cell key={`${d.weekStart}-x`} fillOpacity={d.partial ? 0.5 : 1} />
          ))}
        </Bar>
        <Bar
          dataKey="failed"
          stackId="tasks"
          fill="var(--color-failed)"
          radius={[3, 3, 0, 0]}
        >
          {data.map((d) => (
            <Cell key={`${d.weekStart}-f`} fillOpacity={d.partial ? 0.5 : 1} />
          ))}
        </Bar>
      </BarChart>
    </ChartContainer>
  );
}
