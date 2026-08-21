import {
  Bar,
} from "recharts";
import {
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import { formatTokens, type DailyTokenData } from "../../utils";
import { useT } from "../../../i18n";
import {
  renderTooltipTotalFooter,
  RuntimeBarChart,
} from "./runtime-bar-chart";

// Four-segment stack — input / output / cache read / cache write. Unlike the
// cost chart, cache reads ARE visible here: a typical day on Claude shows
// cache reads dominating raw token counts (often 10×+ input), so the user
// only sees the real shape of usage when reads are stacked in. The cost
// chart drops them for the opposite reason (their dollar contribution is
// two orders of magnitude smaller and would be visually invisible).
//
// Series → CSS chart token: stack reads bottom-up as chart-1 (deepest brand
// blue, "input") → chart-2 (mid) → chart-4 (cache read) → chart-3 (lightest,
// "cache write"). Cache read gets chart-4 so the two cache series are
// visually adjacent and tonally distinct from input/output.
export const tokenStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheRead: { label: "Cache read", color: "var(--chart-4)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

export function DailyTokensChart({ data }: { data: DailyTokenData[] }) {
  const { t } = useT("runtimes");
  // No internal empty-state — same convention as DailyCostChart: the parent
  // decides what to render when there's nothing to show.
  return (
    <RuntimeBarChart
      config={tokenStackConfig}
      data={data}
      yAxisWidth={50}
      tickFormatter={(v: number) => formatTokens(v)}
      tooltip={
        <ChartTooltip
          content={
            <ChartTooltipContent
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
        {/* Legend is rendered by the parent in the chart card header. */}
        <Bar
          dataKey="input"
          stackId="tokens"
          fill="var(--color-input)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="output"
          stackId="tokens"
          fill="var(--color-output)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="cacheRead"
          stackId="tokens"
          fill="var(--color-cacheRead)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="cacheWrite"
          stackId="tokens"
          fill="var(--color-cacheWrite)"
          radius={[3, 3, 0, 0]}
        />
    </RuntimeBarChart>
  );
}
