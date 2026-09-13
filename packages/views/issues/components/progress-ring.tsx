// 本文件：ProgressRing —— 微型圆环进度指示。
// 未完成时渲染一个开口圆环按比例消隐，完成时填成一条实心弧。

/**
 * 微型圆环进度：`done/total` 决定弧的填充比例；进行中为开口环（主题色），
 * 全部完成时弧收成一个点并变"成功"色（info）。total<=0 时视为 0 进度。
 */
export function ProgressRing({
  done,
  total,
  size = 12,
}: {
  done: number;
  total: number;
  size?: number;
}) {
  const stroke = 1.5;
  const radius = (size - stroke) / 2;
  const circumference = 2 * Math.PI * radius;
  const ratio = total > 0 ? Math.min(done / total, 1) : 0;
  const offset = circumference * (1 - ratio);
  const isComplete = total > 0 && done >= total;
  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      className={isComplete ? "text-info" : "text-primary"}
      aria-hidden="true"
    >
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        stroke="currentColor"
        strokeOpacity="0.25"
        strokeWidth={stroke}
      />
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        stroke="currentColor"
        strokeWidth={stroke}
        strokeDasharray={circumference}
        strokeDashoffset={offset}
        strokeLinecap="round"
        transform={`rotate(-90 ${size / 2} ${size / 2})`}
      />
    </svg>
  );
}
