// ============================================================================
// 每日成本四段堆叠图（runtime 用量页「费用」概览）
//
// 角色定位：把每天的费用按四种计费组成——输入/输出/缓存读取/缓存写入——
// 画成纵向堆叠柱。堆叠相加等于顶部「总费用」KPI 报的同一笔钱。数据由
// utils.ts 的 aggregateByDate 产出（DailyCostStackData），金额已按
// estimateCost 口径拆分好。
// ============================================================================
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
} from "recharts";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import type { DailyCostStackData } from "../../utils";
import { useT } from "../../../i18n";

// 四段堆叠（输入/输出/缓存读取/缓存写入）——每一类都是 estimateCost 会计费
// 的组成部分，所以各柱相加恒等于费用 KPI 报的同一笔钱。悬浮提示的 Total
// 是对它能看到的分段求和得到的，因此某类一旦漏出这份 config，就等于从用户
// 的总费用里漏掉了这一类（MUL-6334：仅缓存读取就占某些桶的 50% 以上）。
//
// 序列 → CSS 图表 token：输入/输出/缓存写入沿用 chart-1/2/3，缓存读取占
// chart-4 的槽位（与 DailyTokensChart 给它的颜色和位置完全一致），同一天的
// 成本视图与 token 视图读起来像一张图、只是刻度不同。
export const costStackConfig = {
  input: { label: "Input", color: "var(--chart-1)" },
  output: { label: "Output", color: "var(--chart-2)" },
  cacheRead: { label: "Cache read", color: "var(--chart-4)" },
  cacheWrite: { label: "Cache write", color: "var(--chart-3)" },
} satisfies ChartConfig;

/**
 * 每日成本堆叠柱状图（按输入/输出/缓存读取/缓存写入分色）。
 */
export function DailyCostChart({ data }: { data: DailyCostStackData[] }) {
  const { t } = useT("runtimes");
  // 没有内置空状态——没有费用时显示成什么由父组件决定（往往是解释「为什么
  // 没有费用」的诊断信息）。让 recharts 渲染一根空坐标轴既难看也不说明问题。
  return (
    <ChartContainer config={costStackConfig} className="aspect-[3/1] w-full">
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
              formatter={(value, name) =>
                typeof value === "number"
                  ? `$${value.toFixed(2)} ${name}`
                  : `${value} ${name}`
              }
              footer={(payload) => {
                const total = payload.reduce(
                  (sum, item) =>
                    sum +
                    (typeof item.value === "number" ? item.value : 0),
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
        {/* 图例刻意由父组件渲染（图表卡片头部右上角），让图表主体保持干净、
            获得全部纵向空间。 */}
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
          dataKey="cacheRead"
          stackId="cost"
          fill="var(--color-cacheRead)"
          radius={[0, 0, 0, 0]}
        />
        <Bar
          dataKey="cacheWrite"
          stackId="cost"
          fill="var(--color-cacheWrite)"
          radius={[3, 3, 0, 0]}
        />
      </BarChart>
    </ChartContainer>
  );
}
