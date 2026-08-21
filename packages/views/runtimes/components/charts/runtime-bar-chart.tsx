import type { ReactNode } from "react";
import { BarChart, CartesianGrid, Cell, XAxis, YAxis } from "recharts";
import {
  ChartContainer,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";

const CHART_MARGIN = { left: 0, right: 0, top: 4, bottom: 0 } as const;

interface RuntimeBarChartProps<TData> {
  config: ChartConfig;
  data: TData[];
  yAxisWidth: number;
  allowDecimals?: boolean;
  tickFormatter?: (value: number) => string;
  tooltip: ReactNode;
  children: ReactNode;
}

export function RuntimeBarChart<TData>({
  config,
  data,
  yAxisWidth,
  allowDecimals,
  tickFormatter,
  tooltip,
  children,
}: RuntimeBarChartProps<TData>): ReactNode {
  return (
    <ChartContainer config={config} className="aspect-[3/1] w-full">
      <BarChart data={data} margin={CHART_MARGIN}>
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
          tickFormatter={tickFormatter}
          allowDecimals={allowDecimals}
          width={yAxisWidth}
        />
        {tooltip}
        {children}
      </BarChart>
    </ChartContainer>
  );
}

export function renderTooltipTotalFooter(
  payload: readonly { value?: unknown }[],
  label: ReactNode,
  formatTotal: (total: number) => ReactNode,
): ReactNode {
  const total = payload.reduce(
    (sum, item) => sum + (typeof item.value === "number" ? item.value : 0),
    0,
  );

  return (
    <div className="flex items-center justify-between gap-2 font-medium">
      <span>{label}</span>
      <span className="font-mono tabular-nums">{formatTotal(total)}</span>
    </div>
  );
}

export function PartialWeekCells<
  TData extends { partial: boolean; weekStart: string },
>({
  data,
  keySuffix = "",
}: {
  data: TData[];
  keySuffix?: string;
}): ReactNode {
  return data.map((item) => (
    <Cell
      key={`${item.weekStart}${keySuffix}`}
      fillOpacity={item.partial ? 0.5 : 1}
    />
  ));
}
