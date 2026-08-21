import {
  Bar,
} from "recharts";
import {
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { formatTokens, type WeeklyTokenData } from "../../utils";
import { useT } from "../../../i18n";
import {
  PartialWeekCells,
  renderTooltipTotalFooter,
  RuntimeBarChart,
} from "./runtime-bar-chart";

// Mirror of DailyTokensChart's four-segment stack — same series and colours
// keep the Weekly view legible as a coarser cut of the Daily one.
export const weeklyTokenStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheRead: { label: "Cache read", color: "var(--chart-4)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

export function WeeklyTokensChart({ data }: { data: WeeklyTokenData[] }) {
  const { t } = useT("runtimes");
  return (
    <RuntimeBarChart
      config={weeklyTokenStackConfig}
      data={data}
      yAxisWidth={50}
      tickFormatter={(v: number) => formatTokens(v)}
      tooltip={
        <ChartTooltip
          content={
            <ChartTooltipContent
              labelKey="rangeLabel"
              labelFormatter={(_label, payload) => {
                const row = payload[0]?.payload as WeeklyTokenData | undefined;
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
                  ? `${formatTokens(value)} ${name}`
                  : `${value} ${name}`
              }
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
        <Bar dataKey="input" stackId="tokens" fill="var(--color-input)">
          <PartialWeekCells data={data} />
        </Bar>
        <Bar dataKey="output" stackId="tokens" fill="var(--color-output)">
          <PartialWeekCells data={data} />
        </Bar>
        <Bar dataKey="cacheRead" stackId="tokens" fill="var(--color-cacheRead)">
          <PartialWeekCells data={data} />
        </Bar>
        <Bar
          dataKey="cacheWrite"
          stackId="tokens"
          fill="var(--color-cacheWrite)"
          radius={[3, 3, 0, 0]}
        >
          <PartialWeekCells data={data} />
        </Bar>
    </RuntimeBarChart>
  );
}
