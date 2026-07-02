import {
  Bar,
} from "recharts";
import {
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import type { DailyCostStackData } from "../../utils";
import { useT } from "../../../i18n";
import {
  renderTooltipTotalFooter,
  RuntimeBarChart,
} from "./runtime-bar-chart";

// Three-segment stack (input / output / cache write) — keeps the user's
// attention on what's actually driving spend. Cache reads are excluded
// because their per-token rate is two orders of magnitude smaller and
// would be visually invisible in a stack; we surface their *savings*
// separately as a KPI.
//
// Series → CSS chart token: stack reads bottom-up as chart-1 (deepest brand
// blue, "input") → chart-2 (mid) → chart-3 (lightest, "cache write"), so the
// visual depth maps directly to "primary cost driver → secondary".
export const costStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

export function DailyCostChart({ data }: { data: DailyCostStackData[] }) {
  const { t } = useT("runtimes");
  // No internal empty-state — the parent decides what to show in place of
  // the chart (often a diagnostic explaining *why* there's no cost). Letting
  // recharts render an empty axis would be both ugly and uninformative.
  return (
    <RuntimeBarChart
      config={costStackConfig}
      data={data}
      yAxisWidth={50}
      tickFormatter={(v: number) => `$${v}`}
      tooltip={
        <ChartTooltip
          content={
            <ChartTooltipContent
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
        {/* Legend is intentionally rendered by the parent (in the chart card
            header, top-right) so the chart body stays clean and gets the full
            vertical real estate. */}
        <Bar
          dataKey="input"
          stackId="cost"
          fill="var(--color-input)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="output"
          stackId="cost"
          fill="var(--color-output)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="cacheWrite"
          stackId="cost"
          fill="var(--color-cacheWrite)"
          radius={[3, 3, 0, 0]}
        />
    </RuntimeBarChart>
  );
}
