// ============================================================================
// runtimes（运行时间）视图层共享的小型展示组件
//
// 角色定位：本文件是 runtime 相关页面共用的「状态小件工具箱」：运行模式图标
// （云/本机）、厂商标签、健康状态的点 / 图标 / 徽标三种形态及其文案（含多语言
// 翻译版），以及详情页/卡片通用的「标签+值」字段行与 KPI 数字卡。只做展示，
// 不含任何请求/业务逻辑。
//
// 数据流：调用方把 runtime 本体（或 deriveRuntimeHealth 算出的健康状态）传进
// 这些组件，组件负责渲染成一致的视觉语言。组件内的文案一律通过
// useT("runtimes") 翻译；非组件上下文（如列构造工厂）用 healthLabel 的纯英文
// 兜底。
//
// 核心概念：runtime 健康状态共四档——online（在线）、recently_lost（刚失联、
// 短暂抖动）、offline（离线）、long_offline（长时间失联）。四档的视觉与文案
// 都集中在本文件的映射表里，其它页面不各自定义。
// ============================================================================
import { Cloud, Monitor, Wifi, WifiHigh, WifiOff } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import type { RuntimeHealth } from "@multica/core/runtimes";
import { ProviderLogo } from "./provider-logo";
import { useT } from "../../i18n";

// 运行模式图标：cloud → 云图标，其余（本机 local）→ 显示器图标。
// 用于行/卡片里让用户一眼区分「这台 runtime 托管在云上」还是「跑在自己机器上」。
export function RuntimeModeIcon({ mode }: { mode: string }) {
  return mode === "cloud" ? (
    <Cloud className="h-3.5 w-3.5" />
  ) : (
    <Monitor className="h-3.5 w-3.5" />
  );
}

// 紧凑厂商标签：一个小 logo 方块 + 厂商名。用在较密的列表行里，标明这个
// runtime 背后接的是哪家 CLI / 模型厂商（如 cursor、anthropic）。
export function ProviderChip({ provider }: { provider: string }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-1.5 py-0.5 text-caption font-medium text-muted-foreground">
      <ProviderLogo provider={provider} className="h-3 w-3" />
      <span className="capitalize">{provider}</span>
    </span>
  );
}

// 把派生出的四档健康状态映射成语义配色（圆点底色 + 徽标底色/文字色）。
// 刻意复用现有设计 token（success/warning/muted-foreground/destructive），
// 不为 runtime 引入专属颜色——保持色板精简、与 Skills 等其它产品一致。
// 文案标签走 useT 翻译——见下方 useHealthLabel。
const HEALTH_VISUAL: Record<RuntimeHealth, { dot: string; tone: string }> = {
  online: { dot: "bg-success", tone: "bg-success/10 text-success" },
  recently_lost: { dot: "bg-warning", tone: "bg-warning/10 text-warning" },
  offline: { dot: "bg-muted-foreground/40", tone: "bg-muted text-muted-foreground" },
  long_offline: { dot: "bg-destructive", tone: "bg-destructive/10 text-destructive" },
};

// 纯圆点健康指示灯：只画一个 2x2 的小圆点，颜色表达四档健康状态；loading
// 时渲染灰色占位点（尚未拿到数据时不假装在线/离线）。
export function HealthDot({
  health,
  className = "",
}: {
  health: RuntimeHealth | "loading";
  className?: string;
}) {
  if (health === "loading") {
    return (
      <span
        className={`inline-block h-2 w-2 rounded-full bg-muted ${className}`}
      />
    );
  }
  return (
    <span
      className={`inline-block h-2 w-2 rounded-full ${HEALTH_VISUAL[health].dot} ${className}`}
    />
  );
}

// Wifi 样式的健康指示灯：图标形状传达大致状态（「这台机器还能不能跟我们
// 说话」），颜色传达严重程度。用在比裸圆点需要更多信息量的地方（agent
// 悬停卡的 runtime 行、runtime 列表的健康列）。
//
//   online        → Wifi（满格，success）
//   recently_lost → WifiHigh（少一格，warning）——短暂抖动
//   offline       → WifiOff（斜杠划掉，muted）——长时间连不上
//   long_offline  → WifiOff（斜杠划掉，destructive）——长时间宕机
const HEALTH_ICON: Record<
  RuntimeHealth,
  { Icon: typeof Wifi; tone: string }
> = {
  online: { Icon: Wifi, tone: "text-success" },
  recently_lost: { Icon: WifiHigh, tone: "text-warning" },
  offline: { Icon: WifiOff, tone: "text-muted-foreground" },
  long_offline: { Icon: WifiOff, tone: "text-destructive" },
};

// Wifi 式健康图标组件（映射关系见上方 HEALTH_ICON）。loading 时渲染灰色 Wifi
// 占位图标。
export function HealthIcon({
  health,
  className = "h-3 w-3",
}: {
  health: RuntimeHealth | "loading";
  className?: string;
}) {
  if (health === "loading") {
    return <Wifi className={`${className} text-faint-foreground`} />;
  }
  const { Icon, tone } = HEALTH_ICON[health];
  return <Icon className={`${className} ${tone}`} />;
}

// 纯英文文案兜底表。以纯函数形式存在，供非组件调用方（如列构造工厂）使用；
// 组件内写翻译的调用点应改用下方的 useHealthLabel hook。
const HEALTH_LABEL_EN: Record<RuntimeHealth, string> = {
  online: "Online",
  recently_lost: "Recently lost",
  offline: "Offline",
  long_offline: "Long offline",
};

// 健康文案的纯函数形式：给非组件上下文（如表格列构造）用的英文兜底；
// loading 一律返回「—」。
export function healthLabel(health: RuntimeHealth | "loading"): string {
  if (health === "loading") return "—";
  return HEALTH_LABEL_EN[health];
}

// Hook 形式：在 React 组件里取健康状态的翻译文案。新写的组件调用点应优先用
// 它（而非 healthLabel），让所有界面语言随 i18n 一致切换。
export function useHealthLabel(): (health: RuntimeHealth | "loading") => string {
  const { t } = useT("runtimes");
  return (health) => {
    if (health === "loading") return "—";
    return t(($) => $.health[health].label);
  };
}

// 完整健康徽标：小圆点 + 翻译文案，套在带底色的小徽标里。信息量最全的
// 形态，用在详情页头部等空间充裕、健康是重点的位置。
export function HealthBadge({
  health,
}: {
  health: RuntimeHealth | "loading";
}) {
  const labelOf = useHealthLabel();
  if (health === "loading") {
    return (
      <Badge variant="secondary" className="bg-muted text-muted-foreground">
        —
      </Badge>
    );
  }
  const v = HEALTH_VISUAL[health];
  return (
    <Badge variant="secondary" className={v.tone}>
      <span className={`h-1.5 w-1.5 rounded-full ${v.dot}`} />
      {labelOf(health)}
    </Badge>
  );
}

// 「标签 + 值」的字段行：小号灰色标签在上、值在下（超长省略号截断）。
// mono 打开时值用等宽字体，适合 ID / 版本号等机器信息。
export function InfoField({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div>
      <div className="text-caption text-muted-foreground">{label}</div>
      <div
        className={`mt-0.5 text-body truncate ${mono ? "font-mono text-caption" : ""}`}
      >
        {value}
      </div>
    </div>
  );
}

// 单个 token 数值小卡：小号灰色标签在上、等宽粗体数字在下。用于几个数字
// 并排展示的紧凑场景（如一次运行的 token 统计）。
export function TokenCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border px-3 py-2">
      <div className="text-caption text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-body font-semibold tabular-nums">{value}</div>
    </div>
  );
}

// KPI 大数字卡——Runtime 详情页「关键数字」行的视觉锚点：大数字字号足以压过
// 下方图表的层级。label 是小号大写眉题；hint 是数字下方的一行细字，放涨跌 /
// 比率 / 节省额这类补充说明。
export function KpiCard({
  label,
  value,
  hint,
  accent,
}: {
  label: string;
  value: React.ReactNode;
  hint?: React.ReactNode;
  accent?: "brand" | "success" | "default";
}) {
  const valueClass =
    accent === "brand"
      ? "text-brand"
      : accent === "success"
        ? "text-success"
        : "";
  return (
    <div className="flex flex-col gap-2 p-5">
      <div className="text-micro font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </div>
      <div className={`text-display font-semibold leading-none tabular-nums ${valueClass}`}>
        {value}
      </div>
      {hint != null && (
        <div className="text-caption text-muted-foreground">{hint}</div>
      )}
    </div>
  );
}
