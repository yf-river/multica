import {
  Bar,
} from "recharts";
import {
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { useT } from "../../../i18n";
import {
  PartialWeekCells,
  renderTooltipTotalFooter,
  RuntimeBarChart,
} from "./runtime-bar-chart";

// Weekly counterpart of DailyTasksChart — same completed/failed stacked
// bar, but each bar groups a Mon–Sun calendar week. Partial-week bars at
// half opacity match WeeklyCostChart / WeeklyTokensChart so the in-progress
// week reads as visually subordinate everywhere.
const weeklyTasksChartConfig = {
  completed: { label: "Completed", color: "var(--chart-1)" },
  failed: { label: "Failed", color: "var(--chart-5)" },
} satisfies ChartConfig;

export interface WeeklyTasksData {
  weekStart: string;
  weekEnd: string;
  label: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
  completed: number;
  failed: number;
}

export function WeeklyTasksChart({ data }: { data: WeeklyTasksData[] }) {
  const { t } = useT("usage");
  const { t: tRuntimes } = useT("runtimes");
  return (
    <RuntimeBarChart
      config={weeklyTasksChartConfig}
      data={data}
      yAxisWidth={40}
      allowDecimals={false}
      tooltip={
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
              footer={(payload) =>
                renderTooltipTotalFooter(
                  payload,
                  tRuntimes(($) => $.charts.tooltip_total),
                  (total) => total.toLocaleString(),
                )
              }
            />
          }
        />
      }
    >
        <Bar
          dataKey="completed"
          stackId="tasks"
          fill="var(--color-completed)"
          radius={[0, 0, 0, 0]}
        >
          <PartialWeekCells data={data} keySuffix="-c" />
        </Bar>
        <Bar
          dataKey="failed"
          stackId="tasks"
          fill="var(--color-failed)"
          radius={[3, 3, 0, 0]}
        >
          <PartialWeekCells data={data} keySuffix="-f" />
        </Bar>
    </RuntimeBarChart>
  );
}
