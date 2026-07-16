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

interface UsageSeries {
  key: string;
  fill: string;
  radius: [number, number, number, number];
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

const formatLocaleNumber = (value: number) => value.toLocaleString();
const formatCostTick = (value: number) => `$${value}`;
const formatCost = (value: number) => `$${value.toFixed(2)}`;

interface WeeklyUsageDatum {
  weekStart: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
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
  totalFormatter,
  weekly,
}: {
  config: ChartConfig;
  data: T[];
  series: UsageSeries[];
  stackId?: string;
  yAxisWidth: number;
  allowDecimals?: boolean;
  tickFormatter?: (value: number) => string;
  valueFormatter: (value: number) => string;
  totalFormatter?: (total: number) => ReactNode;
  weekly?: boolean;
}) {
  const { t: tRuntimes } = useT("runtimes");
  const { t: tUsage } = useT("usage");
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
              labelKey={weekly ? "rangeLabel" : undefined}
              labelFormatter={
                weekly
                  ? (_label, payload) => {
                      const row = payload[0]?.payload as WeeklyUsageDatum | undefined;
                      return row
                        ? tUsage(($) => $.weekly.partial_label, {
                            range: row.rangeLabel,
                            covered: row.daysCovered,
                          })
                        : "";
                    }
                  : undefined
              }
              formatter={(value, name) =>
                typeof value === "number"
                  ? `${valueFormatter(value)} ${name}`
                  : `${value} ${name}`
              }
              footer={
                totalFormatter
                  ? (payload) =>
                      renderTooltipTotalFooter(
                        payload,
                        tRuntimes(($) => $.charts.tooltip_total),
                        totalFormatter,
                      )
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
          {weekly ? (
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
  return (
    <UsageBarChart
      config={tokenStackConfig}
      data={data}
      series={tokenSeries}
      stackId="tokens"
      yAxisWidth={50}
      tickFormatter={formatTokens}
      valueFormatter={formatTokens}
      totalFormatter={formatLocaleNumber}
    />
  );
}

export function WeeklyTokensChart({ data }: { data: WeeklyTokenData[] }) {
  return (
    <UsageBarChart
      config={tokenStackConfig}
      data={data}
      series={tokenSeries}
      stackId="tokens"
      yAxisWidth={50}
      tickFormatter={formatTokens}
      valueFormatter={formatTokens}
      totalFormatter={formatLocaleNumber}
      weekly
    />
  );
}

export function DailyCostChart({ data }: { data: DailyCostStackData[] }) {
  return (
    <UsageBarChart
      config={costStackConfig}
      data={data}
      series={costSeries}
      stackId="cost"
      yAxisWidth={50}
      tickFormatter={formatCostTick}
      valueFormatter={formatCost}
      totalFormatter={formatCost}
    />
  );
}

export function WeeklyCostChart({ data }: { data: WeeklyCostStackData[] }) {
  return (
    <UsageBarChart
      config={costStackConfig}
      data={data}
      series={costSeries}
      stackId="cost"
      yAxisWidth={50}
      tickFormatter={formatCostTick}
      valueFormatter={formatCost}
      totalFormatter={formatCost}
      weekly
    />
  );
}

export function DailyTasksChart({ data }: { data: DailyTasksData[] }) {
  return (
    <UsageBarChart
      config={taskStackConfig}
      data={data}
      series={taskSeries}
      stackId="tasks"
      yAxisWidth={40}
      allowDecimals={false}
      valueFormatter={String}
      totalFormatter={formatLocaleNumber}
    />
  );
}

export function WeeklyTasksChart({ data }: { data: WeeklyTasksData[] }) {
  return (
    <UsageBarChart
      config={taskStackConfig}
      data={data}
      series={taskSeries}
      stackId="tasks"
      yAxisWidth={40}
      allowDecimals={false}
      valueFormatter={String}
      totalFormatter={formatLocaleNumber}
      weekly
    />
  );
}

export function DailyTimeChart({
  data,
  formatY,
  formatTooltip,
}: {
  data: DailyTimeData[];
  formatY: (seconds: number) => string;
  formatTooltip: (seconds: number) => string;
}) {
  return (
    <UsageBarChart
      config={timeChartConfig}
      data={data}
      series={timeSeries}
      yAxisWidth={56}
      tickFormatter={formatY}
      valueFormatter={formatTooltip}
    />
  );
}

export function WeeklyTimeChart({
  data,
  formatY,
  formatTooltip,
}: {
  data: WeeklyTimeData[];
  formatY: (seconds: number) => string;
  formatTooltip: (seconds: number) => string;
}) {
  return (
    <UsageBarChart
      config={timeChartConfig}
      data={data}
      series={timeSeries}
      yAxisWidth={56}
      tickFormatter={formatY}
      valueFormatter={formatTooltip}
      weekly
    />
  );
}
