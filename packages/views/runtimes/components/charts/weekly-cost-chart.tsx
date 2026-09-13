// ============================================================================
// 每周成本四段堆叠图（runtime 用量页「周视图」费用）
//
// 角色定位：DailyCostChart 的周粒度版本。序列、颜色与顺序保持完全一致，让
// 「周」读作同一张「日」图的更粗切分，而不是另一张新图。进行中的周半透明
// 显示，「本周尚未结束」不需要单独图例就能一眼看出。
//
// 数据：utils.ts 的 aggregateByWeek 产出（WeeklyCostStackData），每根柱 =
// 一个自然周的四个计费组成。
// ============================================================================
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Cell,
} from "recharts";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import type { WeeklyCostStackData } from "../../utils";
import { useT } from "../../../i18n";

// 与 DailyCostChart 相同的四段堆叠——序列、颜色与顺序保持一致，让用户把
// 「周视图」读作同一张图更粗的切分，而非不同的图。进行中的周半透明渲染，
// 让「本周尚未结束」无需额外图例也一目了然。
export const weeklyCostStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheRead: { label: "Cache read", color: "var(--chart-4)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

/**
 * 每周成本堆叠柱状图。进行中的周（partial=true）半透明显示。
 */
export function WeeklyCostChart({ data }: { data: WeeklyCostStackData[] }) {
  const { t } = useT("runtimes");
  return (
    <ChartContainer config={weeklyCostStackConfig} className="aspect-[3/1] w-full">
      <BarChart data={data} margin={{ left: 0, right: 0, top: 4, bottom: 0 }}>
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
          tickFormatter={(v: number) => `$${v}`}
          width="auto"
        />
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
              footer={(payload) => {
                const total = payload.reduce(
                  (sum, item) =>
                    sum + (typeof item.value === "number" ? item.value : 0),
                  0,
                );
                return (
                  <div className="flex items-center justify-between gap-2 font-medium">
                    <span>{t(($) => $.charts.tooltip_total)}</span>
                    <span className="font-mono tabular-nums">
                      ${total.toFixed(2)}
                    </span>
                  </div>
                );
              }}
            />
          }
        />
        <Bar dataKey="input" stackId="cost" fill="var(--color-input)">
          {data.map((d) => (
            <Cell key={d.weekStart} fillOpacity={d.partial ? 0.5 : 1} />
          ))}
        </Bar>
        <Bar dataKey="output" stackId="cost" fill="var(--color-output)">
          {data.map((d) => (
            <Cell key={d.weekStart} fillOpacity={d.partial ? 0.5 : 1} />
          ))}
        </Bar>
        <Bar dataKey="cacheRead" stackId="cost" fill="var(--color-cacheRead)">
          {data.map((d) => (
            <Cell key={d.weekStart} fillOpacity={d.partial ? 0.5 : 1} />
          ))}
        </Bar>
        <Bar
          dataKey="cacheWrite"
          stackId="cost"
          fill="var(--color-cacheWrite)"
          radius={[3, 3, 0, 0]}
        >
          {data.map((d) => (
            <Cell key={d.weekStart} fillOpacity={d.partial ? 0.5 : 1} />
          ))}
        </Bar>
      </BarChart>
    </ChartContainer>
  );
}
