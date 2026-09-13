import {
  AlertCircle,
  Archive,
  CircleDot,
  CircleSlash,
  Clock,
  Loader2,
  PlugZap,
  type LucideIcon,
} from "lucide-react";
import type { AgentAvailability, Workload } from "@multica/core/agents";

// 本文件负责把 Agent 的两种“在场状态”映射成纯视觉配置（颜色、图标），供列表、详情、悬浮卡片等各处复用。
//
// 这里只提供视觉映射，不包含任何可读文字——文字统一走 `agents` 语言包（`availability.*` / `workload.*`），
// 避免在已翻译的包里再塞一份无法翻译的重复文案（历史上 #7411 就是因为这样才漂移）。
//
// 颜色一律用项目的语义色 token，不写死 Tailwind 颜色：
//
//   AVAILABILITY（可用状态，决定“圆点”在出现圆点处显示的颜色）：
//     online    → success（绿色）
//     unstable  → warning（琥珀色）——和运行时卡片上的琥珀色呼应
//     offline   → muted-foreground（灰色）
//
//   WORKLOAD（工作负载，决定聚焦场景下可选的“负载小标签”颜色）：
//     working   → brand（蓝色）有活动在跑
//     queued    → warning（琥珀色）异常信号：没有在跑，但有任务在排队
//                                      （通常是挂在离线运行时上；在线时短暂闪现是无害的竞态）
//     idle      → muted（灰色）当前没活
//
// `failed` / `completed` / `cancelled` 特意不做列表级视觉，它们属于历史上下文，
// 通过「最近工作 + 收件箱」呈现，而不是列表摘要状态。

// 纯视觉配置：圆点/文字的色调 + 图标。人读的文字永远不在这里，而是来自
// `agents` 语言包（`availability.*` / `workload.*`）。
export interface AvailabilityVisual {
  // 圆点指示器的背景填充色。
  dotClass: string;
  // 圆点旁边标签文字的前景色。
  textClass: string;
  // 用在较大徽章场景（详情页头部、悬浮卡片）的图标。
  icon: LucideIcon;
}

export const availabilityConfig: Record<AgentAvailability, AvailabilityVisual> = {
  online: {
    dotClass: "bg-success",
    textClass: "text-success",
    icon: CircleDot,
  },
  unstable: {
    dotClass: "bg-warning",
    textClass: "text-warning",
    icon: PlugZap,
  },
  offline: {
    dotClass: "bg-muted-foreground/40",
    textClass: "text-muted-foreground",
    icon: CircleSlash,
  },
  // 生命周期状态而非运行状态——已退役的 Agent。颜色和 offline 一样灰
  // （因为它接不了活），但标签文字走 `availability.archived`，让用户读到的
  // 是“此 Agent 已归档”，而不是“暂时失联”。
  archived: {
    dotClass: "bg-muted-foreground/40",
    textClass: "text-muted-foreground",
    icon: Archive,
  },
};

// 可用状态筛选 chip 的展示顺序，让颜色按自然递进排列，而不是按字母序。
export const availabilityOrder: AgentAvailability[] = [
  "online",
  "unstable",
  "offline",
];

export interface WorkloadVisual {
  // 图标 + 标签文字的前景色。
  textClass: string;
  // 内联显示的图标。
  icon: LucideIcon;
}

export const workloadConfig: Record<Workload, WorkloadVisual> = {
  working: {
    textClass: "text-brand",
    icon: Loader2,
  },
  queued: {
    // 琥珀色小标签：没有任务在跑、但有任务在排队。在离线运行时上，
    // 这就是我们明确暴露的“卡住”信号（替代了原来容易误导的 “Running 0/N +Mq” 文案）。
    textClass: "text-warning",
    icon: Clock,
  },
  idle: {
    textClass: "text-muted-foreground",
    icon: AlertCircle,
  },
};

// 未来负载小标签分组使用的顺序；可操作的信号排在前面。
export const workloadOrder: Workload[] = ["working", "queued", "idle"];
