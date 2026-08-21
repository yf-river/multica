import {
  Bar,
} from "recharts";
import {
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import type { WeeklyCostStackData } from "../../utils";
import { useT } from "../../../i18n";
import {
  PartialWeekCells,
  renderTooltipTotalFooter,
  RuntimeBarChart,
} from "./runtime-bar-chart";

// Same three-segment stack as DailyCostChart — keeping series, colours, and
// ordering identical so the user reads "Weekly" as a coarser cut of the same
// chart, not a different chart. Partial-week bars render at half-opacity so
// "this week is in progress" is visually obvious without a separate legend.
export const weeklyCostStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

export function WeeklyCostChart({ data }: { data: WeeklyCostStackData[] }) {
  const { t } = useT("runtimes");
  return (
    <RuntimeBarChart
      config={weeklyCostStackConfig}
      data={data}
      yAxisWidth={50}
      tickFormatter={(v: number) => `$${v}`}
      tooltip={
        <ChartTooltip
          content={
            <ChartTooltipContent
              labelKey="rangeLabel"
              labelFormatter={(_label, payload) => {
                const row = payload[0]?.payload as WeeklyCostStackData | undefined;
                if (!row) return "";
                return row.partial
                  ? t(($) => $.usage.weekly_partial_label, {
                      range: row.rangeLabel,
                      covered: row.daysCovered,
                    })
                  : row.rangeLabel;
              }}
              formatter={(value, name) =>
                typeof value === "number"
                  ? `$${value.toFixed(2)} ${name}`
                  : `${value} ${name}`
              }
              footer={(payload) =>
                renderTooltipTotalFooter(
                  payload,
                  t(($) => $.charts.tooltip_total),
                  (total) => `$${total.toFixed(2)}`,
                )
              }
            />
          }
        />
      }
    >
        <Bar dataKey="input" stackId="cost" fill="var(--color-input)">
          <PartialWeekCells data={data} />
        </Bar>
        <Bar dataKey="output" stackId="cost" fill="var(--color-output)">
          <PartialWeekCells data={data} />
        </Bar>
        <Bar
          dataKey="cacheWrite"
          stackId="cost"
          fill="var(--color-cacheWrite)"
          radius={[3, 3, 0, 0]}
        >
          <PartialWeekCells data={data} />
        </Bar>
    </RuntimeBarChart>
  );
}
