import type { ReactNode } from "react";
import { Bar } from "recharts";
import {
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import {
  formatTokens,
  type DailyCostStackData,
  type DailyTokenData,
  type WeeklyCostStackData,
  type WeeklyTokenData,
} from "../../utils";
import { useT } from "../../../i18n";
import {
  PartialWeekCells,
  renderTooltipTotalFooter,
  RuntimeBarChart,
} from "./runtime-bar-chart";

const tokenStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheRead: { label: "Cache read", color: "var(--chart-4)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

const costStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

const taskStackConfig = {
  completed: { label: "Completed", color: "var(--chart-1)" },
  failed: { label: "Failed", color: "var(--chart-5)" },
} satisfies ChartConfig;

const timeChartConfig = {
  totalSeconds: { label: "Run time", color: "var(--chart-1)" },
} satisfies ChartConfig;

type BarRadius = [number, number, number, number];

interface UsageSeries {
  key: string;
  fill: string;
  radius: BarRadius;
}

const tokenSeries: UsageSeries[] = [
  { key: "input", fill: "var(--color-input)", radius: [0, 0, 0, 0] },
  { key: "output", fill: "var(--color-output)", radius: [0, 0, 0, 0] },
  { key: "cacheRead", fill: "var(--color-cacheRead)", radius: [0, 0, 0, 0] },
  { key: "cacheWrite", fill: "var(--color-cacheWrite)", radius: [3, 3, 0, 0] },
];

const costSeries: UsageSeries[] = [
  { key: "input", fill: "var(--color-input)", radius: [0, 0, 0, 0] },
  { key: "output", fill: "var(--color-output)", radius: [0, 0, 0, 0] },
  { key: "cacheWrite", fill: "var(--color-cacheWrite)", radius: [3, 3, 0, 0] },
];

const taskSeries: UsageSeries[] = [
  { key: "completed", fill: "var(--color-completed)", radius: [0, 0, 0, 0] },
  { key: "failed", fill: "var(--color-failed)", radius: [3, 3, 0, 0] },
];

const timeSeries: UsageSeries[] = [
  { key: "totalSeconds", fill: "var(--color-totalSeconds)", radius: [3, 3, 0, 0] },
];

interface WeeklyUsageDatum {
  weekStart: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
}

interface UsageBarChartProps<T extends { label: string }> {
  config: ChartConfig;
  data: T[];
  series: UsageSeries[];
  stackId?: string;
  yAxisWidth: number;
  allowDecimals?: boolean;
  tickFormatter?: (value: number) => string;
  valueFormatter: (value: number) => string;
  totalLabel?: ReactNode;
  totalFormatter?: (total: number) => ReactNode;
  weeklyLabel?: (row: T) => ReactNode;
}

function UsageBarChart<T extends { label: string }>({
  config,
  data,
  series,
  stackId,
  yAxisWidth,
  allowDecimals,
  tickFormatter,
  valueFormatter,
  totalLabel,
  totalFormatter,
  weeklyLabel,
}: UsageBarChartProps<T>) {
  return (
    <RuntimeBarChart
      config={config}
      data={data}
      yAxisWidth={yAxisWidth}
      allowDecimals={allowDecimals}
      tickFormatter={tickFormatter}
      tooltip={
        <ChartTooltip
          content={
            <ChartTooltipContent
              labelKey={weeklyLabel ? "rangeLabel" : undefined}
              labelFormatter={
                weeklyLabel
                  ? (_label, payload) => {
                      const row = payload[0]?.payload as T | undefined;
                      return row ? weeklyLabel(row) : "";
                    }
                  : undefined
              }
              formatter={(value, name) =>
                typeof value === "number"
                  ? `${valueFormatter(value)} ${name}`
                  : `${value} ${name}`
              }
              footer={
                totalLabel !== undefined && totalFormatter
                  ? (payload) =>
                      renderTooltipTotalFooter(payload, totalLabel, totalFormatter)
                  : undefined
              }
            />
          }
        />
      }
    >
      {series.map((item) => (
        <Bar
          key={item.key}
          dataKey={item.key}
          stackId={stackId}
          fill={item.fill}
          radius={item.radius}
        >
          {weeklyLabel ? (
            <PartialWeekCells
              data={data as unknown as WeeklyUsageDatum[]}
              keySuffix={`-${item.key}`}
            />
          ) : null}
        </Bar>
      ))}
    </RuntimeBarChart>
  );
}

export interface DailyTimeData {
  date: string;
  label: string;
  totalSeconds: number;
}

export interface DailyTasksData {
  date: string;
  label: string;
  completed: number;
  failed: number;
}

export interface WeeklyTimeData extends WeeklyUsageDatum {
  weekEnd: string;
  label: string;
  totalSeconds: number;
}

export interface WeeklyTasksData extends WeeklyUsageDatum {
  weekEnd: string;
  label: string;
  completed: number;
  failed: number;
}

export function DailyTokensChart({ data }: { data: DailyTokenData[] }) {
  const { t } = useT("runtimes");
  return (
    <UsageBarChart
      config={tokenStackConfig}
      data={data}
      series={tokenSeries}
      stackId="tokens"
      yAxisWidth={50}
      tickFormatter={formatTokens}
      valueFormatter={formatTokens}
      totalLabel={t(($) => $.charts.tooltip_total)}
      totalFormatter={(total) => total.toLocaleString()}
    />
  );
}

export function WeeklyTokensChart({ data }: { data: WeeklyTokenData[] }) {
  const { t } = useT("runtimes");
  return (
    <UsageBarChart
      config={tokenStackConfig}
      data={data}
      series={tokenSeries}
      stackId="tokens"
      yAxisWidth={50}
      tickFormatter={formatTokens}
      valueFormatter={formatTokens}
      totalLabel={t(($) => $.charts.tooltip_total)}
      totalFormatter={(total) => total.toLocaleString()}
      weeklyLabel={(row) =>
        t(($) => $.usage.weekly_partial_label, {
          range: row.rangeLabel,
          covered: row.daysCovered,
        })
      }
    />
  );
}

export function DailyCostChart({ data }: { data: DailyCostStackData[] }) {
  const { t } = useT("runtimes");
  return (
    <UsageBarChart
      config={costStackConfig}
      data={data}
      series={costSeries}
      stackId="cost"
      yAxisWidth={50}
      tickFormatter={(value) => `$${value}`}
      valueFormatter={(value) => `$${value.toFixed(2)}`}
      totalLabel={t(($) => $.charts.tooltip_total)}
      totalFormatter={(total) => `$${total.toFixed(2)}`}
    />
  );
}

export function WeeklyCostChart({ data }: { data: WeeklyCostStackData[] }) {
  const { t } = useT("runtimes");
  return (
    <UsageBarChart
      config={costStackConfig}
      data={data}
      series={costSeries}
      stackId="cost"
      yAxisWidth={50}
      tickFormatter={(value) => `$${value}`}
      valueFormatter={(value) => `$${value.toFixed(2)}`}
      totalLabel={t(($) => $.charts.tooltip_total)}
      totalFormatter={(total) => `$${total.toFixed(2)}`}
      weeklyLabel={(row) =>
        t(($) => $.usage.weekly_partial_label, {
          range: row.rangeLabel,
          covered: row.daysCovered,
        })
      }
    />
  );
}

export function DailyTasksChart({ data }: { data: DailyTasksData[] }) {
  const { t } = useT("runtimes");
  return (
    <UsageBarChart
      config={taskStackConfig}
      data={data}
      series={taskSeries}
      stackId="tasks"
      yAxisWidth={40}
      allowDecimals={false}
      valueFormatter={String}
      totalLabel={t(($) => $.charts.tooltip_total)}
      totalFormatter={(total) => total.toLocaleString()}
    />
  );
}

export function WeeklyTasksChart({ data }: { data: WeeklyTasksData[] }) {
  const { t } = useT("usage");
  const { t: tRuntimes } = useT("runtimes");
  return (
    <UsageBarChart
      config={taskStackConfig}
      data={data}
      series={taskSeries}
      stackId="tasks"
      yAxisWidth={40}
      allowDecimals={false}
      valueFormatter={String}
      totalLabel={tRuntimes(($) => $.charts.tooltip_total)}
      totalFormatter={(total) => total.toLocaleString()}
      weeklyLabel={(row) =>
        t(($) => $.weekly.partial_label, {
          range: row.rangeLabel,
          covered: row.daysCovered,
        })
      }
    />
  );
}

interface TimeChartProps<T extends { label: string }> {
  data: T[];
  formatY: (seconds: number) => string;
  formatTooltip: (seconds: number) => string;
  weeklyLabel?: (row: T) => ReactNode;
}

function TimeChart<T extends { label: string }>({
  data,
  formatY,
  formatTooltip,
  weeklyLabel,
}: TimeChartProps<T>) {
  return (
    <UsageBarChart
      config={timeChartConfig}
      data={data}
      series={timeSeries}
      yAxisWidth={56}
      tickFormatter={formatY}
      valueFormatter={formatTooltip}
      weeklyLabel={weeklyLabel}
    />
  );
}

export function DailyTimeChart({
  data,
  formatY,
  formatTooltip,
}: Omit<TimeChartProps<DailyTimeData>, "weeklyLabel">) {
  return <TimeChart data={data} formatY={formatY} formatTooltip={formatTooltip} />;
}

export function WeeklyTimeChart({
  data,
  formatY,
  formatTooltip,
}: Omit<TimeChartProps<WeeklyTimeData>, "weeklyLabel">) {
  const { t } = useT("usage");
  return (
    <TimeChart
      data={data}
      formatY={formatY}
      formatTooltip={formatTooltip}
      weeklyLabel={(row) =>
        t(($) => $.weekly.partial_label, {
          range: row.rangeLabel,
          covered: row.daysCovered,
        })
      }
    />
  );
}
