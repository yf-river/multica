// 本文件：StatusIcon —— 状态图标（SVG 图形集）。
// 一套图标按"状态类别"渲染：积压（环形点阵）、待办（空环）、进行中/评审中
// （饼形填充半程/四分之三）、已完成（实心圆 + 对勾）、阻塞（空环 + 禁止斜杠）、
// 已取消（空环 + 叉）。自定义状态使用其所属类别的图形并允许自定义颜色。

import { statusCategoryOfKey } from "@multica/core/issues";
import type { IssueStatus, IssueStatusCategory } from "@multica/core/types";
import { STATUS_CONFIG } from "@multica/core/issues/config";

// ---------------------------------------------------------------------------
// 几何常量（viewBox 0 0 14 14，圆心 7,7）
// ---------------------------------------------------------------------------

const CX = 7;
const CY = 7;
const OUTER_R = 6;
const FILL_R = 3.5;

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

/** 从 12 点方向起、顺时针构造一块扇形饼图 SVG 路径 */
function piePath(cx: number, cy: number, r: number, progress: number): string {
  const angle = 2 * Math.PI * progress;
  const endX = cx + r * Math.sin(angle);
  const endY = cy - r * Math.cos(angle);
  const largeArc = progress > 0.5 ? 1 : 0;
  return `M${cx},${cy} L${cx},${cy - r} A${r},${r} 0 ${largeArc},1 ${endX},${endY} Z`;
}

// ---------------------------------------------------------------------------
// 基础组件——虚线外环 + 饼形填充 + 可选中心图形
// ---------------------------------------------------------------------------

function ProgressCircle({
  progress,
  children,
}: {
  progress: number;
  children?: React.ReactNode;
}) {
  return (
    <>
      {/* 外圈虚线环 */}
      <circle
        cx={CX}
        cy={CY}
        r={OUTER_R}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.5}
        strokeDasharray="3.14 0"
        strokeDashoffset={-0.7}
      />
      {/* 进度填充 */}
      {progress === 1 ? (
        <circle cx={CX} cy={CY} r={OUTER_R} fill="currentColor" />
      ) : progress > 0 ? (
        <path d={piePath(CX, CY, FILL_R, progress)} fill="currentColor" />
      ) : null}
      {children}
    </>
  );
}

// ---------------------------------------------------------------------------
// 各状态渲染器
// ---------------------------------------------------------------------------

/** 16 个小点排成一圈（积压） */
function BacklogIcon() {
  const count = 16;
  const dotR = 0.55;
  return (
    <g>
      {Array.from({ length: count }, (_, i) => {
        const angle = (i / count) * Math.PI * 2 - Math.PI / 2;
        return (
          <circle
            key={i}
            cx={CX + OUTER_R * Math.cos(angle)}
            cy={CY + OUTER_R * Math.sin(angle)}
            r={dotR}
            fill="currentColor"
          />
        );
      })}
    </g>
  );
}

function TodoIcon() {
  return <ProgressCircle progress={0} />;
}

function InProgressIcon() {
  return <ProgressCircle progress={0.5} />;
}

function InReviewIcon() {
  return <ProgressCircle progress={0.75} />;
}

function PendingReleaseIcon() {
  return <ProgressCircle progress={0.875} />;
}

function DoneIcon() {
  return (
    <ProgressCircle progress={1}>
      <path
        d="M10.951 4.24896C11.283 4.58091 11.283 5.11909 10.951 5.45104L5.95104 10.451C5.61909 10.783 5.0809 10.783 4.74896 10.451L2.74896 8.45104C2.41701 8.11909 2.41701 7.5809 2.74896 7.24896C3.0809 6.91701 3.61909 6.91701 3.95104 7.24896L5.35 8.64792L9.74896 4.24896C10.0809 3.91701 10.6191 3.91701 10.951 4.24896Z"
        fill="white"
        stroke="none"
      />
    </ProgressCircle>
  );
}

/** 外环 + 禁止斜杠（🚫 风格） */
function BlockedIcon() {
  return (
    <ProgressCircle progress={0}>
      <line
        x1={CX + FILL_R * Math.cos(Math.PI * 0.75)}
        y1={CY - FILL_R * Math.sin(Math.PI * 0.75)}
        x2={CX + FILL_R * Math.cos(-Math.PI * 0.25)}
        y2={CY - FILL_R * Math.sin(-Math.PI * 0.25)}
        stroke="currentColor"
        strokeWidth={1.5}
        strokeLinecap="round"
      />
    </ProgressCircle>
  );
}

function CancelledIcon() {
  return (
    <ProgressCircle progress={0}>
      <path
        d="M5 5 L9 9 M9 5 L5 9"
        fill="none"
        stroke="currentColor"
        strokeWidth={1.5}
        strokeLinecap="round"
      />
    </ProgressCircle>
  );
}

// ---------------------------------------------------------------------------
// 渲染器映射表
// ---------------------------------------------------------------------------

const STATUS_RENDERERS: Record<IssueStatusCategory, () => React.ReactNode> = {
  backlog: BacklogIcon,
  todo: TodoIcon,
  in_progress: InProgressIcon,
  in_review: InReviewIcon,
  pending_release: PendingReleaseIcon,
  done: DoneIcon,
  blocked: BlockedIcon,
  cancelled: CancelledIcon,
};

// ---------------------------------------------------------------------------
// 对外组件
// ---------------------------------------------------------------------------

/**
 * 状态图标。按状态类别渲染固定图形集：
 * - `category`：已解析的类别，给持有工作区目录的调用方用；缺省时按 key 自行
 *   解析（对 8 个内置状态是精确的，自定义 key 在没有目录的本组件里回退为 todo）；
 * - `color`：自定义状态的 `#rrggbb`。内置状态保留各自的语义 token 色；
 * - `inheritColor`：为 true 时强制继承父级文字色（选中行/深色 chip 等场景）。
 */
export function StatusIcon({
  status,
  category: categoryProp,
  color,
  className = "h-4 w-4",
  inheritColor = false,
}: {
  status: IssueStatus | string;
  /**
   * 已解析的类别，给持有工作区目录的调用方。不传则按 key 自行解析——
   * 对 8 个内置状态是精确的；对目录外自定义 key 会回退到 `todo`。
   */
  category?: IssueStatusCategory;
  /** 自定义状态的 `#rrggbb` 色值。内置状态保留各自的语义 token 色。 */
  color?: string | null;
  className?: string;
  inheritColor?: boolean;
}) {
  // 图形集按"类别"划分：自定义状态以其类别的图标渲染，
  // 这正是让它读起来像"同一类东西"的原因。（MUL-6243）
  const category = categoryProp ?? statusCategoryOfKey(status);
  const cfg = STATUS_CONFIG[category];
  const Renderer = STATUS_RENDERERS[category] ?? TodoIcon;
  // 自定义颜色优先于类别 token，但仅当调用方没有让图形强制继承文字色时
  //（选中行、深色 chip 等场景）。
  const useCustomColor = !inheritColor && Boolean(color);

  return (
    <svg
      viewBox="0 0 14 14"
      fill="none"
      style={useCustomColor ? { color: color ?? undefined } : undefined}
      className={`${className} ${
        inheritColor || useCustomColor ? "" : cfg?.iconColor ?? "text-muted-foreground"
      } shrink-0`}
    >
      <Renderer />
    </svg>
  );
}
