// ============================================================================
// 每日失败数堆叠图（runtime 用量页「错误」概览）
//
// 角色定位：把每天按失败类别（鉴权/限流/超时/厂商/运行时/agent/其他）归因的
// 失败任务计数画成纵向堆叠柱。配色与类别标签取自 failure-class-visuals，每日
// 聚合逻辑在 utils.ts 的 aggregateByDate。
//
// 数据流：data 数组（每天一条，含各类失败数与 failed/total 汇总）→ 由
// useFailureClassConfig 提供类别配置、activeFailureClasses 挑出需要绘制的类别
// → Recharts 的 Bar 按 stackId="errors" 堆叠。
// ============================================================================
import { BarChart, Bar, XAxis, YAxis, CartesianGrid } from "recharts";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from "@multica/ui/components/ui/chart";
import { useT } from "../../../i18n";
import {
  activeFailureClasses,
  formatRate,
  labelOf,
  useFailureClassConfig,
  type FailureBucketTotals,
  type FailureClassCounts,
} from "./failure-class-visuals";

// 一天的数据：日期/展示标签 + 各类失败计数（FailureClassCounts）+ 该天失败/
// 任务总数（FailureBucketTotals，用于提示里的失败率）。
export interface DailyErrorsData extends FailureClassCounts, FailureBucketTotals {
  date: string;
  label: string;
}

/**
 * 每天按失败类别堆叠的失败任务柱状图。
 *
 * 柱高是失败的绝对次数——运维扫图要找的正是「尖峰」。失败率放在悬浮提示里
 * 而不是画成 y 轴序列：作为序列它会被低量日主导——某天只有 1 个任务且失败
 * 时，会被读成 100% 宕机。
 */
export function DailyErrorsChart({ data }: { data: DailyErrorsData[] }) {
  const { t } = useT("usage");
  const config = useFailureClassConfig();
  const classes = activeFailureClasses(data);

  return (
    <ChartContainer config={config} className="aspect-[3/1] w-full">
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
          allowDecimals={false}
          width="auto"
        />
        <ChartTooltip
          content={
            <ChartTooltipContent
              // `name` 是 Recharts 的 dataKey，即裸类别 id（"rate_limit"）。
              // 经图表 config 解析成已翻译的标签，让提示与图例措辞一致。
              formatter={(value, name) => `${value} ${labelOf(config, name)}`}
              footer={(payload) => {
                const row = payload[0]?.payload as DailyErrorsData | undefined;
                if (!row) return null;
                return (
                  <div className="flex items-center justify-between gap-2 font-medium">
                    <span>{t(($) => $.errors.tooltip_rate)}</span>
                    <span className="font-mono tabular-nums">
                      {formatRate(row.failed, row.total)}
                    </span>
                  </div>
                );
              }}
            />
          }
        />
        {classes.map((c, i) => (
          <Bar
            key={c}
            dataKey={c}
            stackId="errors"
            fill={`var(--color-${c})`}
            // 只有最顶端的柱段带圆角，让整根堆叠像「一根柱子」而不是一串药丸。
            radius={i === classes.length - 1 ? [3, 3, 0, 0] : [0, 0, 0, 0]}
          />
        ))}
      </BarChart>
    </ChartContainer>
  );
}
