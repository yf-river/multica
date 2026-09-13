// 每日运行时长柱状图：展示每天 agent 累计运行秒数，用于 runtime 用量页的
// "运行时间"概览。
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

// 单序列柱状图——每天累计运行秒数。y 轴刻度格式与 tooltip 用同一个
// `formatDuration`，让用户在任何地方读到同一套单位阶梯（h / m / s）。
const timeChartConfig = {
  totalSeconds: { label: "Run time", color: "var(--chart-1)" },
} satisfies ChartConfig;

export interface DailyTimeData {
  date: string;
  label: string;
  totalSeconds: number;
}

/**
 * 每日运行时长柱状图。
 * @param data - 每天一条：日期、展示标签与累计秒数。
 * @param formatY - y 轴刻度格式化函数。
 * @param formatTooltip - tooltip 数值格式化函数。
 */
export function DailyTimeChart({
  data,
  formatY,
  formatTooltip,
}: {
  data: DailyTimeData[];
  // 调用方传一个 `formatDuration` 风格的函数，让图表保持对 UI 文案无感
  // （"< 1m" 这类兜底文案由父组件本地化）。
  formatY: (seconds: number) => string;
  formatTooltip: (seconds: number) => string;
}) {
  return (
    <ChartContainer config={timeChartConfig} className="aspect-[3/1] w-full">
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
          tickFormatter={(v: number) => formatY(v)}
          width="auto"
        />
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
        <Bar
          dataKey="totalSeconds"
          fill="var(--color-totalSeconds)"
          radius={[3, 3, 0, 0]}
        />
      </BarChart>
    </ChartContainer>
  );
}
