import {
  Bar,
} from "recharts";
import {
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { RuntimeBarChart } from "./runtime-bar-chart";

// Single-series bar — total daily run time in seconds. The y-axis tick
// formatter and tooltip both use the same `formatDuration` so the user
// reads the same unit ladder (h / m / s) everywhere.
const timeChartConfig = {
  totalSeconds: { label: "Run time", color: "var(--chart-1)" },
} satisfies ChartConfig;

export interface DailyTimeData {
  date: string;
  label: string;
  totalSeconds: number;
}

export function DailyTimeChart({
  data,
  formatY,
  formatTooltip,
}: {
  data: DailyTimeData[];
  // Caller passes a `formatDuration`-style fn so the chart stays UI-string
  // agnostic (the "< 1m" fallback label is localized by the parent).
  formatY: (seconds: number) => string;
  formatTooltip: (seconds: number) => string;
}) {
  return (
    <RuntimeBarChart
      config={timeChartConfig}
      data={data}
      yAxisWidth={56}
      tickFormatter={(v: number) => formatY(v)}
      tooltip={
        <ChartTooltip
          content={
            <ChartTooltipContent
              formatter={(value, name) =>
                typeof value === "number"
                  ? `${formatTooltip(value)} ${name}`
                  : `${value} ${name}`
              }
            />
          }
        />
      }
    >
        <Bar
          dataKey="totalSeconds"
          fill="var(--color-totalSeconds)"
          radius={[3, 3, 0, 0]}
        />
    </RuntimeBarChart>
  );
}
