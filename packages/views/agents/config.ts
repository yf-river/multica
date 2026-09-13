import {
  Clock,
  CheckCircle2,
  XCircle,
  Loader2,
  Play,
} from "lucide-react";

// 本文件负责把任务状态映射成纯视觉配置（图标 + 颜色）。
//
// 这里刻意不放 `label`——可读的状态文字统一由 `taskStatusLabel(status, t)` 从
// `agents` 语言包取。如果在这里再放一份 `label`，就等于在已翻译的包里塞了第二份
// 无法翻译的重复字符串，正是 #7411 报告的那种漂移。
export const taskStatusConfig: Record<string, { icon: typeof CheckCircle2; color: string }> = {
  queued: { icon: Clock, color: "text-muted-foreground" },
  dispatched: { icon: Play, color: "text-info" },
  running: { icon: Loader2, color: "text-brand" },
  completed: { icon: CheckCircle2, color: "text-success" },
  failed: { icon: XCircle, color: "text-destructive" },
  cancelled: { icon: XCircle, color: "text-muted-foreground" },
};
