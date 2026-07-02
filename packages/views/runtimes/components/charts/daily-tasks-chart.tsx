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
  renderTooltipTotalFooter,
  RuntimeBarChart,
} from "./runtime-bar-chart";

// Two-segment stack — completed runs at the bottom (chart-1, primary
// brand), failed runs on top (chart-5 for distinct emphasis). Lets the
// user see day-over-day failure-rate trend without a separate chart.
const tasksChartConfig = {
  completed: { label: "Completed", color: "var(--chart-1)" },
  failed: { label: "Failed", color: "var(--chart-5)" },
} satisfies ChartConfig;

export interface DailyTasksData {
  date: string;
  label: string;
  completed: number;
  failed: number;
}

export function DailyTasksChart({ data }: { data: DailyTasksData[] }) {
  const { t } = useT("runtimes");
  return (
    <RuntimeBarChart
      config={tasksChartConfig}
      data={data}
      yAxisWidth={40}
      allowDecimals={false}
      tooltip={
        <ChartTooltip
          content={
            <ChartTooltipContent
              formatter={(value, name) => `${value} ${name}`}
              footer={(payload) =>
                renderTooltipTotalFooter(
                  payload,
                  t(($) => $.charts.tooltip_total),
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
        />
        <Bar
          dataKey="failed"
          stackId="tasks"
          fill="var(--color-failed)"
          radius={[3, 3, 0, 0]}
        />
    </RuntimeBarChart>
  );
}
