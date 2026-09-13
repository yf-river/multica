"use client";

// 本文件提供一个堆叠柱状迷你图（sparkline），用一列柱子同时表达吞吐量与失败率。

interface ActivityBucketLike {
  total: number;
  failed: number;
}

interface SparklineProps {
  /**
   * 按展示顺序（旧 → 新）排列的桶数组。每个桶对应一列柱子：
   * `total` 决定柱子总高度，`failed` 会以“失败色”叠加在柱子顶部，
   * 让这一列一眼就能同时看出“吞吐量 + 失败占比”——两个维度放在同一个形状里，
   * 无需额外增加行的装饰。
   */
  buckets: readonly ActivityBucketLike[];
  width: number;
  height: number;
  className?: string;
}

const SUCCESS_FILL = "var(--color-brand)";
const SUCCESS_OPACITY = 0.6;
const FAILED_FILL = "var(--color-destructive)";
const BASELINE_FILL = "var(--color-muted-foreground)";
const BASELINE_OPACITY = 0.25;

/**
 * 堆叠柱状迷你图：成功段在底部、失败段在顶部。一行、一个形状，承载两个维度：
 *
 *   - **柱子高度** = 当天的总吞吐量（按每个组件独立缩放，让低流量 Agent
 *     呈现“自己的形状”，而不是被邻居某个繁忙的日期压扁）。
 *   - **红色占比** = 失败率。一个“跑了 100 次失败 1 次”的 Agent 和
 *     “跑了 100 次失败 99 次”的 Agent，必须在快速扫视时就能区分；用单根柱子
 *     做到这一点的唯一办法，就是把第二个维度编码在柱子内部。
 *
 * 为什么不在出现失败时把整根柱子染红（我们最初上线的“二值”做法）？
 * 因为那样会丢失失败率这一维度——1/100 和 99/100 会画成一样。把失败段单独切出来，
 * 既保留了维度，又守住了“每个格子一个元素”的扫视速度预算。
 *
 * Agent 尚未创建的日子和真正为 0 的日子在这里故意长得一样——区分它们会带来
 * 列本不该有的额外变化（参考 Tufte 的数据墨水原则）；真正需要区分的地方，
 * 由 tooltip 上的“Created N days ago”来承载。
 */
export function Sparkline({
  buckets,
  width,
  height,
  className,
}: SparklineProps) {
  const n = buckets.length;
  if (n === 0) {
    return (
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        className={className}
        aria-hidden
      />
    );
  }

  // 列几何布局：列间距 gap = 1px，剩余宽度由各列均分。取整到像素，
  // 避免亚像素矩形在这个尺寸下渲染模糊。
  const gap = 1;
  const colWidth = Math.max(1, Math.floor((width - gap * (n - 1)) / n));
  const usedWidth = colWidth * n + gap * (n - 1);
  const offsetX = Math.floor((width - usedWidth) / 2);

  // 取每个组件自己的最大值做缩放，让低流量 Agent 的形状不被邻居某个
  // 繁忙日期压扁。
  let maxTotal = 0;
  for (const b of buckets) if (b.total > maxTotal) maxTotal = b.total;
  const scaleDenominator = Math.max(1, maxTotal);

  // 底部预留 1px，让柱子视觉上有“落脚点”而不是悬浮着。
  const baselineY = height - 1;
  const usableH = height - 1;

  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className={className}
      aria-hidden
    >
      {/* 淡淡的底线：没有历史数据的一行仍能读作“一行”，而不是缺失的格子。 */}
      <rect
        x={0}
        y={baselineY}
        width={width}
        height={1}
        fill={BASELINE_FILL}
        fillOpacity={BASELINE_OPACITY}
      />
      {buckets.map((b, i) => {
        if (b.total === 0) return null;
        const x = offsetX + i * (colWidth + gap);
        const totalH = Math.max(
          1,
          Math.round((usableH * b.total) / scaleDenominator),
        );
        // 失败段保留 1px 底线，让哪怕只有 1 次失败也依然可见；再用 total
        // 做上限截断，避免 1/1 这种情况把失败段画得比整根柱子还高。
        const failedH =
          b.failed > 0
            ? Math.min(
                totalH,
                Math.max(1, Math.round((usableH * b.failed) / scaleDenominator)),
              )
            : 0;
        const successH = totalH - failedH;
        const colTop = baselineY - totalH;
        return (
          <g key={i}>
            {successH > 0 && (
              <rect
                x={x}
                y={colTop + failedH}
                width={colWidth}
                height={successH}
                fill={SUCCESS_FILL}
                fillOpacity={SUCCESS_OPACITY}
              />
            )}
            {failedH > 0 && (
              <rect
                x={x}
                y={colTop}
                width={colWidth}
                height={failedH}
                fill={FAILED_FILL}
              />
            )}
          </g>
        );
      })}
    </svg>
  );
}
